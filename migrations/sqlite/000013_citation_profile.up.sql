-- SQLite migration: 000013_citation_profile
-- Mirrors the additive Topic 4 citation-profile schema. The Lite database uses
-- TEXT/INTEGER equivalents and application-generated UUIDs. This migration is
-- default-off: creating empty tables does not enroll or backfill any subject.

CREATE TABLE citation_profile_acl_runtime_state (
    id                    INTEGER PRIMARY KEY CHECK (id = 1),
    enabled               INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    transition_generation INTEGER NOT NULL CHECK (transition_generation >= 0),
    changed_at            DATETIME NOT NULL,
    updated_at            DATETIME NOT NULL
);
INSERT INTO citation_profile_acl_runtime_state (
    id, enabled, transition_generation, changed_at, updated_at
) VALUES (1, 0, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

CREATE TABLE citation_profile_scopes (
    id                       VARCHAR(36) PRIMARY KEY,
    tenant_id                INTEGER NOT NULL,
    subject_id               VARCHAR(512) NOT NULL,
    knowledge_base_id        VARCHAR(36) NOT NULL,
    subject_epoch            VARCHAR(36) NOT NULL,
    profile_read_version     INTEGER NOT NULL DEFAULT 0,
    profile_policy_version   VARCHAR(64) NOT NULL DEFAULT 'profile_policy_v1',
    retention_policy_version VARCHAR(64) NOT NULL DEFAULT 'retention_policy_v1',
    enabled                  INTEGER NOT NULL DEFAULT 1,
    active_run_id            VARCHAR(36),
    mapping_revision         INTEGER NOT NULL DEFAULT 0,
    source_universe_watermark VARCHAR(128) NOT NULL DEFAULT '',
    pending_event_count      INTEGER NOT NULL DEFAULT 0,
    pending_mapping_count    INTEGER NOT NULL DEFAULT 0,
    dirty_mapping_count      INTEGER NOT NULL DEFAULT 0,
    acl_check_state          VARCHAR(32) NOT NULL DEFAULT 'unknown',
    next_acl_check_at        DATETIME,
    acl_check_lease_until    DATETIME,
	acl_check_lease_token    VARCHAR(128) NOT NULL DEFAULT '',
	acl_checked_at           DATETIME,
	acl_principal_type       VARCHAR(32) NOT NULL DEFAULT '',
	acl_principal_id         VARCHAR(512) NOT NULL DEFAULT '',
	acl_authenticated_tenant_id INTEGER NOT NULL DEFAULT 0,
	acl_api_key_id           INTEGER NOT NULL DEFAULT 0,
	acl_access_path          VARCHAR(32) NOT NULL DEFAULT '',
	acl_access_path_id       VARCHAR(128) NOT NULL DEFAULT '',
	acl_generation           INTEGER NOT NULL DEFAULT 0,
    fenced_at                DATETIME,
    fence_reason             VARCHAR(64) NOT NULL DEFAULT '',
    delete_request_id        VARCHAR(36),
    created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at               DATETIME,
    CHECK (profile_read_version >= 0),
	CHECK (enabled IN (0, 1)),
	CHECK (acl_generation >= 0),
	CHECK (
		NOT (enabled = 1 AND deleted_at IS NULL AND fenced_at IS NULL AND acl_check_state = 'current')
		OR (
			acl_checked_at IS NOT NULL
			AND next_acl_check_at IS NOT NULL
			AND julianday(acl_checked_at) IS NOT NULL
			AND julianday(next_acl_check_at) IS NOT NULL
			AND julianday(next_acl_check_at) > julianday(acl_checked_at)
			AND acl_generation > 0
			AND acl_check_lease_token = ''
			AND acl_check_lease_until IS NULL
			AND tenant_id > 0
			AND TRIM(knowledge_base_id) <> ''
			AND TRIM(acl_principal_id) <> ''
			AND acl_authenticated_tenant_id > 0
			AND (
				(acl_principal_type = 'web_user' AND acl_api_key_id = 0)
				OR (acl_principal_type IN ('api_tenant', 'api_external_user') AND acl_api_key_id > 0)
			)
			AND (
				(acl_access_path = 'owner' AND TRIM(acl_access_path_id) = '' AND acl_authenticated_tenant_id = tenant_id)
				OR (acl_access_path = 'kb_share' AND TRIM(acl_access_path_id) = TRIM(knowledge_base_id))
				OR (acl_access_path = 'agent_share' AND TRIM(acl_access_path_id) <> '')
			)
		)
	),
    CHECK (pending_event_count >= 0 AND pending_mapping_count >= 0 AND dirty_mapping_count >= 0)
);

CREATE UNIQUE INDEX uq_citation_profile_scope_live
    ON citation_profile_scopes (tenant_id, subject_id, knowledge_base_id)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX uq_citation_profile_scope_epoch
    ON citation_profile_scopes (tenant_id, subject_id, knowledge_base_id, subject_epoch);
CREATE INDEX idx_citation_profile_scopes_kb
    ON citation_profile_scopes (tenant_id, knowledge_base_id, deleted_at);
CREATE INDEX idx_citation_profile_scopes_acl_queue
    ON citation_profile_scopes (
        CASE WHEN next_acl_check_at IS NULL THEN 0 ELSE 1 END,
        next_acl_check_at,
        tenant_id,
        id
    )
    WHERE enabled = 1 AND deleted_at IS NULL AND fenced_at IS NULL;

CREATE TABLE citation_profile_events (
    id                       VARCHAR(36) PRIMARY KEY,
    tenant_id                INTEGER NOT NULL,
    subject_id               VARCHAR(512) NOT NULL,
    knowledge_base_id        VARCHAR(36) NOT NULL,
    subject_epoch            VARCHAR(36) NOT NULL,
    scope_id                 VARCHAR(36) NOT NULL,
    session_id               VARCHAR(36) NOT NULL DEFAULT '',
    message_id               VARCHAR(36) NOT NULL,
    message_version          VARCHAR(64) NOT NULL DEFAULT '',
    message_completed_at     DATETIME,
    origin_reference_index   INTEGER NOT NULL,
    source_knowledge_id      VARCHAR(36) NOT NULL,
    source_result_id         VARCHAR(128) NOT NULL DEFAULT '',
    source_chunk_index       INTEGER,
    source_ref_raw           TEXT NOT NULL DEFAULT '',
    source_ref_normalized    VARCHAR(512) NOT NULL DEFAULT '',
    source_refs_snapshot     TEXT NOT NULL DEFAULT '{}',
    knowledge_snapshot       TEXT NOT NULL DEFAULT '{}',
    knowledge_base_proof     TEXT NOT NULL DEFAULT '{}',
    producer_event_key       VARCHAR(512) NOT NULL,
    content_hash             VARCHAR(64) NOT NULL DEFAULT '',
    status                   VARCHAR(32) NOT NULL DEFAULT 'pending_resolution',
    active_run_id            VARCHAR(36),
    pending_reason           VARCHAR(64) NOT NULL DEFAULT '',
    failed_reason            TEXT NOT NULL DEFAULT '',
    resolved_at              DATETIME,
    retracted_at             DATETIME,
    created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (origin_reference_index >= 0),
    CHECK (source_chunk_index IS NULL OR source_chunk_index >= 0)
);

-- Topic 4 terminal identity is unconditional in Lite too: a retracted
-- producer event cannot be recreated under the same stable identity.
CREATE UNIQUE INDEX uq_citation_profile_event_producer
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
    );
