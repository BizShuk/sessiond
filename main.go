// Command sessiond ingests AI-coding session turns from agent hooks and appends
// one-line summaries to per-session JSONL files that the superset VSCode panel
// reads. See ./cmd for command definitions.
package main

import (
	"fmt"
	"os"

	"github.com/bizshuk/sessiond/cmd"
	sessiondcfg "github.com/bizshuk/sessiond/config"
)

func main() {
	sessiondcfg.Load()

	if err := cmd.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "sessiond:", err)
		os.Exit(1)
	}
}
