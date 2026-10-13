-- 000003_ssh_accounts
-- One row per managed SSH VPN account.
--
-- Password handling: `password_hash` holds an Argon2id hash used by the panel
-- (and by automated tests) to verify password changes. SSH itself authenticates
-- against /etc/shadow, which the panel writes through the restricted system
-- layer using `chpasswd` over stdin. Plaintext passwords are never persisted,
-- never logged and never returned by the API.
--
-- Accounts are soft-deleted: the Linux user is removed and `deleted_at` is set
-- so that traffic history keeps a stable foreign key.

CREATE TABLE ssh_accounts (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username            varchar(32) NOT NULL,
    password_hash       text        NOT NULL,
    password_algo       text        NOT NULL DEFAULT 'argon2id',
    password_changed_at timestamptz NOT NULL DEFAULT now(),

    linux_uid           integer,
    linux_gid           integer,
    home_dir            text,
    shell               text        NOT NULL DEFAULT '/bin/bash',
    provision_state     text        NOT NULL DEFAULT 'pending',
    provision_error     text,
    provisioned_at      timestamptz,

    is_enabled          boolean     NOT NULL DEFAULT true,
    expires_at          timestamptz,
    traffic_quota_bytes bigint,
    quota_action        quota_action NOT NULL DEFAULT 'disable',
    max_connections     integer,

    notes               text,
    last_login_at       timestamptz,
    last_activity_at    timestamptz,

    created_by          uuid REFERENCES administrators (id) ON DELETE SET NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,

    CONSTRAINT ssh_accounts_username_format
        CHECK (username ~ '^[a-z_][a-z0-9_-]{2,31}$'),
    CONSTRAINT ssh_accounts_password_hash_not_blank CHECK (length(password_hash) > 0),
    CONSTRAINT ssh_accounts_provision_state_valid
        CHECK (provision_state IN ('pending', 'provisioned', 'error', 'deleted')),
    CONSTRAINT ssh_accounts_uid_positive CHECK (linux_uid IS NULL OR linux_uid > 0),
    CONSTRAINT ssh_accounts_gid_positive CHECK (linux_gid IS NULL OR linux_gid > 0),
    CONSTRAINT ssh_accounts_traffic_quota_non_negative
        CHECK (traffic_quota_bytes IS NULL OR traffic_quota_bytes >= 0),
    CONSTRAINT ssh_accounts_max_connections_positive
        CHECK (max_connections IS NULL OR max_connections >= 1),
    CONSTRAINT ssh_accounts_shell_absolute CHECK (shell LIKE '/%')
);

-- Usernames are unique among live accounts and can be reused after deletion.
CREATE UNIQUE INDEX ssh_accounts_username_lower_key
    ON ssh_accounts (lower(username))
    WHERE deleted_at IS NULL;

-- A Linux UID may only be assigned once among live accounts.
CREATE UNIQUE INDEX ssh_accounts_linux_uid_key
    ON ssh_accounts (linux_uid)
    WHERE linux_uid IS NOT NULL AND deleted_at IS NULL;

CREATE INDEX ssh_accounts_live_idx
    ON ssh_accounts (is_enabled, expires_at)
    WHERE deleted_at IS NULL;

CREATE INDEX ssh_accounts_created_at_idx ON ssh_accounts (created_at DESC);

CREATE TRIGGER ssh_accounts_set_updated_at
    BEFORE UPDATE ON ssh_accounts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();