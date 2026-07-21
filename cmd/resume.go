package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// ResumeCmd implements `sessiond resume`.
var ResumeCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume hook ingestion",
	Long:  "Resumes global hook ingestion. Hook calls ignored while paused are not replayed. The state is stored in the app-level settings file, not project .claude or .codex configuration.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := setHooksPaused(false); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "hooks resumed: %s\n", hooksSettingsPath())
		return nil
	},
}
