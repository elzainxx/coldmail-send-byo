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

// recordingPusher is a real in-process Pusher: it records every push_job and reports a connected
// IDLE box, honoring the Phase-3 one-job-in-flight-PER-BOX constraint. boxID is the single default
// box (Phase 3 fixtures); extraBoxes (Chunk 1+) lets a test model several simultaneously-idle
// boxes, so a tick can push to more than one box concurrently. PushJob can be made to fail (to
// exercise the push-failed requeue).
type recordingPusher struct {
	mu         sync.Mutex
	pushes     []boxproto.PushJob
	boxID      string          // "" => AnyConnectedBox returns "" (no box) unless extraBoxes has an idle one
	extraBoxes []string        // additional registered box ids (Chunk 1 multi-box tests)
	inFlightBy map[string]bool // per-box in-flight marker
	failPush   bool
}

func (p *recordingPusher) PushJob(pj boxproto.PushJob, boxID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failPush {
		return fmt.Errorf("simulated push failure")
	}
	p.pushes = append(p.pushes, pj)
	if p.inFlightBy == nil {
		p.inFlightBy = map[string]bool{}
	}
	p.inFlightBy[boxID] = true
	return nil
}

func (p *recordingPusher) AnyConnectedBox() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range append([]string{p.boxID}, p.extraBoxes...) {
		if b != "" && !p.inFlightBy[b] {
			return b
		}
	}
	return ""
}

// freeAllBoxes clears every box's in-flight marker, simulating that each box has reported its
// outcome and is idle again (mirrors boxlink.StreamServer.dispatchFrame's report handling, which
// clears inFlight on every report — accepted, error, or bounced). Used by multi-tick tests that
// need more total pushes than there are boxes (Chunk 5 fairness/gate tests).
func (p *recordingPusher) freeAllBoxes() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inFlightBy = map[string]bool{}
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
	pool      *pgxpool.Pool
	moduleDSN string // for tests that need their OWN pgxpool.Pool (genuine multi-connection concurrency)
	admin     *pgx.Conn
	dec       *credential.Decryptor
	enc       *adminapi.Encryptor
	key       []byte
	px        *recordingPusher
	d         *Dispatcher
	oh        *OutcomeHandler
	cfg       Config
}

// poolDSN returns the env's module DSN so a test can open additional, independent connections
// (genuine concurrency — CLAUDE.md Rule 3 forbids simulating this with one shared pool).
func poolDSN(t *testing.T, e *dispatchEnv) string {
	t.Helper()
	return e.moduleDSN
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

	// Load real config from the clone (seeded by the migration chain) so the OutcomeHandler has the
	// real Phase 5 dials (quarantine_threshold, throttle_pause_seconds, backoff ladder, weights) —
	// not a hand-built Config that would zero them and mask policy bugs.
	cfg, err := LoadConfig(context.Background(), pool)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	px := &recordingPusher{boxID: seedBox(t, admin)}
	d := NewDispatcher(pool, dec, px)
	oh := NewOutcomeHandler(pool, cfg)

	env := &dispatchEnv{pool: pool, moduleDSN: moduleDSN, admin: admin, dec: dec, enc: enc, key: key, px: px, d: d, oh: oh, cfg: cfg}

	// Phase 6: neutralize per-lane pacing BY DEFAULT for the existing tick-loop suites. Those tests
	// (fairness, exhaustion, loop) drive many synthetic Tick() calls in compressed wall-clock to
	// isolate claim/route/fairness behavior; the real 3–7s inter-send delay (§1) would withhold every
	// lane after the first send and stall them. Setting the seeded delay to 0 makes the pacer admit
	// immediately — the exact "delay ~0" configuration T1's anti-vacuity control uses — so pacing is
	// off for tests that don't exercise it. The dedicated pacing behavioral tests (T1/T2) set their
	// OWN non-zero delay via setConfigInt to exercise the real cadence. Pacing is orthogonal to these
	// paths (§1.4), so zeroing the delay changes nothing about what they assert.
	env.setConfigInt(t, "send_delay_min_sec", 0)
	env.setConfigInt(t, "send_delay_max_sec", 0)
	return env
}

// --- fixtures (real committed rows) ---

// seedSystemUserAndPool seeds the system user + a shared pool, sets it as default_shared_pool_id
// (§4.2/§4.3 — base/non-dedicated routing only reaches a shared pool via that config key; an
// operator "has configured" state is what most fixtures want), and returns the pool id.
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
	e.setDefaultSharedPool(t, poolID)
	return poolID
}

