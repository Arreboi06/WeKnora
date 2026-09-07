-- Migration: 000095_workbench_runner_identity rollback
-- Runtime rollback is schema-retaining: disable Workbench admission instead
-- of destructively rewriting control-plane evidence.

SELECT 1;
