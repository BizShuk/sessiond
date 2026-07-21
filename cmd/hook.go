package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bizshuk/sessiond/pkg/hook"
)

// HookCmd implements `sessiond hook <agent>`.
var HookCmd = &cobra.Command{
	Use:   "hook <claude|codex>",
	Short: "Consume a Claude or Codex hook payload (called by the agent, not by users)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		agent := args[0]
		if agent != "claude" && agent != "codex" {
			return fmt.Errorf("unknown agent %q (want claude|codex)", agent)
		}
		hook.Run(hook.RunOptions{Agent: agent})
		return nil
	},
}
