# PR #1904 live verification evidence

Generated on 2026-07-26 from a real local run of PR head
`9dd55f72366d09bda9026c824b590c2fcefced43`.

Environment:

- Official MySQL Community Server 8.4.10 Windows ZIP, run locally on D:.
- WeKnora server binary built from the PR branch.
- MySQL primary database and MySQL retriever/vector-store paths verified with
  real HTTP and database calls.

Files:

- `01-mysql-migration-primary-db.png` - rendered screenshot of migration
  up/down/up and primary DB read/write output.
- `02-mysql-retriever.png` - rendered screenshot of MySQL retriever output.
- `03-weknora-api.png` - rendered screenshot of WeKnora API output.
- `04-test-matrix.png` - rendered screenshot of the final local test matrix.
- `pr-1904-live-verification.gif` - short slideshow of the same real outputs.
- `*.txt` - raw command output backing the rendered images.

These are evidence renderings from command output, not mocked service responses.
