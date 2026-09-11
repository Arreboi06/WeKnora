-- Migration: 000094_citation_profile_acl_sync
-- Description: Persist server-derived ACL bindings, generation-fenced refresh
-- leases, and evidence-outbox retry-budget suspension accounting.
BEGIN;

LOCK TABLE citation_profile_scopes, citation_profile_event_outbox IN ACCESS EXCLUSIVE MODE;

ALTER TABLE citation_profile_scopes
    ADD COLUMN acl_check_lease_token VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN acl_checked_at TIMESTAMPTZ,
    ADD COLUMN acl_principal_type VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN acl_principal_id VARCHAR(512) NOT NULL DEFAULT '',
    ADD COLUMN acl_authenticated_tenant_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN acl_api_key_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN acl_access_path VARCHAR(32) NOT NULL DEFAULT '',
    ADD COLUMN acl_access_path_id VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN acl_generation BIGINT NOT NULL DEFAULT 0;

ALTER TABLE citation_profile_event_outbox
    ADD COLUMN lease_until TIMESTAMPTZ,
    ADD COLUMN retry_budget_paused_at TIMESTAMPTZ,
    ADD COLUMN retry_budget_paused_seconds BIGINT NOT NULL DEFAULT 0;

-- One database-coordinated feature transition prevents a serial rolling
-- deployment from repeatedly quarantining the same scopes after the first
-- replica has already refreshed them. Application startup only advances this
-- marker from disabled to enabled; locally disabled peers must not clear it
-- while another replica may still serve or refresh citation profiles.
CREATE TABLE citation_profile_acl_runtime_state (
    id                    SMALLINT PRIMARY KEY CHECK (id = 1),
    enabled               BOOLEAN NOT NULL,
    transition_generation BIGINT NOT NULL CHECK (transition_generation >= 0),
    changed_at            TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL
);
INSERT INTO citation_profile_acl_runtime_state (
    id, enabled, transition_generation, changed_at, updated_at
) VALUES (1, FALSE, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

-- A rolling old process must not create an implicitly readable scope while it
-- is unaware of the new authority-binding columns.
ALTER TABLE citation_profile_scopes
    ALTER COLUMN acl_check_state SET DEFAULT 'unknown';

ALTER TABLE citation_profile_scopes
    ADD CONSTRAINT chk_citation_profile_acl_generation CHECK (acl_generation >= 0);
ALTER TABLE citation_profile_event_outbox
    ADD CONSTRAINT chk_citation_profile_retry_pause CHECK (retry_budget_paused_seconds >= 0);

-- 000091 shipped an ACL queue index whose leading acl_check_state column and
-- key order cannot satisfy the fair claim query. Replace that deployed shape
-- here (rather than rewriting migration history) with the exact live-scope
-- predicate and deterministic queue order used by ClaimCitationProfileACLScopes.
DROP INDEX idx_citation_profile_scopes_acl_queue;
CREATE INDEX idx_citation_profile_scopes_acl_queue
    ON citation_profile_scopes (
        (CASE WHEN next_acl_check_at IS NULL THEN 0 ELSE 1 END),
        next_acl_check_at,
        tenant_id,
        id
    )
    WHERE enabled = TRUE AND deleted_at IS NULL AND fenced_at IS NULL;

-- 000093 could only index the legacy locked_at timestamp. Replace it after
-- adding the durable deadline so stale-lease recovery and all claim CAS paths
-- use the same persisted clock boundary.
DROP INDEX idx_citation_profile_outbox_due;
CREATE INDEX idx_citation_profile_outbox_due
    ON citation_profile_event_outbox (status, next_attempt_at, lease_until, created_at, id)
    WHERE delivered_at IS NULL AND deadletter_at IS NULL;
COMMENT ON INDEX idx_citation_profile_outbox_due IS
    'Topic 4 durable recovery scan: pending or expired delivering leases; terminal rows are excluded';

-- Keep transition evidence only for this transaction. The previous design
-- retained duplicate principal/evidence identities in permanent restore
-- tables indefinitely. Opaque row IDs and old concurrency state are enough to
-- prove the locked transition, and ON COMMIT DROP prevents a second durable
-- copy from escaping normal privacy purges.
CREATE TEMP TABLE citation_profile_acl_sync_v94_scope_transition (
    scope_id                        VARCHAR(36) PRIMARY KEY,
    old_acl_check_state             VARCHAR(32) NOT NULL,
    old_next_acl_check_at           TIMESTAMPTZ,
    old_acl_check_lease_until       TIMESTAMPTZ,
    old_acl_generation              BIGINT NOT NULL,
    old_profile_read_version        BIGINT NOT NULL,
    old_updated_at                  TIMESTAMPTZ NOT NULL,
    migration_suspended_at          TIMESTAMPTZ NOT NULL
) ON COMMIT DROP;

CREATE TEMP TABLE citation_profile_acl_sync_v94_outbox_transition (
    outbox_id                       VARCHAR(36) PRIMARY KEY,
    scope_id                        VARCHAR(36) NOT NULL,
    old_status                      VARCHAR(32) NOT NULL,
    old_locked_at                   TIMESTAMPTZ,
    old_locked_by                   VARCHAR(128) NOT NULL,
    old_updated_at                  TIMESTAMPTZ NOT NULL,
    migration_paused_at             TIMESTAMPTZ NOT NULL
) ON COMMIT DROP;

-- Legacy live scopes have no replayable authority proof. Capture only the
-- operational state needed for a same-transaction CAS/audit, never principal,
-- tenant, subject, knowledge-base or event data.
INSERT INTO pg_temp.citation_profile_acl_sync_v94_scope_transition (
    scope_id, old_acl_check_state, old_next_acl_check_at,
    old_acl_check_lease_until, old_acl_generation,
    old_profile_read_version, old_updated_at, migration_suspended_at
)
SELECT id, acl_check_state, next_acl_check_at, acl_check_lease_until,
       acl_generation, profile_read_version, updated_at, CURRENT_TIMESTAMP
FROM citation_profile_scopes
WHERE deleted_at IS NULL
  AND fenced_at IS NULL
  AND enabled = TRUE
  AND (acl_principal_type = '' OR acl_principal_id = '' OR acl_authenticated_tenant_id = 0);

INSERT INTO pg_temp.citation_profile_acl_sync_v94_outbox_transition (
    outbox_id, scope_id, old_status, old_locked_at, old_locked_by,
    old_updated_at, migration_paused_at
)
SELECT outbox.id, outbox.scope_id, outbox.status, outbox.locked_at,
       outbox.locked_by, outbox.updated_at, transition.migration_suspended_at
FROM citation_profile_event_outbox AS outbox
JOIN citation_profile_scopes AS scope
  ON scope.id = outbox.scope_id
 AND scope.tenant_id = outbox.tenant_id
 AND scope.subject_id = outbox.subject_id
 AND scope.knowledge_base_id = outbox.knowledge_base_id
 AND scope.subject_epoch = outbox.subject_epoch
JOIN pg_temp.citation_profile_acl_sync_v94_scope_transition AS transition
  ON transition.scope_id = scope.id
WHERE outbox.delivered_at IS NULL
  AND outbox.deadletter_at IS NULL
  AND outbox.retry_budget_paused_at IS NULL;

UPDATE citation_profile_scopes AS live
SET acl_check_state = 'unknown',
    acl_checked_at = NULL,
    next_acl_check_at = transition.migration_suspended_at,
    acl_check_lease_until = NULL,
    acl_check_lease_token = '',
    acl_principal_type = '',
    acl_principal_id = '',
    acl_authenticated_tenant_id = 0,
    acl_api_key_id = 0,
    acl_access_path = '',
    acl_access_path_id = '',
    acl_generation = transition.old_acl_generation + 1,
    profile_read_version = transition.old_profile_read_version + 1,
    updated_at = transition.migration_suspended_at
FROM pg_temp.citation_profile_acl_sync_v94_scope_transition AS transition
WHERE live.id = transition.scope_id
  AND live.enabled IS TRUE
  AND live.deleted_at IS NULL
  AND live.fenced_at IS NULL
  AND live.acl_check_state IS NOT DISTINCT FROM transition.old_acl_check_state
  AND live.next_acl_check_at IS NOT DISTINCT FROM transition.old_next_acl_check_at
  AND live.acl_check_lease_until IS NOT DISTINCT FROM transition.old_acl_check_lease_until
  AND live.acl_generation = transition.old_acl_generation
  AND live.profile_read_version = transition.old_profile_read_version
  AND live.updated_at = transition.old_updated_at;

UPDATE citation_profile_event_outbox AS outbox
SET retry_budget_paused_at = transition.migration_paused_at,
    status = 'pending',
    locked_at = NULL,
    lease_until = NULL,
    locked_by = '',
    updated_at = transition.migration_paused_at
FROM pg_temp.citation_profile_acl_sync_v94_outbox_transition AS transition,
     citation_profile_scopes AS scope
WHERE scope.id = transition.scope_id
  AND outbox.id = transition.outbox_id
  AND outbox.scope_id = scope.id
  AND outbox.tenant_id = scope.tenant_id
  AND outbox.subject_id = scope.subject_id
  AND outbox.knowledge_base_id = scope.knowledge_base_id
  AND outbox.subject_epoch = scope.subject_epoch
  AND outbox.status IS NOT DISTINCT FROM transition.old_status
  AND outbox.locked_at IS NOT DISTINCT FROM transition.old_locked_at
  AND outbox.locked_by IS NOT DISTINCT FROM transition.old_locked_by
  AND outbox.updated_at = transition.old_updated_at
  AND outbox.delivered_at IS NULL
  AND outbox.deadletter_at IS NULL
  AND outbox.retry_budget_paused_at IS NULL;

-- No pre-094 worker had a durable lease deadline. Normalize every legacy lock,
-- including rows outside a live ACL scope, before enforcing the bidirectional
-- invariant. A rolling old writer after COMMIT will then fail its transaction
-- instead of publishing immediately reclaimable delivering work.
UPDATE citation_profile_event_outbox
SET status = CASE
        WHEN delivered_at IS NOT NULL THEN 'delivered'
        WHEN deadletter_at IS NOT NULL THEN 'deadletter'
        ELSE 'pending'
    END,
    locked_at = NULL,
    lease_until = NULL,
    locked_by = '',
    updated_at = CURRENT_TIMESTAMP
WHERE status = 'delivering'
   OR locked_at IS NOT NULL
   OR lease_until IS NOT NULL
   OR locked_by <> '';

ALTER TABLE citation_profile_event_outbox
    ADD CONSTRAINT chk_citation_profile_outbox_lease CHECK (
        (
            status = 'delivering'
            AND delivered_at IS NULL
            AND deadletter_at IS NULL
            AND locked_at IS NOT NULL
            AND btrim(locked_by) <> ''
            AND lease_until IS NOT NULL
            AND lease_until > locked_at
        )
        OR (
            status <> 'delivering'
            AND locked_at IS NULL
            AND lease_until IS NULL
            AND locked_by = ''
        )
    );

-- Disabled/fenced historical rows may retain their old display state, but a
-- live CURRENT row must carry a complete, internally consistent authority
-- binding and a bounded successful check. This rejects explicit writes from a
-- rolling old binary even when it supplies the historical CURRENT literal.
ALTER TABLE citation_profile_scopes
    ADD CONSTRAINT chk_citation_profile_acl_current_binding CHECK (
        acl_check_state <> 'current'
        OR NOT (enabled = TRUE AND deleted_at IS NULL AND fenced_at IS NULL)
        OR (
            acl_checked_at IS NOT NULL
            AND next_acl_check_at IS NOT NULL
            AND next_acl_check_at > acl_checked_at
            AND acl_generation > 0
            AND acl_check_lease_token = ''
            AND acl_check_lease_until IS NULL
            AND tenant_id > 0
            AND btrim(knowledge_base_id) <> ''
            AND btrim(acl_principal_id) <> ''
            AND acl_authenticated_tenant_id > 0
            AND (
                (acl_principal_type = 'web_user' AND acl_api_key_id = 0)
                OR (acl_principal_type IN ('api_tenant', 'api_external_user') AND acl_api_key_id > 0)
            )
            AND (
                (acl_access_path = 'owner' AND btrim(acl_access_path_id) = '' AND acl_authenticated_tenant_id = tenant_id)
                OR (acl_access_path = 'kb_share' AND btrim(acl_access_path_id) = btrim(knowledge_base_id))
                OR (acl_access_path = 'agent_share' AND btrim(acl_access_path_id) <> '')
            )
        )
    );

-- The row-count/CAS audit prevents a partially applied data transition from
-- being accepted as a clean schema migration.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_temp.citation_profile_acl_sync_v94_scope_transition AS transition
        LEFT JOIN citation_profile_scopes AS live ON live.id = transition.scope_id
        WHERE live.id IS NULL
           OR live.acl_check_state IS DISTINCT FROM 'unknown'
           OR live.acl_checked_at IS NOT NULL
           OR live.next_acl_check_at IS DISTINCT FROM transition.migration_suspended_at
           OR live.acl_check_lease_until IS NOT NULL
           OR live.acl_check_lease_token IS DISTINCT FROM ''
           OR live.acl_principal_type IS DISTINCT FROM ''
           OR live.acl_principal_id IS DISTINCT FROM ''
           OR live.acl_authenticated_tenant_id IS DISTINCT FROM 0
           OR live.acl_api_key_id IS DISTINCT FROM 0
           OR live.acl_access_path IS DISTINCT FROM ''
           OR live.acl_access_path_id IS DISTINCT FROM ''
           OR live.acl_generation IS DISTINCT FROM transition.old_acl_generation + 1
           OR live.profile_read_version IS DISTINCT FROM transition.old_profile_read_version + 1
           OR live.updated_at IS DISTINCT FROM transition.migration_suspended_at
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '55000',
            MESSAGE = 'migration 000094 failed to suspend every selected citation profile scope';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM pg_temp.citation_profile_acl_sync_v94_outbox_transition AS transition
        LEFT JOIN citation_profile_event_outbox AS outbox
          ON outbox.id = transition.outbox_id
         AND outbox.scope_id = transition.scope_id
        LEFT JOIN citation_profile_scopes AS scope
          ON scope.id = transition.scope_id
         AND outbox.tenant_id = scope.tenant_id
         AND outbox.subject_id = scope.subject_id
         AND outbox.knowledge_base_id = scope.knowledge_base_id
         AND outbox.subject_epoch = scope.subject_epoch
        WHERE outbox.id IS NULL
           OR scope.id IS NULL
           OR outbox.status IS DISTINCT FROM 'pending'
           OR outbox.locked_at IS NOT NULL
           OR outbox.lease_until IS NOT NULL
           OR outbox.locked_by IS DISTINCT FROM ''
           OR outbox.retry_budget_paused_at IS DISTINCT FROM transition.migration_paused_at
           OR outbox.retry_budget_paused_seconds IS DISTINCT FROM 0
           OR outbox.updated_at IS DISTINCT FROM transition.migration_paused_at
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '55000',
            MESSAGE = 'migration 000094 failed to pause every selected citation profile outbox';
    END IF;
END $$;

COMMENT ON COLUMN citation_profile_scopes.acl_generation IS
    'Monotonic ACL mutation fence; refresh results must CAS generation plus lease token';
COMMENT ON COLUMN citation_profile_event_outbox.retry_budget_paused_seconds IS
    'Accumulated ACL-suspension time excluded from durable resolution max-age';

COMMIT;
