-- Migration: 000091_citation_profile down
DO $$ BEGIN RAISE NOTICE '[Migration 000091 down] Dropping citation profile schema'; END $$;

DROP TABLE IF EXISTS citation_profile_operations;
DROP TABLE IF EXISTS citation_profile_corrections;
DROP TABLE IF EXISTS evidence_node_links;
DROP TABLE IF EXISTS evidence_resolution_runs;
DROP TABLE IF EXISTS wiki_source_ref_index;
DROP TABLE IF EXISTS citation_profile_event_outbox;
DROP TABLE IF EXISTS citation_profile_events;
DROP TABLE IF EXISTS citation_profile_scopes;
