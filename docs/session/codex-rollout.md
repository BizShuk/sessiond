# Codex Rollout JSONL

Codex CLI 為每個 session 寫一個 append-only JSONL（稱為 rollout），記錄完整 model 交互、工具執行、環境狀態與 token 用量。`sessiond` 只讀不寫。

## 位置與命名 (Location)

```tree
~/.codex/sessions/
└── 2026/07/20/                                                     # 日期分層
    └── rollout-2026-07-20T11-34-36-019f7d97-0161-74a1-8862-453cfca381f3.jsonl
        │       └── 本機時區時間戳         └── session UUID
```

- 目錄：`YYYY/MM/DD` 三層日期，`不` 以 workspace 分群
- 檔名：`rollout-<local-timestamp>-<session-uuid>.jsonl`

因為不以 workspace 分群，要靠 session id 找檔案必須遞迴掃描整個 `sessions/` 樹，這正是 `ingest.LocateCodexRollout` 存在的原因。

## 雙層 Discriminator (Two-level Discriminator)

與 Claude 最大的差異：Codex 用兩層分類，頂層 `type` 決定通道，`payload.type` 決定事件。

```json
{ "timestamp": "...", "type": "<channel>", "payload": { "type": "<event>", ... } }
```

頂層永遠只有三個欄位：`timestamp`、`type`、`payload`。

以 `019f7d97` session（575 筆）為樣本：

| `type` | 筆數 | `payload.type` | 筆數 |
| --- | --- | --- | --- |
| `response_item` | 399 | `reasoning` | 184 |
| | | `custom_tool_call` | 87 |
| | | `custom_tool_call_output` | 87 |
| | | `message` | 37 |
| | | `function_call` | 2 |
| | | `function_call_output` | 2 |
| `event_msg` | 165 | `token_count` | 96 |
| | | `agent_message` | 27 |
| | | `patch_apply_end` | 11 |
| | | `web_search_end` | 7 |
| | | `task_started` | 6 |
| | | `user_message` | 6 |
| | | `task_complete` | 6 |
| | | `thread_settings_applied` | 5 |
| | | `context_compacted` | 1 |
| `turn_context` | 7 | 無 | |
| `world_state` | 2 | 無 | |
| `session_meta` | 1 | 無 | |
| `compacted` | 1 | 無 | |

### 兩條通道的分工

| 通道 | 語意 | 適合拿來做什麼 |
| --- | --- | --- |
| `response_item` | 送進 model 的原始 API 訊息流 | 逐字重播、reasoning 分析 |
| `event_msg` | TUI 層的語意事件 | 抽真實 prompt / 回覆、成本統計 |

`response_item/message` 混雜大量注入內容（`developer` role 的 permissions、AGENTS.md、plugin 說明），因此 `sessiond` 只讀 `event_msg` 通道。

## `session_meta` — 開頭一筆

整個 rollout 的第一行，記錄 session 身分與環境。

```json
{
  "timestamp": "2026-07-20T03:35:19.903Z",
  "type": "session_meta",
  "payload": {
    "session_id": "019f7d97-0161-74a1-8862-453cfca381f3",
    "id": "019f7d97-0161-74a1-8862-453cfca381f3",
    "timestamp": "2026-07-20T03:34:36.158Z",
    "cwd": "/Users/shuk/projects/tally",
    "originator": "codex-tui",
    "cli_version": "0.144.6",
    "source": "cli",
    "thread_source": "user",
    "model_provider": "openai",
    "history_mode": "legacy",
    "base_instructions": { "text": "You are Codex, an agent based on GPT-5..." },
    "context_window": { "window_id": "019f7d97-0161-74a1-8862-454e04ccafb1" },
    "git": {
      "commit_hash": "04a151fc797ca1e1bb766af143b2ce812e1c7c94",
      "branch": "master",
      "repository_url": "https://github.com/..."
    }
  }
}
```

| 欄位 | 說明 | sessiond 是否採用 |
| --- | --- | --- |
| `cwd` | workspace 絕對路徑 | 是 |
| `session_id` / `id` | 重複的 session UUID | 間接（檔名比對） |
| `cli_version` | Codex 版本，schema 漂移的判斷依據 | 否 |
| `git.commit_hash` / `branch` | session 起始的 git 狀態 | 否 |
| `base_instructions.text` | 完整 system prompt | 否 |
| `model_provider` | `openai` 等 | 否 |

`git` 區塊是 Claude transcript 沒有的：它記錄 session 起點的 commit hash，可用來把 session 綁定到 git 歷史。

