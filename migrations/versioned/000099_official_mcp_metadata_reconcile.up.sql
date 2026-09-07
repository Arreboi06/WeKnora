-- Migration: 000099_official_mcp_metadata_reconcile
--
-- Official main assigned version 000092 to mcp_metadata after the bound Topic
-- 2 base was frozen. Earlier local Workbench databases may already be above
-- 000092 and would otherwise never execute that official migration after
-- integration. Repeat its additive semantics idempotently here. This neither
-- marks a remote backend eligible nor drops or rewrites application data.

ALTER TABLE mcp_services
    ADD COLUMN IF NOT EXISTS usage_instructions TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS mcp_metadata (
    tenant_id BIGINT NOT NULL,
    service_id VARCHAR(36) NOT NULL REFERENCES mcp_services(id) ON DELETE CASCADE,
    principal VARCHAR(255) NOT NULL DEFAULT '',
    config_fingerprint VARCHAR(64) NOT NULL,
    tools JSONB NOT NULL,
    instructions TEXT NOT NULL DEFAULT '',
    server_name TEXT NOT NULL DEFAULT '',
    server_version TEXT NOT NULL DEFAULT '',
    server_description TEXT NOT NULL DEFAULT '',
    synced_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, service_id, principal)
);
