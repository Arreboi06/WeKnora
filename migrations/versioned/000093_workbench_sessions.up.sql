-- Migration: 000093_workbench_sessions
-- Number 000092 is reserved by official main for mcp_metadata.

CREATE TABLE IF NOT EXISTS workbench_sessions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    chat_session_id VARCHAR(36) NOT NULL,
    purpose VARCHAR(32) NOT NULL DEFAULT 'workbench',
    incarnation_id VARCHAR(36) NOT NULL,
    sandbox_config_id VARCHAR(36) NOT NULL,
    backend_type VARCHAR(32) NOT NULL,
    state VARCHAR(32) NOT NULL DEFAULT 'PROVISIONING',
    state_version BIGINT NOT NULL DEFAULT 0,
    lease_epoch BIGINT NOT NULL DEFAULT 0,
    capability_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    policy_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by VARCHAR(36) NOT NULL,
    terminal_reason VARCHAR(64),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    closed_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT fk_workbench_sessions_chat_session
        FOREIGN KEY (chat_session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    CONSTRAINT chk_workbench_sessions_tenant_nonzero CHECK (tenant_id > 0),
    CONSTRAINT chk_workbench_sessions_purpose CHECK (purpose = 'workbench'),
    CONSTRAINT chk_workbench_sessions_id_nonempty CHECK (length(trim(id)) > 0),
    CONSTRAINT chk_workbench_sessions_chat_session_nonempty CHECK (length(trim(chat_session_id)) > 0),
    CONSTRAINT chk_workbench_sessions_incarnation_nonempty CHECK (length(trim(incarnation_id)) > 0),
    CONSTRAINT chk_workbench_sessions_sandbox_config_nonempty CHECK (length(trim(sandbox_config_id)) > 0),
    CONSTRAINT chk_workbench_sessions_backend_nonempty CHECK (length(trim(backend_type)) > 0),
    CONSTRAINT chk_workbench_sessions_actor_nonempty CHECK (length(trim(created_by)) > 0),
    CONSTRAINT chk_workbench_sessions_state_version_nonnegative CHECK (state_version >= 0),
    CONSTRAINT chk_workbench_sessions_lease_epoch_nonnegative CHECK (lease_epoch >= 0),
    CONSTRAINT chk_workbench_sessions_terminal_closed CHECK (
        (closed_at IS NULL AND state NOT IN ('CLOSED', 'LOST')) OR
        (closed_at IS NOT NULL AND state IN ('CLOSED', 'LOST'))
    )
);

CREATE INDEX IF NOT EXISTS idx_workbench_sessions_tenant_id
    ON workbench_sessions (tenant_id, id);

CREATE UNIQUE INDEX IF NOT EXISTS uq_workbench_sessions_tenant_chat_purpose_incarnation
    ON workbench_sessions (tenant_id, chat_session_id, purpose, incarnation_id);

CREATE UNIQUE INDEX IF NOT EXISTS uq_workbench_sessions_tenant_chat_purpose_active
    ON workbench_sessions (tenant_id, chat_session_id, purpose)
    WHERE closed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_workbench_sessions_tenant_state_updated_at
    ON workbench_sessions (tenant_id, state, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_workbench_sessions_tenant_operator_updated_at
    ON workbench_sessions (tenant_id, created_by, updated_at DESC);
