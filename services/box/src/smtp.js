// smtp.js — the full SMTP session the box runs itself (Phase 3 §4). Dependency-free: Node's
// built-in net/tls only (the box ships NO npm packages — structural no-DB/no-KMS boundary, and
// no SMTP library either). Sequence (§4.1): connect, EHLO, STARTTLS (587/25) or implicit TLS
// (465), AUTH, MAIL FROM, RCPT TO, DATA, QUIT.
//
// LOCKED security properties (§4.2/§4.4):
//   - AUTH is NEVER sent before TLS. On 587/25, if the server does not advertise STARTTLS the
//     session ABORTS BEFORE AUTH (error_reason=starttls_unavailable) — the credential is never
//     transmitted over an unencrypted connection (S5).
//   - The plaintext credential is memory-only (held in a MemoryCredential), never written to
//     disk, never logged, never in a metric/error/exception payload, and zeroed on EVERY exit
//     path — clean QUIT, SMTP error at AUTH, connection failure, timeout, thrown exception (S1/S3).
//   - Any SMTP response line is scrubbed of the credential before it leaves this module (§4.4).

import net from "node:net";
import tls from "node:tls";
import { CredentialZeroedError } from "./creds.js";

// SMTPError carries an internal error_reason slug (§2.4) plus the last SMTP response line. It is
// deliberately built to NEVER carry the credential (only the server's own response text).
export class SMTPError extends Error {
  constructor(reason, smtpResponse) {
    super(reason);
    this.name = "SMTPError";
    this.reason = reason; // internal slug: connect_refused | auth_failed | starttls_unavailable | ...
    this.smtpResponse = smtpResponse || "";
  }
}

// A tiny line-oriented SMTP reader/writer over a socket. Reads multiline replies (RFC 5321:
// "250-" continuation lines, "250 " final line) and returns { code, text }.
class SmtpConn {
  constructor(socket, timeoutMs) {
    this.sock = socket;
    this.timeoutMs = timeoutMs;
    this.buf = "";
    this.waiters = [];
    this.closed = false;
    this.sock.setEncoding("utf8");
    this.sock.on("data", (d) => this.#onData(d));
    this.sock.on("error", () => this.#fail());
    this.sock.on("close", () => this.#fail());
  }

  #fail() {
    this.closed = true;
    for (const w of this.waiters.splice(0)) w.reject(new SMTPError("connection_closed", ""));
  }

  #onData(chunk) {
    this.buf += chunk;
    let idx;
    // A complete reply ends with a line "NNN " (space after code) — collect until we see one.
    while ((idx = this.buf.indexOf("\n")) >= 0) {
      const line = this.buf.slice(0, idx).replace(/\r$/, "");
      this.buf = this.buf.slice(idx + 1);
      this.#pushLine(line);
    }
  }

  #pushLine(line) {
    if (!this._acc) this._acc = [];
    this._acc.push(line);
    // Final line of a reply: "NNN<space>..."; continuation: "NNN-...".
    if (/^\d{3} /.test(line)) {
      const code = parseInt(line.slice(0, 3), 10);
      const text = this._acc.join("\n");
      this._acc = null;
      const w = this.waiters.shift();
      if (w) w.resolve({ code, text });
    }
  }

  // read() awaits the next complete SMTP reply.
  read() {
    return new Promise((resolve, reject) => {
      if (this.closed) return reject(new SMTPError("connection_closed", ""));
      this.waiters.push({ resolve, reject });
    });
  }

  write(line) {
    return new Promise((resolve, reject) => {
      if (this.closed) return reject(new SMTPError("connection_closed", ""));
      this.sock.write(line + "\r\n", "utf8", (err) => (err ? reject(new SMTPError("write_failed", "")) : resolve()));
    });
  }

  // writeRaw sends bytes with no CRLF appended (used for the DATA payload which manages its own).
  writeRaw(data) {
    return new Promise((resolve, reject) => {
      if (this.closed) return reject(new SMTPError("connection_closed", ""));
      this.sock.write(data, "utf8", (err) => (err ? reject(new SMTPError("write_failed", "")) : resolve()));
    });
  }