CREATE UNIQUE INDEX uq_citation_profile_event_key
    ON citation_profile_events (tenant_id, subject_id, knowledge_base_id, subject_epoch, producer_event_key);
CREATE INDEX idx_citation_profile_events_scope_status
    ON citation_profile_events (tenant_id, subject_id, knowledge_base_id, subject_epoch, status, created_at);
CREATE INDEX idx_citation_profile_events_message
    ON citation_profile_events (tenant_id, message_id);
CREATE INDEX idx_citation_profile_events_active_run
    ON citation_profile_events (active_run_id)
    WHERE active_run_id IS NOT NULL;
CREATE INDEX idx_citation_profile_events_source
    ON citation_profile_events (tenant_id, knowledge_base_id, source_knowledge_id, status);

CREATE TABLE citation_profile_event_outbox (
    id                    VARCHAR(36) PRIMARY KEY,
    tenant_id             INTEGER NOT NULL,
    subject_id             VARCHAR(512) NOT NULL,
    knowledge_base_id     VARCHAR(36) NOT NULL,
    subject_epoch         VARCHAR(36) NOT NULL,
    scope_id              VARCHAR(36) NOT NULL,
    event_id              VARCHAR(36) NOT NULL,
    status                VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempt_count         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    locked_at             DATETIME,
    lease_until           DATETIME,
    locked_by             VARCHAR(128) NOT NULL DEFAULT '',
    delivered_at          DATETIME,
    deadletter_at         DATETIME,
    last_error_code       VARCHAR(64) NOT NULL DEFAULT '',
    last_error_message    TEXT NOT NULL DEFAULT '',
	retry_budget_paused_at DATETIME,
	retry_budget_paused_seconds INTEGER NOT NULL DEFAULT 0,
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (attempt_count >= 0),
	CHECK (retry_budget_paused_seconds >= 0),
	CHECK (
		(
			status = 'delivering'
			AND delivered_at IS NULL
			AND deadletter_at IS NULL
			AND locked_at IS NOT NULL
			AND julianday(locked_at) IS NOT NULL
			AND TRIM(locked_by) <> ''
			AND lease_until IS NOT NULL
			AND julianday(lease_until) IS NOT NULL
			AND julianday(lease_until) > julianday(locked_at)
		)
		OR (
			status <> 'delivering'
			AND locked_at IS NULL
			AND lease_until IS NULL
			AND locked_by = ''
		)
	)
);
CREATE UNIQUE INDEX uq_citation_profile_outbox_event
    ON citation_profile_event_outbox (event_id);
