# ADR-0023: The one-click unsubscribe headers are built by the Control from one nullable column, and refused when they cannot be written safely

<!-- file: ADR-0023-the-unsubscribe-headers-are-built-by-the-control-from-one-nullable-column-and-refused-when-unsafe.md -->

- **Status:** accepted
- **Date:** 2026-11-27
- **Supersedes:** —
- **Superseded-by:** —

This is also the research note for the change (Phase 5 S4). Module 2 gets the identical change under its own number; the platform has one adapter, so the **column name, the header values and the capability name are the same** in both.

## Context

Every send now carries a per-recipient unsubscribe link, written by the platform. Mailbox providers expect a bulk sender to put it in two headers as well as in the body. What the sources say (fetched 2026-11-27, quoted):

- **RFC 8058 (one-click), section 3.1:** "The List-Unsubscribe-Post header MUST contain the single key/value pair `List-Unsubscribe=One-Click`", and the List-Unsubscribe header "MUST contain one HTTPS URI"; the message has one List-Unsubscribe and one List-Unsubscribe-Post header field. **Section 4:** both headers "MUST be covered by the signature and included in the `h=` tag of a valid DKIM-Signature". **RFC 2369 section 3.2:** the URL is written in angle brackets.
- **Google's sender guidelines for bulk senders:** marketing and subscribed messages "must support one-click unsubscribe, and include a clearly visible unsubscribe link in the message body"; both headers are shown in the example (`List-Unsubscribe-Post: List-Unsubscribe=One-Click` and `List-Unsubscribe: <https://…>`).

This module sends through **the user's own SMTP account**: the message is built by the worker, handed to the account's SMTP server at `DATA`, and any DKIM signing is done by that provider on the bytes the worker hands over (there is no signing code in this module). So the module can emit the headers, but it **cannot make the provider's DKIM signature cover them** (see "Not verified").

