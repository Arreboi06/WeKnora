-- Migration: 000097_workbench_skill_runs
-- Adds server-derived Workbench Presentation Skill candidate run records.
-- PostgreSQL only; SQLite/Lite remains unsupported for Workbench.

CREATE TABLE IF NOT EXISTS workbench_skill_runs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    chat_session_id VARCHAR(36) NOT NULL,
    workbench_session_id VARCHAR(36) NOT NULL,
    workbench_job_id VARCHAR(36) NOT NULL,
    command_id VARCHAR(36) NOT NULL,
    skill_name VARCHAR(64) NOT NULL,
    skill_operation VARCHAR(64) NOT NULL,
    output_file_ref JSONB NOT NULL,
    output_artifact_id VARCHAR(36) NOT NULL DEFAULT '',
    output_artifact_version INTEGER NOT NULL DEFAULT 0,
    state VARCHAR(32) NOT NULL DEFAULT 'QUEUED',
    state_version BIGINT NOT NULL DEFAULT 0,
    created_by VARCHAR(36) NOT NULL,
    terminal_reason VARCHAR(64),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    closed_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT fk_workbench_skill_runs_chat_session
        FOREIGN KEY (tenant_id, chat_session_id) REFERENCES sessions(tenant_id, id),
    CONSTRAINT fk_workbench_skill_runs_session
        FOREIGN KEY (tenant_id, workbench_session_id) REFERENCES workbench_sessions(tenant_id, id),
    CONSTRAINT fk_workbench_skill_runs_job
        FOREIGN KEY (tenant_id, workbench_job_id) REFERENCES workbench_jobs(tenant_id, id),
    CONSTRAINT fk_workbench_skill_runs_command
        FOREIGN KEY (tenant_id, command_id) REFERENCES workbench_commands(tenant_id, id),
    CONSTRAINT chk_workbench_skill_runs_tenant_nonzero CHECK (tenant_id > 0),
    CONSTRAINT chk_workbench_skill_runs_id_nonempty CHECK (length(trim(id)) > 0),
    CONSTRAINT chk_workbench_skill_runs_chat_nonempty CHECK (length(trim(chat_session_id)) > 0),
    CONSTRAINT chk_workbench_skill_runs_workbench_nonempty CHECK (length(trim(workbench_session_id)) > 0),
    CONSTRAINT chk_workbench_skill_runs_job_nonempty CHECK (length(trim(workbench_job_id)) > 0),
    CONSTRAINT chk_workbench_skill_runs_command_nonempty CHECK (length(trim(command_id)) > 0),
    CONSTRAINT chk_workbench_skill_runs_presentation_only CHECK (skill_name = 'presentations' AND skill_operation = 'create_presentation'),
    CONSTRAINT chk_workbench_skill_runs_output_ref_object CHECK (jsonb_typeof(output_file_ref) = 'object'),
    CONSTRAINT chk_workbench_skill_runs_output_artifact_version_nonnegative CHECK (output_artifact_version >= 0),
    CONSTRAINT chk_workbench_skill_runs_state_version_nonnegative CHECK (state_version >= 0),
    CONSTRAINT chk_workbench_skill_runs_state CHECK (state IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED', 'LOST')),
    CONSTRAINT chk_workbench_skill_runs_terminal_closed CHECK (
        (closed_at IS NULL AND state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELLED', 'LOST')) OR
        (closed_at IS NOT NULL AND state IN ('SUCCEEDED', 'FAILED', 'CANCELLED', 'LOST'))
    ),
    CONSTRAINT chk_workbench_skill_runs_actor_nonempty CHECK (length(trim(created_by)) > 0),
    CONSTRAINT uq_workbench_skill_runs_tenant_id UNIQUE (tenant_id, id),
    CONSTRAINT uq_workbench_skill_runs_tenant_command UNIQUE (tenant_id, command_id)
);

CREATE INDEX IF NOT EXISTS idx_workbench_skill_runs_tenant_chat_created_at
    ON workbench_skill_runs (tenant_id, chat_session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workbench_skill_runs_tenant_job_created_at
    ON workbench_skill_runs (tenant_id, workbench_job_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workbench_skill_runs_tenant_skill_state_created_at
    ON workbench_skill_runs (tenant_id, skill_name, state, created_at DESC);
