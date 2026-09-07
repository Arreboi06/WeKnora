-- Migration: 000094_workbench_control
-- Adds the default-off local Workbench control-plane records needed after
-- T2-M01A: jobs, command envelopes, runner events, and audit outbox.
-- PostgreSQL only; SQLite/Lite remains unsupported for Workbench.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'uq_sessions_tenant_id'
          AND conrelid = 'sessions'::regclass
    ) THEN
        ALTER TABLE sessions ADD CONSTRAINT uq_sessions_tenant_id UNIQUE (tenant_id, id);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'uq_workbench_sessions_tenant_id'
          AND conrelid = 'workbench_sessions'::regclass
    ) THEN
        ALTER TABLE workbench_sessions ADD CONSTRAINT uq_workbench_sessions_tenant_id UNIQUE (tenant_id, id);
    END IF;
END $$;
CREATE TABLE IF NOT EXISTS workbench_jobs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    workbench_session_id VARCHAR(36) NOT NULL,
    chat_session_id VARCHAR(36) NOT NULL,
    incarnation_id VARCHAR(36) NOT NULL,
    lease_epoch BIGINT NOT NULL,
    start_nonce_hash VARCHAR(64) NOT NULL,
    backend_type VARCHAR(32) NOT NULL,
    backend_identity VARCHAR(128) NOT NULL,
    state VARCHAR(32) NOT NULL DEFAULT 'QUEUED',
    state_version BIGINT NOT NULL DEFAULT 0,
    resource_policy_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by VARCHAR(36) NOT NULL,
    terminal_reason VARCHAR(64),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    closed_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT fk_workbench_jobs_session
        FOREIGN KEY (tenant_id, workbench_session_id) REFERENCES workbench_sessions(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_workbench_jobs_chat_session
        FOREIGN KEY (tenant_id, chat_session_id) REFERENCES sessions(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_workbench_jobs_tenant_nonzero CHECK (tenant_id > 0),
    CONSTRAINT chk_workbench_jobs_id_nonempty CHECK (length(trim(id)) > 0),
    CONSTRAINT chk_workbench_jobs_workbench_nonempty CHECK (length(trim(workbench_session_id)) > 0),
    CONSTRAINT chk_workbench_jobs_chat_nonempty CHECK (length(trim(chat_session_id)) > 0),
    CONSTRAINT chk_workbench_jobs_incarnation_nonempty CHECK (length(trim(incarnation_id)) > 0),
    CONSTRAINT chk_workbench_jobs_epoch_nonnegative CHECK (lease_epoch >= 0),
    CONSTRAINT chk_workbench_jobs_nonce_hash CHECK (length(start_nonce_hash) = 64),
    CONSTRAINT chk_workbench_jobs_backend_type_nonempty CHECK (length(trim(backend_type)) > 0),
    CONSTRAINT chk_workbench_jobs_backend_identity_nonempty CHECK (length(trim(backend_identity)) > 0),
    CONSTRAINT chk_workbench_jobs_actor_nonempty CHECK (length(trim(created_by)) > 0),
    CONSTRAINT chk_workbench_jobs_state_version_nonnegative CHECK (state_version >= 0),
    CONSTRAINT chk_workbench_jobs_state CHECK (state IN ('QUEUED', 'STARTING', 'RUNNING', 'CLOSING', 'SUCCEEDED', 'FAILED', 'CANCELLED', 'LOST')),
    CONSTRAINT chk_workbench_jobs_terminal_closed CHECK (
        (closed_at IS NULL AND state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELLED', 'LOST')) OR
        (closed_at IS NOT NULL AND state IN ('SUCCEEDED', 'FAILED', 'CANCELLED', 'LOST'))
    ),
    CONSTRAINT uq_workbench_jobs_tenant_id UNIQUE (tenant_id, id),
    CONSTRAINT uq_workbench_jobs_tenant_workbench_epoch_nonce UNIQUE (tenant_id, workbench_session_id, lease_epoch, start_nonce_hash)
);

CREATE INDEX IF NOT EXISTS idx_workbench_jobs_tenant_workbench_created_at
    ON workbench_jobs (tenant_id, workbench_session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workbench_jobs_tenant_state_updated_at
    ON workbench_jobs (tenant_id, state, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_workbench_jobs_tenant_operator_updated_at
    ON workbench_jobs (tenant_id, created_by, updated_at DESC);

CREATE TABLE IF NOT EXISTS workbench_commands (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    workbench_job_id VARCHAR(36) NOT NULL,
    workbench_session_id VARCHAR(36) NOT NULL,
    sequence BIGINT NOT NULL,
    kind VARCHAR(32) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    state VARCHAR(32) NOT NULL DEFAULT 'QUEUED',
    state_version BIGINT NOT NULL DEFAULT 0,
    created_by VARCHAR(36) NOT NULL,
    terminal_reason VARCHAR(64),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    closed_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT fk_workbench_commands_job
        FOREIGN KEY (tenant_id, workbench_job_id) REFERENCES workbench_jobs(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_workbench_commands_session
        FOREIGN KEY (tenant_id, workbench_session_id) REFERENCES workbench_sessions(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_workbench_commands_tenant_nonzero CHECK (tenant_id > 0),
    CONSTRAINT chk_workbench_commands_id_nonempty CHECK (length(trim(id)) > 0),
    CONSTRAINT chk_workbench_commands_job_nonempty CHECK (length(trim(workbench_job_id)) > 0),
    CONSTRAINT chk_workbench_commands_session_nonempty CHECK (length(trim(workbench_session_id)) > 0),
    CONSTRAINT chk_workbench_commands_sequence_positive CHECK (sequence > 0),
    CONSTRAINT chk_workbench_commands_kind_nonempty CHECK (length(trim(kind)) > 0),
    CONSTRAINT chk_workbench_commands_actor_nonempty CHECK (length(trim(created_by)) > 0),
    CONSTRAINT chk_workbench_commands_state_version_nonnegative CHECK (state_version >= 0),
    CONSTRAINT chk_workbench_commands_state CHECK (state IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED', 'LOST')),
    CONSTRAINT chk_workbench_commands_terminal_closed CHECK (
        (closed_at IS NULL AND state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELLED', 'LOST')) OR
        (closed_at IS NOT NULL AND state IN ('SUCCEEDED', 'FAILED', 'CANCELLED', 'LOST'))
    ),
    CONSTRAINT uq_workbench_commands_tenant_id UNIQUE (tenant_id, id),
    CONSTRAINT uq_workbench_commands_tenant_job_sequence UNIQUE (tenant_id, workbench_job_id, sequence)
);

CREATE INDEX IF NOT EXISTS idx_workbench_commands_tenant_job_created_at
    ON workbench_commands (tenant_id, workbench_job_id, created_at ASC);

CREATE INDEX IF NOT EXISTS idx_workbench_commands_tenant_state_updated_at
    ON workbench_commands (tenant_id, state, updated_at DESC);

CREATE TABLE IF NOT EXISTS workbench_runner_events (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    workbench_job_id VARCHAR(36) NOT NULL,
    workbench_session_id VARCHAR(36) NOT NULL,
    command_id VARCHAR(36) NOT NULL DEFAULT '',
    seq BIGINT NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_workbench_runner_events_job
        FOREIGN KEY (tenant_id, workbench_job_id) REFERENCES workbench_jobs(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_workbench_runner_events_session
        FOREIGN KEY (tenant_id, workbench_session_id) REFERENCES workbench_sessions(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_workbench_runner_events_tenant_nonzero CHECK (tenant_id > 0),
    CONSTRAINT chk_workbench_runner_events_id_nonempty CHECK (length(trim(id)) > 0),
    CONSTRAINT chk_workbench_runner_events_job_nonempty CHECK (length(trim(workbench_job_id)) > 0),
    CONSTRAINT chk_workbench_runner_events_session_nonempty CHECK (length(trim(workbench_session_id)) > 0),
    CONSTRAINT chk_workbench_runner_events_seq_positive CHECK (seq > 0),
    CONSTRAINT chk_workbench_runner_events_type_nonempty CHECK (length(trim(event_type)) > 0),
    CONSTRAINT uq_workbench_runner_events_tenant_job_seq UNIQUE (tenant_id, workbench_job_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_workbench_runner_events_tenant_job_seq
    ON workbench_runner_events (tenant_id, workbench_job_id, seq ASC);

CREATE TABLE IF NOT EXISTS workbench_audit_outbox (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    workbench_session_id VARCHAR(36) NOT NULL,
    workbench_job_id VARCHAR(36) NOT NULL DEFAULT '',
    command_id VARCHAR(36) NOT NULL DEFAULT '',
    action VARCHAR(64) NOT NULL,
    actor_user_id VARCHAR(36) NOT NULL DEFAULT '',
    outcome VARCHAR(16) NOT NULL DEFAULT 'success',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    idempotency_key VARCHAR(128) NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'PENDING',
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT fk_workbench_audit_outbox_session
        FOREIGN KEY (tenant_id, workbench_session_id) REFERENCES workbench_sessions(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT chk_workbench_audit_outbox_tenant_nonzero CHECK (tenant_id > 0),
    CONSTRAINT chk_workbench_audit_outbox_session_nonempty CHECK (length(trim(workbench_session_id)) > 0),
    CONSTRAINT chk_workbench_audit_outbox_action_nonempty CHECK (length(trim(action)) > 0),
    CONSTRAINT chk_workbench_audit_outbox_idempotency_nonempty CHECK (length(trim(idempotency_key)) > 0),
    CONSTRAINT chk_workbench_audit_outbox_state CHECK (state IN ('PENDING', 'PUBLISHED', 'FAILED')),
    CONSTRAINT chk_workbench_audit_outbox_attempts_nonnegative CHECK (attempts >= 0),
    CONSTRAINT uq_workbench_audit_outbox_tenant_idempotency UNIQUE (tenant_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_workbench_audit_outbox_tenant_state_created_at
    ON workbench_audit_outbox (tenant_id, state, created_at ASC);

CREATE INDEX IF NOT EXISTS idx_workbench_audit_outbox_tenant_workbench_created_at
    ON workbench_audit_outbox (tenant_id, workbench_session_id, created_at ASC);

CREATE INDEX IF NOT EXISTS idx_workbench_audit_outbox_tenant_operator_created_at
    ON workbench_audit_outbox (tenant_id, actor_user_id, created_at ASC);
