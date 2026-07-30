# PR #1904 live verification evidence

Generated from a fresh D: runtime on 2026-07-30 for WeKnora PR #1904.

- Implementation HEAD: 231d02cc61a82668f654c6187ea6dea278e5537e
- Upstream main used for verification: cc846d8e2947300d38f681edff4b2c1a684b17f1
- MySQL runtime: official MySQL 8.4.10 Windows package, initialized under D:\agent\runtime\...
- Server runtime: WeKnora cmd/server built from the implementation HEAD and started with DB_DRIVER=mysql and RETRIEVE_DRIVER=mysql

## Assets

- pr-1904-live-verification.gif - short visual summary of the live checks
- 01-mysql-migration-primary-db.png - migration up/down/up, app DB migration, primary DB write/read, JSON prefix repository semantics
- 02-mysql-retriever.png - MySQL retriever JSON embedding, cosine, FULLTEXT, TopK/exclude/delete chain
- 03-weknora-api.png - register/login/vector-store types/env store health through the real WeKnora API
- 04-test-matrix.png - final Go test matrix and diff check

The .txt files beside the images contain the underlying command output used to render these screenshots.
