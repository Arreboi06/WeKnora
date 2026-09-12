# Rhino Bird 2026 Topic 2 Evidence Index

Evidence classification: **local final-integration evidence, not full official acceptance**

## Published Release

- Public result: https://github.com/Arreboi06/WeKnora/tree/rhino-2026-final-T2
- Repository: https://github.com/Arreboi06/WeKnora
- Branch: `codex/rhino-2026-topic2-midterm-56d3086`
- Immutable code Tag: `rhino-2026-final-T2`
- Tag target / code commit: `dfa561a526975e4294ef5528db8e14af52fc39c8`
- Evidence UTC: `2026-09-12T13:57:38Z` to `2026-09-12T14:06:33Z`
- Local evidence root: `D:\agent\memory3\weknora-rhino-2026\midterm\topic2-56d3086\evidence\FINAL-INTEGRATION-20260912T134404Z`

The raw browser DOM, screenshot, logs, and reports remain in the local evidence root. They are not copied into the public repository because they contain machine-local paths, session identifiers, and runtime artifact details. The fingerprints below identify the exact retained files without exposing those raw session details publicly.

## Results

| Check | Result | Assertion |
| --- | --- | --- |
| Go scoped tests | PASS | 7 packages, exit 0 |
| Frontend tests | PASS | 18 tests, 18 pass, 0 fail, 0 skipped, 0 todo |
| Frontend type-check | PASS | `vue-tsc --build`, exit 0 |
| Frontend production build | PASS | `vite build`, exit 0 |
| Real Docker/PostgreSQL/browser runner | PASS | 5 cases pass, 0 fail, 0 skip |
| Negative security tests | PASS | 16 targeted rejection assertions, exit 0 |
| Browser network isolation probe | PASS | 0 probe requests |
| Restart/schema-retaining probe | PASS | Artifact count retained across restart |
| Exact-scope cleanup | PASS | 0 leftovers |
| Evidence password scan | PASS | No generated database password hits |
| First runner attempt | ERROR, retained | Windows PowerShell 5.1 lacked the runner's `RandomNumberGenerator.GetBytes` API; no resource mutation occurred |
| Historical R2 80-case gate | RED | 39 pass, 37 fail, 4 error, 3 cleanup_failure |
| D3/D4 independent remote acceptance | NOT_RUN | No authorized E2B mutation or eligible remote backend |

The local passing checks prove the bounded default-off Docker-backed Workbench integration. They do not prove a complete duplex PTY/WSS implementation, an independent remote backend, or full official Topic 2 acceptance.

## Fingerprints

All fingerprints are SHA-256 of retained local evidence files. The E2B API key was not loaded, copied, logged, or hashed.

```text
go-tests.log                                      a6c058433fd11ab295f4ec442c01ebc23b1d90b0edd4eb095ea942c31c2685cf
frontend-tests.log                               7c9a60465d5caeb00b62599acdcd395d6c03fa7a6b5c44c0fadba7027080704e
frontend-typecheck.log                           61b2316eb53e21abd9ebc48ce5bc8e843492ad5f129ba8d561812f22f416565c
frontend-build.log                               96c6e6febae44dfee584fd25d718df7ec30b20472adc46a13131d89fac1c8288
negative-security-tests-verbose.log              2611537e5b48f368632cdd156a57244c38e169f2808660921ac29a7da62cff58
real-run-20260912T135738Z.report.json             419ec5aa1eba27c78e59b8ec1874f06a859c9f9931666dbb902372ddd8d4e6a7
browser-report-20260912T135738Z.json              abce7f3349b7808da5c763f87fc2a93c08fb608f04af2a601d4f21f6cc7a8df8
browser-screenshot-20260912T135738Z.png           77cb25acf5697043356121358c5582038bf4704a24ce3f06ea55e3b0a7197fdd
browser-dom-20260912T135738Z.html                 caee74e4d4c8601d7c1a07632f01799f6bb9698dc4c779e42f352c92550e384b
rollback-output-20260912T135738Z.txt              8defa01e66f0d0039dee3874de84d36ec9eb88759afe625ed140e45d55cc0a68
cleanup-receipt-20260912T135738Z.txt              b1fba36e3a2fa98a7b54be53aafc4cce46ae76785af6c04403e51028399689fa
secret-scan-20260912T135738Z.txt                  bec2da0830a7dbf63d60373a8d252dd3fad27facb7f5fe47da69c4e7a33ec383
integration-runner-first-attempt.log              8d8e6474ef5197212d24d74b3e2df246087c4f5aece890ef6f38ec432a2b89d1
```

## Known Boundary

R2 remains RED and D3/D4 remain NOT_RUN. The evidence index is intended to make the real local result auditable; it is not a replacement for the failed acceptance gates.