// setDefaultSharedPool sets sender.config's default_shared_pool_id to poolID (the Phase 2
// config-update path, done directly here since these are dispatch-package fixtures).
func (e *dispatchEnv) setDefaultSharedPool(t *testing.T, poolID string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE sender.config SET value = jsonb_build_object('v', $1::text) WHERE key = 'default_shared_pool_id'`,
		poolID); err != nil {
		t.Fatalf("set default_shared_pool_id: %v", err)
	}
}

// seedPoolWithStrategy seeds a shared pool with an EXPLICIT routing_strategy (§3 Chunk 3 tests:
// random/round_robin/one_by_one). Ensures the system user exists (same as seedPool).
func (e *dispatchEnv) seedPoolWithStrategy(t *testing.T, strategy string) string {
	t.Helper()
	ctx := context.Background()
	_, err := e.admin.Exec(ctx,
		`INSERT INTO public.users (id, email, is_active) VALUES ('00000000-0000-0000-0000-000000000000','sys@sender.local',true) ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		t.Fatalf("seed system user: %v", err)
	}
	var poolID string
	if err := e.admin.QueryRow(ctx,
		`INSERT INTO sender.pools (user_id, kind, name, routing_strategy) VALUES ('00000000-0000-0000-0000-000000000000','shared','p4-strategy',$1::sender.routing_strategy) RETURNING id`,
		strategy).Scan(&poolID); err != nil {
		t.Fatalf("seed pool with strategy %s: %v", strategy, err)
	}
	e.setDefaultSharedPool(t, poolID)
	return poolID
}

// seedDedicatedPool seeds a dedicated pool OWNED BY userID (§4.2 — a user may own several; this
// helper is called multiple times with the same userID for that case). fallbackPoolID may be ""
// (no explicit fallback — resolvePoolChain falls through to the config default, §4.2/P4-4).
func (e *dispatchEnv) seedDedicatedPool(t *testing.T, userID, fallbackPoolID string) string {
	t.Helper()
	ctx := context.Background()
	var fb *string
	if fallbackPoolID != "" {
		fb = &fallbackPoolID
	}
	var poolID string
	if err := e.admin.QueryRow(ctx,
		`INSERT INTO sender.pools (user_id, kind, owner_user_id, name, fallback_pool_id)
		 VALUES ($1,'dedicated',$1,'dedicated-p4',$2) RETURNING id`, userID, fb).Scan(&poolID); err != nil {
		t.Fatalf("seed dedicated pool: %v", err)
	}
	return poolID
}

// seedSharedPool seeds a plain shared pool WITHOUT registering it as default_shared_pool_id (the
// Chunk 4 fallback-resolution tests need a shared pool that is reachable ONLY via an explicit
// fallback_pool_id, to prove (a) of P4-4's resolution order — not via the config default).
func (e *dispatchEnv) seedSharedPool(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	_, err := e.admin.Exec(ctx,
		`INSERT INTO public.users (id, email, is_active) VALUES ('00000000-0000-0000-0000-000000000000','sys@sender.local',true) ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		t.Fatalf("seed system user: %v", err)
	}
	var poolID string
	if err := e.admin.QueryRow(ctx,
		`INSERT INTO sender.pools (user_id, kind, name) VALUES ('00000000-0000-0000-0000-000000000000','shared',$1) RETURNING id`, name).Scan(&poolID); err != nil {
		t.Fatalf("seed shared pool: %v", err)
	}
	return poolID
}

// clearDefaultSharedPool sets default_shared_pool_id back to NULL (its Phase 1 seed, §4.3).
func (e *dispatchEnv) clearDefaultSharedPool(t *testing.T) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE sender.config SET value = jsonb_build_object('v', NULL) WHERE key = 'default_shared_pool_id'`); err != nil {
		t.Fatalf("clear default_shared_pool_id: %v", err)
	}
}

// seedAccount seeds an eligible account in poolID with a real encrypted credential (secret). It
// uses the harness Encryptor so the Decryptor round-trips (AAD = account.id ‖ key_id). Because
// account.id must exist before Encrypt (AAD binding), we generate the UUID app-side. smtp_username
// is derived from the account's own id (never a fixed literal): sender_0013's
// accounts_pool_host_user_unique UNIQUE(pool_id, smtp_host, smtp_username) would otherwise reject
// a second account in the same pool.
func (e *dispatchEnv) seedAccount(t *testing.T, poolID, secret string) string {
	t.Helper()
	ctx := context.Background()
	id, raw := newUUID(t)
	ct, nonce, err := e.enc.Encrypt(raw, []byte(secret))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	username := "u-" + id + "@ourdomain.com"
	_, err = e.admin.Exec(ctx,
		`INSERT INTO sender.accounts
		   (id, user_id, pool_id, smtp_host, smtp_port, smtp_username,
		    credential_ciphertext, credential_key_id, credential_nonce, from_address, from_name)
		 VALUES ($1,'00000000-0000-0000-0000-000000000000',$2,'smtp-sink',587,$6,$3,$4,$5,'u@ourdomain.com','Tester')`,
		id, poolID, ct, testKeyID, nonce, username)
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return id
}

