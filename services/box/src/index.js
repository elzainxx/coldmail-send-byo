// Phase-0 SKELETON Sender Box entrypoint. Scope: start, dial the Brain over mTLS, register
// (id + one-time token), then heartbeat on an interval. NO SMTP session, NO real send —
// those are Phase 5+. The box has NO DB access and NO KMS (structural: no such deps here).

import { BoxLink } from "./boxlink.js";

function env(k, def) {
  const v = process.env[k];
  return v === undefined || v === "" ? def : v;
}

async function main() {
  const link = new BoxLink({
    host: env("BRAIN_HOST", "brain"),
    port: Number(env("BRAIN_BOXLINK_PORT", "8443")),
    boxId: env("BOX_ID", ""),
    token: env("BOX_TOKEN", ""),
    certPath: env("MTLS_CLIENT_CERT", "/secrets/mtls/box.crt"),
    keyPath: env("MTLS_CLIENT_KEY", "/secrets/mtls/box.key"),
    caPath: env("MTLS_CA_CERT", "/secrets/mtls/ca.crt"),
    serverName: env("BRAIN_SERVER_NAME", "brain"),
  });

  if (!link.boxId || !link.token) {
    console.error("box: BOX_ID and BOX_TOKEN are required (out-of-band provisioned)");
    process.exit(2);
  }

  const heartbeatSec = Number(env("BOX_HEARTBEAT_INTERVAL_SEC", "10"));

  // Register with bounded retry — the Brain may still be coming up.
  let registered = false;
  for (let attempt = 1; attempt <= 30 && !registered; attempt++) {
    try {
      await link.register();
      registered = true;
      console.log(`box ${link.boxId}: registered over mTLS`);
    } catch (e) {
      console.log(`box ${link.boxId}: register attempt ${attempt} not ready (${e.message}); retrying`);
      await new Promise((r) => setTimeout(r, 1000));
    }
  }
  if (!registered) {
    console.error(`box ${link.boxId}: could not register; exiting`);
    process.exit(1);
  }

  const timer = setInterval(async () => {
    try {
      await link.heartbeat();
      console.log(`box ${link.boxId}: heartbeat ok`);
    } catch (e) {
      console.error(`box ${link.boxId}: heartbeat error: ${e.message}`);
    }
  }, heartbeatSec * 1000);

  const shutdown = () => {
    clearInterval(timer);
    console.log(`box ${link.boxId}: shutdown`);
    process.exit(0);
  };
  process.on("SIGTERM", shutdown);
  process.on("SIGINT", shutdown);
}

main().catch((e) => {
  console.error(`box: fatal: ${e.message}`);
  process.exit(1);
});
