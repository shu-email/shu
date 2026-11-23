---
name: shu-issues
description: >-
  逐条处理 shu-email/shu 的 GitHub issue。读取和分诊，定位原生桌面 UI、邮件引擎、SQLite 或 AI 层的改动，运行 Go 测试和 MyGo 桌面验收，完成一条就提交并推送，再按要求开 PR、附截图、评论或关闭 issue。用户要求查看、修复或批量处理 Shu issue 时使用。
---

# 处理 Shu 的 GitHub issue

在包含 `go.mod`、`mygo.json` 和 `AGENTS.md` 的 `shu` 仓库根目录执行命令。若从 `Shu-email` 工作区开始，进入 `shu/`。GitHub 仓库是 `shu-email/shu`，默认分支是 `main`。UI、邮件引擎和存储在同一仓库，一个 issue 可以跨层交付。

## 选取与排期

- `$shu-issues #编号` 处理指定 issue，支持多个编号或该仓库的 issue 链接。`$shu-issues`、`全部` 或 `all` 处理当前打开的 issue，按编号升序，用户指定顺序或筛选条件时照做。用户只要求查看、汇总或分诊时，读完后给出结果。
- 先读仓库根目录的 `AGENTS.md`，核对 `git status --short --branch`、`git remote -v` 和目标仓库。若现有分支或用户改动会干扰修复，从已核对的基线开独立 worktree，保留用户的工作区。
- 批量处理时先发编号、标题和总数。每个 issue 占一项，在读取、实现、测试、桌面验收和交付时更新当前阶段，例如 `**进度 2/5** · #42 搜索结果未更新 · 测试`。遇到阻塞时记下原因，继续处理独立的 issue。待处理清单以本次读取时的范围为准。

```sh
gh api --paginate 'repos/shu-email/shu/issues?state=open&per_page=100' \
  --jq '.[] | select(.pull_request == null) | {number, title, html_url}'
gh issue view 42 --repo shu-email/shu \
  --json number,title,body,comments,labels,state,url,closedByPullRequestsReferences
```

## 逐条实现与验收

1. 读正文、评论和相关附件，提炼复现步骤和验收条件。结合时间线及关联 PR 核对是否已有修复，记录已关闭、已有交付或无法复现的情况。遇到取舍时，遵循 `docs/glossary.md` 和相关的 `docs/decisions/`，记录影响用户的决定。
2. 从 `cmd/shu/` 的入口走到相关层。原生界面在 `app/`，通过 `mail.Service` 调用 `mail/`，SQLite 实现在 `store/`，模型处理在 `ai/`。修改 MyGo UI 先读 [mygo-maintenance](../mygo-maintenance/SKILL.md)，涉及原生模块再读 [mygo-native-modules](../mygo-native-modules/SKILL.md)。可见文案同步 `locales/en.json`、`locales/zh-Hans.json` 和生成的访问器。改 SQLite schema 时按 `docs/schema-migrations.md` 追加迁移，覆盖已有数据库升级。
3. 先跑相关包的复现或回归测试。UI 行为用 `ui.NewTester`，邮件协议用 `internal/fakemail` 的本地 IMAP/SMTP。格式化改过的 Go 文件，并运行下列 CI 检查。涉及 SQLite 时确认相关用例实际执行。需要验证编译、打包或语言目录生成时，使用 `go tool mygo build -skip-dmg -skip-installer`。

   ```sh
   go vet ./...
   go tool mygo vet .
   CGO_ENABLED=0 SHU_REQUIRE_SQLITE=1 go test ./...
   ```

4. 涉及桌面流程时，按 [shu-desktop-check](../shu-desktop-check/SKILL.md) 用 `shu Dev` 的 seeded 邮箱验收。启动前检查是否已有 `shu Dev` 和 automation 会话，避免占用其数据目录或 socket；结束时只关闭本次启动的进程。布局改动检查最小窗口，记录实际覆盖的系统、语言和外观。需要截图时，用临时目录保存能说明结果的画面，例如 `issue-42-search-results.png`。平台、SQLite 模块或 GUI 环境缺失时，记录未覆盖项和当前验证结果。

5. **验收通过后必须提交并执行 `git push`。** 按 `AGENTS.md` 单独提交该 issue 的改动，核对待推内容后推送到 `origin` 的主题分支。以下以 `fix/issue-42` 为例。

   ```sh
   git push -u origin HEAD:refs/heads/fix/issue-42
   git ls-remote --heads origin refs/heads/fix/issue-42
   ```

   确认远端分支包含本次提交后，汇报「已推送」和短 SHA，再处理下一条。这一步也适用于无需创建 PR 的修复。用户指定分支或要求仅本地处理时，按其要求执行。推送失败时先检查远端状态，解决可恢复的问题后重试。仍有阻塞时保留本地提交并报告原因。

## GitHub 交付与收尾

- 需要 PR 时，从已推送的分支向 `main` 开 PR，或使用用户指定的目标分支。完整修复且预期合并时关闭 issue，用 `Fixes #编号`。部分修复或应保留 open 状态时，用 `Refs #编号`。
- 用户授权发 PR 或评论时，说明用户可观察的结果、验证过的场景和未覆盖项。多行正文用 `--body-file`。需要上传验收截图时，按 [gh-cli-attachments](../gh-cli-attachments/SKILL.md) 检查画面并用 `gh --attach` 附到对应 PR 或评论。使用 seeded 数据，截图留在临时目录。上传报错时先检查 GitHub 上是否已经创建 PR 或评论，再补传缺失的附件。
- 用户要求评论并关闭时，先确认修复已合入或验收条件已满足，再发评论、核对链接和附件并更新 issue 状态。若关联 PR 合并时已自动关闭，核实状态即可。待评审、未完成或验证受阻的 issue 保持 open，标明下一步和已有证据。
- 汇报每个 issue 的结果、相关提交或 PR 链接、实际通过的检查、截图位置及未覆盖的环境。以 GitHub 实际状态区分「已开 PR」「已合并」和「已关闭」。