## `event_msg` — sessiond 唯一讀的通道

### `user_message` — 真實人類 prompt

```json
{
  "type": "event_msg",
  "payload": {
    "type": "user_message",
    "message": "how to delete xcode, toolchain...",
    "images": [],
    "local_images": [],
    "text_elements": []
  }
}
```

`message` 是純字串，不像 Claude 需要拆 block 陣列。

### `agent_message` — 模型面向使用者的回覆

```json
{
  "type": "event_msg",
  "payload": {
    "type": "agent_message",
    "message": "我先做只讀盤點，確認本機實際的 Xcode/toolchain...",
    "phase": "commentary",
    "memory_citation": null
  }
}
```

`phase` 可為 `commentary` 等，區分回覆屬於過程說明還是最終答案。

### `task_started` / `task_complete` — turn 邊界

```json
{
  "type": "event_msg",
  "payload": {
    "type": "task_started",
    "turn_id": "019f7d97-ac11-7f73-a9b0-9990498bf858",
    "started_at": 1784518519,
    "model_context_window": 258400,
    "collaboration_mode_kind": "default"
  }
}
```

```json
{
  "type": "event_msg",
  "payload": {
    "type": "task_complete",
    "turn_id": "019f7d97-ac11-7f73-a9b0-9990498bf858",
    "last_agent_message": "可以。我把「its」解讀為...",
    "completed_at": 1784520696,
    "duration_ms": 2177000,
    "time_to_first_token_ms": 13540
  }
}
```

`task_complete` 是 turn 邊界的權威來源，且 `last_agent_message` 直接給出該輪最終回覆全文、`duration_ms` 給出耗時。`sessiond` 目前靠 `user_message` 切 turn，並未使用 `turn_id`。

### `token_count` — 成本與配額

96 筆，每次 model 呼叫後都記一次。

```json
{
  "type": "event_msg",
  "payload": {
    "type": "token_count",
    "info": {
      "total_token_usage": {
        "input_tokens": 33644,
        "cached_input_tokens": 9984,
        "output_tokens": 1090,
        "reasoning_output_tokens": 851,
        "total_tokens": 34734
      },
      "last_token_usage": { "...": "同結構，僅最後一次" },
      "model_context_window": 258400
    },
    "rate_limits": {
      "limit_id": "codex",
      "primary": { "used_percent": 32.0, "window_minutes": 10080, "resets_at": 1784949927 },
      "credits": { "has_credits": false, "unlimited": false, "balance": "0" },
      "plan_type": "pro"
    }
  }
}
```

`total_token_usage` 是累計值，取最後一筆即為整個 session 的總用量。`rate_limits.primary.used_percent` 是配額水位。

### `patch_apply_end` — 檔案異動

要做「資料夾進度追蹤」時最有價值的一筆。

```json
{
  "type": "event_msg",
  "payload": {
    "type": "patch_apply_end",
    "call_id": "exec-b8734124-e602-4583-99f6-eae3f4beccf0",
    "turn_id": "019f7eec-2ace-7cd3-b961-0a39a61b8049",
    "success": false,
    "stdout": "",
    "stderr": "Failed to create parent directories for ...",
    "changes": {
      "/abs/path/to/file.sh": { "type": "add", "content": "#!/usr/bin/env bash\n..." }
    }
  }
}
```

`changes` 是路徑 → 異動的 map，`type` 可為 `add` / `update` / `delete`。這等於現成的「這個 turn 改了哪些檔」清單。

### `web_search_end` / `thread_settings_applied` / `context_compacted`

| `payload.type` | 關鍵欄位 | 說明 |
| --- | --- | --- |
| `web_search_end` | `query`, `action.queries[]` | 網路搜尋的查詢字串 |
| `thread_settings_applied` | `thread_settings.{model,reasoning_effort,approval_policy,cwd}` | session 中途改設定 |
| `context_compacted` | 無 | 標記發生過 context 壓縮 |

## `response_item` — 原始 API 訊息流

`sessiond` 不讀，但重播與深度分析需要。

