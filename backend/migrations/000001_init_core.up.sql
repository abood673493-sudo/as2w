-- 000001_init_core
-- Shared enum types and trigger helpers used by the rest of the schema.
-- Requires PostgreSQL 14+ (gen_random_uuid() is core since PostgreSQL 13, so
-- no extension is needed and this migration does not require superuser).

CREATE TYPE connection_kind AS ENUM ('tcp', 'ws', 'wss');

CREATE TYPE connection_status AS ENUM ('active', 'closed', 'terminated');

CREATE TYPE certificate_source AS ENUM ('letsencrypt', 'uploaded', 'self_signed');

CREATE TYPE certificate_status AS ENUM ('pending', 'valid', 'expiring', 'expired', 'error');

CREATE TYPE dns_status AS ENUM ('unknown', 'ok', 'mismatch', 'missing', 'error');

CREATE TYPE background_task_status AS ENUM
    ('pending', 'running', 'succeeded', 'failed', 'cancelled');

CREATE TYPE quota_action AS ENUM ('notify', 'disable');

-- Generic updated_at maintenance, attached to tables that carry updated_at.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
    LANGUAGE plpgsql AS
$$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;