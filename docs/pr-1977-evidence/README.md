# PR #1977 verification evidence

Final implementation HEAD: `78ea4ab3de2ac2c858e15acb48de27c7fa80f315`

Latest official main: `8dd997f0f406c19ce4ec9acd622456d24eac2130`

Merge-base after merge: `8dd997f0f406c19ce4ec9acd622456d24eac2130`

Implementation branch: `codex/issue-1679-content-cache`

Evidence branch: `codex/pr-1977-evidence`

## Assets

- `status.png` - final status snapshot for the implementation branch.
- `verification-matrix.png` - current command/result matrix for this validation pass.
- `cache-flow.gif` - visual summary of cache reuse, invalidation, stale cleanup, and attempt fences.
- `cache-flow-final.png` - final GIF frame for static renderers.

## Validation environment

All Go build/test cache, module cache, temporary files, and CGO artifacts were kept on `D:\agent`.

- Go: `D:\agent\tools\go`
- GCC: `D:\agent\tools\winlibs-gcc-16.1.0-ucrt\bin`
- `GOPATH`: `D:\agent\go-1977`
- `GOMODCACHE`: `D:\agent\cache\gomod-1977`
- `GOCACHE`: `D:\agent\cache\go-build-1977-ucrt`
- `GOTELEMETRYDIR`: `D:\agent\cache\go-telemetry-1977`
- `TEMP` / `TMP` / `GOTMPDIR`: `D:\agent\temp`
- `GOPROXY`: `https://goproxy.cn,direct`
- CGO: `CGO_ENABLED=1`, `CC=gcc`, `CXX=g++`
- SQLite headers: `D:\agent\tools\sqlite-headers-1977`

## Current verification

- `go test ./internal/application/repository/retriever/sqlite -count=1` - PASS
- `go test ./internal/application/repository/retriever/postgres -count=1` - PASS
- `go test ./internal/contentcache -count=1` - PASS
- `go test ./internal/application/service -run 'Test(NextStableChunkID|MultimodalPendingKey|StableGeneratedQuestionID|GraphExtractCache|WikiMapCache|UpsertStableChunks|CollectReparse|DeleteReparse|KnowledgePostProcessSkipsSupersededAttempt|EnqueueImageMultimodalTasks|ImageMultimodalFinalizeFallbacks)' -count=1` - PASS
- `go test ./internal/contentcache ./internal/models/embedding ./internal/application/repository/retriever/sqlite ./internal/application/repository/retriever/postgres ./internal/application/service -run '^$' -count=1` - PASS
- `git diff --check` - PASS
- `git diff --cached --check` - PASS

Only evidence files and the PR body were updated after the code validation commands above; implementation code did not change after those test runs.

## Not run

Per requested scope, known unrelated Feishu fixture, Notion/docparser localhost SSRF, and Windows sandbox/Python failure paths were not rerun.
