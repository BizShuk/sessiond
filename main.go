// Command sessiond ingests AI-coding session turns from agent hooks and appends
// one-line summaries to per-session JSONL files that the superset VSCode panel
// reads. See ./cmd for command definitions.
package main

import (
	"fmt"
	"os"

	gosdkcfg "github.com/bizshuk/gosdk/config"

	"github.com/bizshuk/sessiond/cmd"
	sessiondcfg "github.com/bizshuk/sessiond/config"
)

func main() {
	gosdkcfg.Default(gosdkcfg.WithAppName("superset"))
	sessiondcfg.Init()

	if err := cmd.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "sessiond:", err)
		os.Exit(1)
	}
}