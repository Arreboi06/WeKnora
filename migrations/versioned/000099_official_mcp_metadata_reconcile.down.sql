-- Schema-retaining rollback: this migration repairs an official-main schema
-- obligation that pre-release Workbench numbering may have skipped.
-- Runtime rollback disables Workbench admission and retains all evidence.
SELECT 1;
