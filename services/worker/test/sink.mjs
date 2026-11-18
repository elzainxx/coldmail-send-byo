// A REAL, controllable throwaway SMTP server for worker unit tests (no mocks — a genuine socket
// server the real src/smtp.js talks to). Configurable to exercise the worker's security paths:
//   - advertiseStartTLS:false  → the worker must ABORT BEFORE AUTH (S5).
//   - authResult:"fail"        → 535 at AUTH (S1 failed-AUTH trigger; S3 error exit).
//   - authResult:"ok"          → full accept (positive control).
// It records every line the client sent so a test can assert the credential was NEVER transmitted
// on the no-STARTTLS path (S5).

import net from "node:net";
import tls from "node:tls";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
// Reuse the dev sink leaf cert for TLS (loopback target; the worker skips cert validation for
// private hosts as the documented test affordance, §4.2).
const certDir = path.resolve(here, "../../../secrets/dev/certs");

export function startSink(opts = {}) {
  const advertiseStartTLS = opts.advertiseStartTLS !== false; // default true
  const authResult = opts.authResult || "ok"; // "ok" | "fail"
  // authMechanisms: which mechanisms the EHLO response advertises, and in which order (the worker's
  // chooseAuthMechanism prefers LOGIN when offered — Phase 7 §1.3, M3-P7-OD-2). Default both, LOGIN
  // first (today's existing behavior, unchanged for any server that already worked). Pass
  // ["PLAIN"] to force the worker down the PLAIN path, or [] to test the no-mechanism-in-common case.
  const authMechanisms = opts.authMechanisms || ["LOGIN", "PLAIN"];
  // echoAuthInError: a hostile/verbose server that quotes the received AUTH argument (the
  // base64-encoded password for LOGIN, or the full base64 blob for PLAIN) back in its 535 text.
  // Used to prove scrubSecret redacts the base64 form, not just the plaintext (§4.4/§1.3).
  // Default false (well-behaved server).
  const echoAuthInError = opts.echoAuthInError === true;
  // echoUsernameInError: a REAL provider-shaped response that ALSO echoes the username in
  // plaintext (Phase 7 §10 T1's own required fixture: "a genuine provider-shaped response that
  // echoes the username, verbatim from Google's form ... extended to also echo the offending
  // base64 argument"). The username is not a secret (M3-P7-OD-3) and must SURVIVE the scrub;
  // this option produces the realistic response shape a scrub test needs to prove that against.
  const echoUsernameInError = opts.echoUsernameInError === true;
  // captureData: record the full DATA payload (dot-unstuffed, joined lines) so a test can assert
  // on the ACTUAL delivered body — the only real proof a message was genuinely sent with specific
  // content (Module 3 Phase 3 T3b's negative control on the dataBytes hazard).
  const captureData = opts.captureData === true;
  // rejectData: reject DATA with a permanent 5xx (Module 3 Phase 3 T3's negative control) — a
  // fault reachable ONLY by the send path (verify never issues MAIL FROM/RCPT/DATA at all), so it
  // proves the two paths are genuinely distinct where they SHOULD be, the counterpart to
  // advertiseStartTLS:false proving they are genuinely shared where they SHOULD be.
  const rejectData = opts.rejectData === true;
  const received = []; // every line the client sent (plaintext + TLS phases)
  let tlsActive = false;
  let dataLines = null;
  let capturedBody = null;
  // messages: the DATA payload of EVERY message the sink accepted, exactly as the wire carried it -- CRLF line
  // endings, SMTP dot-stuffing undone (RFC 5321 4.5.2), without the terminating "." line (Phase 3 close-out C5:
  // the plain-text part's wire test compares these bytes with the assembled message). Unlike capturedBody
  // (LF-joined, only the last message, only with captureData) this is always recorded.
  const messages = [];

  const tlsOpts = {
    cert: fs.readFileSync(path.join(certDir, "sink.crt")),
    key: fs.readFileSync(path.join(certDir, "sink.key")),
  };

  const server = net.createServer((sock) => handleConn(sock, false));

  function handleConn(sock, isTls) {
    sock.setEncoding("utf8");
    let buf = "";
    let inData = false;
    // A server greets ONCE, on the plaintext connection. After STARTTLS (RFC 3207 §4.2) it does NOT greet again — the
    // client simply restarts the session with EHLO. This used to greet on the secure socket too, which put an unsolicited
    // "220" on the wire ahead of the reply to the client's post-TLS EHLO. src/smtp.js drops a reply that arrives while no
    // read() is pending (#pushLine), so whether the stray line was swallowed or taken for the EHLO reply — shifting every
    // later reply by one, so AUTH LOGIN was "answered" with a 250 — depended on event-loop timing between TLS 'secureConnect'
    // and the first data event. MEASURED 2026-11-08: on the Windows dev host the stray line arrived before any read() was
    // pending and was swallowed (every test green); on Linux — Node 20.20 AND Node 24.22, 3 of 3 runs each — it arrived
    // after, was taken for the EHLO reply, and 10 tests failed with auth_failed, the S1/S3 credential-scrub tests among
    // them. So it was never a Node-version difference; the dev host's timing was hiding it. Pinned by sink_fidelity.test.mjs.
    if (!isTls) write(sock, "220 test-sink ESMTP ready");
    sock.on("data", (chunk) => {
      buf += chunk;
      let idx;
      while ((idx = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, idx).replace(/\r$/, "");
        buf = buf.slice(idx + 1);
        received.push((isTls ? "[tls] " : "[plain] ") + line);
        onLine(sock, line, isTls, () => (inData = true), () => (inData = false), () => inData);
      }
    });
    sock.on("error", () => {});
  }

  function onLine(sock, line, isTls, setData, clearData, inData) {
    if (inData()) {
      if (line === ".") {
        messages.push((sock._msgLines || []).join("\r\n"));
        sock._msgLines = [];
        if (captureData && dataLines) capturedBody = dataLines.join("\n");
        dataLines = null;
        clearData();
        write(sock, "250 2.0.0 Ok: queued as SINK12345");
        return;
      }
      (sock._msgLines ||= []).push(line.startsWith("..") ? line.slice(1) : line);
      if (captureData) {
        if (!dataLines) dataLines = [];
        // RFC 5321 §4.5.2 transparency: a line starting with ".." on the wire means a real leading
        // "." in the body — undo exactly one level of dot-stuffing when capturing.
        dataLines.push(line.startsWith("..") ? line.slice(1) : line);
      }
      return;
    }
    const up = line.toUpperCase();
    if (up.startsWith("EHLO") || up.startsWith("HELO")) {
      const caps = ["250-test-sink greets you"];
      if (advertiseStartTLS && !isTls) caps.push("250-STARTTLS");
      if (authMechanisms.length > 0) caps.push("250-AUTH " + authMechanisms.join(" "));
      caps.push("250 8BITMIME");
      write(sock, caps.join("\r\n"));
    } else if (up === "STARTTLS") {
      if (!advertiseStartTLS) { write(sock, "502 not supported"); return; }
      write(sock, "220 ready to start TLS");
      const secure = new tls.TLSSocket(sock, { isServer: true, ...tlsOpts });
      tlsActive = true;
      handleConn(secure, true);
    } else if (up.startsWith("AUTH PLAIN ")) {
      // AUTH PLAIN is a ONE-SHOT: the base64("\0"+user+"\0"+pass) blob rides on the AUTH line
      // itself (RFC 4616), no 334 continuation. `line` here is "AUTH PLAIN <blob>" verbatim, so
      // the offending argument for an echo is the blob substring after the mechanism name.
      const blob = line.slice("AUTH PLAIN ".length);
      if (authResult === "fail") {
        if (echoUsernameInError) {
          // Real provider shape (Phase 7 §10 T1): Google-style prose PLUS the echoed raw
          // argument. Decode the blob to extract the plaintext username for the prose half —
          // a real server has the username in hand at auth-decision time regardless of mechanism.
          const parts = Buffer.from(blob, "base64").toString("utf8").split("\0");
          const plainUser = parts[1] || "";
          write(sock, `535-5.7.8 Username and Password not accepted for ${plainUser}. Learn more at\r\n535 5.7.8  https://support.example.com/mail/?p=BadCredentials, argument '${blob}'`);
        } else if (echoAuthInError) {
          write(sock, `535 5.7.8 Authentication failed for argument '${blob}'`);
        } else {
          write(sock, "535 5.7.8 Authentication credentials invalid");
        }
      } else {
        write(sock, "235 2.7.0 Authentication successful");
      }
    } else if (up.startsWith("AUTH")) {
      // AUTH LOGIN: reply 334, then username(334), then password → 235/535.
      write(sock, "334 VXNlcm5hbWU6"); // "Username:"
      sock._authStep = "user";
    } else if (sock._authStep === "user") {
      // `line` here is base64(username) — decode it now so a later echo can quote the PLAINTEXT
      // username (a real provider has already decoded it by auth-decision time).
      sock._authUser = Buffer.from(line, "base64").toString("utf8");
      write(sock, "334 UGFzc3dvcmQ6"); // "Password:"
      sock._authStep = "pass";
    } else if (sock._authStep === "pass") {
      sock._authStep = null;
      if (authResult === "fail") {
        // `line` here is the base64-encoded password the client just sent. A hostile server may
        // echo it in the error; a well-behaved one does not.
        if (echoUsernameInError) {
          write(sock, `535-5.7.8 Username and Password not accepted for ${sock._authUser || ""}. Learn more at\r\n535 5.7.8  https://support.example.com/mail/?p=BadCredentials, argument '${line}'`);
        } else if (echoAuthInError) {
          write(sock, `535 5.7.8 Authentication failed for argument '${line}'`);
        } else {
          write(sock, "535 5.7.8 Authentication credentials invalid");
        }
      } else {
        write(sock, "235 2.7.0 Authentication successful");
      }
    } else if (up.startsWith("MAIL FROM")) {
      write(sock, "250 2.1.0 Ok");
    } else if (up.startsWith("RCPT TO")) {
      write(sock, "250 2.1.5 Ok");
    } else if (up === "DATA") {
      if (rejectData) { write(sock, "554 5.6.0 rejected for test"); return; }
      write(sock, "354 End data with <CR><LF>.<CR><LF>");
      sock._msgLines = [];
      setData();
    } else if (up === "QUIT") {
      write(sock, "221 2.0.0 Bye");
      sock.end();
    } else {
      write(sock, "250 2.0.0 Ok");
    }
  }

  function write(sock, line) {
    try { sock.write(line + "\r\n"); } catch { /* ignore */ }
  }

  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      const port = server.address().port;
      resolve({
        port,
        received,
        tlsWasActive: () => tlsActive,
        get capturedBody() { return capturedBody; },
        messages,
        close: () => new Promise((r) => server.close(r)),
      });
    });
  });
}