  end() {
    try { this.sock.end(); } catch { /* ignore */ }
    try { this.sock.destroy(); } catch { /* ignore */ }
  }
}

// connectPlain / upgradeTLS helpers -----------------------------------------------------------

function connectPlain(host, port, timeoutMs) {
  return new Promise((resolve, reject) => {
    const sock = net.connect({ host, port });
    const to = setTimeout(() => { sock.destroy(); reject(new SMTPError("timeout", "")); }, timeoutMs);
    sock.once("connect", () => { clearTimeout(to); resolve(sock); });
    sock.once("error", () => { clearTimeout(to); reject(new SMTPError("connect_refused", "")); });
  });
}

function connectImplicitTLS(host, port, timeoutMs, allowSelfSigned) {
  return new Promise((resolve, reject) => {
    const sock = tls.connect({ host, port, servername: host, rejectUnauthorized: !allowSelfSigned });
    const to = setTimeout(() => { sock.destroy(); reject(new SMTPError("timeout", "")); }, timeoutMs);
    sock.once("secureConnect", () => { clearTimeout(to); resolve(sock); });
    sock.once("error", () => { clearTimeout(to); reject(new SMTPError("connect_refused", "")); });
  });
}

function upgradeToTLS(plainSock, host, timeoutMs, allowSelfSigned) {
  return new Promise((resolve, reject) => {
    const sock = tls.connect({ socket: plainSock, servername: host, rejectUnauthorized: !allowSelfSigned });
    const to = setTimeout(() => { sock.destroy(); reject(new SMTPError("timeout", "")); }, timeoutMs);
    sock.once("secureConnect", () => { clearTimeout(to); resolve(sock); });
    sock.once("error", () => { clearTimeout(to); reject(new SMTPError("starttls_failed", "")); });
  });
}

// isPrivateHost reports whether a host is loopback/RFC-1918/link-local, i.e. a test/sink target
// on a private network. Certificate validation MAY be skipped ONLY for such targets, and ONLY
// as a test affordance (§4.2) — never for a public host. Mirrors the inspiration worker's
// shouldSkipCertValidation. A public hostname always requires a valid cert.
export function isPrivateHost(host) {
  if (!host) return false;
  if (host === "localhost" || host === "smtp-sink") return true;
  if (/^127\./.test(host) || host === "::1") return true;
  if (/^10\./.test(host)) return true;
  if (/^192\.168\./.test(host)) return true;
  if (/^172\.(1[6-9]|2\d|3[01])\./.test(host)) return true;
  if (/^169\.254\./.test(host)) return true;
  return false;
}

// expect asserts the reply code is in the accepted set; else throws an SMTPError with the given
// reason and the (scrubbed) server response.
function expect(reply, okCodes, reason) {
  if (!okCodes.includes(reply.code)) {
    throw new SMTPError(reason, reply.text);
  }
  return reply;
}

/**
 * runSmtpSession performs one full SMTP send, OR — with opts.stopAfterAuth — a connect+STARTTLS+
 * AUTH-only VERIFICATION that returns before any recipient-bearing command is issued (Phase 3
 * §2.2). The credential is provided as a MemoryCredential and used ONLY inside this function; the
 * caller zeroes it after (and this function tolerates it already being zeroed on the
 * abort-before-AUTH path). Returns { smtpResponse } on success. Throws SMTPError (reason +
 * scrubbed smtpResponse) on any failure — the caller maps it to a report. This function NEVER
 * logs and NEVER puts the secret into an error.
 *
 * stopAfterAuth IS THE WHOLE MECHANISM THAT MAKES VERIFY-ON-ADD SAFE (§2.2's own words). Omitting
 * job.dataBytes does NOT abort the send — dotStuff(undefined) coerces to the literal string
 * "undefined" and a REAL MESSAGE IS DELIVERED to job.recipient. So this flag must, and does,
 * `return` BEFORE step 5 (MAIL FROM, the first recipient-bearing command) rather than merely skip
 * assembling the body — there is no path through the "unless stopAfterAuth" branch below that can
 * reach DATA. Defense in depth: the verify caller (dispatch/verify.go) ALSO never sets
 * job.recipient/job.dataBytes on a verify job at all, so even a future regression in this branch
 * would find no address to send to.
 *
 * @param {object} job  { smtp:{host,port,username,security}, dataBytes:string, envelopeFrom, recipient }
 * @param {MemoryCredential} credential  memory-only plaintext secret
 * @param {object} opts { timeoutMs, stopAfterAuth }
 */
