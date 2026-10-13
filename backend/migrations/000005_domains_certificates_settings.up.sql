-- 000005_domains_certificates_settings
-- Public hostname inventory, TLS certificate metadata, application settings and
-- operator notifications.
--
-- Private keys are NEVER stored in the database. Certificates are referenced by
-- path only and the key files stay on disk with restrictive permissions,
-- owned by the reverse proxy (Caddy/Nginx).

CREATE TABLE domains (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname       text NOT NULL,
    -- 'panel' = dashboard host, 'ws' = SSH-over-WS/WSS host, 'both'
    purpose        text NOT NULL DEFAULT 'panel',
    is_primary     boolean NOT NULL DEFAULT false,
    dns_status     dns_status NOT NULL DEFAULT 'unknown',
    dns_checked_at timestamptz,
    dns_records    jsonb NOT NULL DEFAULT '{}'::jsonb,
    dns_error      text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT domains_hostname_format
        CHECK (hostname ~ '^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$'),
    CONSTRAINT domains_purpose_valid CHECK (purpose IN ('panel', 'ws', 'both'))
);

CREATE UNIQUE INDEX domains_hostname_lower_key ON domains (lower(hostname));

-- At most one primary domain.
CREATE UNIQUE INDEX domains_single_primary_key ON domains (is_primary) WHERE is_primary;

CREATE TRIGGER domains_set_updated_at
    BEFORE UPDATE ON domains
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE certificates (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain_id          uuid NOT NULL REFERENCES domains (id) ON DELETE CASCADE,
    source             certificate_source NOT NULL DEFAULT 'letsencrypt',
    status             certificate_status NOT NULL DEFAULT 'pending',
    issuer             text,
    subject            text,
    serial_number      text,
    fingerprint_sha256 text,
    not_before         timestamptz,
    not_after          timestamptz,
    sans               jsonb NOT NULL DEFAULT '[]'::jsonb,
    -- Filesystem locations only. Contents (and especially private keys) are
    -- never read into the database or exposed through the API.
    cert_path          text,
    key_path           text,
    acme_account_email text,
    auto_renew         boolean NOT NULL DEFAULT true,
    last_checked_at    timestamptz,
    last_renewed_at    timestamptz,
    renewal_error      text,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT certificates_validity_order
        CHECK (not_before IS NULL OR not_after IS NULL OR not_after > not_before)
);

CREATE INDEX certificates_domain_idx ON certificates (domain_id);
CREATE INDEX certificates_status_expiry_idx ON certificates (status, not_after);

CREATE TRIGGER certificates_set_updated_at
    BEFORE UPDATE ON certificates
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Key/value application settings. Secrets (ACME account keys, API tokens) are
-- stored outside this table; `is_secret` marks keys whose value must be redacted
-- in API responses and audit logs.
CREATE TABLE app_settings (
    key         text PRIMARY KEY,
    value       jsonb NOT NULL,
    description text,
    is_secret   boolean NOT NULL DEFAULT false,
    updated_by  uuid REFERENCES administrators (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT app_settings_key_not_blank CHECK (length(btrim(key)) > 0)
);

CREATE TRIGGER app_settings_set_updated_at
    BEFORE UPDATE ON app_settings
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- In-app notifications (certificate renewal failures, quota thresholds, ...).
CREATE TABLE notifications (
    id              bigserial PRIMARY KEY,
    level           text NOT NULL DEFAULT 'info',
    category        text NOT NULL,
    title           text NOT NULL,
    message         text,
    metadata        jsonb NOT NULL DEFAULT '{}'::jsonb,
    acknowledged_at timestamptz,
    acknowledged_by uuid REFERENCES administrators (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notifications_level_valid
        CHECK (level IN ('info', 'warning', 'error'))
);

CREATE INDEX notifications_unacknowledged_idx
    ON notifications (created_at DESC) WHERE acknowledged_at IS NULL;