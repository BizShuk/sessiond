# sessiond — 術語表 (Terminology)

本檔是領域名詞、狀態值與縮寫的單一定義來源。CLI、JSONL 契約與文件使用同一組正名。

## 資料模型 (Data Model)

| 術語 (Term) | 英文 (English) | 定義 (Definition) | 出處 (Source) |
| --- | --- | --- | --- |
| 工作區 | Workspace | 一個 agent 會話所屬的專案目錄；在儲存路徑中以 `%2F` 編碼成單一目錄段 | `pkg/store` |
| 會話 | Session | 一次 agent 對話，對應磁碟上一個 JSONL 檔 | `model.Session` |
| 會話識別字 | Session ID | 會話的唯一鍵，同時是檔名 | `<session_id>.jsonl` |
| 中繼列 | Meta Line | JSONL 第一行，記錄 agent、session_id、workspace_path、title、resume 與 schema_version | `{"type":"meta",...}` |
| 回合列 | Turn Line | 其餘每一行，記錄 index、event、user、summary、source、status、at、token_count | `{"type":"turn",...}` |
| 回合 | Turn | 一次使用者輸入到 agent 回應結束的區段，被濃縮成一行摘要 | `model.Turn` |
| Schema 版本 | Schema Version | 儲存契約的相容性版本號；改欄位必須升版 | `meta.schema_version` |

## 攝取流程 (Ingestion)

| 術語 (Term) | 英文 (English) | 定義 (Definition) | 出處 (Source) |
| --- | --- | --- | --- |
| 生命週期掛鉤 | Lifecycle Hook | 由 Claude Code 或 Codex 在特定事件時呼叫本工具的機制，走 JSON-over-stdin | `pkg/hook` |
| 掛鉤載荷 | Hook Payload | 兩家 agent 各自的 stdin JSON 結構 | `pkg/hookpayload` |
| 逐字稿 | Transcript | agent 保存的完整對話紀錄，是摘要的輸入來源 | `pkg/ingest` |
| 攝取 | Ingest | 解析逐字稿、取出尚未處理的事件的過程 | `pkg/ingest` |
| 冪等同步 | Idempotent Sync | 重複觸發只 append 新 turn、不重複也不重寫 meta 的性質；由 turn count 比對達成 | `pkg/store` |
| 掛鉤佈線 | Hook Wiring | 把本工具註冊進 agent 設定檔的動作 | `pkg/install` |
| 暫停旗標 | Paused Flag | app-level 設定中的 `sessiond.hooks.paused`，控制所有 project 的攝取 | `pause` / `resume` |

### 事件來源 (Event Sources)

| Agent | 設定檔 | 事件 (Events) |
| --- | --- | --- |
| Claude | `<git-root>/.claude/settings.json` | `Stop` / `StopFailure` / `TaskCompleted` |
| Codex | `<git-root>/.codex/config.toml` | `Stop` / `SubagentStop` |

## 摘要 (Summarization)

| 術語 (Term) | 英文 (English) | 定義 (Definition) | 出處 (Source) |
| --- | --- | --- | --- |
| 摘要後端 | Summarizer | 產生一行摘要的實作，可為 heuristic 或 LLM | `pkg/summarize` |
| 啟發式後端 | Heuristic Backend | 零成本後備方案，直接取 user prompt | `summarize/heuristic.go` |
| LLM 後端 | LLM Backend | 經 `agentsdk` provider registry 呼叫模型產生摘要 | `summarize/llm.go` |
| 降級 | Fallback | LLM 失敗、逾時或空回覆時改用 heuristic 的行為；`rate limit 不算失敗` | `summarize/llm.go` |
| 事件佇列 | Event Queue | 保序的 FIFO 佇列，確保後續事件不越過被 rate limit 卡住的 head | `summarize/queue.go` |
| 速率限制等待 | Rate-limit Pause | 收到 HTTP 429 時保留 queue head、固定暫停 1 分鐘後重試 | `summarize/queue.go` |
| 重新推導 | Re-derivation | context 被取消而未寫入時，下次 hook 依逐字稿與既有 turn count 重算未完成事件 | `pkg/ingest` + `pkg/store` |

## 設定值 (Settings)

| 鍵 (Key) | 預設 (Default) | 用途 (Purpose) |
| --- | --- | --- |
| `sessiond.summarizer.provider` | `minimax` | agentsdk registry 中的 provider 名稱 |
| `sessiond.summarizer.model` | `MiniMax-M3` | 該 provider catalog 內的 model id |
| `sessiond.hooks.paused` | `false` | 全域暫停攝取 |
| `<PROVIDER>_API_KEY` | — | 對應 provider 的 API key，未設即降級 heuristic |

## 不變條件 (Invariants)

以下是不得違反的行為約定：

- `hook 永遠 exit 0`。非零結束碼（尤其 `exit 2`）會 block 宿主 agent 的 `Stop`。
- 摘要 LLM 只收到清洗後的 user prompt 與 final response，各截 3000 字。
- `token_count` 由上游 usage 計算，`不`送進摘要 LLM。
- `pause` 立即生效且不 flush 既有 session；paused 期間的事件被忽略，`resume` 不重播。
