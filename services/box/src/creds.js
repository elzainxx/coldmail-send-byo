// Memory-only credential holder (CLAUDE.md Rule 6). In Phase 0 the box has no real SMTP
// credential to hold yet (no send path), but the holder that later phases MUST use is
// established here: credentials live in a Buffer, are never logged/persisted/serialized,
// and are zeroed on EVERY exit path. The box has NO DB and NO KMS — it can only ever
// RECEIVE a decrypted credential from the Brain over mTLS at send time (Phase 5+).

// CredentialZeroedError is thrown by use() on an already-zeroed buffer (Phase 7 §9.1,
// M3-P7-OD-8). A distinguishable class — not a bare Error — so callers up the stack (smtp.js's
// scrubSecret, stream.js's #runJob/#runVerify) can recognize this specific fault and let it
// propagate as a NO-REPORT exit rather than folding it into a generic SMTPError and sending a
// fabricated outcome (Rule 5: never invent an outcome the box did not report).
export class CredentialZeroedError extends Error {
  constructor() {
    super("credential already zeroed");
    this.name = "CredentialZeroedError";
  }
}

export class MemoryCredential {
  #buf; // Buffer | null — the only place the plaintext ever lives

  constructor(plaintext) {
    // Accept string or Buffer; copy into a private Buffer we control.
    this.#buf = Buffer.isBuffer(plaintext) ? Buffer.from(plaintext) : Buffer.from(String(plaintext), "utf8");
  }

  /** Use the secret within a callback; never returns it to a caller that could log it. */
  use(fn) {
    if (this.#buf === null) throw new CredentialZeroedError();
    return fn(this.#buf);
  }

  /** Zero the buffer and drop the reference. Idempotent. Call on EVERY exit path. */
  zero() {
    if (this.#buf !== null) {
      this.#buf.fill(0);
      this.#buf = null;
    }
  }

  /** Never expose the secret in logs, JSON, or string coercion. */
  toString() { return "[MemoryCredential redacted]"; }
  toJSON() { return "[MemoryCredential redacted]"; }
  [Symbol.for("nodejs.util.inspect.custom")]() { return "[MemoryCredential redacted]"; }
}