CREATE INDEX idx_citation_profile_outbox_pending
    ON citation_profile_event_outbox (tenant_id, knowledge_base_id, subject_epoch, status, next_attempt_at, locked_at);
CREATE INDEX idx_citation_profile_outbox_due
    ON citation_profile_event_outbox (status, next_attempt_at, lease_until, created_at, id)
    WHERE delivered_at IS NULL AND deadletter_at IS NULL;

CREATE TABLE wiki_source_ref_index (
    id                   VARCHAR(36) PRIMARY KEY,
    tenant_id            INTEGER NOT NULL,
    knowledge_base_id    VARCHAR(36) NOT NULL,
    source_knowledge_id  VARCHAR(36) NOT NULL,
    page_uuid            VARCHAR(36) NOT NULL,
    page_version         INTEGER NOT NULL,
    page_slug            VARCHAR(255) NOT NULL DEFAULT '',
    page_title           VARCHAR(512) NOT NULL DEFAULT '',
    normalized_ref       VARCHAR(512) NOT NULL,
    mapping_revision     INTEGER NOT NULL,
    lifecycle_state      VARCHAR(32) NOT NULL DEFAULT 'current',
    index_watermark      VARCHAR(128) NOT NULL DEFAULT '',
    indexed_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (page_version > 0),
    CHECK (mapping_revision >= 0)
);
CREATE UNIQUE INDEX uq_wiki_source_ref_index_version
    ON wiki_source_ref_index (tenant_id, knowledge_base_id, source_knowledge_id, page_uuid, page_version, normalized_ref, mapping_revision, lifecycle_state);
CREATE INDEX idx_wiki_source_ref_lookup
    ON wiki_source_ref_index (tenant_id, knowledge_base_id, source_knowledge_id, lifecycle_state, mapping_revision);
CREATE INDEX idx_wiki_source_ref_page
    ON wiki_source_ref_index (tenant_id, knowledge_base_id, page_uuid, page_version, mapping_revision);

CREATE TABLE evidence_resolution_runs (
    id                     VARCHAR(36) PRIMARY KEY,
    tenant_id              INTEGER NOT NULL,
    subject_id             VARCHAR(512) NOT NULL,
    knowledge_base_id       VARCHAR(36) NOT NULL,
    subject_epoch          VARCHAR(36) NOT NULL,
    scope_id               VARCHAR(36) NOT NULL,
    event_id               VARCHAR(36) NOT NULL,
    status                 VARCHAR(32) NOT NULL,
    run_mapping_revision   INTEGER NOT NULL,
    run_universe_watermark VARCHAR(128) NOT NULL,
    input_hash             VARCHAR(64) NOT NULL DEFAULT '',
    output_hash            VARCHAR(64) NOT NULL DEFAULT '',
    input_count            INTEGER NOT NULL DEFAULT 0,
    output_count           INTEGER NOT NULL DEFAULT 0,
    error_code             VARCHAR(64) NOT NULL DEFAULT '',
    error_message          TEXT NOT NULL DEFAULT '',
    started_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at            DATETIME,
    failed_at              DATETIME,
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (input_count >= 0 AND output_count >= 0 AND output_count <= 100),
    CHECK (run_mapping_revision >= 0)
);
CREATE UNIQUE INDEX uq_evidence_resolution_run_token
    ON evidence_resolution_runs (event_id, run_mapping_revision, run_universe_watermark);
