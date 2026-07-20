# sessiond Store JSONL

`sessiond` 把 Claude 與 Codex 兩種上游 transcript 收斂成的統一格式。這是 superset extension 唯一讀的 session 來源，也是唯一由本專案定義並寫入的格式。

契約定義在 `pkg/sessiond/pkg/model/model.go`，寫入邏輯在 `pkg/sessiond/pkg/store/store.go`。

## 位置與命名 (Location)

```tree
~/.config/superset/data/sessions/
├── %2FUsers%2Fshuk%2Fprojects%2Ftmp%2Fsuperset/     # workspace，"/" 換成 "%2F"
│   ├── f77be567-3c74-4f27-9592-bb7626b7ea45.jsonl   # Claude session
│   └── 019f7d97-0161-74a1-8862-453cfca381f3.jsonl   # Codex session
└── %2FUsers%2Fshuk%2Fprojects%2Fmap-generator/
```

- 根目錄由 `gosdk/config.GetAppDataDir()` 決定，app name 為 `superset`
- workspace 段：`EncodeWorkspace` 把 `/` 換成 `%2F`，空路徑編成 `_unknown`
- 檔名：session UUID，跨 agent 共用同一命名空間

`%2F` 編碼可逆（`DecodeWorkspace`），且列一個目錄就等於列該 workspace 全部 session，不需遞迴。這是刻意選擇：與 Claude 的 `-` 編碼（不可逆）和 Codex 的日期分層（無法依 workspace 查詢）都不同。

## 檔案結構 (File Structure)

一個 session 一個檔，第一行是 `meta`，其後每行一筆 `turn`。

```
line 1  : {"type":"meta", ...}     ← 寫一次，永不重寫
line 2  : {"type":"turn","index":1, ...}
line 3  : {"type":"turn","index":2, ...}
...
```

`meta` 只在檔案建立時寫入；後續 hook 觸發只 append 新 `turn`。

## `meta` Record

```json
{
  "type": "meta",
  "agent": "claude",
  "session_id": "f77be567-3c74-4f27-9592-bb7626b7ea45",
  "workspace_path": "/Users/shuk/projects/tmp/superset",
  "title": "check whether hook works",
  "resume": {
    "kind": "terminal",
    "command": "claude --resume f77be567-3c74-4f27-9592-bb7626b7ea45",
    "cwd": "/Users/shuk/projects/tmp/superset"
  },
  "created_at": "2026-07-20T11:03:44Z",
  "schema_version": 1
}
```

| 欄位 | 型別 | 來源 | 說明 |
| --- | --- | --- | --- |
| `type` | `"meta"` | 常數 | discriminator |
| `agent` | string | hook 參數 | `claude` / `codex` |
| `session_id` | string | hook payload | 上游 session UUID |
| `workspace_path` | string | `payload.cwd` 或 transcript 的 cwd | 絕對路徑，未編碼 |
| `title` | string | `Heuristic.Summarize(第一個 prompt)` | 取 prompt 第一行，截至 60 字 |
| `resume.kind` | `"terminal"` | 常數 | 目前只有終端機一種 |
| `resume.command` | string | `resumeSpec()` | `claude --resume <id>` / `codex resume <id>` |
| `resume.cwd` | string | workspace | 執行 resume 的目錄 |
| `created_at` | RFC3339 | `time.Now().UTC()` | hook 首次觸發時間，非 session 起始時間 |
| `schema_version` | int | 常數 `1` | schema 漂移偵測 |

`created_at` 是 hook 首次寫入的時間，不是 session 真正開始的時間，兩者可能差數小時。

`resume.command` 對 Codex 的 `codex resume <id>` 語法是版本相依的，程式碼註解已標記需驗證。

## `turn` Record

```json
{
  "type": "turn",
  "index": 6,
  "turn_id": "019f7d97-ac11-7f73-a9b0-9990498bf858",
  "event": "Stop",
  "user": "先建立腳本 再來review",
  "summary": "先建立腳本 再來review",
  "source": "heuristic",
  "status": "ok",
  "at": "2026-07-20T09:47:15.228Z"
}
```

| 欄位 | 型別 | 說明 |
| --- | --- | --- |
| `type` | `"turn"` | discriminator |
| `index` | int | 1-based 單調遞增，冪等判斷的依據 |
| `turn_id` | string | 只有最後一筆帶值（來自 hook payload），其餘為空 |
| `event` | string | `Stop` / `StopFailure` / `SubagentStop` / `TaskCompleted` |
| `user` | string | 清洗後的 prompt，截至 120 字 |
| `summary` | string | 一行摘要，heuristic 截 60 字、LLM 截 80 字 |
| `source` | string | `heuristic` / `llm` / `native` |
| `status` | string | `ok` / `error`（`StopFailure` 時最後一筆為 `error`） |
| `at` | ISO-8601 | 上游 transcript 的時間戳，缺值時 fallback 到當下 |
| `tools` | `ToolCall[]` | 選填，目前永遠為空，見下節 |

只有最後一筆 turn 會帶 `turn_id` 與 `status: error`，因為 hook payload 只描述觸發它的那一輪。

### `ToolCall`（已定義未使用）

