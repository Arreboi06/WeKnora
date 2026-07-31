# DingTalk live-tenant connector verification

Date: 2026-07-30 (Asia/Shanghai)

Implementation tested: `3858ff894f19ebfcb6b24f983ee67ce0b0f0e3c9`

Current PR head after reliability and post-review follow-ups:
`f3dd216a1e2765e2b7ad9cde528e524f98499dd9`

## Evidence boundary

This record describes a connector-level test against a real DingTalk tenant.
The test used an ephemeral Go harness located in the implementation worktree;
the harness and all credentials were removed after the run and were never
committed.

This is **not** a claim that the complete WeKnora browser-to-ingestion flow was
recorded against the live tenant. The screenshots elsewhere in this branch
remain local mock evidence.

All tenant identifiers, credentials, tokens, user identifiers, node identifiers,
and document contents are intentionally omitted or summarized.

## Observed API flow

The connector successfully:

1. obtained an internal-app access token (`expireIn=7200`);
2. listed the test knowledge-base workspace;
3. recursively listed the workspace and a nested folder;
4. classified native DingTalk documents separately from folders and uploaded
   or unsupported files;
5. queried document blocks for all native documents;
6. rendered the returned blocks to Markdown;
7. resolved workspace, folder, and single-document resource selections;
8. restored ancestor resources for a selected descendant document; and
9. performed an incremental re-run without returning unchanged documents.

## Redacted observations

- Root resources: 7
- Node shape: 6 files and 1 folder
- Native DingTalk documents fetched: 3
- Other observed node kinds: 1 image, 1 uploaded PDF/document, 2 other nodes
- Observed native-document block types included:
  - paragraph
  - heading
  - unordered list
  - ordered list
  - table
  - block quote
- Observed text included Chinese, English, and emoji.
- Workspace selection, folder selection, and individual-document selection each
  resolved to the expected native documents.
- First incremental fetch returned the 3 native documents; the immediate second
  fetch returned 0 unchanged documents.
- Calling the blocks endpoint with the uploaded PDF's node key returned HTTP
  400 ("document key illegal"). The connector's native-document predicate
  filtered that node during normal traversal, so it was not fetched as a DingTalk
  document.
- The tested tenant returned document content when the native node ID was used
  as the block-query document key fallback. This is an observed compatibility
  behavior, not an assertion that DingTalk's public documentation guarantees
  node ID and document key are universally interchangeable.

## What this run did not prove

- It did not cover every DingTalk block or media type.
- It did not prove behavior for every tenant, permission configuration, or
  extremely large workspace.
- It did not exercise multi-instance token-cache sharing; the connector cache is
  process-local.
- It did not record a complete live WeKnora UI flow through downstream storage
  and indexing.
- It did not modify a live document between incremental runs; the second run
  verifies the unchanged-document path only.

The later reliability and post-review changes in the current PR head are
covered by unit, integration-style `httptest`, race, type-check, and build
verification in the PR body. They were not rerun against the tenant and are not
retroactively represented as part of this live run.
