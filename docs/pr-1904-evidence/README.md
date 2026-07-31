# WeKnora PR #1904 final verification

This report indexes checks that were actually executed for the existing PR
branch. It is not a mock-service report, and it does not treat screenshots or
PR prose as a substitute for tests. Secrets are intentionally omitted.

## Verified identity

- Implementation HEAD: `57d70e4273897d9595c3539c0aff850bb10b5921`
- Upstream base: `8dd997f0f406c19ce4ec9acd622456d24eac2130`
- Local branch: `codex/issue-1418-main-db-compat`
- `upstream base` is an ancestor of HEAD: yes
- Worktree at final verification: clean
- Final range: 111 files, 10,092 insertions, 554 deletions
- `git diff --check` (worktree and upstream range): PASS

## Issue interpretation and implementation scope

Issue #1418 asks for configurable MySQL as the backend database. The verified
implementation treats this as application/business-database configurability,
not as removal of PostgreSQL. PostgreSQL and SQLite remain supported. A MySQL
retriever/vector-store is also included as an optional path so that a
PostgreSQL-free deployment can store both business data and retrieval data in
MySQL; existing retrievers remain available.

The implementation covers MySQL startup/DSN configuration, the GORM MySQL
dialector, versioned migrations with up/down behavior, dialect-sensitive SQL,
queue claim concurrency, deployment configuration, vector-store registration
and health checks, JSON embeddings, FULLTEXT keyword matching, exact cosine
scoring, TopK, batch mutations, and error paths.

## Regressions found and fixed during independent verification

### Session defaults after migration rollback

A real MySQL migration cycle reproduced this pre-fix failure after migration
79 was rolled back to 78:

```text
Field 'fallback_response' doesn't have a default value
```

The integration test was added before the fix. The down migration now restores
the version-78 schema while preserving the defaults required by application
session creation. On the final HEAD,
`TestMySQLSessionDefaultsMatchApplicationCreate` and the full real-MySQL
migration round trip both pass.

### Zero vector threshold semantics

The previous MySQL candidate ranking discarded a negatively correlated vector
when `threshold=0`, while the existing SQLite contract treats zero as “no
filter.” The regression test
`TestRankVectorCandidatesZeroThresholdDoesNotFilterNegativeSimilarity` failed
before the fix and passes on the final HEAD. Positive-threshold filtering is
unchanged.

## Exact final test matrix

All Go commands below used D-drive TEMP/TMP and the D-drive Go/toolchain cache.
Real MySQL integration tests used MySQL 8.4.10 on `127.0.0.1:3310` and did not
skip.

| Area | Result | Observed Go time / detail |
| --- | --- | --- |
| `go test ./internal/application/repository -count=1 -v` | PASS | 4.101s; five real-MySQL integration tests ran |
| `go test ./internal/application/repository/retriever/mysql -count=1 -v` | PASS | 8.928s; includes zero-threshold regression; 200x1024 write 348.1292ms, exact retrieval 120.2316ms |
| `go test ./internal/database -run 'Test.*MySQL\|TestMigration' -count=1 -v` | PASS | 32.394s; real migration up/down/up and session-default contract ran |
| `go test ./internal/types -run 'TestGetVectorStoreTypes\|TestVectorStore' -count=1 -v` | PASS | 4.364s |
| `go test ./internal/container -run 'Test.*MySQL\|Test.*Database\|Test.*Engine' -count=1 -v` | PASS | 12.719s; live `TestCreateMySQLEngineIntegration` ran |
| `go test ./internal/application/service/retriever -count=1` | PASS | 4.792s |
| targeted application-service MySQL/database/vector-store/wiki/session tests | PASS | 4.510s |
| `go vet` on affected database/repository/retriever/types/container/service packages | PASS | no diagnostics |
| `git diff --check` and `git diff tencent/main...HEAD --check` | PASS | no diagnostics |

