-- send_byo_0060_jobs_unsubscribe_url.up.sql -- Phase 5 S4: THE PER-RECIPIENT UNSUBSCRIBE LINK (List-Unsubscribe, RFC 2369, and its one-click
-- companion List-Unsubscribe-Post, RFC 8058).
--
-- send_byo.jobs freezes a denormalized copy of the platform's rendered message at run time (rendered_subject, rendered_body, rendered_text,
-- reply_to ...). This adds the per-recipient unsubscribe link beside them: a NULLABLE text column that `POST /v1/run` copies from the
-- platform-owned public.job_items.unsubscribe_url (the platform adds that column in its OWN migration, platform_0017, and writes it; this
-- module only ever reads it -- it does NOT create or alter anything in the public schema) in the same statement that copies rendered_body
-- and rendered_text. Mailbox providers require both headers of a bulk sender, and this module sends through the user's OWN SMTP account, so
-- the Control builds them (dispatch/headers.go) from this column and hands them to the worker with the other Control-supplied headers.
--
--   unsubscribe_url IS NULL  =>  today's message, byte for byte: neither header.
--   unsubscribe_url set      =>  `List-Unsubscribe: <URL>` and `List-Unsubscribe-Post: List-Unsubscribe=One-Click`, each exactly once.
--
-- ADDITIVE: one nullable column with no default, so every existing row reads as ABSENT, and an older Control binary running against this
-- schema never names the column.
--
-- THE CHECK MIRRORS THE PLATFORM'S: NULL, or a single https URL with at least one character after the scheme, at most 2001 characters in all
-- (the scheme's 8 plus the platform's 1993), no whitespace, no `<` or `>` (either one would break out of the angle brackets the header wraps
-- it in), and NO CONTROL CHARACTER of any kind (the header-injection class: a CR or LF would end the header line and start another).
-- `[[:space:]]` already covers CR, LF, TAB, VT and FF; the separate `[[:cntrl:]]` predicate closes the rest of the C0 range and DEL, which the
-- first leaves open. The Control's builder (dispatch/headers.go) refuses all of these again before a byte is written -- and more (it also
-- refuses a non-ASCII byte and a header line over RFC 5322's 998 octets): the CHECK stops a bad value at the database, the builder stops one
-- that reached the row some other way. An INSERT that violates the CHECK fails 23514, which POST /v1/run answers as 400 validation_failed
-- (the existing job_item_invalid path) and writes nothing. Rule 13: the predicate says IS NULL explicitly.
--
-- WHY THE LENGTH IS char_length AND NOT A REGEX BOUND. PostgreSQL caps a regex repetition bound at 255 (`{1,1993}` is "invalid repetition
-- count(s)" and would make this migration fail), so the length is its own predicate and the regex says only `+`.
--
-- THE PLATFORM CLEARS ITS COPY 7 DAYS AFTER A JOB ENDS (its retention); this module's frozen copy is not touched by that. It is a link the
-- recipient already holds in a message that was sent, and the platform's token keeps working after its own copy is cleared (it is derived
-- from the item id), so nothing here needs to follow the platform's clearing.

ALTER TABLE send_byo.jobs
    ADD COLUMN unsubscribe_url TEXT,
    ADD CONSTRAINT jobs_unsubscribe_url_check
        CHECK (unsubscribe_url IS NULL
               OR (unsubscribe_url ~ '^https://[^[:space:]<>]+$'
                   AND char_length(unsubscribe_url) <= 2001
                   AND unsubscribe_url !~ '[[:cntrl:]]'));

COMMENT ON COLUMN send_byo.jobs.unsubscribe_url IS
    'Per-recipient unsubscribe link, frozen at run from public.job_items.unsubscribe_url (NULL when the platform wrote none). Set => the Control supplies List-Unsubscribe: <URL> and List-Unsubscribe-Post: List-Unsubscribe=One-Click with the other Control-supplied headers, each exactly once. NULL => neither header, the message exactly as before. A single https URL with no whitespace, control character, < or > (jobs_unsubscribe_url_check mirrors the platform''s CHECK).';

INSERT INTO send_byo.schema_migrations (version) VALUES ('0060');
