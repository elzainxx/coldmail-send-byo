package dispatch

// Real-infra test support for the Phase 3 send-path suite. These tests drive the ACTUAL
// Dispatcher + OutcomeHandler (production code) against a REAL Postgres clone (migrated
// 0000..0014) — never a mock (CLAUDE.md Rule 3). The box channel is a real in-process Pusher
// that records push_job frames (a genuine object, not a stubbed transport): the credential
// handoff, the idempotency gate, and the state machine are exercised for real; only the SMTP
// wire (proven by the live-stack suite + the Node box units) is stood in for here.
//
// Isolation mirrors the Phase 2 adminapi harness: TEST_ADMIN_DATABASE_URL + TEST_TEMPLATE_DB.
// Tests SKIP when unset so `go test` never silently passes on nothing.

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sender-module/brain/internal/adminapi"
	"github.com/sender-module/brain/internal/boxproto"
	"github.com/sender-module/brain/internal/credential"
)

const testKeyID = "cred-dek-test"

// recordingPusher is a real in-process Pusher: it records every push_job and reports the
// configured connected box. PushJob can be made to fail (to exercise the push-failed requeue).
type recordingPusher struct {
	mu       sync.Mutex
	pushes   []boxproto.PushJob
	boxID    string // "" => AnyConnectedBox returns "" (no box)
	failPush bool
	inFlight bool
}

func (p *recordingPusher) PushJob(pj boxproto.PushJob, boxID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failPush {
		return fmt.Errorf("simulated push failure")
	}
	p.pushes = append(p.pushes, pj)
	p.inFlight = true
	return nil
}

func (p *recordingPusher) AnyConnectedBox() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight {
		return "" // one-in-flight: no idle box
	}
	return p.boxID
}

func (p *recordingPusher) lastPush() (boxproto.PushJob, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pushes) == 0 {
		return boxproto.PushJob{}, false
	}
	return p.pushes[len(p.pushes)-1], true
}

func (p *recordingPusher) pushCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pushes)
}

// dispatchEnv bundles a real Dispatcher + OutcomeHandler over a real clone.
type dispatchEnv struct {
	pool  *pgxpool.Pool
	admin *pgx.Conn
	dec   *credential.Decryptor
	enc   *adminapi.Encryptor
	key   []byte
	px    *recordingPusher
	d     *Dispatcher
	oh    *OutcomeHandler
	cfg   Config
}

func newDispatchEnv(t *testing.T, suffix string) *dispatchEnv {
	t.Helper()
	adminDSN := requireAdminDSN(t)
	template := requireTemplate(t)

	cloneDSN, drop := cloneDB(t, adminDSN, template, suffix)
	t.Cleanup(drop)

	admin := connect(t, cloneDSN)
	moduleDSN := withUserPass(cloneDSN, "sender_module", modulePassword())
	pool, err := pgxpool.New(context.Background(), moduleDSN)
	if err != nil {
		t.Fatalf("module pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// One dev key shared by the harness Encryptor (to seed accounts) and the Decryptor (send path).
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	enc, err := adminapi.NewEncryptor(key, testKeyID)
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	dec, err := credential.NewDecryptor(key, testKeyID)
	if err != nil {
		t.Fatalf("decryptor: %v", err)
	}

	cfg := Config{JobMaxProcessingTimeSec: 120, DebugLogRetentionDays: 7}
	px := &recordingPusher{boxID: seedBox(t, admin)}
	d := NewDispatcher(pool, dec, px, cfg)
	oh := NewOutcomeHandler(pool, cfg)

	return &dispatchEnv{pool: pool, admin: admin, dec: dec, enc: enc, key: key, px: px, d: d, oh: oh, cfg: cfg}
}

// --- fixtures (real committed rows) ---

// seedSystemUserAndPool seeds the system user + a shared pool and returns the pool id.
func (e *dispatchEnv) seedPool(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	_, err := e.admin.Exec(ctx,
		`INSERT INTO public.users (id, email, is_active) VALUES ('00000000-0000-0000-0000-000000000000','sys@sender.local',true) ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		t.Fatalf("seed system user: %v", err)
	}
	var poolID string
	if err := e.admin.QueryRow(ctx,
		`INSERT INTO sender.pools (user_id, kind, name) VALUES ('00000000-0000-0000-0000-000000000000','shared','p3') RETURNING id`).Scan(&poolID); err != nil {
		t.Fatalf("seed pool: %v", err)
	}
	return poolID
}

// seedAccount seeds an eligible account in poolID with a real encrypted credential (secret). It
// uses the harness Encryptor so the Decryptor round-trips (AAD = account.id ‖ key_id). Because
// account.id must exist before Encrypt (AAD binding), we generate the UUID app-side.
func (e *dispatchEnv) seedAccount(t *testing.T, poolID, secret string) string {
	t.Helper()
	ctx := context.Background()
	id, raw := newUUID(t)
	ct, nonce, err := e.enc.Encrypt(raw, []byte(secret))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, err = e.admin.Exec(ctx,
		`INSERT INTO sender.accounts
		   (id, user_id, pool_id, smtp_host, smtp_port, smtp_username,
		    credential_ciphertext, credential_key_id, credential_nonce, from_address, from_name)
		 VALUES ($1,'00000000-0000-0000-0000-000000000000',$2,'smtp-sink',587,'u@ourdomain.com',$3,$4,$5,'u@ourdomain.com','Tester')`,
		id, poolID, ct, testKeyID, nonce)
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

// seedQueuedJob seeds a job_item + a queued sender.jobs row (genesis event) and returns the job id.
func (e *dispatchEnv) seedQueuedJob(t *testing.T, recipient string) string {
	return e.seedQueuedJobSubject(t, recipient, "Subj")
}

// seedQueuedJobSubject is seedQueuedJob with a caller-chosen rendered_subject (used to inject a
// CR/LF-bearing subject for the header-injection guard test). The subject is bound as a parameter
// (never string-concatenated into SQL), so the CRLF reaches the row verbatim.
func (e *dispatchEnv) seedQueuedJobSubject(t *testing.T, recipient, subject string) string {
	t.Helper()
	return e.seedQueuedJobImpl(t, recipient, subject)
}

func (e *dispatchEnv) seedQueuedJobImpl(t *testing.T, recipient, subject string) string {
	t.Helper()
	ctx := context.Background()
	var jobItemID string
	if err := e.admin.QueryRow(ctx,
		`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status)
		 VALUES ('00000000-0000-0000-0000-000000000000','00000000-0000-0000-0000-000000000000',$1,$2,'<p>b</p>','reply@ourdomain.com','pending')
		 RETURNING id`, recipient, subject).Scan(&jobItemID); err != nil {
		t.Fatalf("seed job_item: %v", err)
	}
	var jobID string
	tx, err := e.admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('sender.actor','system',true)"); err != nil {
		t.Fatalf("set actor: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO sender.jobs (job_item_id, campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, state, next_attempt_at)
		 VALUES ($1,'00000000-0000-0000-0000-000000000000','00000000-0000-0000-0000-000000000000',$2,$3,'<p>b</p>','reply@ourdomain.com','queued',NOW())
		 RETURNING id`, jobItemID, recipient, subject).Scan(&jobID); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit job: %v", err)
	}
	return jobID
}

