-- Migration: 000095_workbench_runner_identity
-- Relaxes queued workbench jobs so the protected runner can create the
-- control-plane row before binding the disposable backend identity.
-- PostgreSQL only; SQLite/Lite remains unsupported for Workbench.

DO $$
BEGIN
    ALTER TABLE workbench_jobs DROP CONSTRAINT IF EXISTS chk_workbench_jobs_backend_identity_nonempty;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_workbench_jobs_backend_identity_after_bind'
          AND conrelid = 'workbench_jobs'::regclass
    ) THEN
        ALTER TABLE workbench_jobs ADD CONSTRAINT chk_workbench_jobs_backend_identity_after_bind CHECK (
            state IN ('QUEUED', 'STARTING', 'CANCELLED', 'LOST') OR length(trim(backend_identity)) > 0
        );
    END IF;
END $$;
