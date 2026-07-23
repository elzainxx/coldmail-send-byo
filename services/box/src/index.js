// Sender Box entrypoint (Phase 3 — the minimal send path). The box dials the Brain over mTLS,
// holds a persistent control stream (src/stream.js), registers (id + token), heartbeats, and —
// when the Brain pushes a job — runs the full SMTP session itself (src/smtp.js). The box has NO
// DB access and NO KMS: it receives an already-decrypted credential per send, holds it in memory
// only, and zeroes it after the session (CLAUDE.md Rule 6; Phase 3 §3.4/§4.4).

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
    metadata: {
      box_version: env("BOX_VERSION", "1.0.0"),
      hostname: env("HOSTNAME", ""),
    },
  });

  // Register with bounded retry — the Brain (and its stream listener) may still be coming up.
  let connected = false;
  for (let attempt = 1; attempt <= 30 && !connected; attempt++) {
    try {
      await stream.start();
      connected = true;
      console.log(`box ${boxId}: registered over mTLS stream; awaiting pushed jobs`);
    } catch (e) {
      console.log(`box ${boxId}: connect attempt ${attempt} not ready (${e.message}); retrying`);
      await new Promise((r) => setTimeout(r, 1000));
    }
  }
  if (!connected) {
    console.error(`box ${boxId}: could not register; exiting`);
    process.exit(1);
  }

  const shutdown = () => {
    console.log(`box ${boxId}: shutdown`);
    stream.stop();
    process.exit(0);
  };
  process.on("SIGTERM", shutdown);
  process.on("SIGINT", shutdown);

  // Keep the process alive on the held stream. If the stream drops, exit non-zero so the
  // orchestrator restarts us (Phase 3 runs no in-box reconnect loop; the buffer/replay is Phase 7).
  stream.sock.on("close", () => {
    if (!stream.stopped) {
      console.error(`box ${boxId}: control stream closed; exiting for restart`);
      process.exit(1);
    }
  });
}

main().catch((e) => {
  console.error(`box: fatal: ${e.message}`);
  process.exit(1);
});
