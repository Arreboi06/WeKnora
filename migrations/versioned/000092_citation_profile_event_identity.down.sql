-- Migration: 000092_citation_profile_event_identity down
--
-- This migration is intentionally irreversible. Its purpose is to make
-- terminal event and outbox identities unique even after retraction or
-- dead-lettering. Restoring the former partial indexes would permit the same
-- producer event to be admitted again, which would violate the Fact A audit
-- trail and make a rollback a semantic data-integrity downgrade.
DO $$
BEGIN
    RAISE EXCEPTION USING
        ERRCODE = '55000',
        MESSAGE = 'migration 000092 is irreversible; restore a pre-000092 backup or roll forward without dropping terminal identity constraints';
END $$;
