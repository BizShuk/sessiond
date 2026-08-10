# CLAUDE.md — sessiond 技術脈絡 (Technical Context)

獨立 Go module `github.com/bizshuk/sessiond`，Go `1.26.0`。
以 `gosdk` 取得 config/data 目錄，以 `agentsdk` provider registry 取得 LLM 摘要能力。

## 結構 (Structure)

```text
sessiond/
├── main.go              # composition root：config.Default + RootCmd
├── cmd/                 # cobra 命令 (hook / install / uninstall / pause / resume)
├── config/              # gosdk config 整合 + Settings struct
├── pkg/
│   ├── hookpayload/     # 兩家 agent 的 stdin JSON payload 型別
│   ├── hook/            # hook 進入點：讀 stdin → 分派 → 永遠 exit 0
│   ├── ingest/          # transcript 解析：claude.go / codex.go / text.go
│   ├── summarize/       # 摘要後端：heuristic / llm / FIFO queue / layout
│   ├── model/           # Session、Meta、Turn 等領域型別
│   ├── store/           # per-session JSONL 讀寫，冪等 append
│   └── install/         # hook wiring 的安裝與移除 (含備份)
├── docs/session/        # session 儲存契約文件
└── plans/
```

## 分層規則 (Layering)

```
cmd  →  hook  →  ingest    (transcript → events)
             →  summarize  (events → 一行摘要)
             →  store      (摘要 → JSONL)
        install            (只碰 agent 設定檔，不碰 store)
model                      ← leaf，被所有人 import，不 import 任何人
```

- `store` 不知道摘要怎麼來，`summarize` 不知道結果寫去哪。
- `ingest` 是唯一認識 Claude/Codex transcript 格式差異的地方；
  新增第三家 agent 只需在此加一個檔案 + `hookpayload` 一個型別。
- `install` 與 ingestion 路徑完全分離，兩者唯一的交集是設定檔中的 paused flag。

## 關鍵決策 (Key Decisions)

- `hook 永遠 exit 0`：ingestion 失敗只寫 stderr。非零 exit code（特別是 `exit 2`）
  會 block Claude 的 `Stop` 事件，讓使用者的 agent 卡住 — 摘要工具沒有資格
  中斷宿主 agent。唯一的例外是 HTTP 429：為了不丟事件，會同步等待 1 分鐘後重試。
- `摘要後端可換不改 code`：provider 與 model 都是設定值，走 `agentsdk` registry
  查找。有對應 API key 就用 LLM，沒有就降級零成本 heuristic（取 user prompt）。
  非 rate-limit 的失敗、逾時、空回覆一律降級而非報錯。
- `FIFO queue 保序`：新增 turn 依序摘要，429 時保留 queue head 不讓後續 event 越過它。
  等待期間 context 被取消則本批不寫入，下次 hook 依 transcript 與既有 turn count
  重新推導未完成事件 — 用`重新推導`而非`持久化佇列`來達成 crash 安全。
- `Sync 冪等`：重複 hook 觸發（Stop、SubagentStop、retry）只 append 新 turn，
  不重複、不重寫 meta。冪等性由 turn count 比對決定，不依賴外部狀態。
- `workspace 以 %2F 編碼成單一目錄段`：一次 `readdir` 就能列出某 workspace
  的所有 session，不需遞迴。編碼可逆，因此不需要額外的路徑索引。
- `pause/resume 寫在 app-level 設定`：`~/.config/superset/settings.local.json`
  的 `sessiond.hooks.paused` 控制所有 project，與 project 層的 hook wiring
  （`.claude/settings.json` / `.codex/config.toml`）刻意分離 —
  暫停不該需要動每一個 project 的設定檔。

## 輸出契約 (Storage Contract)

```tree
~/.config/superset/data/sessions/<%2F-encoded-workspace>/<session_id>.jsonl
```

第一行 `{"type":"meta",...}`，其餘每行 `{"type":"turn",...}`。
欄位定義與 schema 版本規則見 `docs/session/` 與 `docs/terminology.md`。
這份契約是對外承諾 — `superset` VSCode side panel 是消費者，
改欄位必須升 `schema_version`。

## 開發 (Dev)

```bash
go test ./...                      # ingest parser + store 冪等，純函式測試
go build -o ~/.local/bin/sessiond .
```

`gosdk` 未 tag 前走本地 checkout（`replace` 指向本機路徑），
發佈前務必確認 `go.mod` 的 replace 已移除。

## 已知陷阱 (Gotchas)

- `gosdk` 的 `config.Default(...)` 沒呼叫時，viper 取到的是空字串而非錯誤；
  新增子命令若讀設定拿到空值，先確認 composition root 有初始化。
- `install` 會動使用者的 agent 設定檔，預設是 dry-run；
  `--apply` 才實際寫入，且先備份。`uninstall` 只移除 sessiond 自己註冊的 hook，
  不得整段覆寫使用者的 hooks 區塊。
- 摘要 LLM 每次只收到清洗後的 user prompt 與 final response（各截 3000 字）；
  `token_count` 由上游 usage 計算，`不`送進摘要 LLM。
