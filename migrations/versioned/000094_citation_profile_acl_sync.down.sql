-- Migration 000094 deliberately has no automatic rollback.
--
-- Once authority generations, leases and retry-budget pauses are live, their
-- absence cannot be reconstructed safely. Removing them could make stale
-- citation data readable or revive expired work. Refuse before taking a lock
-- or changing any schema/data; recovery requires an explicit audited forward
-- migration, never a persistent shadow copy of principal/evidence data.
DO $$
BEGIN
    RAISE EXCEPTION USING
        ERRCODE = '0A000',
        MESSAGE = 'migration 000094 down refused: irreversible ACL authority migration; use an audited forward recovery migration';
END $$;