The Docker-backed Compose test could not run because Docker is not installed
on this host. Exact-final static deployment tests passed. Standalone Compose
and Helm rendering had also passed in the earlier deployment verification, but
that is not represented as an exact-final Docker runtime test.

## Real runtime and E2E evidence

- MySQL Community Server 8.4.10, strict six-mode SQL session and UTC timezone
- Schema migration version 79, `dirty=0`
- Final WeKnora server binary:
  `weknora-server-57d70e42.exe`, SHA-256
  `611D25B713C09AA142AEE76C4B61D9EA719CFDA8DEDADBD885D9B6186285B999`
- Final CLI binary: `weknora-cli-57d70e42.exe`, SHA-256
  `1394239708D33D2E8292FAFB4A399D9CC0C50D4A711C796B994E7D5740EC7DD3`
- Real DocReader 0.1.0 gRPC service
- Real Ollama `all-minilm` 384-dimensional embedding model
- Real Ollama `qwen2.5:0.5b` chat model

The initial fresh document upload, DocReader parse, summary, embedding, MySQL
storage, and enablement chain was executed on commit `366ec470`, before the
final rollback and zero-threshold hardening. The stored fixture metadata keeps
that lineage visible. The exact final binary at `57d70e42` was then used to
rerun authenticated health checks, MySQL vector-store registration/probe,
hybrid retrieval, CLI doctor, and grounded chat against those real stored
chunks.

Observed exact-final results:

- CLI doctor: 4 passed, 0 failed, 0 skipped
- MySQL vector-store type and environment store: registered
- MySQL health probe: success, detected version 8.4.10
- Hybrid query: two results; the top result contains `COBALTLANTERN4729`
- `knowledge_search`: success, two chunks, two references
- Grounded answer: `37 days`

This lineage distinction matters: the full initial upload was not rerun after
the final code-only hardening/rebase, while the exact final service did perform
the retrieval and answer path using the data produced by that real upload.

## Visual evidence

The PNG files are direct captures of actual terminal windows. The GIF is an
animated sequence of those four captures, not a continuous screen recording.

| Artifact | SHA-256 |
| --- | --- |
| `live-final-head-57d70e42.png` | `89AF6EA27BE09F97813D729265C6A91A57FD6F66E7B9C5AEAC68FF0E7B239BA3` |
| `live-mysql-runtime-57d70e42.png` | `8C2EA8840F99540193A231A57DEF5AE113D22661A0060D57F1525295BABEC975` |
| `live-api-retrieval-57d70e42.png` | `0E84A5AB25BF26EB85C055C689A14BFD4FCD785819563378503F1E0B43F591C6` |
| `live-cli-grounded-chat-57d70e42.png` | `68C1A99FD3991B793E420C6DAAFC528AFDAFFF682DB771F3AA04BFCE452A54DC` |
| `live-verification-57d70e42.gif` (4 frames, 1199x616) | `031CDC8B2D289EF7893CC9A4E7B906D81AFE49A50B5B891D72E7DA993603E07A` |

## Residual limits

- MySQL cosine ranking is a correctness-first exact scan with
  `O(candidate rows x dimensions)` cost; it is not an ANN index.
- MySQL TLS/private-CA/mTLS/SNI DSN configuration is not implemented.
- No automatic PostgreSQL-to-MySQL data migration is provided.
- Percona Server 8.0.16+ is accepted by compatibility checks but was not
  exercised in this run. MariaDB, TiDB, and OceanBase are intentionally
  rejected until independently validated.
- The queue claim contract still lacks ownership fencing after a stale claim
  is reassigned; that longer-window race is inherited from the existing
  cross-database contract and was not widened in this PR.
- A real Docker Compose startup was not available on this Windows host.

Within the verified Issue #1418 scope, no reproducible merge-blocking defect
remains. This is an evidence-based readiness statement, not a guarantee that a
maintainer will merge the PR.