// seedUser inserts a real public.users row (a distinct tenant, for the per-user claim tests) and
// returns its id.
func (e *dispatchEnv) seedUser(t *testing.T, email string) string {
	t.Helper()
	var id string
	if err := e.admin.QueryRow(context.Background(),
		`INSERT INTO public.users (email, is_active) VALUES ($1, true) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

// addBox seeds one more real sender.boxes row and registers it as an additional idle box on the
// env's recordingPusher, so a test can exercise more than one simultaneous push (the default
// dispatchEnv has exactly one box, mirroring the Phase 3 fixtures).
func (e *dispatchEnv) addBox(t *testing.T) {
	t.Helper()
	id := seedBox(t, e.admin)
	e.px.extraBoxes = append(e.px.extraBoxes, id)
}

// jobBox returns the box a job was actually PUSHED TO (assigned_box_id), falling back to the default
// box when the column is NULL (claimed but never pushed). Tests that feed a report into the real
// OutcomeHandler must report AS THIS BOX: a real box only reports outcomes for jobs pushed to itself,
// and the Brain's stale-replay attribution guard (Phase 7 §1.3) applies a report only from the box
// owning the current attempt. In any multi-box test a hardcoded e.px.boxID is a mis-attributed report
// and is correctly dropped, which would leave the job stuck 'sending'.
func (e *dispatchEnv) jobBox(t *testing.T, jobID string) string {
	t.Helper()
	var box *string
	if err := e.admin.QueryRow(context.Background(),
		`SELECT assigned_box_id::text FROM sender.jobs WHERE id=$1`, jobID).Scan(&box); err != nil {
		t.Fatalf("read assigned box for job %s: %v", jobID, err)
	}
	if box == nil {
		return e.px.boxID
	}
	return *box
}

// mustLoadConfig reads the CURRENT live sender.config (via the real LoadConfig, same as Tick/
// TryDispatch use) — for tests that call selectAccountForJob directly and need the freshest
// config after mutating a key mid-test (e.g. setDefaultSharedPool/clearDefaultSharedPool).
func (e *dispatchEnv) mustLoadConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := LoadConfig(context.Background(), e.pool)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

// setConfigInt overwrites a seeded sender.config integer value (default_value is left untouched —
// CLAUDE.md Rule 1 forbids mutating default_value; only value is overridden for the test).
func (e *dispatchEnv) setConfigInt(t *testing.T, key string, v int) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE sender.config SET value = jsonb_build_object('v', $2::int), updated_at = NOW() WHERE key = $1`,
		key, v); err != nil {
		t.Fatalf("set config %s: %v", key, err)
	}
}

// setConfigStr is setConfigInt for a string-valued key (default_value again left untouched, Rule 1).
func (e *dispatchEnv) setConfigStr(t *testing.T, key, v string) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE sender.config SET value = jsonb_build_object('v', $2::text), updated_at = NOW() WHERE key = $1`,
		key, v); err != nil {
		t.Fatalf("set config %s: %v", key, err)
	}
}

// setConfigBool is setConfigInt for a boolean-valued key (Phase 8's twelve toggle keys).
func (e *dispatchEnv) setConfigBool(t *testing.T, key string, v bool) {
	t.Helper()
	if _, err := e.admin.Exec(context.Background(),
		`UPDATE sender.config SET value = jsonb_build_object('v', $2::boolean), updated_at = NOW() WHERE key = $1`,
		key, v); err != nil {
		t.Fatalf("set config %s: %v", key, err)
	}
}

// seedQueuedJob seeds a job_item + a queued sender.jobs row (genesis event) and returns the job id.
func (e *dispatchEnv) seedQueuedJob(t *testing.T, recipient string) string {
	return e.seedQueuedJobSubject(t, recipient, "Subj")
}

// seedQueuedJobFor is seedQueuedJob for an EXPLICIT tenant (userID), used by the per-user claim
// tests (Chunk 1) where the system-user default would collapse every job into one tenant.
func (e *dispatchEnv) seedQueuedJobFor(t *testing.T, userID, recipient string) string {
	t.Helper()
	ctx := context.Background()
	var jobItemID string
	if err := e.admin.QueryRow(ctx,
		`INSERT INTO public.job_items (campaign_id, user_id, recipient_email, rendered_subject, rendered_body, reply_to, status)
		 VALUES (gen_random_uuid(),$1,$2,'Subj','<p>b</p>','reply@ourdomain.com','pending')
		 RETURNING id`, userID, recipient).Scan(&jobItemID); err != nil {
		t.Fatalf("seed job_item for user: %v", err)
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
		 VALUES ($1,(SELECT campaign_id FROM public.job_items WHERE id=$1),$2,$3,'Subj','<p>b</p>','reply@ourdomain.com','queued',NOW())
		 RETURNING id`, jobItemID, userID, recipient).Scan(&jobID); err != nil {
		t.Fatalf("seed job for user: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit job: %v", err)
	}
	return jobID
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
