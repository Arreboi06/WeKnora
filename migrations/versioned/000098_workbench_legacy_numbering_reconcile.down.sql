-- 000098 is a compatibility repair. It is intentionally irreversible:
-- removing the repaired official column would break the mainline contract.
-- Schema and evidence are retained during runtime rollback (schema-retaining no-op).
SELECT 1;
