// Command brain is the Phase-0 SKELETON of the byosmtp-module orchestrator ("the Brain").
//
// Scope (Phase 0 only — NO feature logic): start; acquire the single-active-Brain
// advisory lock (a second Brain exits cleanly); connect to Postgres as byosmtp_module;
// load the dev KMS key (Brain is the SOLE decryptor); serve a health stub; serve an
// mTLS box-link that lets a box register (id+token) and heartbeat. There is deliberately
// NO dispatch loop, NO routing, NO SMTP — those are Phase 1+.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/byosmtp-module/brain/internal/adminapi"
	"github.com/byosmtp-module/brain/internal/boxlink"
	"github.com/byosmtp-module/brain/internal/config"
	"github.com/byosmtp-module/brain/internal/credential"
	"github.com/byosmtp-module/brain/internal/db"
	"github.com/byosmtp-module/brain/internal/dispatch"
	"github.com/byosmtp-module/brain/internal/httpapi"
	"github.com/byosmtp-module/brain/internal/kms"
	"github.com/byosmtp-module/brain/internal/platformapi"
	"github.com/byosmtp-module/brain/internal/singleton"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetPrefix("brain ")

	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("FATAL config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// DB connection as the app role (byosmtp_module) — never a superuser at runtime.
	database, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("FATAL db connect: %v", err)
	}
	defer database.Close()
	log.Printf("db connected")

	// Singleton guard: exactly one active Brain. Bounded-wait-then-fail. A second Brain
	// that cannot acquire the session-level advisory lock exits non-zero and does nothing.
	// Uses a DEDICATED standalone connection (its own DSN), independent of the pool.
	//
	// brain_singleton_acquire_timeout_sec (Phase 9.5 B5, §5.2, seeded sender_0021) is config-not-code
	// (CLAUDE.md Rule 1) — the env var BRAIN_SINGLETON_ACQUIRE_TIMEOUT_SEC is ONLY the Phase-0
	// bootstrap default (OD-18: before byosmtp.config existed) and is used ONLY as the fallback when
	// the DB read fails (e.g. a genuinely pre-migration database — the runner hasn't applied
	// sender_0021 yet). The already-connected pool can read the live value BEFORE the singleton
	// lock is attempted; a bad/missing row degrades to the env default rather than blocking startup
	// on a config problem the singleton guard itself has nothing to do with.
	acquireTimeout := readSingletonAcquireTimeout(ctx, database.Pool, cfg.SingletonAcquireTimeout)
	guard, err := singleton.Acquire(ctx, cfg.DatabaseURL, acquireTimeout)
	if err != nil {
		if errors.Is(err, singleton.ErrNotAcquired) {
			// This is the correct, clean exit for a second Brain — not a crash.
			log.Printf("singleton lock not acquired within %s: another Brain is active; exiting cleanly", acquireTimeout)
			database.Close()
			os.Exit(3)
		}
		log.Fatalf("FATAL singleton acquire: %v", err)
	}
	defer guard.Release(context.Background())
	log.Printf("singleton lock acquired (key=%d)", singleton.LockKey)

	// G11-OD-1 / ADR-0011 (option B, ruled 2026-07-29): install the bounded re-acquire cadence for a
	// MID-LIFE lock loss. A dropped lock connection (DB restart, failover, pg_terminate_backend,
	// connection reaper) no longer leaves the keeper dead — it pauses dispatch (see SetLeadership
	// below) and re-attempts on this cadence. Config-not-code (Rule 1); 0/unreadable = never
	// re-acquire, which leaves the Brain paused-and-honest rather than resuming on a guessed cadence.
	reacquireEvery := readSingletonReacquireInterval(ctx, database.Pool)
	guard.SetReacquireInterval(reacquireEvery)
	if reacquireEvery > 0 {
		log.Printf("singleton: mid-life lock loss will PAUSE dispatch/reclaim/retention and re-attempt acquisition every %s (brain_singleton_reacquire_interval_sec)", reacquireEvery)
	} else {
		log.Printf("singleton: mid-life re-acquisition DISABLED (brain_singleton_reacquire_interval_sec=0 or unreadable) — on lock loss this Brain pauses dispatch and stays paused until restarted")
	}

	// Brain is the SOLE decryptor: load the dev KMS key path. The box holds NO KMS key
	// and cannot reach this material (enforced by container mounts + a harness test).
	decryptor, err := kms.LoadDev(cfg.KMSDevKeyFile)
	if err != nil {
		log.Fatalf("FATAL kms load: %v", err)
	}
	log.Printf("kms dev key loaded (sole decryptor; %d bytes of key material held in-process, never logged)", decryptor.KeyLen())

	// Admin API construction is DEFERRED to just after the verify-on-add Verifier exists (below) —
	// M3-P3-OD-4's eligibility term (credential_verification_state='verified') makes an
	// admin-created account UNROUTABLE unless it too runs verify-on-add, and the admin server's own
	// AccountVerifier param needs the Verifier built at line ~163. See the admin server construction
	// after the verifier below; cfg.AdminEnabled/KMS/enc/auth setup for it stays wherever it was.
	var adminHandler http.Handler

	// --- Phase 3: the minimal send path (dispatch + credential handoff + outcome handler) -------
	// The credential DECRYPTOR is the sole decryptor (Rule 6). It is loaded via a DELIBERATELY
	// separate KMS entry point and wired ONLY into the dispatcher — NEVER into the admin surface
	// (OD-7, structural: the admin Encryptor has no Decrypt; this Decryptor is not injected there).
	decKey, err := kms.LoadDecryptKey(cfg.KMSDevKeyFile)
	if err != nil {
		log.Fatalf("FATAL send-path decrypt key load: %v", err)
	}
	dec, err := credential.NewDecryptor(decKey, cfg.CredentialKeyID)
	if err != nil {
		log.Fatalf("FATAL credential decryptor init: %v", err)
	}
	// Zero the raw key bytes now that the Decryptor holds its own copy.
	for i := range decKey {
		decKey[i] = 0
	}

	sendCfg, err := dispatch.LoadConfig(ctx, database.Pool)
	if err != nil {
		log.Fatalf("FATAL send-path config load: %v", err)
	}

	// The persistent box↔Brain send stream (§2.1). The outcome handler is its ReportHandler.
	outcome := dispatch.NewOutcomeHandler(database.Pool, sendCfg)
	stream, err := boxlink.NewStreamServer(cfg.BoxlinkStreamAddr, cfg.TLS, database.Pool, outcome.Handle)
	if err != nil {
		log.Fatalf("FATAL boxlink stream init: %v", err)
	}

	// The dispatcher pushes over the stream; a Runner drives it off a KICK signal (event-driven,
	// NOT a polling loop — Phase 4). A box coming online kicks it too (a queued job may be waiting).
	dispatcher := dispatch.NewDispatcher(database.Pool, dec, stream)

	// Module 3 Phase 3 (§2.1): the verify-on-add Verifier shares the SAME decryptor and box stream
	// the Dispatcher uses (Rule 6: Brain is the sole decryptor; verify runs on the box, never here)
	// — verify and send are two operations against one set of capabilities, never two
	// implementations. Its verify_report frames arrive on the same stream as job reports, so it is
	// wired as the stream's VerifyHandler alongside the OutcomeHandler's ReportHandler.
	verifier := dispatch.NewVerifier(database.Pool, dec, stream)
	stream.SetOnVerify(verifier.HandleVerifyReport)

	// Admin API (Domain B, operator — Phase 2). Mounted under /v1/admin/* on the health server.
	// It authenticates callers with a hashed operator bearer key and is the ONLY writer of
	// account credentials — Encrypt-ONLY (OD-7: Decrypt stays Brain-only, the send path). The
	// surface only mounts when an admin key hash is configured (fail-closed: no key => no admin
	// surface). The credential Encrypt key material is loaded via a DELIBERATELY separate KMS
	// entry point (never the decryptor) so the encrypt-only property is structural.
	//
	// Module 3 Phase 3 C5: admin-created accounts now ALSO run verify-on-add, sharing the SAME
	// Verifier the user-facing surface uses (verifierAdapter, below) — M3-P3-OD-4's eligibility
	// term (credential_verification_state='verified') applies uniformly across BOTH surfaces; an
	// admin-created account that never verifies is exactly as unroutable as a user-created one,
	// with no special-casing by which surface created the row. Construction is deliberately
	// AFTER the verifier exists (moved from its original position earlier in this file).
	if cfg.AdminEnabled {
		encKey, err := kms.LoadEncryptKey(cfg.KMSDevKeyFile)
		if err != nil {
			log.Fatalf("FATAL admin credential key load: %v", err)
		}
		adminEnc, err := adminapi.NewEncryptor(encKey, cfg.CredentialKeyID)
		if err != nil {
			log.Fatalf("FATAL admin encryptor init: %v", err)
		}
		adminAuth := adminapi.NewAuthenticator(cfg.AdminKeyHashes)
		adminSrv := adminapi.NewServer(database.Pool, adminEnc, adminAuth, adminVerifierAdapter{verifier})
		adminHandler = adminSrv.Handler()
		log.Printf("admin API mounted on %s under /v1/admin/* (operator-key auth; Encrypt-only; %d key(s) configured)", cfg.HealthAddr, len(cfg.AdminKeyHashes))
	} else {
		log.Printf("admin API NOT mounted (ADMIN_API_KEY_HASHES unset — operator surface disabled, fail-closed)")
	}

	// Phase 7 (§2, P7-3): the Supabase-down gate. The dispatcher owns the ONE shared DBHealth so the
	// dispatch loop, the reclaim loop and the retention sweep all pause on a single detection; the
	// outcome handler shares it as an OBSERVER only — it always attempts a report (a commit is what
	// ends an outage) and merely reports what the DB did, so a failure there also pauses the loops
	// and a success resumes them promptly. The Brain writes NO job state while the DB is unreachable;
	// it stays alive on bounded backoff and never crash-loops.
	outcome.WithHealth(dispatcher.Health())

	// G11-OD-1 / ADR-0011 (option B): the LEADERSHIP gate. Until this line existed, guard was passed
	// ONLY to the health server, so held=false was visible on readyz and acted on NOWHERE — a Brain
	// that lost the lock kept claiming and pushing (observed: 56 minutes, while a second Brain held
	// the lock and came fully up). Now the same three loops that pause on a DB outage also pause on
	// lost leadership.
	//
	// DELIBERATELY NOT GATED: the outcome path. A box reporting an in-flight send must always be
	// persisted, even by a Brain that no longer holds the lock — that job was pushed while this Brain
	// WAS the leader, the box really did run the SMTP session, and refusing the report would either
	// invent a non-outcome (Rule 5) or force a needless duplicate send later. Persisting it is
	// idempotent (provider_message_id UNIQUE + the pre-persist check), so a successor Brain reaching
	// the same row cannot double-count it. Same reasoning for the mTLS listeners and the admin/
	// platform read surfaces: none of them CLAIM work.
	dispatcher.SetLeadership(guard.Healthy)

	// Phase 7 (§1): the one-time STARTUP RESCAN — reconnect-reconciliation (P7-2). It runs ONCE,
	// BEFORE the dispatch/reclaim loops start, so there is no window in which both it and Detector B
	// act on the same job (§1.4). It extends orphaned in-flight leases by startup_reconnect_window_sec
	// (EVENT-FREE — no state change, no job_events, no account lock, cannot deadlock; the Phase 6
	// lesson) and refreshes box heartbeats, opening a bounded window for reconnecting boxes to replay
	// buffered outcomes with no duplicate. It issues NO reclaim itself — actual reclaim stays the
	// continuous loop's job via the shared §2.4 transition. A transient failure here does not wedge
	// boot (the continuous loop is the backstop, §1.4).
	dispatcher.RunStartupRescan(ctx)

	runner := dispatch.NewRunner(dispatcher)
	stream.SetOnRegister(func(string) { runner.Kick() })
	go runner.Run(ctx)

	// Phase 6 (§2): the continuous reclaim loop — a THIRD single-Brain polling loop that recovers
	// jobs stranded on dead boxes (heartbeat scan) or hung on live ones (per-job lease backstop),
	// applying the locked reclaim transition. Ticks every reclaim_tick_interval_ms; no Kick (a
	// periodic sweep, not event-driven).
	reclaimer := dispatch.NewReclaimRunner(dispatcher)
	go reclaimer.Run(ctx)

	// Phase 7 (§3.1): the retention sweep — a FOURTH single-Brain housekeeping loop that DELETEs raw
	// byosmtp.send_logs content past debug_log_retention_days, in batches of retention_delete_batch_size,
	// every retention_sweep_interval_ms. It drops ONLY raw content: job OUTCOMES (job_events + final job
	// states) are permanent and are never rolled up or deleted, and the per-day summaries are DERIVED ON
	// DEMAND from job_events (P7-1, no summary table). It shares the §2 outage gate, so a DB outage
	// pauses it too — retention is a ceiling on raw-content lifetime, not a deadline, so a delayed sweep
	// is correct. Deliberately in-Brain rather than pg_cron (P7-7: one owner of byosmtp.* DML).
	retention := dispatch.NewRetentionRunner(dispatcher)
	go retention.Run(ctx)

	// Module 3 Phase 3 (§6.3): the account purge sweep — a FIFTH single-Brain housekeeping loop
	// that hard-DELETEs soft-deleted accounts past their purge_after retention window, removing
	// the credential together with the record (no separate scrub step, Phase 1 §4.7). SEPARATE
	// from the retention loop above (M3-P3-OD-10, ruled): a wedged purge is a privacy failure, a
	// wedged send-log sweep is a storage one, and folding them would let one mask the other.
	purger := dispatch.NewPurgeRunner(dispatcher)
	go purger.Run(ctx)

	// Module 3 Phase 5 (§5.3): the campaign expiry sweep — a genuinely periodic housekeeping loop,
	// not a lazy claim-time check, because the pathological case it bounds (every account
	// terminal_blocked or throttled) means the campaign's jobs are NEVER claimed, so a lazy check
	// would never fire on precisely the campaign that most needs bounding.
	campaignExpirer := dispatch.NewCampaignExpiryRunner(dispatcher)
	go campaignExpirer.Run(ctx)
	go func() {
		if err := stream.Serve(); err != nil {
			log.Printf("boxlink stream server stopped: %v", err)
		}
	}()
	log.Printf("boxlink send-stream mTLS server on %s (client cert required; credential channel)", cfg.BoxlinkStreamAddr)

	// Platform API (Domain A, Phase 3). Service-token auth (distinct credential from the operator
	// admin key). run kicks the dispatcher asynchronously; it never blocks on the send.
	//
	// Module 3 Phase 3 (§1.1): the user-owned account surface mounts HERE, not on adminapi — it is
	// Domain A work (a user-facing route), inheriting the service-token gate and the tenant-scoping
	// pattern platformapi already proved (OD-25). Its Encryptor is a SEPARATE instance from
	// adminapi's (OD-7 isolation: platformapi cannot import internal/adminapi) built from the SAME
	// KMS entry point (LoadEncryptKey re-reads the file; Encrypt-only, never Decrypt).
	var platformHandler http.Handler
	if cfg.PlatformEnabled {
		platEncKey, err := kms.LoadEncryptKey(cfg.KMSDevKeyFile)
		if err != nil {
			log.Fatalf("FATAL platform credential key load: %v", err)
		}
		platEnc, err := platformapi.NewEncryptor(platEncKey, cfg.CredentialKeyID)
		if err != nil {
			log.Fatalf("FATAL platform encryptor init: %v", err)
		}
		pauth := platformapi.NewAuthenticator(cfg.PlatformTokenHashes)
		psrv := platformapi.NewServer(database.Pool, pauth, runner.Kick, platEnc, verifierAdapter{verifier}, testSenderAdapter{dispatcher, database.Pool})
		platformHandler = psrv.Handler()
		log.Printf("platform API mounted under /v1/{quote,run,report,describe,byosmtp/accounts...} (service-token auth; %d token(s) configured)", len(cfg.PlatformTokenHashes))
	} else {
		log.Printf("platform API NOT mounted (PLATFORM_TOKEN_HASHES unset — Domain A surface disabled, fail-closed)")
	}

	// Health stub (plain HTTP; TLS termination is a deployment concern — §4.5). Reports true
	// state. The admin surface (/v1/admin/*) and platform surface (/v1/{quote,run,report,describe})
	// mount on the same server when their credentials are configured.
	health := httpapi.NewHealthServer(cfg.HealthAddr, database, guard, adminHandler, platformHandler)
	go func() {
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("FATAL health server: %v", err)
		}
	}()
	log.Printf("health server on %s", cfg.HealthAddr)

	// Box-link: the Phase-0 mTLS HTTP register/heartbeat routes (still served, additive-only). The
	// Phase-3 persistent send stream is the separate listener above.
	link, err := boxlink.NewServer(cfg.BoxlinkAddr, cfg.TLS, cfg.BoxRegistry)
	if err != nil {
		log.Fatalf("FATAL boxlink init: %v", err)
	}
	go func() {
		if err := link.ListenAndServeTLS(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("FATAL boxlink server: %v", err)
		}
	}()
	log.Printf("boxlink mTLS server on %s (client cert required)", cfg.BoxlinkAddr)

	log.Printf("brain ready")
	<-ctx.Done()
	log.Printf("shutdown signal received")

	// --- Phase 7 §4 (P7-5): GRACEFUL SHUTDOWN — stop-claim → drain → leave-in-place -------------
	//
	// STEP 1 (stop-claim). The dispatcher enters the drain: it claims no new job and pushes nothing,
	// so no fresh work enters flight behind us. The signal ctx is already cancelled at this point, so
	// the dispatch/reclaim/retention loop goroutines are exiting between ticks (never mid-transaction:
	// each tick's DB work runs to commit or rolls back on its own bounded context).
	//
	// STEP 2 (drain). The box-link stream and the outcome handler stay UP for the whole drain window,
	// so boxes finishing in-flight sends can still REPORT and have their outcomes persisted and acked
	// (Chunk 2's persist-ack). Each job that reports resolves normally — sent/bounced/requeued — which
	// is the clean terminal for it. The wait is bounded by the seeded shutdown_drain_timeout_sec
	// (config-not-code; NOT the old hardcoded 10s), and ends early the moment in-flight hits zero.
	//
	// STEP 3 (leave-in-place). Whatever is still in-flight at the timeout is LEFT EXACTLY AS STORED —
	// `sending`/`assigned` with its lease. We deliberately do NOT flip sending→queued: that job may
	// already have hit 250 at the provider, so an eager requeue would manufacture a duplicate (§4.2).
	// The next boot's startup rescan extends those leases so a reconnecting box can replay its buffered
	// outcome with no duplicate, and the continuous loop reclaims the genuinely-stuck (§4.3). A SIGKILL
	// that preempts all of this reduces to the same path — nothing was going to be written anyway.
	drainTimeout := time.Duration(sendCfg.ShutdownDrainTimeoutSec) * time.Second
	// Re-read the key so an operator's change since boot is honored; fall back to the boot value if the
	// DB is unreachable (a shutdown must never block on a sick DB).
	if freshCfg, err := dispatch.LoadConfig(context.Background(), database.Pool); err == nil {
		drainTimeout = time.Duration(freshCfg.ShutdownDrainTimeoutSec) * time.Second
	}
	// The drain gets its own context, NOT the cancelled signal ctx (which would abort it instantly).
	// Bounded a little above the drain window so the drain, not this context, decides when to stop.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), drainTimeout+5*time.Second)
	left := dispatcher.Drain(drainCtx, drainTimeout)
	cancelDrain()
	if left > 0 {
		log.Printf("shutdown: %d job(s) left in flight and RECOVERABLE (not requeued, not marked) — the next boot's rescan resolves them (§4.3)", left)
	}

	// Now the listeners come down. SAFETY BOUND (Phase 4 closeout triage,
	// Docs/history/open-decisions.md): the HTTP/listener close grace is a deployment concern, not a runtime
	// dial — same class as MAX_FALLBACK_HOPS. The DRAIN window above is the operator-tunable part.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = health.Shutdown(shutdownCtx)
	_ = link.Shutdown(shutdownCtx)
	_ = stream.Close()
	log.Printf("shutdown complete (no job was requeued or marked on the way out — §4.2 leave-in-place)")
}

