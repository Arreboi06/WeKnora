-- Migration: 000091_citation_profile
-- Description: Topic 4 citation profile schema. All tables are additive and
-- default-off; no existing rows are read, changed, or copied by this migration.
DO $$ BEGIN RAISE NOTICE '[Migration 000091] Creating citation profile schema'; END $$;

CREATE TABLE IF NOT EXISTS citation_profile_scopes (
    id                       VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id                BIGINT NOT NULL,
    subject_id               VARCHAR(512) NOT NULL,
    knowledge_base_id         VARCHAR(36) NOT NULL,
    subject_epoch            VARCHAR(36) NOT NULL,
    profile_read_version     BIGINT NOT NULL DEFAULT 0,
    profile_policy_version   VARCHAR(64) NOT NULL DEFAULT 'profile_policy_v1',
    retention_policy_version VARCHAR(64) NOT NULL DEFAULT 'retention_policy_v1',
    enabled                  BOOLEAN NOT NULL DEFAULT TRUE,
    active_run_id            VARCHAR(36),
    mapping_revision         BIGINT NOT NULL DEFAULT 0,
    source_universe_watermark VARCHAR(128) NOT NULL DEFAULT '',
    pending_event_count      INTEGER NOT NULL DEFAULT 0,
    pending_mapping_count    INTEGER NOT NULL DEFAULT 0,
    dirty_mapping_count      INTEGER NOT NULL DEFAULT 0,
    acl_check_state          VARCHAR(32) NOT NULL DEFAULT 'current',
    next_acl_check_at        TIMESTAMP WITH TIME ZONE,
    acl_check_lease_until    TIMESTAMP WITH TIME ZONE,
    fenced_at                TIMESTAMP WITH TIME ZONE,
    fence_reason             VARCHAR(64) NOT NULL DEFAULT '',
    delete_request_id        VARCHAR(36),
    created_at               TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    deleted_at               TIMESTAMP WITH TIME ZONE,
    CONSTRAINT chk_citation_profile_scope_read_version
        CHECK (profile_read_version >= 0),
    CONSTRAINT chk_citation_profile_scope_counts
        CHECK (pending_event_count >= 0 AND pending_mapping_count >= 0 AND dirty_mapping_count >= 0)
);

COMMENT ON TABLE citation_profile_scopes IS
    'Topic 4 per-tenant, per-subject, per-knowledge-base profile scope. subject_id is server derived.';

CREATE UNIQUE INDEX IF NOT EXISTS uq_citation_profile_scope_live
    ON citation_profile_scopes (tenant_id, subject_id, knowledge_base_id)
    WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_citation_profile_scope_epoch
    ON citation_profile_scopes (tenant_id, subject_id, knowledge_base_id, subject_epoch);

CREATE INDEX IF NOT EXISTS idx_citation_profile_scopes_kb
    ON citation_profile_scopes (tenant_id, knowledge_base_id, deleted_at);

CREATE INDEX IF NOT EXISTS idx_citation_profile_scopes_acl_queue
    ON citation_profile_scopes (acl_check_state, next_acl_check_at, acl_check_lease_until)
    WHERE deleted_at IS NULL AND fenced_at IS NULL;

