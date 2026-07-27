# PR 1891 DingTalk Datasource Evidence

This branch stores screenshots for Tencent/WeKnora#1891 only. It is not used as the PR head branch, so the implementation diff remains clean.

> [!IMPORTANT]
> Every UI screenshot in this branch was captured against a local demo/mock API.
> The images demonstrate the WeKnora product flow and are **not** evidence that a
> live DingTalk tenant API was exercised. A live-tenant end-to-end run has not
> yet been performed.

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

## Validation artifact boundary

The files under `validation/` are a historical local snapshot captured for the
earlier PR head. They are retained for traceability, but their timing and summary
image are not the current validation record.

The current implementation head and re-run command results are documented in
the PR body:

https://github.com/Tencent/WeKnora/pull/1891

Current PR head at the time of this clarification:
`5ac28da7dcded37d069c72241705fb0f5b08550a`.