// readSingletonAcquireTimeout reads the live brain_singleton_acquire_timeout_sec (Phase 9.5 B5,
// §5.2) from the already-connected pool, falling back to envDefault (the Phase-0 bootstrap value,
// OD-18) on any read/parse failure — a missing row (pre-sender_0021 database) or a transient DB
// blip must not block startup on a config problem the singleton guard itself has nothing to do
// with. This is a minimal, scoped-to-one-key reader rather than a full dispatch.LoadConfig call:
// at this point in startup the send-path config (job_max_processing_time_sec etc.) has not been
// validated yet, and a config-ordering violation elsewhere must not prevent the singleton guard
// itself from running.
func readSingletonAcquireTimeout(ctx context.Context, pool *pgxpool.Pool, envDefault time.Duration) time.Duration {
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT value FROM byosmtp.config WHERE key = 'brain_singleton_acquire_timeout_sec'`).Scan(&raw); err != nil {
		return envDefault
	}
	var wrap struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil || *wrap.V < 0 {
		return envDefault
	}
	return time.Duration(*wrap.V) * time.Second
}

// readSingletonReacquireInterval reads the live brain_singleton_reacquire_interval_sec (G11-OD-1 /
// ADR-0011 option B, seeded sender_0023) — the cadence at which a Brain that LOST the lock
// re-attempts it. Same minimal scoped-reader shape and same reasoning as
// readSingletonAcquireTimeout above.
//
// The failure fallback here is 0 (never re-acquire), NOT a hardcoded interval, and that direction is
// deliberate: 0 leaves the Brain paused-and-honest (readyz 503, container unhealthy, dispatching
// nothing) until an operator or supervisor acts, whereas guessing a cadence would resume dispatch on
// a schedule nobody configured. Rule 1 says a literal that governs runtime behavior is a bug — a
// literal that DISABLES an optional behavior when its config is unreadable is the fail-closed
// default, which is the same posture PLATFORM_TOKEN_HASHES-empty and alert_webhook_url-empty take.
func readSingletonReacquireInterval(ctx context.Context, pool *pgxpool.Pool) time.Duration {
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT value FROM byosmtp.config WHERE key = 'brain_singleton_reacquire_interval_sec'`).Scan(&raw); err != nil {
		return 0
	}
	var wrap struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || wrap.V == nil || *wrap.V < 0 {
		return 0
	}
	return time.Duration(*wrap.V) * time.Second
}

