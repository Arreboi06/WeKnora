-- T2-L01 rollback is intentionally schema-retaining.
-- Runtime rollback disables Workbench admission/capabilities while preserving
-- job, command, runner-event, and audit-outbox evidence for read-old and
-- incident-response compatibility.
SELECT 1;