export async function runSmtpSession(job, credential, opts = {}) {
  const timeoutMs = opts.timeoutMs || 30000;
  const stopAfterAuth = opts.stopAfterAuth === true;
  const { host, port, username, security } = job.smtp;
  const allowSelfSigned = isPrivateHost(host); // test affordance ONLY (§4.2)

  let conn = null;
  let sock = null;
  try {
    // 1) Connect (implicit TLS at connect for 465; plain otherwise).
    if (security === "implicit_tls") {
      sock = await connectImplicitTLS(host, port, timeoutMs, allowSelfSigned);
    } else {
      sock = await connectPlain(host, port, timeoutMs);
    }
    conn = new SmtpConn(sock, timeoutMs);

    // Server greeting.
    expect(await withTimeout(conn.read(), timeoutMs), [220], "connect_refused");

    // 2) EHLO with the sending domain derived from from_address (§4.1, P3-11 default).
    const heloName = "mail." + sendingDomain(job.envelopeFrom);
    let ehlo = expect(await ehloOrHelo(conn, heloName, timeoutMs), [250], "ehlo_failed");

    // 3) STARTTLS if requested. ABORT BEFORE AUTH if not advertised (§4.2, S5).
    if (security !== "implicit_tls") {
      if (!advertisesStartTLS(ehlo.text)) {
        throw new SMTPError("starttls_unavailable", ehlo.text);
      }
      expect(await withTimeout(conn.write("STARTTLS").then(() => conn.read()), timeoutMs), [220], "starttls_unavailable");
      // Upgrade the socket and re-EHLO over the now-encrypted channel.
      const tlsSock = await upgradeToTLS(sock, host, timeoutMs, allowSelfSigned);
      conn = new SmtpConn(tlsSock, timeoutMs);
      sock = tlsSock;
      ehlo = expect(await ehloOrHelo(conn, heloName, timeoutMs), [250], "ehlo_failed");
    }

    // 4) AUTH — now, and only now, is the credential put on the wire (over TLS). The MECHANISM is
    // the server's choice (Phase 7 §1.3, M3-P7-OD-2): prefer LOGIN when advertised (no behavior
    // change for any server that already worked), fall back to PLAIN when only PLAIN is offered.
    // Neither offered is a clean, named failure — never a silent LOGIN attempt against a server
    // that never advertised it.
    const authMechanism = chooseAuthMechanism(ehlo.text);
    if (!authMechanism) {
      throw new SMTPError("auth_mechanism_unsupported", ehlo.text);
    }
    const authReply = authMechanism === "LOGIN"
      ? await doAuthLogin(conn, username, credential, timeoutMs)
      : await doAuthPlain(conn, username, credential, timeoutMs);

    // VERIFY STOPS HERE (§2.2). This is the entire mechanism: a `return` before step 5, the first
    // recipient-bearing command — not a body-assembly skip further down, where dotStuff's
    // String(undefined) coercion would still deliver a real message. AUTH's own 235 is the whole
    // outcome for a verify; there is no DATA reply to report on, so smtpResponse is AUTH's real
    // reply text. QUIT's 2000ms bound below is NOT reused here: its own comment says the outcome
    // is discarded "because the send already succeeded by this point" — false for a verify, where
    // AUTH's result IS the entire outcome. A verify's QUIT gets the full per-operation timeoutMs.
    if (stopAfterAuth) {
      try { await withTimeout(conn.write("QUIT").then(() => conn.read()), timeoutMs); } catch { /* best-effort */ }
      return { smtpResponse: scrubSecret(authReply.text, credential, username) };
    }

    // 5) MAIL FROM.
    expect(await cmd(conn, `MAIL FROM:<${job.envelopeFrom}>`, timeoutMs), [250], "mail_from_rejected");

    // 6) RCPT TO (single recipient in Phase 3).
    const rcpt = await cmd(conn, `RCPT TO:<${job.recipient}>`, timeoutMs);
    if (rcpt.code === 550 || rcpt.code === 551 || rcpt.code === 553) {
      throw new SMTPError("bounced", rcpt.text); // hard reject → sending→bounced (§5.4)
    }
    expect(rcpt, [250, 251], "rcpt_rejected");

    // 7) DATA.
    expect(await cmd(conn, "DATA", timeoutMs), [354], "data_rejected_4xx");
    const payload = dotStuff(job.dataBytes) + "\r\n.\r\n";
    await conn.writeRaw(payload);
    const dataReply = await withTimeout(conn.read(), timeoutMs);
    if (dataReply.code >= 500) throw new SMTPError("data_rejected_5xx", dataReply.text);
    if (dataReply.code >= 400) throw new SMTPError("data_rejected_4xx", dataReply.text);
    expect(dataReply, [250], "data_rejected_4xx");

    // 8) QUIT (best-effort). SAFETY BOUND, deliberately short and NOT timeoutMs (Phase 4 closeout
    // triage, Docs/history/open-decisions.md): the send already succeeded by this point (DATA got 250), so
    // QUIT's outcome is discarded either way — waiting the full per-operation timeoutMs here would
    // only slow down freeing this connection for no benefit. Not a tunable send-behavior dial.
    try { await withTimeout(conn.write("QUIT").then(() => conn.read()), 2000); } catch { /* ignore */ }

    return { smtpResponse: scrubSecret(dataReply.text, credential, username) };
  } catch (e) {
    // M3-P7-OD-8: a CredentialZeroedError means scrubSecret ran against an already-zeroed
    // buffer — the ordering invariant this session's report assembly depends on has broken.
    // Propagate it AS ITSELF, never re-mapped to an SMTPError: the caller (stream.js) must send
    // NO report at all on this path (Rule 5 — never invent an outcome), not a fabricated failure
    // report whose smtp_response happens to be empty.
    if (e instanceof CredentialZeroedError) throw e;
    // Re-map generic errors to an SMTPError; scrub any credential from the response text.
    if (e instanceof SMTPError) {
      e.smtpResponse = scrubSecret(e.smtpResponse, credential, username);
      throw e;
    }
    throw new SMTPError("timeout", "");
  } finally {
    // Close the socket on every exit path. (Credential zeroing is the caller's finally, but we
    // also never retained the plaintext outside the MemoryCredential here.)
    if (conn) conn.end();
    else if (sock) { try { sock.destroy(); } catch { /* ignore */ } }
  }
}