// verifierAdapter satisfies platformapi.AccountVerifier by wrapping *dispatch.Verifier and
// converting dispatch.VerifyResult to platformapi.VerifyResult field-by-field. This is the ONE
// place that conversion happens — platformapi cannot import internal/dispatch (OD-7 isolation),
// so main.go, which already imports both, is the seam. operationTimeoutSec is read fresh from
// byosmtp.config on every call (config-not-code) rather than captured once at startup.
type verifierAdapter struct {
	v *dispatch.Verifier
}

func (a verifierAdapter) Verify(ctx context.Context, accountID, userID string, operationTimeoutSec int) (platformapi.VerifyResult, error) {
	r, err := a.v.Verify(ctx, accountID, userID, operationTimeoutSec)
	if err != nil {
		return platformapi.VerifyResult{}, err
	}
	return platformapi.VerifyResult{
		Verified:     r.Verified,
		State:        r.State,
		ExternalCode: r.ExternalCode,
		Message:      r.Message,
		SMTPResponse: r.SMTPResponse,
	}, nil
}

// adminVerifierAdapter satisfies adminapi.AccountVerifier, wrapping the SAME *dispatch.Verifier
// verifierAdapter wraps for platformapi — one Verifier, two adapters (OD-7 isolation: adminapi
// cannot import internal/dispatch either, so it needs its own narrow interface + its own adapter
// type, even though the underlying call is identical). Module 3 Phase 3 C5.
type adminVerifierAdapter struct {
	v *dispatch.Verifier
}

