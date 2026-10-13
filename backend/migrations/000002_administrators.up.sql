-- 000002_administrators
-- Panel administrators, their sessions and login rate-limiting bookkeeping.
-- Only Argon2id hashes are stored; plaintext passwords never reach the DB.

CREATE TABLE administrators (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username            varchar(64) NOT NULL,
    password_hash       text        NOT NULL,
    password_algo       text        NOT NULL DEFAULT 'argon2id',
    password_changed_at timestamptz NOT NULL DEFAULT now(),
    is_active           boolean     NOT NULL DEFAULT true,
    is_superadmin       boolean     NOT NULL DEFAULT true,
    last_login_at       timestamptz,
    last_login_ip       inet,
    failed_login_count  integer     NOT NULL DEFAULT 0,
    locked_until        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT administrators_username_not_blank CHECK (length(btrim(username)) > 0),
    CONSTRAINT administrators_password_hash_not_blank CHECK (length(password_hash) > 0),
    CONSTRAINT administrators_failed_login_count_non_negative
        CHECK (failed_login_count >= 0)
);

-- Case-insensitive uniqueness without depending on the citext extension.
CREATE UNIQUE INDEX administrators_username_lower_key
    ON administrators (lower(username));

CREATE TRIGGER administrators_set_updated_at
    BEFORE UPDATE ON administrators
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Panel login sessions. `token_hash` and `csrf_token_hash` store SHA-256
-- digests of high-entropy random values; the raw values only exist in the
-- client cookie / response body.
CREATE TABLE admin_sessions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_id        uuid        NOT NULL REFERENCES administrators (id) ON DELETE CASCADE,
    token_hash      text        NOT NULL,
    csrf_token_hash text        NOT NULL,
    ip_address      inet,
    user_agent      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    revoked_at      timestamptz,
    revoked_reason  text,
    CONSTRAINT admin_sessions_token_hash_not_blank CHECK (length(token_hash) > 0),
    CONSTRAINT admin_sessions_expires_after_creation CHECK (expires_at > created_at)
);

CREATE UNIQUE INDEX admin_sessions_token_hash_key ON admin_sessions (token_hash);
CREATE INDEX admin_sessions_admin_id_idx ON admin_sessions (admin_id);
CREATE INDEX admin_sessions_expires_at_idx ON admin_sessions (expires_at);

-- Login attempt log, used for rate limiting and auditing.
CREATE TABLE login_attempts (
    id         bigserial PRIMARY KEY,
    username   text        NOT NULL,
    ip_address inet,
    succeeded  boolean     NOT NULL,
    reason     text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX login_attempts_username_time_idx
    ON login_attempts (lower(username), created_at DESC);
CREATE INDEX login_attempts_ip_time_idx
    ON login_attempts (ip_address, created_at DESC);