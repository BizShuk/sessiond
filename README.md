# sessiond

跨 agent session 摘要 ingestor。由 `Claude Code` 與 `Codex` 的 lifecycle hook 觸發,
把每個 turn 濃縮成一行摘要,append 到 per-session JSONL,供 `superset` VSCode side panel 讀取。

獨立 Go module(`github.com/bizshuk/sessiond`),import 框架層 `gosdk`(config/data dir)。
`gemma` 摘要(`agentSDK provider/google`)為後續階段;目前用零成本 heuristic(取 user prompt)。

## 用法 (Usage)

```bash
sessiond hook <claude|codex>  # 由 lifecycle hook 呼叫,讀 stdin JSON,exit 0
sessiond install             # dry-run 預覽要註冊的 hook 設定
sessiond install --apply     # 實際寫入(自動備份 + symlink 警告)
sessiond uninstall           # dry-run 預覽要移除的 sessiond hooks
sessiond uninstall --apply   # 備份後只移除 sessiond-owned hooks
sessiond pause               # 立即暫停所有 hook ingestion
sessiond resume              # 恢復 hook ingestion
sessiond --version
```

子命令採 cobra 風格(spf13/cobra)。`hook` 永遠 `exit 0`,任何錯誤只 log stderr,絕不阻擋 agent。

hook 是 best-effort:任何錯誤只寫 stderr 並 `exit 0`,絕不阻擋或拖慢 agent(`exit 2` 會 block Claude 的 Stop)。

`pause` / `resume` 透過 gosdk `config --update` 寫入 app-level `~/.config/superset/settings.local.json` 的 `sessiond.hooks.paused`，控制所有 project 的 ingestion；這與 project `.claude/settings.json` / `.codex/config.toml` 的 hook wiring 分離。`pause` 立即生效且不 flush 既有 session；paused 期間的 hook 會被忽略，`resume` 不會重播。Claude paused hook 保持空 stdout，Codex 仍回覆 `{"continue": true}`，因此不會阻擋 agent。

## 輸出契約 (Storage contract)

```tree
~/.config/superset/data/sessions/<%2F-encoded-workspace>/<session_id>.jsonl
```

- 第一行 `{"type":"meta",...}`:agent / session_id / workspace_path / title / resume / schema_version。
- 其餘每行 `{"type":"turn",...}`:index / event / user / summary / source / status / at。
- `Sync` 冪等:重複 hook 觸發(Stop、SubagentStop、retry)只 append 新 turn,不重複、不重寫 meta。
- workspace 以 `%2F` 可逆編碼為`單一目錄段`(Grok 風格),一次 readdir 即列出該 workspace 所有 session。

## 觸發機制 (Hook wiring)

兩家都是 `JSON-over-stdin`。`install` 註冊:

| Agent | 設定檔 | events |
| --- | --- | --- |
| Claude | `<git-root>/.claude/settings.json` (`hooks`) | `Stop` / `StopFailure` / `TaskCompleted` |
| Codex | `<git-root>/.codex/config.toml` (`[[hooks.*]]`) | `Stop` / `SubagentStop` |

`install` / `uninstall` 從目前 working directory 解析 Git root；在 repository 子目錄執行仍寫入 root，worktree 使用該 worktree root。非 Git 目錄則以目前目錄為 project root。Claude 固定使用可提交共享的 `.claude/settings.json`，不另建 local scope；設定 precedence 為 managed → CLI → local project → shared project → user。Codex project hooks 僅在 trusted project 生效，且支援狀態依 installed Codex version 而異。

舊版寫入 `~/.claude/settings.json` / `~/.codex/config.toml` 的 user-level hooks 不會自動遷移或刪除；確認 project hooks 正常後，再從舊 backup 復原或手動精確移除 legacy sessiond entries。

`uninstall` 預設只預覽；加 `--apply` 後才修改設定。它只移除 sessiond 自己的 command entries / marker block，保留其他 hooks、設定、TOML 註解與 symlink。每次實際修改前會在原檔旁建立 `.bak.<timestamp>`；需要手動復原時，把最新 backup 複製回原路徑即可。設定無 sessiond hook 時為 no-op；JSON、marker 或 hook 結構 malformed / ambiguous 時會 fail closed，不寫入任何內容。

hook payload 提供 `session_id` / `transcript_path` / `cwd` / `hook_event_name`;實際 turn 內容一律
`從 transcript 檔重讀`(source of truth),不依賴 payload 文字。

- Claude turn 來源:transcript JSONL 的 `type:user/assistant`(濾 sidechain / tool_result / caveat)。
- Codex turn 來源:rollout JSONL 的 `event_msg` `user_message`/`agent_message`(濾 AGENTS.md / plugins / permissions 注入)。

## 開發 (Dev)

```bash
go test ./...     # ingest parser + store 冪等 純函式測試
go build -o ~/.local/bin/sessiond .
```

`replace github.com/bizshuk/gosdk => /Users/shuk/projects/tmp/gosdk`(gosdk 未 tag 前走本地 checkout)。

## 摘要後端 (Summarizer)

無 `GOOGLE_API_KEY` 時用 heuristic(取 user prompt);有 key 時自動切 `gemma`(`agentSDK provider/google`),
失敗/逾時/空回覆自動降級 heuristic。只摘要`新增`的 turn(每次 hook 約 1 次 LLM 呼叫)。

| env | 預設 | 用途 |
| --- | --- | --- |
| `GOOGLE_API_KEY` | — | 設了才啟用 gemma |
| `SUPERSET_SUMMARIZER_MODEL` | `gemma-3-27b-it`(暫定,需 verify) | gemma model id |
| `SUPERSET_SUMMARIZER` | — | 設 `heuristic` 可強制關閉 gemma |

## 狀態 (Status)

- ✅ Claude hook + Codex hook ingest → JSONL(冪等,只摘要新增 turn)
- ✅ `gemma` 摘要後端(`internal/summarize/gemini.go`);加 `GOOGLE_API_KEY` 即生效,否則 heuristic
- ⏸ VSCode `superset/src/sessions/`:讀本契約 + TreeView + resume-in-terminal

設計全文見 [`../plans/2026-07-19-multi-agent-session-summary.md`](../plans/2026-07-19-multi-agent-session-summary.md)。