// jobState reads the current state + whether provider_message_id is set + lease/completed nullity.
func (e *dispatchEnv) jobState(t *testing.T, jobID string) (state string, hasPMID, leaseNull, completedSet bool, attempt int) {
	t.Helper()
	var pmid *string
	var lease, completed *time.Time
	if err := e.admin.QueryRow(context.Background(),
		`SELECT state::text, provider_message_id, lease_expires_at, completed_at, attempt_count FROM sender.jobs WHERE id=$1`, jobID).
		Scan(&state, &pmid, &lease, &completed, &attempt); err != nil {
		t.Fatalf("job state: %v", err)
	}
	return state, pmid != nil, lease == nil, completed != nil, attempt
}

func (e *dispatchEnv) eventCount(t *testing.T, jobID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM sender.job_events WHERE job_id=$1`, jobID).Scan(&n); err != nil {
		t.Fatalf("event count: %v", err)
	}
	return n
}

func (e *dispatchEnv) sendLogCount(t *testing.T, jobID string) int {
	t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(),
		`SELECT count(*) FROM sender.send_logs WHERE job_id=$1`, jobID).Scan(&n); err != nil {
		t.Fatalf("send_log count: %v", err)
	}
	return n
}

// --- clone/connect helpers (mirror the adminapi harness) ---

func requireAdminDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_ADMIN_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_ADMIN_DATABASE_URL not set; skipping real-infra dispatch test")
	}
	return dsn
}

func requireTemplate(t *testing.T) string {
	t.Helper()
	tpl := os.Getenv("TEST_TEMPLATE_DB")
	if tpl == "" {
		t.Skip("TEST_TEMPLATE_DB not set; skipping (needs a migrated template DB)")
	}
	return tpl
}

func modulePassword() string {
	if p := os.Getenv("SENDER_MODULE_PASSWORD"); p != "" {
		return p
	}
	return "devmodulepass"
}

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func cloneDB(t *testing.T, adminDSN, templateDB, suffix string) (string, func()) {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("clone admin connect: %v", err)
	}
	defer admin.Close(ctx)
	name := "test_disp_" + suffix
	_, _ = admin.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(name)))
	if _, err := admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", quoteIdent(name), quoteIdent(templateDB))); err != nil {
		t.Fatalf("clone db: %v", err)
	}
	cloneDSN := replaceDBName(adminDSN, name)
	cleanup := func() {
		c, err := pgx.Connect(context.Background(), adminDSN)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(),
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()", name)
		_, _ = c.Exec(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(name)))
	}
	return cloneDSN, cleanup
}

// seedBox seeds a real sender.boxes row and returns its id (a UUID). Its token_hash is irrelevant
// to these tests (the recording Pusher is used directly, not the stream auth path).
func seedBox(t *testing.T, admin *pgx.Conn) string {
	t.Helper()
	ctx := context.Background()
	_, _ = admin.Exec(ctx,
		`INSERT INTO public.users (id, email, is_active) VALUES ('00000000-0000-0000-0000-000000000000','sys@sender.local',true) ON CONFLICT (id) DO NOTHING`)
	var id string
	if err := admin.QueryRow(ctx,
		`INSERT INTO sender.boxes (id, user_id, token_hash) VALUES (gen_random_uuid(),'00000000-0000-0000-0000-000000000000','x') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seed box: %v", err)
	}
	return id
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func replaceDBName(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		if i := strings.LastIndex(dsn, "/"); i >= 0 {
			return dsn[:i+1] + name
		}
		return dsn
	}
	u.Path = "/" + name
	return u.String()
}

func withUserPass(dsn, user, pass string) string {
	i := strings.Index(dsn, "://")
	if i < 0 {
		return dsn
	}
	rest := dsn[i+3:]
	at := strings.Index(rest, "@")
	if at < 0 {
		return dsn
	}
	return dsn[:i+3] + user + ":" + pass + "@" + rest[at+1:]
}

// newUUID returns a random UUID string + its raw 16 bytes (for the AAD in seedAccount).
func newUUID(t *testing.T) (string, [16]byte) {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("uuid rand: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	return s, b
}
