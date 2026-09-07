-- Migration: 000098_workbench_legacy_numbering_reconcile
-- Compatibility repair for the local Topic 2 pre-release numbering collision.
--
-- The first local Workbench slice used version 000091 while the official main
-- branch later used 000091 for mcp_tool_approvals.enabled. Current Workbench
-- files are numbered 000093..000097. A database that already ran the local
-- 000091..000095 sequence will therefore skip the official 000091 body.
-- Repair the official column explicitly, then fail closed if the Workbench
-- tables expected from that pre-release sequence are absent. This migration
-- never drops or rewrites data and is PostgreSQL-only.

ALTER TABLE mcp_tool_approvals
    ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT true;

DO $$
DECLARE
    missing_tables TEXT[];
BEGIN
    SELECT array_agg(required.table_name ORDER BY required.table_name)
      INTO missing_tables
      FROM (VALUES
          ('workbench_sessions'),
          ('workbench_jobs'),
          ('workbench_commands'),
          ('workbench_runner_events'),
          ('workbench_audit_outbox'),
          ('workbench_artifact_versions'),
          ('workbench_skill_runs')
      ) AS required(table_name)
     WHERE to_regclass('public.' || required.table_name) IS NULL;

    IF missing_tables IS NOT NULL THEN
        RAISE EXCEPTION
            'legacy Topic 2 numbering reconciliation found missing Workbench tables: %; refuse silent schema downgrade',
            array_to_string(missing_tables, ', ');
    END IF;
END $$;