CREATE INDEX idx_evidence_resolution_runs_scope_status
    ON evidence_resolution_runs (tenant_id, subject_id, knowledge_base_id, subject_epoch, status, created_at);
CREATE INDEX idx_evidence_resolution_runs_event
    ON evidence_resolution_runs (event_id, created_at DESC);

CREATE TABLE evidence_node_links (
    id                     VARCHAR(36) PRIMARY KEY,
    tenant_id              INTEGER NOT NULL,
    subject_id             VARCHAR(512) NOT NULL,
    knowledge_base_id      VARCHAR(36) NOT NULL,
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
    mapping_revision       INTEGER NOT NULL,
    universe_watermark     VARCHAR(128) NOT NULL DEFAULT '',
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (page_version > 0),
    CHECK (mapping_revision >= 0)
);
CREATE UNIQUE INDEX uq_evidence_node_link_run_page
    ON evidence_node_links (resolution_run_id, page_uuid, page_version, normalized_ref);
CREATE INDEX idx_evidence_node_links_event
    ON evidence_node_links (event_id, relation_state);
CREATE INDEX idx_evidence_node_links_scope_page
    ON evidence_node_links (tenant_id, subject_id, knowledge_base_id, subject_epoch, page_uuid, relation_state);

CREATE TABLE citation_profile_corrections (
    id                     VARCHAR(36) PRIMARY KEY,
    tenant_id              INTEGER NOT NULL,
    subject_id             VARCHAR(512) NOT NULL,
    knowledge_base_id      VARCHAR(36) NOT NULL,
    subject_epoch          VARCHAR(36) NOT NULL,
    scope_id               VARCHAR(36) NOT NULL,
    event_id               VARCHAR(36) NOT NULL,
    page_uuid              VARCHAR(36) NOT NULL DEFAULT '',
    page_version           INTEGER,
    correction_type        VARCHAR(32) NOT NULL,
    reason                 TEXT NOT NULL DEFAULT '',
    actor_id               VARCHAR(512) NOT NULL,
    expected_read_version  INTEGER NOT NULL,
    resulting_read_version INTEGER NOT NULL,
    idempotency_key        VARCHAR(128) NOT NULL DEFAULT '',
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (expected_read_version >= 0 AND resulting_read_version > expected_read_version)
);
CREATE UNIQUE INDEX uq_citation_profile_correction_idem
    ON citation_profile_corrections (tenant_id, subject_id, knowledge_base_id, subject_epoch, idempotency_key)
	WHERE idempotency_key <> '';
CREATE INDEX idx_citation_profile_corrections_event_page
    ON citation_profile_corrections (tenant_id, subject_id, knowledge_base_id, subject_epoch, event_id, page_uuid, created_at DESC);
CREATE INDEX idx_citation_profile_corrections_actor
    ON citation_profile_corrections (tenant_id, actor_id, created_at DESC);

CREATE TABLE citation_profile_operations (
    id              VARCHAR(36) PRIMARY KEY,
    tenant_id       INTEGER NOT NULL,
    subject_id      VARCHAR(512) NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    subject_epoch   VARCHAR(36) NOT NULL,
    scope_id        VARCHAR(36) NOT NULL,
    operation_type  VARCHAR(32) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    status          VARCHAR(32) NOT NULL DEFAULT 'pending',
    request_snapshot TEXT NOT NULL DEFAULT '{}',
    result_summary   TEXT NOT NULL DEFAULT '{}',
    artifact_uri    TEXT NOT NULL DEFAULT '',
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_until     DATETIME,
    completed_at    DATETIME,
    expires_at      DATETIME,
    error_code      VARCHAR(64) NOT NULL DEFAULT '',
    error_message   TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (attempt_count >= 0)
);
CREATE UNIQUE INDEX uq_citation_profile_operation_idem
    ON citation_profile_operations (tenant_id, subject_id, knowledge_base_id, subject_epoch, operation_type, idempotency_key);
CREATE INDEX idx_citation_profile_operations_scope
    ON citation_profile_operations (tenant_id, subject_id, knowledge_base_id, subject_epoch, created_at DESC);
CREATE INDEX idx_citation_profile_operations_pending
    ON citation_profile_operations (operation_type, status, next_attempt_at, lease_until)
    WHERE completed_at IS NULL;
