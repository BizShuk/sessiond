# Context

目前 `sessiond stop` 是一個「掃描既有 session 並重新觸發 hook」的手動 flush 命令；使用者希望改成真正的 hook 開關，而且已確認：

- CLI 完整改名為 `sessiond pause` / `sessiond resume`，不保留 `stop` 或舊 flush 行為。
- `pause` 要立即停用，不先補寫任何 session summary。
- 狀態持久化到 app-level `~/.config/superset/settings.json`；後續任何 Claude/Codex hook invocation 都 no-op。
- `resume` 將相同 toggle 恢復，讓 hook ingestion 重新運作。

目標是讓此開關跨 process 生效、保留 settings 中其他欄位，且不混用 project hook wiring 檔（`.claude/settings.json` / `.codex/config.toml`）。

# Implementation

1. **新增可持久化的 hook pause 設定 API** — `config/config.go`（必要時拆出 `config/settings.go`）
   - 使用 key `sessiond.hooks.paused`，default 為 `false`。
   - production 路徑固定由 `gosdkcfg.GetAppConfigDir()` 組成 `~/.config/superset/settings.json`，不寫 cwd 的 layered settings，也不寫 project agent config。
   - 提供讀取最新檔案狀態與設定狀態的 typed API（例如 `HooksPaused()`、`SetHooksPaused(bool)`）；讀取不能只依賴 process 啟動時載入的 Viper cache，確保 pause/resume 後立刻及跨 process 生效。
   - 以 `map[string]any` 合併 nested JSON，只改 `sessiond.hooks.paused` 並保留未知 top-level/nested keys；沿用 `pkg/install/install.go` 的 temp-file + rename 思路進行 atomic write、建立缺少的 config directory，並保留既有檔案 mode。
   - malformed/unreadable settings 在 CLI 寫入時回傳明確 error；hook gate 讀取失敗時由 hook 記錄後 fail-open，避免 agent 因設定損壞被阻塞。

2. **以 pause/resume commands 取代 stop** — `cmd/stop.go` 改為對應的 pause/resume command 檔，`cmd/root.go`
   - 移除 `newStopCmd` 註冊，新增無 positional args、無 scope/dry-run flags 的 `newPauseCmd` 與 `newResumeCmd`。
   - `pause` 只呼叫 `config.SetHooksPaused(true)`；`resume` 只呼叫 `config.SetHooksPaused(false)`，成功時輸出穩定的一行狀態與 settings 路徑。
   - 更新 help，明確說明這是全域 app-level hook ingestion toggle，pause 不會 flush 歷史 session，resume 不會重播 pause 期間略過的 hook。
   - 完全移除已無入口的 `pkg/stop` 實作與測試，避免保留不可達的舊 flush 邏輯。

3. **在共用 hook pipeline 最前方套用 gate** — `pkg/hook/hook.go`
   - `withDefaults` 後先維持 agent stdout contract：Codex 仍輸出 `{"continue":true}`，Claude 保持空 stdout。
   - 在讀 stdin、找 transcript、建立 summarizer、存 JSONL 之前查詢 `sessiond.hooks.paused`；paused 時直接 `return nil`，因此所有 `sessiond hook claude|codex` 都不產生 ingestion 副作用。
   - 為測試在 `RunOptions` 注入 pause-state reader（production default 使用 config API），以驗證 no-op 而不碰使用者真實 settings。
   - 保留 hook 現有「錯誤只記錄、永遠回傳 nil」契約；設定讀取錯誤記 warning 並繼續正常 hook 流程。

4. **更新範例與操作文件** — `settings.example.json`, `README.md`
   - 加入 `sessiond.hooks.paused: false` 與 pause/resume 說明。
   - 將所有 `stop` usage/語意替換為 `pause` / `resume`，註明實際寫入 `~/.config/superset/settings.json`，與 project hook installation settings 分離。
   - 說明 paused 期間 Claude 無 stdout、Codex 仍取得 continue response，且被略過的 hooks 不會由 resume 自動補寫。

# Tests

1. **Config persistence tests** — `config/config_test.go`
   - 不存在 settings 時建立正確 nested JSON；true/false 可來回切換。
   - 保留未知 top-level 與 `sessiond` nested keys，只更新 `hooks.paused`。
   - 同一 process 每次讀到最新檔案，而非 stale Viper state。
   - malformed JSON、不可寫路徑回傳 error；atomic write 後 JSON 完整且 mode 合理。

2. **Hook gate tests** — `pkg/hook/hook_test.go`
   - paused Claude：不讀/解析 transcript、不呼叫 summarizer、不寫 store、stdout 空、return nil。
   - paused Codex：仍精確輸出 `{"continue":true}`，但不解析、不摘要、不寫 store。
   - gate reader error 時 fail-open，既有 happy path 繼續工作；現有 idempotence、StopFailure、malformed payload tests 保持通過。

3. **CLI tree/behavior tests** — `cmd/root_test.go`（必要時新增 command test）
   - root 包含 pause/resume 且不再包含 stop；兩者拒絕 positional args，也沒有舊 scope/dry-run flags。
   - 驗證 pause/resume 各自寫入正確 boolean、成功輸出與錯誤傳遞。

4. **End-to-end verification**
   - 執行 `go test ./...`、`go vet ./...`、`go build ./...`。
   - 使用隔離的 settings fixture/可覆寫測試入口執行：正常 hook 可同步 → pause → Claude/Codex hooks no-op（Codex response 保留）→ resume → hook 再次可同步。
   - 檢查 pause/resume 前後 settings 的其他欄位未改變，且 repo 不再暴露 `sessiond stop`。

# Critical files

- `config/config.go`（及可能新增的 `config/settings.go`、config tests）
- `pkg/hook/hook.go`
- `pkg/hook/hook_test.go`
- `cmd/root.go`
- `cmd/stop.go`（改由 pause/resume command 取代）
- `cmd/root_test.go`
- `pkg/stop/`（移除舊實作與 tests）
- `settings.example.json`
- `README.md`
