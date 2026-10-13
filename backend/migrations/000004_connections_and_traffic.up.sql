-- 000004_connections_and_traffic
--
-- Three related concepts are kept distinct, because one user can hold several
-- of each at the same time:
--
--   * transport connection : a TCP socket, a WebSocket or a secure WebSocket
--                            accepted by the panel gateway (kind = tcp/ws/wss)
--   * SSH session          : one authenticated sshd session
--   * account              : a row in ssh_accounts (one or more sessions)
--
-- A WS/WSS transport connection owns an SSH session and is linked through
-- `parent_connection_id`. Pure TCP sessions are discovered from the operating
-- system and have no parent.
--
-- Byte counters:
--   rx_bytes = bytes received from the client (client -> server, "upload")
--   tx_bytes = bytes sent to the client (server -> client, "download")
-- Counters are monotonic and owned by exactly one writer per connection, which
-- is what prevents double counting across reconnects.

CREATE TABLE connections (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id           text NOT NULL,
    ssh_account_id       uuid REFERENCES ssh_accounts (id) ON DELETE SET NULL,
    parent_connection_id uuid REFERENCES connections (id) ON DELETE SET NULL,

    kind                 connection_kind   NOT NULL,
    status               connection_status NOT NULL DEFAULT 'active',

    source_ip            inet,
    source_port          integer,
    -- Client address as reported by the reverse proxy (X-Forwarded-For).
    transport_peer       text,
    user_agent           text,
    ssh_pid              integer,
    ssh_tty              text,

    started_at           timestamptz NOT NULL DEFAULT now(),
    last_seen_at         timestamptz NOT NULL DEFAULT now(),
    ended_at             timestamptz,
    end_reason           text,

    rx_bytes             bigint NOT NULL DEFAULT 0,
    tx_bytes             bigint NOT NULL DEFAULT 0,
    -- Which mechanism produced the counters above: gateway | procfs | sshd-audit
    accounting_source    text   NOT NULL DEFAULT 'gateway',

    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT connections_session_id_not_blank CHECK (length(session_id) > 0),
    CONSTRAINT connections_source_port_range
        CHECK (source_port IS NULL OR (source_port >= 0 AND source_port <= 65535)),
    CONSTRAINT connections_bytes_non_negative CHECK (rx_bytes >= 0 AND tx_bytes >= 0),
    CONSTRAINT connections_accounting_source_valid
        CHECK (accounting_source IN ('gateway', 'procfs', 'sshd-audit')),
    CONSTRAINT connections_ended_after_started
        CHECK (ended_at IS NULL OR ended_at >= started_at)
);

CREATE UNIQUE INDEX connections_session_id_key ON connections (session_id);
CREATE INDEX connections_active_idx
    ON connections (status, last_seen_at DESC) WHERE status = 'active';
CREATE INDEX connections_account_idx
    ON connections (ssh_account_id, started_at DESC);
CREATE INDEX connections_started_at_idx ON connections (started_at DESC);
CREATE INDEX connections_parent_idx ON connections (parent_connection_id);
CREATE INDEX connections_kind_status_idx ON connections (kind, status);

CREATE TRIGGER connections_set_updated_at
    BEFORE UPDATE ON connections
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Raw time series used to compute live throughput and historical graphs.
-- Rows are written by the sampler with the deltas observed since the previous
-- sample; `rx_total`/`tx_total` mirror the parent connection for auditing.
CREATE TABLE traffic_samples (
    id             bigserial PRIMARY KEY,
    connection_id  uuid NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    ssh_account_id uuid REFERENCES ssh_accounts (id) ON DELETE SET NULL,
    sampled_at     timestamptz NOT NULL DEFAULT now(),
    rx_delta       bigint NOT NULL DEFAULT 0,
    tx_delta       bigint NOT NULL DEFAULT 0,
    rx_total       bigint NOT NULL DEFAULT 0,
    tx_total       bigint NOT NULL DEFAULT 0,
    CONSTRAINT traffic_samples_non_negative
        CHECK (rx_delta >= 0 AND tx_delta >= 0 AND rx_total >= 0 AND tx_total >= 0)
);

CREATE INDEX traffic_samples_connection_time_idx
    ON traffic_samples (connection_id, sampled_at DESC);
CREATE INDEX traffic_samples_sampled_at_idx ON traffic_samples (sampled_at DESC);

-- Rolling per-account totals. This is the authoritative source for quota
-- checks: a single row read per authentication.
CREATE TABLE traffic_totals (
    ssh_account_id uuid PRIMARY KEY REFERENCES ssh_accounts (id) ON DELETE CASCADE,
    rx_bytes       bigint NOT NULL DEFAULT 0,
    tx_bytes       bigint NOT NULL DEFAULT 0,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT traffic_totals_non_negative CHECK (rx_bytes >= 0 AND tx_bytes >= 0)
);

-- Per-day rollup for historical charts and reports.
CREATE TABLE traffic_daily (
    ssh_account_id uuid NOT NULL REFERENCES ssh_accounts (id) ON DELETE CASCADE,
    day            date NOT NULL,
    rx_bytes       bigint NOT NULL DEFAULT 0,
    tx_bytes       bigint NOT NULL DEFAULT 0,
    sessions       integer NOT NULL DEFAULT 0,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (ssh_account_id, day),
    CONSTRAINT traffic_daily_non_negative
        CHECK (rx_bytes >= 0 AND tx_bytes >= 0 AND sessions >= 0)
);

CREATE INDEX traffic_daily_day_idx ON traffic_daily (day DESC);

-- Accounts that must be rejected at authentication time. Used by the PAM
-- helper and by the panel's own pre-flight checks.
CREATE VIEW ssh_accounts_blocked AS
SELECT a.id,
       a.username,
       CASE
           WHEN a.deleted_at IS NOT NULL THEN 'deleted'
           WHEN NOT a.is_enabled THEN 'disabled'
           WHEN a.expires_at IS NOT NULL AND a.expires_at <= now() THEN 'expired'
           WHEN a.traffic_quota_bytes IS NOT NULL
                AND COALESCE(t.rx_bytes, 0) + COALESCE(t.tx_bytes, 0) >= a.traffic_quota_bytes
               THEN 'quota_exceeded'
           ELSE NULL
       END AS blocked_reason
FROM ssh_accounts a
LEFT JOIN traffic_totals t ON t.ssh_account_id = a.id;