CREATE TABLE IF NOT EXISTS citation_profile_events (
    id                       VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id                BIGINT NOT NULL,
    subject_id               VARCHAR(512) NOT NULL,
    knowledge_base_id         VARCHAR(36) NOT NULL,
    subject_epoch            VARCHAR(36) NOT NULL,
    scope_id                 VARCHAR(36) NOT NULL,
    session_id               VARCHAR(36) NOT NULL DEFAULT '',
    message_id               VARCHAR(36) NOT NULL,
    message_version          VARCHAR(64) NOT NULL DEFAULT '',
    message_completed_at     TIMESTAMP WITH TIME ZONE,
    origin_reference_index   INTEGER NOT NULL,
    source_knowledge_id      VARCHAR(36) NOT NULL,
    source_result_id         VARCHAR(128) NOT NULL DEFAULT '',
    source_chunk_index       INTEGER,
    source_ref_raw           TEXT NOT NULL DEFAULT '',
    source_ref_normalized    VARCHAR(512) NOT NULL DEFAULT '',
    source_refs_snapshot     JSONB NOT NULL DEFAULT '{}'::JSONB,
    knowledge_snapshot       JSONB NOT NULL DEFAULT '{}'::JSONB,
    knowledge_base_proof     JSONB NOT NULL DEFAULT '{}'::JSONB,
    producer_event_key       VARCHAR(512) NOT NULL,
    content_hash             VARCHAR(64) NOT NULL DEFAULT '',
    status                   VARCHAR(32) NOT NULL DEFAULT 'pending_resolution',
    active_run_id            VARCHAR(36),
    pending_reason           VARCHAR(64) NOT NULL DEFAULT '',
    failed_reason            TEXT NOT NULL DEFAULT '',
    resolved_at              TIMESTAMP WITH TIME ZONE,
    retracted_at             TIMESTAMP WITH TIME ZONE,
    created_at               TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_citation_profile_event_reference_index
        CHECK (origin_reference_index >= 0),
    CONSTRAINT chk_citation_profile_event_chunk_index
        CHECK (source_chunk_index IS NULL OR source_chunk_index >= 0)
);

COMMENT ON TABLE citation_profile_events IS
    'Immutable Fact A event rows captured from completed assistant messages and frozen source references.';

CREATE UNIQUE INDEX IF NOT EXISTS uq_citation_profile_event_producer
    ON citation_profile_events (
        tenant_id,
        subject_id,
        knowledge_base_id,
        subject_epoch,
        message_id,
        origin_reference_index,
        source_knowledge_id,
        source_result_id,
        COALESCE(source_chunk_index, -1)
    )
    WHERE retracted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_citation_profile_event_key
    ON citation_profile_events (tenant_id, subject_id, knowledge_base_id, subject_epoch, producer_event_key)
    WHERE retracted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_citation_profile_events_scope_status
    ON citation_profile_events (tenant_id, subject_id, knowledge_base_id, subject_epoch, status, created_at);

CREATE INDEX IF NOT EXISTS idx_citation_profile_events_message
    ON citation_profile_events (tenant_id, message_id);

CREATE INDEX IF NOT EXISTS idx_citation_profile_events_active_run
    ON citation_profile_events (active_run_id)
    WHERE active_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_citation_profile_events_source
    ON citation_profile_events (tenant_id, knowledge_base_id, source_knowledge_id, status);

