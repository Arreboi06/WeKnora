# PR 1891 DingTalk Datasource Evidence

This branch stores screenshots for Tencent/WeKnora#1891 only. It is not used as the PR head branch, so the implementation diff remains clean.

Validated product path:

Knowledge Base Settings -> Data Sources -> Data Source Management -> Add data source

Screenshots:

1. `dingtalk/01-kb-list-dingtalk-demo.png` - demo knowledge base used for validation.
2. `dingtalk/02-kb-settings-datasource-list.png` - data source management with an existing DingTalk data source.
3. `dingtalk/03-add-datasource-type-selection.png` - add data source drawer with DingTalk Docs.
4. `dingtalk/04-dingtalk-credentials-tested.png` - DingTalk credential form and successful connection test.
5. `dingtalk/05-dingtalk-resource-picker-root.png` - DingTalk resource picker at workspace level.
6. `dingtalk/06-dingtalk-resource-picker-expanded.png` - expanded DingTalk workspace showing folders/documents.
7. `dingtalk/07-dingtalk-sync-history-open.png` - sync history drawer with successful DingTalk sync metrics and expanded details.

Local targeted test:

```bash
go test ./internal/datasource/connector/dingtalk -count=1
```

Result:

```text
ok github.com/Tencent/WeKnora/internal/datasource/connector/dingtalk 6.161s
```