| `payload.type` | 關鍵欄位 | 說明 |
| --- | --- | --- |
| `reasoning` | `id`, `summary[]`, `encrypted_content` | 推理內容，多為加密字串，本地無法解讀 |
| `message` | `role`, `content[].{type,text}` | `role` 含 `developer`（注入）/ `user` / `assistant` |
| `custom_tool_call` | `name`, `input`, `call_id`, `status` | `name` 常見 `exec`；`input` 是一段 JS 腳本字串 |
| `custom_tool_call_output` | `call_id`, `output[].text` | 執行輸出，含 `Wall time` 前綴 |
| `function_call` | `name`, `arguments`, `call_id` | 傳統 function calling，如 `wait` |
| `function_call_output` | `call_id`, `output[]` | 同上的回傳 |

`custom_tool_call.input` 是可執行 JS，例如：

```javascript
const r = await tools.exec_command({
  cmd: "sed -n '1,240p' /Users/shuk/.agents/skills/tutorial/SKILL.md",
  workdir: "/Users/shuk/projects/tally",
  yield_time_ms: 10000,
  max_output_tokens: 20000
});
text(r.output);
```

`reasoning` 佔 184 筆是所有類型最多的，但 `encrypted_content` 為服務端加密，本地端拿不到明文。

## `turn_context` / `world_state` / `compacted`

### `turn_context`

每輪開始時的環境快照。

```json
{
  "type": "turn_context",
  "payload": {
    "turn_id": "019f7d97-ac11-7f73-a9b0-9990498bf858",
    "cwd": "/Users/shuk/projects/tally",
    "workspace_roots": ["/Users/shuk/projects/tally"],
    "current_date": "2026-07-20",
    "timezone": "Asia/Singapore",
    "approval_policy": "never",
    "sandbox_policy": { "type": "danger-full-access" },
    "permission_profile": { "type": "disabled" },
    "model": "gpt-5.6-sol",
    "personality": "pragmatic"
  }
}
```

`workspace_roots` 是陣列，Codex 支援多 workspace root，Claude 沒有對等概念。

### `world_state`

注入模型的環境內容快照。

```json
{
  "type": "world_state",
  "payload": {
    "full": true,
    "state": {
      "agents_md": { "directory": "...", "text": "# 專案工作區..." },
      "apps_instructions": {},
      "environments": {},
      "plugins_instructions": {},
      "skills": {}
    }
  }
}
```

`full: true` 表示完整快照；後續可能有增量。

### `compacted`

context 壓縮後，用摘要取代原始歷史。

```json
{
  "type": "compacted",
  "payload": {
    "message": "",
    "replacement_history": [
      { "type": "message", "role": "user", "content": [{ "type": "input_text", "text": "..." }] }
    ]
  }
}
```

`replacement_history` 是壓縮後保留的訊息序列。解析 turn 時若同時掃 `compacted` 與原始事件會導致重複計算。

## sessiond 的解析規則 (Parsing Rules)

實作見 `pkg/sessiond/pkg/ingest/codex.go`。

```
逐行掃描：
  1. payload 不是物件            → 丟棄
  2. type == "session_meta"      → 取 payload.cwd，繼續
  3. type != "event_msg"         → 丟棄（response_item / turn_context / world_state 全跳過）
  4. payload.type == "user_message":
       cleanUserText(message) == ""  → 丟棄（注入的 AGENTS.md / plugins / permissions）
       否則                           → flush 前一筆，開新 RawTurn
  5. payload.type == "agent_message":
       message 非空 → 串接進當前 RawTurn.AssistantText
```

找檔案的邏輯 `LocateCodexRollout`：遞迴走訪 `sessionsDir`，比對檔名前綴 `rollout-`、後綴 `.jsonl`、且 `strings.Contains(name, sessionID)`。找到多個時取最後一個。

hook 端的 fallback：payload 給的 `transcript_path` 若不是 `.jsonl` 或檔案不存在，才呼叫 `LocateCodexRollout` 掃描。

## 已知落差 (Known Gaps)

| 落差 | 影響 |
| --- | --- |
| `patch_apply_end.changes` 不解析 | 放棄現成的檔案異動清單 |
| `custom_tool_call` 不解析 | 無法追蹤執行了什麼指令 |
| `token_count` 不解析 | 無成本追蹤 |
| `task_complete.turn_id` 不使用 | turn 邊界靠 `user_message` 推斷，較脆弱 |
| `task_complete.last_agent_message` 不使用 | 現成的最終回覆全文被忽略，改用 `agent_message` 串接 |
| `compacted` 不處理 | 發生 context 壓縮的 session 可能重複計算 turn |
| `LocateCodexRollout` 全樹掃描 | session 目錄大時成本隨檔案數線性成長 |
| 找到多檔取最後一個 | `WalkDir` 順序決定結果，非顯式 latest-wins |