// --- helpers ---------------------------------------------------------------------------------

function withTimeout(promise, ms) {
  return Promise.race([
    promise,
    new Promise((_, reject) => setTimeout(() => reject(new SMTPError("timeout", "")), ms)),
  ]);
}

async function cmd(conn, line, timeoutMs) {
  await conn.write(line);
  return withTimeout(conn.read(), timeoutMs);
}

async function ehloOrHelo(conn, name, timeoutMs) {
  await conn.write("EHLO " + name);
  const reply = await withTimeout(conn.read(), timeoutMs);
  if (reply.code === 250) return reply;
  // Fall back to HELO for servers that reject EHLO.
  await conn.write("HELO " + name);
  return withTimeout(conn.read(), timeoutMs);
}

function advertisesStartTLS(ehloText) {
  return /(^|\n)\d{3}[ -]STARTTLS\b/i.test(ehloText);
}

// advertisedAuthMechanisms parses the EHLO response's "250-AUTH ..." line (RFC 4954) into the
// set of mechanism names the server offers, e.g. "250-AUTH LOGIN PLAIN" -> ["LOGIN","PLAIN"].
// Some servers use the obsolete "250-AUTH=LOGIN PLAIN" form; both are accepted.
function advertisedAuthMechanisms(ehloText) {
  const m = /(^|\n)\d{3}[ -]AUTH[= ]+([A-Z0-9=_-]+(?:\s+[A-Z0-9=_-]+)*)/i.exec(ehloText);
  if (!m) return [];
  return m[2].trim().split(/\s+/).map((s) => s.toUpperCase());
}

