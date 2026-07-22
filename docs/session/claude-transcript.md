# Claude Code Transcript JSONL

Claude Code CLI 為每個 session 寫一個 append-only JSONL，記錄完整對話、工具呼叫、hook 執行與環境快照。`sessiond` 只讀不寫。

## 位置與命名 (Location)

```tree
~/.claude/projects/
└── -Users-shuk-projects-tmp-superset/    # cwd 路徑，"/" 換成 "-"
    ├── f77be567-3c74-4f27-9592-bb7626b7ea45.jsonl    # session transcript
    └── f77be567-3c74-4f27-9592-bb7626b7ea45/         # 附屬目錄（部分 session 才有）
```

- 目錄段：workspace 絕對路徑的 `/` 全換成 `-`，開頭也保留一個 `-`
- 檔名：session UUID，等同 `claude --resume <uuid>` 的參數

注意這個編碼與 `sessiond` store 的 `%2F` 編碼不同，不可互推；`-` 編碼不可逆（原路徑含 `-` 時會混淆）。

## 共用 Envelope (Common Envelope)

多數 record 共用這組欄位：

| 欄位 | 型別 | 說明 |
| --- | --- | --- |
| `type` | string | discriminator，見下節 |
| `uuid` | string | 本筆 record 的 ID |
| `parentUuid` | string \| null | 指向上一筆，形成鏈狀結構 |
| `sessionId` | string | 檔案所屬的 session UUID，`可靠的 owner 欄位` |
| `session_id` | string | 產生該筆記錄的 session UUID，`fork 後會與 sessionId 分岔` |
| `timestamp` | ISO-8601 | 寫入時間 |
| `isSidechain` | bool | `true` 表示 subagent，主流程需跳過 |
| `cwd` | string | 工作目錄絕對路徑 |
| `gitBranch` | string | 當下 git branch |
| `version` | string | Claude Code 版本，如 `2.1.215` |
| `userType` | string | `external` / `internal` |
| `entrypoint` | string | `cli` / `ide` |

### `sessionId` 與 `session_id` 不保證相同

兩者`並非`同義的歷史遺留欄位。實測 `f77be567` session（387 筆）：

| 情況 | 筆數 |
| --- | --- |
| 兩者相同 | 118 |
| 兩者不同 | 101 |
| 只有其中一個（輕量 record） | 160 |

101 筆不同的記錄全部出現在執行 `/fork` 之後，分佈為 `assistant 44` / `user 27` / `attachment 29` / `system 3`。fork 產生的新 session id 會被蓋印到`母 session 的 transcript 記錄`上：

```json
{
  "session_id": "2b402689-3231-49e9-8a3b-f2d4639ef9a2",
  "sessionId":  "f77be567-3c74-4f27-9592-bb7626b7ea45"
}
```

解析規則：

- 判斷記錄歸屬於哪個檔案時一律用 `sessionId`
- `session_id` 表示實際產生該筆記錄的 session，fork 場景下與檔名不符
- 真實 user prompt 的 `session_id` 為 `absent`，因此 turn 邊界不受污染

`不要`用 `session_id != sessionId` 當過濾條件。實測分岔出的 `2b402689` `沒有自己的 transcript 檔`（已搜尋 `~/.claude/projects/` 確認），而那 101 筆記錄承載 `12,653` 字元的 assistant text。過濾掉等於純資料遺失，沒有其他來源補得回來。

正確做法是照收，並理解 `session_id` 只是「產生者標記」而非歸屬判準。

## Record 類型 (Record Types)

以 `f77be567` session（162 筆）為樣本：

| `type` | 筆數 | 說明 |
| --- | --- | --- |
| `assistant` | 46 | 模型回應 |
| `user` | 35 | 人類 prompt 或 tool_result echo |
| `attachment` | 39 | hook 輸出、IDE 事件等附件 |
| `last-prompt` | 8 | session 起始游標 |
| `mode` | 7 | `normal` / `plan` |
| `permission-mode` | 7 | `bypassPermissions` 等 |
| `system` | 7 | hook summary 等系統事件 |
| `ai-title` | 6 | Claude 自動生成的 session 標題 |
| `file-history-snapshot` | 5 | Edit/Write 前的檔案快照 |
| `queue-operation` | 2 | 佇列 enqueue/dequeue |

### `user` — 型 A：真實 prompt

`sessiond` 認定為一個 turn 的起點。

```json
{
  "type": "user",
  "promptId": "f8f02a32-27e7-4974-9989-3c1d78791b9b",
  "origin": { "kind": "human" },
  "promptSource": "typed",
  "permissionMode": "bypassPermissions",
  "message": {
    "role": "user",
    "content": "check whether hook works"
  },
  "uuid": "1f203681-98f8-4b64-bf82-a67f9ca0e181",
  "timestamp": "2026-07-20T10:59:47.678Z"
}
```

