package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bizshuk/sessiond/pkg/hook"
)

// newHookCmd builds `sessiond hook <agent>`. It is invoked by an agent's
// lifecycle hook with a single-line JSON payload on stdin. The handler is
// best-effort telemetry: a failing hook must never disturb the host agent.
func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook <claude|codex>",
		Short: "Consume a Claude or Codex hook payload (called by the agent, not by users)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agent := args[0]
			if agent != "claude" && agent != "codex" {
				return fmt.Errorf("unknown agent %q (want claude|codex)", agent)
			}
			// Agents ignore exit code, but be loud for anyone who runs this by hand.
			hook.Run(hook.RunOptions{Agent: agent})
			return nil
		},
	}
	return cmd
}