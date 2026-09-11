-- Description: Make Topic 4 event and outbox identities terminal across
-- retraction/deadletter states. Existing duplicate data is never rewritten;
-- the migration fails with an actionable invariant error instead.
DO $$ BEGIN RAISE NOTICE '[Migration 000092] Auditing terminal citation identities'; END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM citation_profile_events
        GROUP BY tenant_id, subject_id, knowledge_base_id, subject_epoch,
                 message_id, origin_reference_index, source_knowledge_id,
                 source_result_id, COALESCE(source_chunk_index, -1)
        HAVING COUNT(*) > 1
    ) OR EXISTS (
        SELECT 1
        FROM citation_profile_events
        GROUP BY tenant_id, subject_id, knowledge_base_id, subject_epoch,
                 producer_event_key
        HAVING COUNT(*) > 1
    ) OR EXISTS (
        SELECT 1
        FROM citation_profile_event_outbox
        GROUP BY event_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23505',
            MESSAGE = 'citation profile terminal identity audit failed; resolve duplicate event/outbox identities before applying migration 000092';
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_citation_profile_event_producer_v2
    ON citation_profile_events (
        tenant_id,
        subject_id,
        knowledge_base_id,
        subject_epoch,
        message_id,
        origin_reference_index,
        source_knowledge_id,
        source_result_id,
        COALESCE(source_chunk_index, -1)
    );

CREATE UNIQUE INDEX IF NOT EXISTS uq_citation_profile_event_key_v2
    ON citation_profile_events (tenant_id, subject_id, knowledge_base_id, subject_epoch, producer_event_key);

CREATE UNIQUE INDEX IF NOT EXISTS uq_citation_profile_outbox_event_v2
    ON citation_profile_event_outbox (event_id);

-- IF NOT EXISTS makes a non-transactional/interrupted operator retry safe only
-- when the staging indexes are exactly the ones this migration owns. Never
-- promote a same-named drifted relation into the terminal identity contract.
DO $$
DECLARE
    expected RECORD;
    definition_ok BOOLEAN;
    actual_keys TEXT[];
BEGIN
    FOR expected IN
        SELECT *
        FROM (VALUES
            (
                'uq_citation_profile_event_producer_v2',
                'citation_profile_events',
                ARRAY[
                    'tenant_id',
                    'subject_id',
                    'knowledge_base_id',
                    'subject_epoch',
                    'message_id',
                    'origin_reference_index',
                    'source_knowledge_id',
                    'source_result_id',
                    'COALESCE(source_chunk_index, ''-1''::integer)'
                ]::TEXT[]
            ),
            (
                'uq_citation_profile_event_key_v2',
                'citation_profile_events',
                ARRAY[
                    'tenant_id',
                    'subject_id',
                    'knowledge_base_id',
                    'subject_epoch',
                    'producer_event_key'
                ]::TEXT[]
            ),
            (
                'uq_citation_profile_outbox_event_v2',
                'citation_profile_event_outbox',
                ARRAY['event_id']::TEXT[]
            )
        ) AS contracts(index_name, table_name, key_definitions)
    LOOP
        definition_ok := NULL;
        actual_keys := NULL;

        SELECT
            index_meta.indisunique
                AND index_meta.indisvalid
                AND index_meta.indisready
                AND index_meta.indislive
                AND index_meta.indimmediate
                AND NOT index_meta.indisprimary
                AND NOT index_meta.indisexclusion
                AND index_meta.indpred IS NULL
                AND position('NULLS NOT DISTINCT' IN pg_get_indexdef(index_meta.indexrelid)) = 0
                AND index_meta.indnkeyatts = cardinality(expected.key_definitions)
                AND index_meta.indnatts = cardinality(expected.key_definitions)
                AND table_relation.relname = expected.table_name
                AND access_method.amname = 'btree',
            ARRAY(
                SELECT pg_get_indexdef(index_meta.indexrelid, key_position, TRUE)
                FROM generate_series(1, index_meta.indnatts) AS key_positions(key_position)
                ORDER BY key_position
            )
        INTO definition_ok, actual_keys
        FROM pg_index AS index_meta
        JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
        JOIN pg_namespace AS index_namespace ON index_namespace.oid = index_relation.relnamespace
        JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
        JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
        JOIN pg_am AS access_method ON access_method.oid = index_relation.relam
        WHERE index_namespace.nspname = current_schema()
          AND table_namespace.nspname = current_schema()
          AND index_relation.relname = expected.index_name;

        IF definition_ok IS DISTINCT FROM TRUE
           OR actual_keys IS DISTINCT FROM expected.key_definitions THEN
            RAISE EXCEPTION USING
                ERRCODE = '55000',
                MESSAGE = format(
                    'migration 000092 invariant violation: staging index %I has an unexpected definition',
                    expected.index_name
                ),
                DETAIL = format(
                    'expected table=%I immediate non-primary non-exclusion unique valid ready live btree NULLS DISTINCT keys=%s; actual keys=%s',
                    expected.table_name,
                    expected.key_definitions,
                    COALESCE(actual_keys::TEXT, '<missing>')
                ),
                HINT = 'Remove or rename the drifted staging index after operator review, then retry from a clean migration state.';
        END IF;
    END LOOP;
END $$;

DROP INDEX IF EXISTS uq_citation_profile_event_producer;
DROP INDEX IF EXISTS uq_citation_profile_event_key;
DROP INDEX IF EXISTS uq_citation_profile_outbox_event;

ALTER INDEX uq_citation_profile_event_producer_v2 RENAME TO uq_citation_profile_event_producer;
ALTER INDEX uq_citation_profile_event_key_v2 RENAME TO uq_citation_profile_event_key;
ALTER INDEX uq_citation_profile_outbox_event_v2 RENAME TO uq_citation_profile_outbox_event;

DO $$ BEGIN RAISE NOTICE '[Migration 000092] Terminal citation identities applied'; END $$;