// chooseAuthMechanism picks which AUTH mechanism to speak (Phase 7 §1.3, M3-P7-OD-2: AUTH PLAIN
// is in scope — "users bring their own servers, so the mechanism is the provider's choice", not
// module 3's). Prefers LOGIN when advertised (the mechanism this box has always spoken, so a
// server offering both sees no behavior change); falls back to PLAIN when the server offers PLAIN
// but not LOGIN. A server offering NEITHER is a clean, named failure rather than a silent
// LOGIN attempt against a server that never advertised it.
function chooseAuthMechanism(ehloText) {
  const offered = advertisedAuthMechanisms(ehloText);
  if (offered.includes("LOGIN")) return "LOGIN";
  if (offered.includes("PLAIN")) return "PLAIN";
  return null;
}

// doAuthLogin runs AUTH LOGIN (base64 username then password). The password is read from the
// MemoryCredential and base64-encoded in a transient Buffer that is zeroed immediately. On a
// non-235 reply it throws auth_failed WITHOUT including the credential.
async function doAuthLogin(conn, username, credential, timeoutMs) {
  const start = await cmd(conn, "AUTH LOGIN", timeoutMs);
  expect(start, [334], "auth_failed");
  await conn.write(Buffer.from(username, "utf8").toString("base64"));
  const userReply = await withTimeout(conn.read(), timeoutMs);
  expect(userReply, [334], "auth_failed");

  // Encode the password from the memory-only buffer; zero the transient b64 immediately.
  let b64 = null;
  await credential.use((buf) => {
    b64 = buf.toString("base64");
  });
  await conn.write(b64);
  // Best-effort scrub of the transient base64 string (JS strings are immutable, so at least drop
  // the reference; the durable secret lives only in the MemoryCredential, which the caller zeroes).
  b64 = null;

  const authReply = await withTimeout(conn.read(), timeoutMs);
  if (authReply.code !== 235) {
    // NEVER include the credential; only the server's own response text (scrubbed defensively).
    throw new SMTPError("auth_failed", scrubSecret(authReply.text, credential, username));
  }
  return authReply;
}

// doAuthPlain runs AUTH PLAIN (RFC 4616): one base64("\0"+username+"\0"+password) blob sent as
// the SASL initial response on the AUTH PLAIN line itself (no continuation round trip, unlike
// LOGIN's two-step 334/334). Phase 7 §1.3/M3-P7-OD-2: module 3 ships PLAIN because the remote
// server — not this box — chooses which mechanisms it advertises. The transient blob buffer is
// dropped immediately after use (the durable secret lives only in the MemoryCredential, zeroed by
// the caller).
async function doAuthPlain(conn, username, credential, timeoutMs) {
  let blob = null;
  await credential.use((buf) => {
    blob = Buffer.concat([Buffer.from("\0" + username + "\0", "utf8"), buf]).toString("base64");
  });
  const start = await cmd(conn, "AUTH PLAIN " + blob, timeoutMs);
  blob = null;

  if (start.code !== 235) {
    throw new SMTPError("auth_failed", scrubSecret(start.text, credential, username));
  }
  return start;
}

// sendingDomain extracts the domain from an email address for the HELO name (§4.1).
function sendingDomain(addr) {
  const at = String(addr || "").lastIndexOf("@");
  return at >= 0 ? addr.slice(at + 1) : "localhost";
}

// dotStuff applies SMTP transparency (RFC 5321 §4.5.2): a line beginning with "." gets an extra ".".
function dotStuff(data) {
  return String(data).replace(/\r\n/g, "\n").replace(/\n/g, "\r\n").replace(/(^|\r\n)\./g, "$1..");
}