```go
type ToolCall struct {
    Name       string `json:"name"`
    Input      string `json:"input,omitempty"`
    Result     string `json:"result,omitempty"`
    Status     string `json:"status,omitempty"`      // ok | error
    DurationMs int    `json:"duration_ms,omitempty"`
}
```

schema 已預留、extension 端契約為「每筆 tool 渲染成一個 H3 區塊」，但 `ingest` 端沒有任何程式碼填這個欄位，所以實際檔案永遠不含 `tools`。要做工具軌跡追蹤必須先補 ingest。

## 寫入語意 (Write Semantics)

`store.Sync(dataDir, meta, turns)` 的行為：

```
1. mkdir -p <dataDir>/sessions/<encoded-workspace>/
2. countTurns(fp) → (existing, isNew)
     掃描既有檔案，數含 `"type":"turn"` 的行數
     檔案不存在 → (0, true)
3. O_CREATE|O_APPEND|O_WRONLY 開檔
4. isNew → 寫入 meta（補上 Type 與 SchemaVersion）
5. 逐筆 turn：index <= existing 則跳過，否則 append
6. 回傳實際 append 的筆數
```

冪等保證：重複用同一組 turns 呼叫 `Sync`，第二次 append 0 筆。這讓 `Stop`、`SubagentStop`、retry 等重複觸發不會產生重複行。

`countTurns` 用字串比對 `"type":"turn"` 而非 JSON 解析，前提是 `encoding/json` 對 struct 的欄位順序固定（`Type` 是 `Turn` 的第一個欄位）。

摘要成本控制：`hook.Run` 先呼叫 `store.CountTurns` 拿到 `existing`，`buildTurns` 只對 `index > existing` 的部分呼叫 summarizer，所以每個 turn 只會被 LLM 摘要一次。

## 摘要來源 (Summarizer Sources)

| `source` | 產生方式 | 觸發條件 |
| --- | --- | --- |
| `heuristic` | 取 prompt 第一行，截 60 字，零成本 | `SUMMARIZER_PROVIDER=heuristic`，或 `auto` 且 `GOOGLE_API_KEY` 未設，或 LLM 失敗 fallback |
| `llm` | Gemini（gemma 模型）摘要，截 80 字 | provider 非 heuristic 且 provider 建構成功 |
| `native` | 保留值 | 目前無實作 |

LLM prompt 同時包含 user 與 assistant 文字（各截 3000 字），但只有摘要落地，assistant 原文不落地。

已知不一致：`hook.Run` 的 log 以型別判斷輸出 `summarizer=llm`，但 `Gemini.Summarize` 遇錯會 fallback 到 `Heuristic`，此時每筆 turn 的 `source` 是 `heuristic` 而 log 仍寫 `llm`。屬顯示問題，不影響資料。

## 已丟棄的資料 (Discarded Data)

相對上游 transcript，本格式是有損壓縮。以 `f77be567` 為例：transcript 154 行 → store 4 行。

| 上游有 | store 有 | 說明 |
| --- | --- | --- |
| assistant 全文 | 否 | 只有 LLM 摘要（且 heuristic 模式下連摘要都只是 prompt 第一行） |
| `thinking` 內容 | 否 | 完全不解析 |
| tool 呼叫與結果 | 否 | `tools[]` 欄位空著 |
| token 用量 | 否 | 無成本追蹤 |
| git branch / commit | 否 | 上游有，未採用 |
| hook 執行耗時 | 否 | `stop_hook_summary` 未採用 |
| Claude `ai-title` | 否 | 標題改用 heuristic |
| Codex `patch_apply_end.changes` | 否 | 現成檔案異動清單未採用 |

這是刻意取捨：上游原檔仍在磁碟上，需要細節時可回頭讀原檔；store 只負責讓 TreeView 能快速列出與定位。代價是 store 本身無法回答「這個 session 改了哪些檔」。

## 維護契約 (Invariants)

以下規則來自 `CLAUDE.md`，變更前需確認：

- `src/sessions/` 對本 store 只讀，唯一寫入路徑是 `sample-*.jsonl` 假資料指令
- 清除假資料只認 `sample-` 前綴，不得動到 ingest 產生的檔案
- Summary markdown 的 heading 契約固定為 `#` session / `##` round / `###` tool，由 `src/sessions/markdown.ts` 單點決定
- schema 擴充採 additive（新欄位加 `omitempty`），破壞性變更需 bump `SCHEMA_VERSION`

## 驗證 (Verification)

```bash
# 手動觸發一次 hook（Claude）
echo '{"session_id":"<uuid>","transcript_path":"<abs path>","cwd":"<workspace>","hook_event_name":"Stop"}' \
  | sessiond hook claude

# 預期 stderr: INFO session synced ... appended=N
# 預期 exit code: 0（hook 永遠不得阻塞 host agent）

# 檢查產出
DIR=~/.config/superset/data/sessions
ls "$DIR"
head -1 "$DIR"/<encoded>/<uuid>.jsonl | python3 -m json.tool   # 應為 type=meta
grep -c '"type":"turn"' "$DIR"/<encoded>/<uuid>.jsonl           # turn 筆數

# 冪等驗證：同一 payload 再跑一次
# 預期 appended=0，行數不變
```
