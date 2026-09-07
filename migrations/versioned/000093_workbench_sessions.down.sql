-- T2-M01A rollback is intentionally schema-retaining.
-- Runtime rollback disables Workbench admission through WEKNORA_SANDBOX_WORKBENCH_ENABLED=false.
-- Keeping this additive table preserves incident evidence and read-old compatibility.
SELECT 1;
