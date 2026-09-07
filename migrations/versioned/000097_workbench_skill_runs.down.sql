-- Migration: 000097_workbench_skill_runs down
-- Intentionally schema-retaining: runtime rollback disables Workbench admission
-- while preserving immutable candidate skill run evidence.
SELECT 1;