CREATE TABLE IF NOT EXISTS citation_profile_event_outbox (
    id                    VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id             BIGINT NOT NULL,
    subject_id            VARCHAR(512) NOT NULL,
    knowledge_base_id      VARCHAR(36) NOT NULL,
    subject_epoch         VARCHAR(36) NOT NULL,
    scope_id              VARCHAR(36) NOT NULL,
    event_id              VARCHAR(36) NOT NULL,
    status                VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempt_count         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    locked_at             TIMESTAMP WITH TIME ZONE,
    locked_by             VARCHAR(128) NOT NULL DEFAULT '',
    delivered_at          TIMESTAMP WITH TIME ZONE,
    deadletter_at         TIMESTAMP WITH TIME ZONE,
    last_error_code       VARCHAR(64) NOT NULL DEFAULT '',
    last_error_message    TEXT NOT NULL DEFAULT '',
    created_at            TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_citation_profile_outbox_attempts
        CHECK (attempt_count >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_citation_profile_outbox_event
    ON citation_profile_event_outbox (event_id)
    WHERE deadletter_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_citation_profile_outbox_pending
    ON citation_profile_event_outbox (tenant_id, knowledge_base_id, subject_epoch, status, next_attempt_at, locked_at);

CREATE TABLE IF NOT EXISTS wiki_source_ref_index (
    id                   VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id            BIGINT NOT NULL,
    knowledge_base_id     VARCHAR(36) NOT NULL,
    source_knowledge_id  VARCHAR(36) NOT NULL,
    page_uuid            VARCHAR(36) NOT NULL,
    page_version         INTEGER NOT NULL,
    page_slug            VARCHAR(255) NOT NULL DEFAULT '',
    page_title           VARCHAR(512) NOT NULL DEFAULT '',
    normalized_ref       VARCHAR(512) NOT NULL,
    mapping_revision     BIGINT NOT NULL,
    lifecycle_state      VARCHAR(32) NOT NULL DEFAULT 'current',
    index_watermark      VARCHAR(128) NOT NULL DEFAULT '',
    indexed_at           TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    created_at           TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_wiki_source_ref_index_page_version
        CHECK (page_version > 0),
    CONSTRAINT chk_wiki_source_ref_index_mapping_revision
        CHECK (mapping_revision >= 0)
);

COMMENT ON TABLE wiki_source_ref_index IS
    'Subject-free SourceRefs lookup built only from normalized wiki SourceRefs and page versions.';

CREATE UNIQUE INDEX IF NOT EXISTS uq_wiki_source_ref_index_version
    ON wiki_source_ref_index (
        tenant_id,
        knowledge_base_id,
        source_knowledge_id,
        page_uuid,
        page_version,
        normalized_ref,
        mapping_revision,
        lifecycle_state
    );

CREATE INDEX IF NOT EXISTS idx_wiki_source_ref_lookup
    ON wiki_source_ref_index (tenant_id, knowledge_base_id, source_knowledge_id, lifecycle_state, mapping_revision);

CREATE INDEX IF NOT EXISTS idx_wiki_source_ref_page
    ON wiki_source_ref_index (tenant_id, knowledge_base_id, page_uuid, page_version, mapping_revision);

CREATE TABLE IF NOT EXISTS evidence_resolution_runs (
    id                     VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id              BIGINT NOT NULL,
    subject_id             VARCHAR(512) NOT NULL,
    knowledge_base_id       VARCHAR(36) NOT NULL,
    subject_epoch          VARCHAR(36) NOT NULL,
    scope_id               VARCHAR(36) NOT NULL,
    event_id               VARCHAR(36) NOT NULL,
    status                 VARCHAR(32) NOT NULL,
    run_mapping_revision   BIGINT NOT NULL,
    run_universe_watermark VARCHAR(128) NOT NULL,
    input_hash             VARCHAR(64) NOT NULL DEFAULT '',
    output_hash            VARCHAR(64) NOT NULL DEFAULT '',
    input_count            INTEGER NOT NULL DEFAULT 0,
    output_count           INTEGER NOT NULL DEFAULT 0,
    error_code             VARCHAR(64) NOT NULL DEFAULT '',
    error_message          TEXT NOT NULL DEFAULT '',
    started_at             TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    resolved_at            TIMESTAMP WITH TIME ZONE,
    failed_at              TIMESTAMP WITH TIME ZONE,
    created_at             TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_evidence_resolution_run_counts
        CHECK (input_count >= 0 AND output_count >= 0 AND output_count <= 100),
    CONSTRAINT chk_evidence_resolution_run_mapping_revision
        CHECK (run_mapping_revision >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_evidence_resolution_run_token
    ON evidence_resolution_runs (event_id, run_mapping_revision, run_universe_watermark);

CREATE INDEX IF NOT EXISTS idx_evidence_resolution_runs_scope_status
    ON evidence_resolution_runs (tenant_id, subject_id, knowledge_base_id, subject_epoch, status, created_at);

CREATE INDEX IF NOT EXISTS idx_evidence_resolution_runs_event
    ON evidence_resolution_runs (event_id, created_at DESC);

CREATE TABLE IF NOT EXISTS evidence_node_links (
    id                     VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id              BIGINT NOT NULL,
    subject_id             VARCHAR(512) NOT NULL,
    knowledge_base_id       VARCHAR(36) NOT NULL,
    subject_epoch          VARCHAR(36) NOT NULL,
    scope_id               VARCHAR(36) NOT NULL,
    event_id               VARCHAR(36) NOT NULL,
    resolution_run_id      VARCHAR(36) NOT NULL,
    source_knowledge_id    VARCHAR(36) NOT NULL,
    page_uuid              VARCHAR(36) NOT NULL,
    page_version           INTEGER NOT NULL,
    normalized_ref         VARCHAR(512) NOT NULL DEFAULT '',
    relation_state         VARCHAR(32) NOT NULL DEFAULT 'evidenced_current',
    relation_source        VARCHAR(32) NOT NULL DEFAULT 'source_ref_index',
    mapping_revision       BIGINT NOT NULL,
    universe_watermark     VARCHAR(128) NOT NULL DEFAULT '',
    created_at             TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_evidence_node_link_page_version
        CHECK (page_version > 0),
    CONSTRAINT chk_evidence_node_link_mapping_revision
        CHECK (mapping_revision >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_evidence_node_link_run_page
    ON evidence_node_links (resolution_run_id, page_uuid, page_version, normalized_ref);

CREATE INDEX IF NOT EXISTS idx_evidence_node_links_event
    ON evidence_node_links (event_id, relation_state);

CREATE INDEX IF NOT EXISTS idx_evidence_node_links_scope_page
    ON evidence_node_links (tenant_id, subject_id, knowledge_base_id, subject_epoch, page_uuid, relation_state);

CREATE TABLE IF NOT EXISTS citation_profile_corrections (
    id                     VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id              BIGINT NOT NULL,
    subject_id             VARCHAR(512) NOT NULL,
    knowledge_base_id       VARCHAR(36) NOT NULL,
    subject_epoch          VARCHAR(36) NOT NULL,
    scope_id               VARCHAR(36) NOT NULL,
    event_id               VARCHAR(36) NOT NULL,
    page_uuid              VARCHAR(36) NOT NULL DEFAULT '',
    page_version           INTEGER,
    correction_type        VARCHAR(32) NOT NULL,
    reason                 TEXT NOT NULL DEFAULT '',
    actor_id               VARCHAR(512) NOT NULL,
    expected_read_version  BIGINT NOT NULL,
    resulting_read_version BIGINT NOT NULL,
    idempotency_key        VARCHAR(128) NOT NULL DEFAULT '',
    created_at             TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_citation_profile_correction_versions
        CHECK (expected_read_version >= 0 AND resulting_read_version > expected_read_version)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_citation_profile_correction_idem
    ON citation_profile_corrections (tenant_id, subject_id, knowledge_base_id, subject_epoch, idempotency_key)
    WHERE idempotency_key <> '';

CREATE INDEX IF NOT EXISTS idx_citation_profile_corrections_event_page
    ON citation_profile_corrections (tenant_id, subject_id, knowledge_base_id, subject_epoch, event_id, page_uuid, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_citation_profile_corrections_actor
    ON citation_profile_corrections (tenant_id, actor_id, created_at DESC);

CREATE TABLE IF NOT EXISTS citation_profile_operations (
    id                     VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id              BIGINT NOT NULL,
    subject_id             VARCHAR(512) NOT NULL,
    knowledge_base_id       VARCHAR(36) NOT NULL,
    subject_epoch          VARCHAR(36) NOT NULL,
    scope_id               VARCHAR(36) NOT NULL,
    operation_type         VARCHAR(32) NOT NULL,
    idempotency_key        VARCHAR(128) NOT NULL,
    status                 VARCHAR(32) NOT NULL DEFAULT 'pending',
    request_snapshot       JSONB NOT NULL DEFAULT '{}'::JSONB,
    result_summary         JSONB NOT NULL DEFAULT '{}'::JSONB,
    artifact_uri           TEXT NOT NULL DEFAULT '',
    attempt_count          INTEGER NOT NULL DEFAULT 0,
    next_attempt_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    lease_until            TIMESTAMP WITH TIME ZONE,
    completed_at           TIMESTAMP WITH TIME ZONE,
    expires_at             TIMESTAMP WITH TIME ZONE,
    error_code             VARCHAR(64) NOT NULL DEFAULT '',
    error_message          TEXT NOT NULL DEFAULT '',
    created_at             TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_citation_profile_operation_attempts
        CHECK (attempt_count >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_citation_profile_operation_idem
    ON citation_profile_operations (tenant_id, subject_id, knowledge_base_id, subject_epoch, operation_type, idempotency_key);

CREATE INDEX IF NOT EXISTS idx_citation_profile_operations_scope
    ON citation_profile_operations (tenant_id, subject_id, knowledge_base_id, subject_epoch, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_citation_profile_operations_pending
    ON citation_profile_operations (operation_type, status, next_attempt_at, lease_until)
    WHERE completed_at IS NULL;

DO $$ BEGIN RAISE NOTICE '[Migration 000091] citation profile schema applied successfully'; END $$;
