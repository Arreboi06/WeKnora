-- Migration: 000096_workbench_artifacts rollback
-- Runtime rollback is schema-retaining: disable Workbench admission/capability
-- instead of destructively deleting immutable artifact evidence.

SELECT 1;
