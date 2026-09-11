-- Migration: 000093_citation_profile_outbox_due_index
-- Description: Add the global due-work index used by the durable Topic 4
-- recovery runner. The predicate excludes terminal rows and never rewrites
-- existing events or outbox data.
CREATE INDEX IF NOT EXISTS idx_citation_profile_outbox_due
    ON citation_profile_event_outbox (status, next_attempt_at, locked_at, created_at, id)
    WHERE delivered_at IS NULL AND deadletter_at IS NULL;

COMMENT ON INDEX idx_citation_profile_outbox_due IS
    'Topic 4 durable recovery scan: due pending/stale-delivering rows only; terminal rows are excluded';