| 欄位 | 說明 |
| --- | --- |
| `message.content` | 字串（真 prompt）或陣列（含 `text` block） |
| `origin.kind` | `human` 表示人類輸入 |
| `promptSource` | `typed` / `command` / 其他來源 |
| `promptId` | 同一輪 prompt 的關聯 ID，tool_result 也會沿用 |

### `user` — 型 B：tool_result echo

`sessiond` 完全跳過。識別信號是 `toolUseResult` 存在且 `content` 只有 `tool_result` block。

```json
{
  "type": "user",
  "message": {
    "role": "user",
    "content": [
      {
        "type": "tool_result",
        "tool_use_id": "call_019f7f2ebd9b7a82b64ac743",
        "content": "gitHooks.ts\ngitHooks.test.ts\ngitHooksCommand.test.ts",
        "is_error": false
      }
    ]
  },
  "toolUseResult": {
    "stdout": "gitHooks.ts\ngitHooks.test.ts\ngitHooksCommand.test.ts",
    "stderr": "",
    "interrupted": false,
    "isImage": false,
    "noOutputExpected": false
  },
  "sourceToolAssistantUUID": "8e76c18a-22bf-4011-b5e3-b22f9045d09a"
}
```

| 欄位 | 說明 |
| --- | --- |
| `toolUseResult.stdout` / `stderr` | 結構化執行輸出 |
| `toolUseResult.interrupted` | 是否被使用者中斷 |
| `sourceToolAssistantUUID` | 反查發出這個工具呼叫的 assistant record |
| `tool_use_id` | 對應 assistant `tool_use.id` |

### `assistant`

`message.content` 是混合 block 陣列。本樣本 46 筆中：`tool_use` 29、`thinking` 17、`text` 2。

```json
{
  "type": "assistant",
  "message": {
    "id": "06ad32a76754b464d3f2f34cae05000f",
    "type": "message",
    "role": "assistant",
    "model": "claude-opus-4-8",
    "stop_reason": "tool_use",
    "stop_sequence": null,
    "content": [
      { "type": "thinking", "thinking": "..." },
      { "type": "text", "text": "..." },
      {
        "type": "tool_use",
        "id": "call_019f7f2ebd9b7a82b64ac735",
        "name": "Bash",
        "input": {
          "command": "ls -la .codex/",
          "description": "Check codex config directory"
        }
      }
    ],
    "usage": {
      "input_tokens": 62083,
      "output_tokens": 496,
      "cache_creation_input_tokens": 0,
      "cache_read_input_tokens": 128,
      "service_tier": "standard"
    }
  },
  "effort": "xhigh"
}
```

| block type | 內容 | sessiond 是否採用 |
| --- | --- | --- |
| `thinking` | 模型 thinking 全文 | 否 |
| `text` | 面向使用者的回應 | 是，串接成 `AssistantText` |
| `tool_use` | `name` + `input` 物件 | 否 |

### `stop_reason` 是最終回覆的鑑別欄位

`message.stop_reason` 可為 `end_turn` / `stop_sequence` / `tool_use` / `max_tokens` / `error`。要區分「該輪最終回覆」與「工具之間的過程說明」只能靠這個欄位 —— 兩者在 `content[]` 裡都是一模一樣的 `text` block。

跨 120 個 transcript 實測「含非空 `text` block 的 assistant record」分佈：

| `stop_reason` | 筆數 | 語意 |
| --- | --- | --- |
| `tool_use` | 2,067 | 中間訊息，講完話接著叫工具 |
| `end_turn` | 295 | 該輪最終回覆 |
| `stop_sequence` | 55 | 最終回覆（觸及 stop sequence） |
| `error` | 1 | 異常 |
| `null` | 1 | 異常 |

中間訊息是最終訊息的約 `7 倍`。

取最終回覆的完整 filter：

```python
r["type"] == "assistant"
and r.get("isSidechain") is not True
and r["message"]["role"] == "assistant"
and r["message"]["stop_reason"] in ("end_turn", "stop_sequence")
and any(b["type"] == "text" for b in r["message"]["content"])
→ "".join(b["text"] for b in content if b["type"] == "text")
```

`end_turn` 的記錄不保證帶 `text`：本檔 12 筆 `end_turn` 中有 5 筆只含 `thinking` block、`text` 為空，因此 `any(text)` 這個條件不可省。

`ingest/claude.go` 已實作此規則（`isTurnFinal`），策略為`只取 final`：

- 每個 turn 只累積 `end_turn` / `stop_sequence` 的 text
- commentary、thinking、tool call/result 不會進入摘要器
- 沒有 final response 時，assistant text 保持空白，摘要器只根據 user prompt 工作
- `stop_reason` 缺席（舊 transcript）視為 final，避免行為倒退

token 用量與摘要文字分開處理：同一 turn 內所有 model call 的 `message.usage` 都會納入，
但 Claude 可能把同一 response 拆成多筆 transcript record，因此先依 `message.id` 去重，再加總
`input_tokens + cache_creation_input_tokens + cache_read_input_tokens + output_tokens`。

