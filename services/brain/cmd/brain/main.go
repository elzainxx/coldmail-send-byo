// Command brain is the Phase-0 SKELETON of the sender-module orchestrator ("the Brain").
//
// Scope (Phase 0 only — NO feature logic): start; acquire the single-active-Brain
// advisory lock (a second Brain exits cleanly); connect to Postgres as sender_module;
// load the dev KMS key (Brain is the SOLE decryptor); serve a health stub; serve an
// mTLS box-link that lets a box register (id+token) and heartbeat. There is deliberately
// NO dispatch loop, NO routing, NO SMTP — those are Phase 1+.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sender-module/brain/internal/adminapi"
	"github.com/sender-module/brain/internal/boxlink"
	"github.com/sender-module/brain/internal/config"
	"github.com/sender-module/brain/internal/credential"
	"github.com/sender-module/brain/internal/db"
	"github.com/sender-module/brain/internal/dispatch"
	"github.com/sender-module/brain/internal/httpapi"
	"github.com/sender-module/brain/internal/kms"
	"github.com/sender-module/brain/internal/platformapi"
	"github.com/sender-module/brain/internal/singleton"
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

	// DB connection as the app role (sender_module) — never a superuser at runtime.
	database, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("FATAL db connect: %v", err)
	}
	defer database.Close()
	log.Printf("db connected")

	// Singleton guard: exactly one active Brain. Bounded-wait-then-fail. A second Brain
	// that cannot acquire the session-level advisory lock exits non-zero and does nothing.
	// Uses a DEDICATED standalone connection (its own DSN), independent of the pool.
	guard, err := singleton.Acquire(ctx, cfg.DatabaseURL, cfg.SingletonAcquireTimeout)
	if err != nil {
		if errors.Is(err, singleton.ErrNotAcquired) {
			// This is the correct, clean exit for a second Brain — not a crash.
			log.Printf("singleton lock not acquired within %s: another Brain is active; exiting cleanly", cfg.SingletonAcquireTimeout)
			database.Close()
			os.Exit(3)
		}
		log.Fatalf("FATAL singleton acquire: %v", err)
	}
	defer guard.Release(context.Background())
	log.Printf("singleton lock acquired (key=%d)", singleton.LockKey)

	// Brain is the SOLE decryptor: load the dev KMS key path. The box holds NO KMS key
	// and cannot reach this material (enforced by container mounts + a harness test).
	decryptor, err := kms.LoadDev(cfg.KMSDevKeyFile)
	if err != nil {
		log.Fatalf("FATAL kms load: %v", err)
	}
	log.Printf("kms dev key loaded (sole decryptor; %d bytes of key material held in-process, never logged)", decryptor.KeyLen())

	// Admin API (Domain B, operator — Phase 2). Mounted under /v1/admin/* on the health server.
	// It authenticates callers with a hashed operator bearer key and is the ONLY writer of
	// account credentials — Encrypt-ONLY (OD-7: Decrypt stays Brain-only, the send path). The
	// surface only mounts when an admin key hash is configured (fail-closed: no key => no admin
	// surface). The credential Encrypt key material is loaded via a DELIBERATELY separate KMS
	// entry point (never the decryptor) so the encrypt-only property is structural.
	var adminHandler http.Handler
	if cfg.AdminEnabled {
		encKey, err := kms.LoadEncryptKey(cfg.KMSDevKeyFile)
		if err != nil {
			log.Fatalf("FATAL admin credential key load: %v", err)
		}
		enc, err := adminapi.NewEncryptor(encKey, cfg.CredentialKeyID)
		if err != nil {
			log.Fatalf("FATAL admin encryptor init: %v", err)
		}
		auth := adminapi.NewAuthenticator(cfg.AdminKeyHashes)
		adminSrv := adminapi.NewServer(database.Pool, enc, auth)
		adminHandler = adminSrv.Handler()
		log.Printf("admin API mounted on %s under /v1/admin/* (operator-key auth; Encrypt-only; %d key(s) configured)", cfg.HealthAddr, len(cfg.AdminKeyHashes))
	} else {
		log.Printf("admin API NOT mounted (ADMIN_API_KEY_HASHES unset — operator surface disabled, fail-closed)")
	}

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

	// Phase 7 (§2, P7-3): the Supabase-down gate. The dispatcher owns the ONE shared DBHealth so the
	// dispatch loop, the reclaim loop and the retention sweep all pause on a single detection; the
	// outcome handler shares it as an OBSERVER only — it always attempts a report (a commit is what
	// ends an outage) and merely reports what the DB did, so a failure there also pauses the loops
	// and a success resumes them promptly. The Brain writes NO job state while the DB is unreachable;
	// it stays alive on bounded backoff and never crash-loops.
	outcome.WithHealth(dispatcher.Health())

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
	go func() {
		if err := stream.Serve(); err != nil {
			log.Printf("boxlink stream server stopped: %v", err)
		}
	}()
	log.Printf("boxlink send-stream mTLS server on %s (client cert required; credential channel)", cfg.BoxlinkStreamAddr)

	// Platform API (Domain A, Phase 3). Service-token auth (distinct credential from the operator
	// admin key). run kicks the dispatcher asynchronously; it never blocks on the send.
	var platformHandler http.Handler
	if cfg.PlatformEnabled {
		pauth := platformapi.NewAuthenticator(cfg.PlatformTokenHashes)
		psrv := platformapi.NewServer(database.Pool, pauth, runner.Kick)
		platformHandler = psrv.Handler()
		log.Printf("platform API mounted under /v1/{quote,run,report,describe} (service-token auth; %d token(s) configured)", len(cfg.PlatformTokenHashes))
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
	log.Printf("shutdown signal received; draining")

	// SAFETY BOUND (Phase 4 closeout triage, Docs/open-decisions.md): a SIGTERM drain grace
	// period, not a runtime behavior a campaign/operator would tune — deployment/orchestration
	// concern, same class as MAX_FALLBACK_HOPS.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = health.Shutdown(shutdownCtx)
	_ = link.Shutdown(shutdownCtx)
	_ = stream.Close()
	log.Printf("shutdown complete")
}