// scrubSecret removes the credential from a string defense-in-depth (§4.4; extended Phase 7 §1.3,
// M3-P7-OD-2). It reads the secret from the MemoryCredential ONLY to redact it.
//
// CRITICAL: the credential travels the AUTH exchange BASE64-encoded, and WHICH encoding depends
// on the mechanism — the remote server's choice, not this box's (§1.3's general lesson: "a scrub
// is complete only against the set of encodings the credential can take on the wire, and in
// module 3 that set is chosen by a party we do not control"). AUTH LOGIN sends base64(password)
// alone, always at a 3-byte-group-aligned offset (offset 0). AUTH PLAIN sends
// base64("\0"+username+"\0"+password) as ONE blob — base64 groups bytes in 3s, so the password's
// bytes inside that blob start at whichever of the three possible alignments
// (len("\0"+username+"\0") mod 3) the username's length produces, and at a non-zero alignment
// base64(password) alone is NOT a substring of the blob's base64 text (verified by execution,
// §1.3 GAP 1) — each alignment produces a genuinely different character run that must be computed
// and redacted separately; there is no way to derive one alignment's needle from another's.
//
// Redacted forms, all computed from the SAME underlying plaintext buffer:
//   1. utf8 plaintext
//   2. base64(password) at offset 0 (AUTH LOGIN's wire form; also PLAIN's alignment when
//      len("\0"+username+"\0") % 3 == 0)
//   3. base64url variant of #2
//   4-5. base64(password) as it appears when password is preceded by 1 or 2 extra bytes before
//      the next 3-byte boundary — the other two alignments AUTH PLAIN's blob can produce
//   6. the FULL AUTH PLAIN blob base64("\0"+username+"\0"+password), when username is known —
//      strictly stronger than redacting a substring, since a server echoing the whole argument is
//      redacted whole rather than relying on the substring match to land inside it
//
// M3-P7-OD-8 (fail-closed beats fail-silent on a credential path): if the buffer is ALREADY
// ZEROED, credential.use() throws (creds.js) and this function lets that THROW PROPAGATE rather
// than catching it and returning the input unchanged. Module 2 returned the input unchanged on a
// zeroed buffer — a scrub moved (by a future edit) to after the zero would silently return raw
// provider text and report success. module 3 fails closed instead: the caller's report never
// reaches Brain, which reclaims the job by lease expiry rather than fabricating an outcome (Rule
// 5) — safe because the caller's own `finally` still zeroes on this throw path (Rule 6). Never
// logs.
function scrubSecret(text, credential, username) {
  if (!text) return text || "";
  const forms = [];
  credential.use((buf) => {
    forms.push(buf.toString("utf8")); // plaintext

    const b64 = buf.toString("base64");
    forms.push(b64); // AUTH LOGIN wire form (offset 0)
    forms.push(b64.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "")); // base64url

    // AUTH PLAIN's other two byte alignments (offsets 1 and 2): base64 groups bytes in 3s, so
    // when the password does not start at a 3-byte boundary inside the blob, only the password's
    // bytes that fall on a CLEAN 3-byte group of their own re-encode to the same characters
    // regardless of what precedes them — the bytes straddling a group boundary depend on
    // neighboring bytes too and cannot be redacted by a fixed substring. Dropping `skip` leading
    // bytes (the ones that would straddle) and encoding what's left of a whole-group length
    // isolates exactly that safely-redactable middle slice. A password under 3 bytes at a given
    // alignment has no such slice at all (null) — every one of its bytes straddles — so there is
    // nothing to redact at that alignment and the form is skipped, not a false empty match.
    for (const skip of [1, 2]) {
      const usableLen = Math.floor((buf.length - skip) / 3) * 3;
      if (usableLen > 0) {
        forms.push(buf.slice(skip, skip + usableLen).toString("base64"));
      }
    }

    // The full AUTH PLAIN blob, when the username is known — strictly stronger than a substring
    // match, since it also catches a server echoing the entire AUTH argument verbatim.
    if (username) {
      forms.push(Buffer.concat([Buffer.from("\0" + username + "\0", "utf8"), buf]).toString("base64"));
    }
  });
  // LONGEST FIRST. The full-blob form fully contains every middle-slice form as a substring, so
  // redacting a shorter needle first can consume characters the full-blob needle needed for its
  // own exact match — the full-blob redaction then silently fails to fire, leaving whatever
  // shorter needle didn't cover (verified by execution: a shortest-first order left 2 raw
  // password bytes decodable in the surviving blob prefix). Sorting longest-first guarantees the
  // strongest, most specific match consumes the text before any shorter substring of it gets a
  // chance to fragment it.
  const bySize = forms.filter(Boolean).sort((a, b) => b.length - a.length);
  let out = text;
  for (const f of bySize) {
    if (out.includes(f)) out = out.split(f).join("[redacted]");
  }
  return out;
}
