package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bizshuk/sessiond/pkg/install"
)

func newUninstallCmd() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove sessiond hooks from Claude and Codex config files",
		Long: "Resolves the project root from the current directory, then removes only " +
			"sessiond-owned hooks from <project>/.claude/settings.json and " +
			"<project>/.codex/config.toml. Dry-runs by default; pass --apply to back up and write. " +
			"Malformed or ambiguous configuration is left untouched.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return install.RunUninstall(install.Options{Apply: apply})
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "actually remove the hooks (default: dry-run)")
	return cmd
}
