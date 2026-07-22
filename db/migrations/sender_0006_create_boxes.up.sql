-- sender_0006_create_boxes.up.sql — Phase 1: sender.boxes (sender box registry).
-- id is client-supplied at registration (NOT gen_random_uuid()). token_hash never stores
-- plaintext (bcrypt/PBKDF2 — verify logic is Phase 3). Owned by sender_owner.

CREATE TABLE sender.boxes (
    -- Identity (box supplies its own UUID at registration)
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL,

    -- Authentication (hash only; plaintext token never stored)
    token_hash TEXT NOT NULL,

    -- Status
    status sender.box_status NOT NULL DEFAULT 'active',
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Metadata (open-ended in Phase 1)
    box_metadata JSONB,

    -- Timestamps
    registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    disabled_at TIMESTAMPTZ,

    -- Constraints
    CONSTRAINT boxes_user_fk
        FOREIGN KEY (user_id)
        REFERENCES public.users(id)
        ON DELETE CASCADE,

    CONSTRAINT boxes_disabled_at_check
        CHECK (
            (status = 'disabled' AND disabled_at IS NOT NULL)
            OR (status != 'disabled' AND disabled_at IS NULL)
        )
);

ALTER TABLE sender.boxes OWNER TO sender_owner;

INSERT INTO sender.schema_migrations (version) VALUES ('0006');
