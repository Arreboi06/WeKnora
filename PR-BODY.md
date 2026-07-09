## Summary

Adds first-class MySQL support across the backend database path, the retriever path, and the VectorStore API surface.

## Why

Issue #1418 asks to make MySQL available because it is more common and easier for users to provision than PostgreSQL. This PR keeps PostgreSQL as the default, but makes MySQL a real supported option instead of a config-only placeholder.

Closes #1418.

## What Changed

- Adds `DB_DRIVER=mysql` support with a MySQL GORM dialector and MySQL migration source selection.
- Adds versioned MySQL migrations through the current schema baseline, including the tenant API key follow-up migration.
- Adds a local MySQL 8.0 development profile and updates migration/dev scripts to honor `DB_DRIVER=mysql`.
- Adds a MySQL 8.0+ retriever that stores embeddings as JSON arrays, calculates cosine similarity with portable MySQL JSON functions, and uses FULLTEXT indexes for keyword retrieval.
- Registers `RETRIEVE_DRIVER=mysql` for both keyword and vector retrieval and exposes it through tenant default engine mapping.
- Exposes MySQL as a DB-managed VectorStore type with connection validation, SSRF validation, healthcheck/version detection, env-store projection, and dynamic registry creation.
- Adds per-dimension table-prefix support through `MYSQL_TABLE_PREFIX` and `index_config.collection_prefix`.

## Reliability Work

- Uses `go-sql-driver/mysql.Config.FormatDSN()` for MySQL retriever and app-database DSNs so special characters in credentials do not break connection strings.
- Fixes MySQL keyword retrieval placeholder ordering when filters are present.
- Sorts keyword results globally across per-dimension MySQL tables before applying TopK.
- Normalizes empty/invalid TopK to a safe default.
- Preserves `is_enabled` in scanned retrieval results.
- Safely quotes generated MySQL table names in DDL/DML and normalizes effective table prefixes for duplicate detection.
- Adds MySQL to score normalization and known-engine handling for multi-store fanout.

## Validation

- `git diff --check`
- Static migration parity checks: MySQL up/down versions are paired and no migration file contains accidental `;;`.
- Static script/config checks: MySQL dev profile, targeted compose service selection, and `DB_DRIVER=mysql` migration path are present.
- Unit coverage added for MySQL env stores, VectorStore type metadata, tenant engine mapping, migration source selection, DSN construction, retriever SQL construction, table-prefix normalization, and score normalization registration.
- Go tests were prepared but not executed in this local environment because `go`, `gofmt`, `bash`, Docker, and `migrate` are not installed on the runner used for this update.

## Notes

PostgreSQL and SQLite remain env-store-only for VectorStore registration. MySQL is registerable because the retriever supports per-store physical table isolation through `collection_prefix`, making multiple MySQL stores meaningful.
