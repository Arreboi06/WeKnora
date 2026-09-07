-- Migration: 000096_workbench_artifacts
-- Adds immutable Workbench artifact versions for the default-off local preview chain.
-- PostgreSQL only; SQLite/Lite remains unsupported for Workbench.

CREATE TABLE IF NOT EXISTS workbench_artifact_versions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    artifact_id VARCHAR(36) NOT NULL,
    version INTEGER NOT NULL,
    chat_session_id VARCHAR(36) NOT NULL,
    workbench_session_id VARCHAR(36) NOT NULL,
    workbench_job_id VARCHAR(36) NOT NULL,
    command_id VARCHAR(36) NOT NULL DEFAULT '',
    message_id VARCHAR(36) NOT NULL DEFAULT '',
    skill_run_id VARCHAR(64) NOT NULL DEFAULT '',
    source_file_ref JSONB NOT NULL,
    content_sha256 VARCHAR(64) NOT NULL,
    size_bytes BIGINT NOT NULL,
    mime_type VARCHAR(128) NOT NULL,
    preview_class VARCHAR(32) NOT NULL,
    file_name VARCHAR(255) NOT NULL,
    content BYTEA NOT NULL,
    created_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_workbench_artifacts_chat_session
        FOREIGN KEY (tenant_id, chat_session_id) REFERENCES sessions(tenant_id, id),
    CONSTRAINT fk_workbench_artifacts_session
        FOREIGN KEY (tenant_id, workbench_session_id) REFERENCES workbench_sessions(tenant_id, id),
    CONSTRAINT fk_workbench_artifacts_job
        FOREIGN KEY (tenant_id, workbench_job_id) REFERENCES workbench_jobs(tenant_id, id),
    CONSTRAINT fk_workbench_artifacts_command
        FOREIGN KEY (tenant_id, command_id) REFERENCES workbench_commands(tenant_id, id),
    CONSTRAINT chk_workbench_artifacts_tenant_nonzero CHECK (tenant_id > 0),
    CONSTRAINT chk_workbench_artifacts_id_nonempty CHECK (length(trim(id)) > 0),
    CONSTRAINT chk_workbench_artifacts_artifact_nonempty CHECK (length(trim(artifact_id)) > 0),
    CONSTRAINT chk_workbench_artifacts_version_positive CHECK (version > 0),
    CONSTRAINT chk_workbench_artifacts_chat_nonempty CHECK (length(trim(chat_session_id)) > 0),
    CONSTRAINT chk_workbench_artifacts_workbench_nonempty CHECK (length(trim(workbench_session_id)) > 0),
    CONSTRAINT chk_workbench_artifacts_job_nonempty CHECK (length(trim(workbench_job_id)) > 0),
    CONSTRAINT chk_workbench_artifacts_command_nonempty CHECK (length(trim(command_id)) > 0),
    CONSTRAINT chk_workbench_artifacts_sha CHECK (length(content_sha256) = 64),
    CONSTRAINT chk_workbench_artifacts_size_nonnegative CHECK (size_bytes >= 0),
    CONSTRAINT chk_workbench_artifacts_mime_nonempty CHECK (length(trim(mime_type)) > 0),
    CONSTRAINT chk_workbench_artifacts_preview_class CHECK (preview_class IN ('presentation_page', 'html_active', 'table_csv', 'download_only')),
    CONSTRAINT chk_workbench_artifacts_file_nonempty CHECK (length(trim(file_name)) > 0),
    CONSTRAINT chk_workbench_artifacts_actor_nonempty CHECK (length(trim(created_by)) > 0),
    CONSTRAINT uq_workbench_artifacts_tenant_artifact_version UNIQUE (tenant_id, artifact_id, version)
);

CREATE INDEX IF NOT EXISTS idx_workbench_artifacts_tenant_chat_created_at
    ON workbench_artifact_versions (tenant_id, chat_session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workbench_artifacts_tenant_job_created_at
    ON workbench_artifact_versions (tenant_id, workbench_job_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_workbench_artifacts_tenant_preview_created_at
    ON workbench_artifact_versions (tenant_id, preview_class, created_at DESC);
