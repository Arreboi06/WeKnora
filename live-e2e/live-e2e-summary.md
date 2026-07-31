# DingTalk live-tenant verification

Dates: 2026-07-30 to 2026-07-31 (Asia/Shanghai)

Implementation tested: `3858ff894f19ebfcb6b24f983ee67ce0b0f0e3c9`

Current PR head after reliability, post-review follow-ups, and merging the
latest Tencent main:
`18399351d4a8bb17a0f7e3ec12e8e5fad33fccd7`

## Evidence boundary

This record combines two live-tenant evidence layers:

1. a connector-level test against a real DingTalk tenant; and
2. a credential-redacted WeKnora UI recording against a real DingTalk test
   tenant, showing datasource setup, resource selection, sync strategy, and a
   successful datasource sync status.

All tenant identifiers, credentials, tokens, user identifiers, node identifiers,
and document contents are intentionally omitted or summarized.

This is **not** a claim that production RAG-answer E2E indexing was completed.
The UI recording stops at datasource sync success. Downstream parsing,
embedding, vector indexing, and final answer retrieval remain environment/model
dependent and are not represented as live-production evidence here.

## Live UI recording

- Recording: `live-e2e/pr1891-dingtalk-live-ui-public-final.mp4`
- Resource selection frame: `live-e2e/pr1891-resource-selection.png`
- Sync success frame: `live-e2e/pr1891-sync-success.png`

The recording is public-safe:

- Client Secret is not visible.
- Client ID and operator UnionID values are masked in the credential step.
- The recording does not include access tokens, workspace IDs, node IDs, or raw
  document content.
- The recording ends on the successful datasource sync card and avoids
  representing local parser/indexing environment issues as DingTalk connector
  behavior.

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

The live WeKnora UI run additionally showed:

1. selecting DingTalk as the datasource type;
2. configuring Client ID, Client Secret, and operator UnionID;
3. passing the datasource connection test;
4. loading the live DingTalk resource tree;
5. selecting the DingTalk test workspace, nested folder, and documents;
6. configuring incremental sync; and
7. reaching datasource sync success with 3 fetched documents, 3 created, and 0
   failed items.

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

## What these runs did not prove

- It did not cover every DingTalk block or media type.
- It did not prove behavior for every tenant, permission configuration, or
  extremely large workspace.
- It did not exercise multi-instance token-cache sharing; the connector cache is
  process-local.
- It did not prove production RAG-answer E2E indexing or retrieval.
- It did not modify a live document between incremental runs; the second run
  verifies the unchanged-document path only.

The later reliability and post-review changes in the current PR head are
covered by unit, integration-style `httptest`, race, type-check, and build
verification in the PR body. They were not rerun against the tenant and are not
retroactively represented as part of this live run.