func (a adminVerifierAdapter) Verify(ctx context.Context, accountID, userID string, operationTimeoutSec int) (adminapi.VerifyResult, error) {
	r, err := a.v.Verify(ctx, accountID, userID, operationTimeoutSec)
	if err != nil {
		return adminapi.VerifyResult{}, err
	}
	return adminapi.VerifyResult{
		Verified:     r.Verified,
		State:        r.State,
		ExternalCode: r.ExternalCode,
		Message:      r.Message,
		SMTPResponse: r.SMTPResponse,
	}, nil
}

// testSenderAdapter satisfies platformapi.TestSender by wrapping *dispatch.Dispatcher. It loads
// dispatch.Config FRESH on every call (config-not-code freshness — mirrors every other Dispatcher
// entry point, TryDispatch/Tick/Verify) and maps dispatch's two sentinel errors onto platformapi's
// own (OD-7: platformapi never imports internal/dispatch, so it cannot compare against dispatch's
// error values directly).
type testSenderAdapter struct {
	d  *dispatch.Dispatcher
	db *pgxpool.Pool
}

func (a testSenderAdapter) SendTest(ctx context.Context, userID, accountID, recipient string) (string, error) {
	cfg, err := dispatch.LoadConfig(ctx, a.db)
	if err != nil {
		return "", err
	}
	result, err := a.d.SendTest(ctx, userID, accountID, recipient, cfg)
	if err != nil {
		switch {
		case errors.Is(err, dispatch.ErrAccountIneligibleForTestSend):
			return "", platformapi.ErrAccountIneligible
		case errors.Is(err, dispatch.ErrNoBoxForTestSend):
			return "", platformapi.ErrNoBoxAvailable
		default:
			return "", err
		}
	}
	return result.JobID, nil
}