The platform adds one nullable column, `public.job_items.unsubscribe_url` (migration `platform_0017`, run before this module's migrations): a single https URL, at most 2000 characters, no whitespace, control character, `<` or `>`. `POST /v1/run` already freezes a denormalized copy of the rendered message into `send_byo.jobs` (subject, body, text, reply-to); the headers have to be built at send time from that frozen copy, so a retry, a reclaim or a restart sends the same message.

## Decision

1. **One more column on the frozen copy, copied by the same statement.** `send_byo_0060` adds `send_byo.jobs.unsubscribe_url` (nullable text, no default) and `jobs_unsubscribe_url_check` (NULL, or `^https://[^[:space:]<>]+$`, at most 2001 characters, no `[[:cntrl:]]`). `POST /v1/run`'s freeze statement (`J2`) copies `ji.unsubscribe_url` next to `ji.rendered_text`; the read (`J1`) is unchanged. A NULL stays NULL and the job behaves exactly as it did; the module's own test-send `INSERT` into `public.job_items` names no new column and is unchanged. No field is added to the run request (an `unsubscribe_url` field there is a `400`): the link rides on the platform's row, like the body.
   - *The length is a `char_length` predicate and not the regex's `{1,1993}`:* PostgreSQL limits a regex repetition bound to 255, so that regex would fail to create. The two bounds agree (8 for the scheme plus 1993 is 2001); any platform CHECK written with that regex has the same problem and needs the same form.
2. **The Control builds the two headers and the worker only relays them.** Both claim statements return the column; `dispatch.listUnsubscribeHeaders` turns a non-NULL link into `List-Unsubscribe: <URL>` and `List-Unsubscribe-Post: List-Unsubscribe=One-Click`, and `addHeaders` merges them into the Control-supplied header map (and refuses a name already present under any capitalisation, so each appears exactly once). The worker's existing "additional Control-supplied headers" path carries them; **no worker code changes**, which is what keeps every golden byte of a message without a link identical. A worker test over all 19 golden cases proves the variation stage leaves them alone (removing the two lines from the with-link render gives exactly the without-link render). `send_logs.headers` records the pair too, so the record still matches the bytes sent.
3. **The builder is header-safe and strictly stricter than the CHECK.** It refuses CR, LF, NUL, every control character, space, `<`, `>`, any byte above 0x7E (a raw 8-bit byte in a header needs SMTPUTF8, which this module does not negotiate), a non-`https` or empty value, and a header line over RFC 5322's 998 octets. It runs **before an account is selected**, so a refused link spends no slot of the daily limit; a refusal sends nothing and **requeues the job** with a fixed internal reason `header_invalid:<slug>` that never carries the URL (the link is a per-recipient capability). This follows the existing header-injection guard (`header_injection:<field>`), whose requeue-and-log shape it reuses; unlike that guard it does not wait for the account.
4. **The migration pre-flight requires the column by name**, as it does `rendered_text`: a platform that has not run `platform_0017` makes the migrator refuse, naming `unsubscribe_url`, with nothing applied.
5. **`GET /v1/describe` gains `capabilities.list_unsubscribe: true`** (additive; `plain_text_alternative`'s shape).

## Alternatives considered

- **A free header map on the run request** (the caller supplies headers): rejected by the platform's own decision (a single URL, never a free map: no header-injection surface) and by Rule 5 (a new request field).
- **Let the worker build the headers** from the link: the worker would then need its own copy of the safety rules and a new wire field; the Control already owns every other header and the injection guard.
- **Send the message without the headers when the link is unsafe** (degrade): rejected; a bulk message without a working link is not allowed by the platform, and a silent downgrade hides the defect. The cost of refusing is a requeue loop for a value that can only reach the row if the CHECK was bypassed or if the platform's rule is looser than this module's builder (a link of 979 to 2001 characters, or a non-ASCII one).
- **Make the module's CHECK as strict as the builder** (fail the run with `400` instead of requeueing): better operationally for those two edge cases, but the task is to mirror the platform's CHECK; the gap is recorded here and the platform can tighten its own rule (ASCII only, at most 978 characters) at any time without a change here.

## Consequences

- **Additive only (Rule 5):** one nullable column and one CHECK on this module's table, one capability, two entries in an existing map, nothing renamed or removed. An older Control against this schema never names the column and sends no headers. `send_byo_0060` is forward-only in production; its down drops the column with its data (the platform's own column still holds the link until its retention clears it), and belongs to a source revert because the claim statements name the column.
- **Order of deployment:** `platform_0017`, then this module's migrations (the pre-flight enforces it), then the Control, then the workers (nothing to do on the worker).
- **The dev fixture** `deploy/pg-init/00-platform-fixtures.sql` and every platform-shaped `public.job_items` the harness creates gained the nullable column; a developer's persistent dev volume that predates it must be reset (the migrator refuses, naming the column).
- **Content variation x URLs:** the `unique_url_params` toggle adds an inert parameter to the links in the HTML body, including a footer unsubscribe link; the header link is not varied. The platform's token is in the path and the platform ignores the extra query, but this is an interaction to confirm in the owner's real-provider test.
- **Config:** no new key (the two header names, the one-click value and the 998-octet line limit are protocol constants, not tuning).

## Not verified (named owner)

- **DKIM coverage of the two headers.** RFC 8058 section 4 requires the signature to cover them. This module does not sign; whether each user's provider signs `List-Unsubscribe` and `List-Unsubscribe-Post` is the provider's behaviour and is not something the module can assert. Owner: the real-provider smoke test (platform `OWNER-ACTIONS.md`).
- **How real mailbox providers display and act on the one-click link** for a message sent through a user's own SMTP account: the live suite proves the bytes at a Mailpit sink, not a provider's behaviour.
- **The platform's side:** that `/u/{token}` answers the one-click `POST`; that is the platform's own test.

## Evidence

`platformapi/unsubscribe_run_test.go`, `dispatch/list_unsubscribe_test.go`, `services/worker/test/list-unsubscribe.test.mjs`, `tools/send-byo-migrate/unsubscribe_probe_test.go`, and SUITE 16 of the real-infrastructure harness (`tests/harness/phase5_unsubscribe.sh`); each test has its paired control (the NULL twin, the accepted value, the without-link render). Specified in `Docs/api-contract.md`, "The per-recipient unsubscribe link".

The open edges above (the requeue loop for a link only the builder refuses, the varied body link, DKIM coverage) are registered as [`FW-M3-25`](../future-work.md#fw-m3-25--the-unsubscribe-headers-a-link-the-builder-refuses-is-requeued-in-a-hot-loop-the-bodys-links-are-varied-but-the-headers-is-not-and-dkim-coverage-is-the-providers).
