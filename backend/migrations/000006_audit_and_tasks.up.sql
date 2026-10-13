-- 000006_audit_and_tasks
-- Append-only audit trail and the durable queue used by background jobs
-- (certificate renewal, metrics sampling, retention pruning, ...).

CREATE TABLE audit_events (
    id             bigserial PRIMARY KEY,
    actor_admin_id uuid REFERENCES administrators (id) ON DELETE SET NULL,
    -- Denormalised so the trail survives administrator deletion.
    actor_username text,
    action         text NOT NULL,
    target_type    text,
    target_id      text,
    outcome        text NOT NULL DEFAULT 'success',
    details        jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip_address     inet,
    user_agent     text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT audit_events_action_not_blank CHECK (length(btrim(action)) > 0),
    CONSTRAINT audit_events_outcome_valid CHECK (outcome IN ('success', 'failure'))
);

CREATE INDEX audit_events_created_at_idx ON audit_events (created_at DESC);
CREATE INDEX audit_events_action_idx ON audit_events (action, created_at DESC);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_admin_id, created_at DESC);
CREATE INDEX audit_events_target_idx ON audit_events (target_type, target_id, created_at DESC);

CREATE TABLE background_tasks (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind         text NOT NULL,
    status       background_task_status NOT NULL DEFAULT 'pending',
    payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
    result       jsonb,
    last_error   text,
    attempts     integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 3,
    scheduled_at timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT background_tasks_kind_not_blank CHECK (length(btrim(kind)) > 0),
    CONSTRAINT background_tasks_attempts_non_negative
        CHECK (attempts >= 0 AND max_attempts >= 1),
    CONSTRAINT background_tasks_attempts_within_budget CHECK (attempts <= max_attempts)
);

-- Worker claim query: pending tasks ordered by schedule time.
CREATE INDEX background_tasks_claim_idx
    ON background_tasks (scheduled_at) WHERE status = 'pending';
CREATE INDEX background_tasks_status_idx ON background_tasks (status, updated_at DESC);

CREATE TRIGGER background_tasks_set_updated_at
    BEFORE UPDATE ON background_tasks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();