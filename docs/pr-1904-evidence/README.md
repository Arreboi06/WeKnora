# WeKnora PR #1904 verification evidence

This directory stores evidence for the existing PR branch only. The images are
direct captures of real local terminal windows; they are not mock-service
screenshots. Secrets, bearer tokens, and passwords are intentionally omitted.

## Verified identity

- Implementation HEAD: `ce0c6bed3a25afdbca45cccfd3682adf077321d7`
- Commit subject: `fix(mysql): secure database and retriever connections`
- Official upstream main used for the final merge gate:
  `5780affdfc76342ddd0f5cf95b548a1a4d0b2a5a`
- Local branch: `codex/issue-1418-main-db-compat`
- Worktree at final verification: clean
- Merge check: `git merge-tree --write-tree HEAD upstream/main` succeeded
  with no content conflict
- Range check: `git diff --check upstream/main...HEAD` passed
- Final range: 122 files, 12,186 insertions, 590 deletions
- Final server binary:
  `D:\agent\runtime\weknora-pr1904-tls-20260731a\weknora-server-ce0c6bed.exe`
- Final server binary SHA-256:
  `DAC011505354629584C6438B51AE25176244657CA2445F801C9B7774FD54F078`

## Issue interpretation

Issue #1418 asks for configurable MySQL as the backend database. The
implementation treats that as application/business-database support for MySQL,
while preserving PostgreSQL and SQLite compatibility. A MySQL vector-store
retriever is included so a PostgreSQL-free deployment can also keep retrieval
data in MySQL when configured that way.

## What the implementation covers

- MySQL main database startup and DSN construction
- GORM MySQL dialector wiring
- Versioned MySQL migration up/down behavior
- Dirty migration fail-closed behavior
- Dialect-sensitive SQL for repositories and services
- Task queue and reset semantics on MySQL
- MySQL vector-store registration and API health probing
- JSON embedding storage, FULLTEXT keyword search, cosine ranking, TopK,
  batch mutation paths, and empty/error paths
- TLS/private CA/mTLS/SNI support for both the main database and env MySQL
  retriever
- Docker Compose and Helm configuration surfaces for MySQL/TLS settings

## Exact-head verification

All local commands used D-drive temp/cache locations. Real MySQL tests used
MySQL Community Server 8.4.10. The TLS runtime used verified server identity
and client certificates.

| Gate | Result | Evidence |
| --- | --- | --- |
| `git diff --check upstream/main...HEAD` | PASS | no whitespace errors |
| merge with official `upstream/main` | PASS | no content conflict |
| `go test ./internal/database -run '^TestMySQLTLSMainDatabaseAndRetrieverIntegration$' -count=1 -v` | PASS | verified migration TLS up, rollback one, restore, main DB write/read, TLS cipher, retriever TLS, plaintext and wrong-hostname negative controls |
| `go test ./internal/application/repository -run '^TestMySQL' -count=1 -v` | PASS | includes real MySQL task queue concurrent-claim coverage |
| `go test ./internal/application/repository/retriever/mysql -count=1 -v` | PASS | includes JSON embeddings, FULLTEXT, cosine, TopK, batch mutation, empty/error paths |
| `go test ./internal/types -run '^TestMySQLVectorStoreConnectionIntegration$' -count=1 -v` | PASS | real vector-store connection policy |
| `go test ./internal/container -run 'Test.*MySQL|Test.*Database|Test.*Engine' -count=1 -v` | PASS | includes MySQL reset coverage |
| authenticated WeKnora API probe on exact server binary | PASS | register, login, list vector-store types, list env store, test `__env_mysql__`, logout |

The API probe observed:

```json
{"commit":"ce0c6bed3a25afdbca45cccfd3682adf077321d7","health":"ok","register_success":true,"login_success":true,"mysql_type_registered":true,"env_store_listed":true,"probe_success":true,"probe_version":"8.4.10","logout":"ok"}
```

## Broader matrix already exercised during the final hardening

The following also passed during the same final hardening pass, using the
D-drive Go/toolchain cache and real MySQL where applicable:

- `go test ./internal/application/repository`
- `go test ./internal/application/repository/retriever/mysql`
- `go test ./internal/database -run 'Test.*MySQL|TestMigration'`
- `go test ./internal/types -run 'TestGetVectorStoreTypes|TestVectorStore'`
- `go test ./internal/container -run 'Test.*MySQL|Test.*Database|Test.*Engine'`
- service-level MySQL configuration tests
- handler env-store dispatch tests
- SQLite retriever regression tests with D-drive C/C++ headers/toolchain
- `go vet` on affected backend packages
- Compose rendering checks
- Helm lint and render checks

## Visual evidence

Current-head artifacts:

| Artifact | SHA-256 |
| --- | --- |
| `live-final-head-ce0c6bed.png` | `F4057A3DDFFA4AD58964C65C4CB834EEA31AA7C11BAD5AC290EF53792ADB1309` |
| `live-mysql-tls-runtime-ce0c6bed.png` | `91D71291145A3D8484118B5970EEFFFF0A1F5E0E619D682D5E5852AEF6161178` |
| `live-api-tls-ce0c6bed.png` | `0D91839E2A56D4D44CA24D80DB27C295F61D315878D170211C12EB4BCDE7A08C` |
| `live-verification-ce0c6bed.gif` | `79581B5C967A14FE8CF47F3DFB772E052EDB884296AFAEB63012420D364111DC` |

The GIF is an animated sequence of the three current-head terminal captures,
not a continuous screen recording.

Older `c5b0a356` artifacts remain in this directory as historical evidence
from the previous final pass. The `ce0c6bed` artifacts above supersede them for
the current PR head.

## Limits not hidden by this evidence

- The latest `ce0c6bed` server was verified through MySQL/TLS startup, API
  registration/login, vector-store registration, env MySQL probe, migrations,
  repository behavior, reset behavior, and retriever tests. A fresh end-to-end
  external DocReader + embedding model + chat model upload-to-answer run was
  not repeated on `ce0c6bed`.
- MySQL vector ranking is a correctness-first exact scan over candidate rows,
  not an ANN implementation.
- No automatic PostgreSQL-to-MySQL data migration is provided.
- MariaDB, TiDB, OceanBase, and Percona-specific variants were not claimed as
  validated.
- Docker was not available on this Windows host, so real Docker Compose startup
  was not executed locally; Compose/Helm config was rendered and linted.

Within the verified Issue #1418 scope, no reproducible local merge-blocking
defect remains. This is an evidence-based readiness statement, not a guarantee
that a maintainer will merge the PR.
