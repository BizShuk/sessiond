// Package example 內嵌本服務的範例設定, 並登記給 `config default`,
// 由它複製到設定目錄 (~/.config/<app>/). .env.example 只列 credential key, 值一律留空.
// config.example.yaml 只是 YAML 寫法的參考, 不登記, 以免與 settings.json 重複種下同一組設定.
package example

import (
	_ "embed"

	cfgcmd "github.com/bizshuk/gosdk/cmd/config"
)

//go:embed settings.example.json
var Settings []byte

//go:embed .env.example
var Env []byte

func init() {
	cfgcmd.MustRegisterDefault("settings.json", Settings)
	cfgcmd.MustRegisterDefault(".env", Env)
}
