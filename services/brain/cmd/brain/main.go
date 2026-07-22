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

	"github.com/sender-module/brain/internal/boxlink"
	"github.com/sender-module/brain/internal/config"
	"github.com/sender-module/brain/internal/db"
	"github.com/sender-module/brain/internal/httpapi"
	"github.com/sender-module/brain/internal/kms"
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
	guard, err := singleton.Acquire(ctx, database, cfg.SingletonAcquireTimeout)
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

	// Health stub (plain HTTP; real Domain-B operator auth is Phase 2). Reports true state.
	health := httpapi.NewHealthServer(cfg.HealthAddr, database, guard)
	go func() {
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("FATAL health server: %v", err)
		}
	}()
	log.Printf("health server on %s", cfg.HealthAddr)

	// Box-link: mTLS server. A box registers (id+token, token verified against its hash
	// AND the client cert) then heartbeats. No persistent job stream in Phase 0.
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = health.Shutdown(shutdownCtx)
	_ = link.Shutdown(shutdownCtx)
	log.Printf("shutdown complete")
}
