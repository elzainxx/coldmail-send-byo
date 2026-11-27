# Accepted risk: the credential master key has no rotation in V1

**Status: ACCEPTED for V1 by the owner (decision of 2026-11-27); awaiting the owner's signature below.** Go-live checklist item: [`go-live-checklist.md`](./go-live-checklist.md) section 12. The work to remove the risk stays registered as [`FW-M3-4`](./future-work.md) (not deleted, not scheduled).

## The risk, in plain words

Every user's SMTP password is stored encrypted in `send_byo.accounts.credential_ciphertext`. One 32-byte key, held outside the database, decrypts all of them, and **there is no way to change that key in V1**.

- **If the key leaks** (a copy of the key file, a backup of it, root on the Control host) together with any copy of the database, every user's SMTP password is exposed at once. Changing the key afterwards does not help: no code path can decrypt under the old key and re-encrypt under a new one, so a changed key only makes every stored credential unreadable. The honest way out is a coordinated re-add campaign: every user re-enters their own SMTP password, and the operator cannot do it for them because the operator has never held a plaintext password ([`operator-runbook.md`](./operator-runbook.md#disaster-recovery-key-backup-and-rotation) section 3). Until a user does, that user's accounts cannot send.
- **A retired key stays live.** Any database backup made before a change still opens with the old key for as long as that key exists. The only thing that makes an exposed password harmless is the user changing it at their mail provider.
- **The key-id label does not rotate anything.** `credential_key_id` is bound into each ciphertext and checked at decrypt time, so a Control configured with the wrong label refuses the row loudly instead of failing silently. It labels one key; it is not a keyring (a Control resolves exactly one label).
- **Losing the key is the opposite failure and is also total** (checklist item 8: backup and recovery drill). This record covers only the leak and rotation side.

## Why it is accepted for V1

The owner accepted it on 2026-11-27. The reasons on record in this repository:

1. Closing it needs a second Decrypt call site outside the send path (a Control-side bulk re-encrypt), which CLAUDE.md Rule 6 forbids adding casually. It is a design decision with its own ruling and ADR, not polish ([`FW-M3-4`](./future-work.md)).
2. Exposure needs a breach of the Control host or of a key backup, plus a copy of the database. A compromised Control host is total compromise whatever the key scheme ([`security-posture.md`](./security-posture.md)), so rotation would not undo what that attacker already read.
3. A manual way out exists (the re-add campaign above). It is slow, not impossible.

## Mitigations that exist today (each checked against the code or a gate)

- The key is never in the database: a database dump alone is ciphertext only (`internal/credential`; runbook section 1).
- AES-256-GCM, 12-byte nonce, AAD = account id plus key id. A ciphertext moved to another row or relabelled fails authentication and releases nothing (`credential.Decryptor.Decrypt`, `TestDecrypt_TamperFailsClosed`).
- The key-id label check: a row whose label this Control does not resolve is refused, never tried with the wrong key.
- Control is the only decryptor. The admin and platform APIs hold an Encrypt-only primitive with no Decrypt method; the Decryptor is wired only into the send and verify path; no route returns a credential (only `has_credential`).
- The worker holds no key and no database driver (`dependencies: {}`). It receives one decrypted secret per send over mutual TLS, keeps it in memory only (`MemoryCredential`) and zeroes it on every exit of a send. Residual, stated: the shutdown path does not zero an in-flight credential ([`future-work.md`](./future-work.md) section 4a).
- Key file handling: mode 400, owned by the Control's user (install guide), mounted by the Control only (`docker-compose.yml`); the `secret_ownership` gate fails if the worker's uid can read it, and `no_secret_leak` scans for it in logs.
- Off-host encrypted backup and a recovery drill before the first real credential (checklist item 8).

**Not a mitigation, stated so nobody assumes it:** the repository reads the key from a file (`KMS_DEV_KEY_FILE`); there is no cloud KMS client. Separating Encrypt from Decrypt at infrastructure level is a deployment sign-off (checklist item 4), not something the code enforces.

## What rotation would need (not built)

A Control-side re-encrypt path ruled and audited (the second Decrypt call site); a dual-read window (a Decryptor that resolves the old and the new `credential_key_id` at once, so no send freeze is needed); a re-encrypt run over every `send_byo.accounts` row (decrypt under the old key, encrypt under the new key with a fresh nonce and the new AAD, one transaction per row, resumable, counted by label until the old label reaches zero); the new key backed up and drilled before cutover; the runbook section 3 rewritten from "not an operator action" to a procedure; tests against real Postgres for the crash-midway and the mixed-label states. Trigger to revisit: the first real rotation need in production ([`FW-M3-4`](./future-work.md)).

## Signature

I have read this record and accept the risk above for V1.

Owner: ____  Date: ____