### `system` — `stop_hook_summary`

hook 執行後的結算，是驗證 hook 是否真的跑起來最直接的證據。

```json
{
  "type": "system",
  "subtype": "stop_hook_summary",
  "hookCount": 3,
  "hookInfos": [
    { "command": "/Users/shuk/.local/go/bin/sessiond hook claude", "durationMs": 511 },
    { "command": "sh ${CLAUDE_PLUGIN_ROOT}/bin/hook-wrapper.sh handle-hook Stop", "durationMs": 713 }
  ],
  "hookErrors": [],
  "hookAdditionalContext": [],
  "preventedContinuation": false,
  "stopReason": "",
  "hasOutput": true,
  "level": "suggestion",
  "toolUseID": "be3078e9-d8aa-47e1-8a14-f2dfcc8d3c53"
}
```

| 欄位 | 說明 |
| --- | --- |
| `hookInfos[].command` | 實際執行的指令字串 |
| `hookInfos[].durationMs` | 執行耗時 |
| `hookErrors` | 非空表示某個 hook 失敗 |
| `preventedContinuation` | hook 是否阻止模型繼續（exit 2 的效果） |

### `attachment`

包裹 hook 輸出與 IDE 事件。`attachment.type` 二次分類：`hook_success`、`hook_system_message`、`hook_additional_context`、`opened_file_in_ide`、`selected_lines_in_ide`、`agent_listing_delta`、`mcp_instructions_delta`、`skill_listing`、`output_style`、`task_reminder`。

```json
{
  "type": "attachment",
  "attachment": {
    "type": "hook_success",
    "hookName": "SessionStart:startup",
    "hookEvent": "SessionStart",
    "toolUseID": "6844f4cd-f5d2-42f4-ace7-6a228dd66910",
    "content": "{\"continue\":true,\"status\":\"ready\"}",
    "stdout": "...",
    "stderr": "",
    "exitCode": 0,
    "command": "..."
  }
}
```

### 輕量 record

不帶 envelope，只有 `type` + `sessionId` + 一兩個欄位。

| `type` | 關鍵欄位 | 說明 |
| --- | --- | --- |
| `last-prompt` | `leafUuid` | 指向 session 最新一筆 prompt |
| `mode` | `mode` | `normal` / `plan` |
| `permission-mode` | `permissionMode` | `bypassPermissions` / `default` / `acceptEdits` |
| `ai-title` | `aiTitle` | Claude 生成的標題，例如 `Verify hook functionality` |
| `queue-operation` | `operation`, `content`, `timestamp` | `enqueue` / `dequeue` |
| `file-history-snapshot` | `messageId`, `snapshot.trackedFileBackups` | Edit/Write 前備份 |

`ai-title` 是現成的高品質標題，但 `sessiond` 目前的 `Title` 用 heuristic 取 prompt 第一行，並未採用此欄位。

## sessiond 的解析規則 (Parsing Rules)

實作見 `pkg/sessiond/pkg/ingest/claude.go`。

```
逐行掃描：
  1. isSidechain == true          → 整條丟棄（subagent 不進主流程）
  2. message 不存在               → 丟棄
  3. type=user && role=user:
       onlyToolResult == true     → 丟棄（工具 echo）
       cleanUserText(text) == ""  → 丟棄（system 包裹）
       否則                        → flush 前一筆，開新 RawTurn
  4. type=assistant && role=assistant:
       message.usage 依 message.id 去重後加進 RawTurn.TokenCount
       只有 final response 的 text 才串接進 RawTurn.AssistantText
  5. cwd 取第一個非空值
```

`onlyToolResult` 的判定：`content` 是陣列、含 `tool_result` block、且沒有任何 `text` block。

`cleanUserText` 會剔除含以下標記的訊息：`local-command-caveat`、`<environment_context>`、`<user_instructions>`、`<recommended_plugins>`、`<permissions instructions>`、開頭是 `# agents.md`、開頭是 `caveat:`。

一個 turn 的邊界：從一個真 prompt 開始，累計其後所有 model call 的 usage，但只保留 final response text，
直到下一個真 prompt。

## 已知落差 (Known Gaps)

| 落差 | 影響 |
| --- | --- |
| `tool_use` / `tool_result` 完全不解析 | store 的 `tools[]` 欄位永遠為空，無法追蹤改了哪些檔 |
| `thinking` block 不採用 | 摘要看不到模型推理脈絡 |
| `message.usage` 分類明細不落地 | store 只保留每 turn 加總後的 `token_count` |
| `ai-title` 不採用 | 標題品質低於 Claude 自產的 |
| `stop_hook_summary` 不採用 | 無 hook 健康度指標 |
| `-` 目錄編碼不可逆 | 無法從目錄名精確還原原始路徑 |
