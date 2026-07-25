// Sender Box entrypoint (Phase 3 — the minimal send path; Phase 7 — outcome buffering). The box
// dials the Brain over mTLS, holds a persistent control stream (src/stream.js), registers
// (id + token), heartbeats, and — when the Brain pushes a job — runs the full SMTP session itself
// (src/smtp.js). The box has NO DB access and NO KMS: it receives an already-decrypted credential
// per send, holds it in memory only, and zeroes it after the session (CLAUDE.md Rule 6; Phase 3
// §3.4/§4.4).
//
// Phase 7 §1.3 (P7-4): on a stream drop the box now RECONNECTS IN-PROCESS (preserving its in-memory
// outcome buffer) and REPLAYS any unacked outcomes on reconnect, rather than exiting. This is the
// minimum P7-4 contract — in-memory retention across a STREAM reconnect (the common blip / Brain-
// restart case). Surviving a box PROCESS restart via on-disk buffer persistence is P7-6 (FLAGGED,
// NOT built). A SIGTERM/SIGINT still exits cleanly.

import { BoxStream } from "./stream.js";

function env(k, def) {
  const v = process.env[k];
  return v === undefined || v === "" ? def : v;
}

async function main() {
  const boxId = env("BOX_ID", "");
  const token = env("BOX_TOKEN", "");
  if (!boxId || !token) {
    console.error("box: BOX_ID and BOX_TOKEN are required (out-of-band provisioned)");
    process.exit(2);
  }

  const stream = new BoxStream({
    host: env("BRAIN_HOST", "brain"),
    port: Number(env("BRAIN_STREAM_PORT", "8444")),
    boxId,
    token,
    certPath: env("MTLS_CLIENT_CERT", "/secrets/mtls/box.crt"),
    keyPath: env("MTLS_CLIENT_KEY", "/secrets/mtls/box.key"),
    caPath: env("MTLS_CA_CERT", "/secrets/mtls/ca.crt"),
    serverName: env("BRAIN_SERVER_NAME", "brain"),
    heartbeatSec: Number(env("BOX_HEARTBEAT_INTERVAL_SEC", "10")),
    // Per-operation SMTP timeout: the Brain seeds smtp_operation_timeout_sec (Phase 6) and would
    // deliver it via push_job; until Phase 6 wires it, the box uses this bootstrap default (ms).
    smtpTimeoutMs: Number(env("BOX_SMTP_TIMEOUT_MS", "30000")),
    // Bootstrap outcome-buffer TTL (ms). The Brain overrides this on register_ack with the seeded
    // box_outcome_buffer_ttl_sec (config-not-code, P7-4); this env is only the pre-register default.
    outcomeBufferTtlMs: Number(env("BOX_OUTCOME_BUFFER_TTL_MS", "900000")),
    metadata: {
      box_version: env("BOX_VERSION", "1.0.0"),
      hostname: env("HOSTNAME", ""),
    },
  });

  const shutdown = () => {
    console.log(`box ${boxId}: shutdown`);
    stream.stop();
    process.exit(0);
  };
  process.on("SIGTERM", shutdown);
  process.on("SIGINT", shutdown);

  // connectOnce dials + registers with bounded retry. SAFETY BOUND (Phase 4 closeout triage,
  // Docs/open-decisions.md): a startup-ordering retry, not a runtime dial — a box that can't register
  // within ~30 attempts is a deployment-ordering problem, not something an operator tunes.
  const connectOnce = async () => {
    for (let attempt = 1; attempt <= 30; attempt++) {
      if (stream.stopped) return false;
      try {
        await stream.start();
        console.log(`box ${boxId}: registered over mTLS stream; awaiting pushed jobs (buffered outcomes replayed on connect)`);
        return true;
      } catch (e) {
        console.log(`box ${boxId}: connect attempt ${attempt} not ready (${e.message}); retrying`);
        await new Promise((r) => setTimeout(r, 1000));
      }
    }
    return false;
  };

  // Supervise loop (Phase 7 §1.3, P7-4): on a stream drop, RECONNECT IN-PROCESS — the same BoxStream
  // instance is reused, so its in-memory outcome buffer survives the drop and unacked outcomes are
  // replayed on the next register_ack. We do NOT exit on a mere stream close anymore (that would lose
  // the buffer and defeat the no-duplicate guarantee). Only a failed initial/repeated connect (Brain
  // unreachable for the whole retry budget) exits non-zero for the orchestrator to restart us.
  while (!stream.stopped) {
    const ok = await connectOnce();
    if (!ok) {
      console.error(`box ${boxId}: could not (re)register within the retry budget; exiting for restart`);
      process.exit(1);
    }
    // Wait for THIS connection to close, then loop to reconnect (buffer intact).
    await new Promise((resolve) => {
      if (!stream.sock) return resolve();
      stream.sock.once("close", () => {
        if (!stream.stopped) console.error(`box ${boxId}: control stream closed; reconnecting (outcome buffer retained)`);
        resolve();
      });
    });
  }
}

main().catch((e) => {
  console.error(`box: fatal: ${e.message}`);
  process.exit(1);
});
