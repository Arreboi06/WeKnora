-- SQLite rollback: 000013_citation_profile
--
-- Topic 4 tables contain user-derived evidence, corrections, ACL state and
-- privacy-operation receipts. SQLite has no safe automatic reconstruction for
-- those rows. The signed-minimum ABS overflow is a schema-independent runtime
-- error: unlike a missing sentinel relation, no pre-created table/view can
-- bypass it. The named CTE keeps the operator-facing intent in the SQL source.
WITH migration_000013_down_refused_citation_profile_schema_contains_user_data(refusal) AS (
    SELECT abs(-9223372036854775808)
)
SELECT refusal
FROM migration_000013_down_refused_citation_profile_schema_contains_user_data;
