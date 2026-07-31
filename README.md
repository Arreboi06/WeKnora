# PR 1891 DingTalk Datasource Evidence

This branch stores evidence assets for Tencent/WeKnora#1891 only. It is not
used as the PR head branch, so the implementation diff remains clean.

> [!IMPORTANT]
> The historical screenshots under `dingtalk/` were captured against a local
> demo/mock API and remain UI-flow evidence only.
>
> Live DingTalk tenant evidence is stored under `live-e2e/`, including a
> credential-redacted WeKnora UI recording and the redacted connector/API
> verification notes in
> [`live-e2e/live-e2e-summary.md`](live-e2e/live-e2e-summary.md).

Demonstrated product path:

`Knowledge Base Settings -> Data Sources -> Data Source Management -> Add data source`

Screenshots:

1. `dingtalk/01-kb-list-dingtalk-demo.png` - local demo knowledge base.
2. `dingtalk/02-kb-settings-datasource-list.png` - local mock data-source management view.
3. `dingtalk/03-add-datasource-type-selection.png` - local mock add-data-source drawer.
4. `dingtalk/04-dingtalk-credentials-tested.png` - local mock credential-flow success state.
5. `dingtalk/05-dingtalk-resource-picker-root.png` - local mock resource picker at workspace level.
6. `dingtalk/06-dingtalk-resource-picker-expanded.png` - local mock workspace/folder/document tree.
7. `dingtalk/07-dingtalk-sync-history-open.png` - local mock sync-history state and metrics.

Live-tenant UI evidence:

1. `live-e2e/pr1891-dingtalk-live-ui-public-final.mp4` - redacted WeKnora UI
   recording showing DingTalk datasource setup, credential validation, live
   resource selection, sync strategy, and successful datasource sync status.
2. `live-e2e/pr1891-resource-selection.png` - redacted frame showing the
   DingTalk workspace/folder/document tree selection.
3. `live-e2e/pr1891-sync-success.png` - redacted frame showing the datasource
   sync card after a successful live sync.

## Validation artifact boundary

The files under `validation/` are a historical local snapshot captured for the
earlier PR head. They are retained for traceability, but their timing and summary
image are not the current validation record.

The current implementation head and re-run command results are documented in
the PR body:

https://github.com/Tencent/WeKnora/pull/1891

The live API/UI observations are redacted. No Client Secret, access token,
operator UnionID, workspace ID, node ID, or document content that could identify
the tenant is stored in this branch. The public recording masks credential
values and stops at the datasource sync result; it does not claim production
RAG-answer E2E indexing.
