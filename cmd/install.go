package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bizshuk/sessiond/pkg/install"
)

var installApply bool

// InstallCmd implements `sessiond install`.
var InstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Register the sessiond hooks in Claude and Codex config files",
	Long: "Resolves the project root from the current directory, then writes " +
		"Stop/StopFailure/TaskCompleted hooks into <project>/.claude/settings.json " +
		"and Stop/SubagentStop hooks into <project>/.codex/config.toml. " +
		"Dry-runs by default; pass --apply to write. Each existing target is backed up first.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return install.Run(install.Options{Apply: installApply})
	},
}

func init() {
	InstallCmd.Flags().BoolVar(&installApply, "apply", false, "actually write the changes (default: dry-run)")
}
