// Package cmd wires sessiond's cobra commands. It follows the conventional
// Go "cmd/" layout so that each subcommand lives in its own file and the test
// binary can introspect the tree without touching os.Exit.
package cmd

import (
	gosdkcmd "github.com/bizshuk/gosdk/cmd"
	"github.com/spf13/cobra"
)

// NewRootCmd builds the sessiond CLI. main.go calls Execute on it.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "sessiond",
		Short:         "AI-coding session summary ingestor",
		Long:          "Reads Claude Code and Codex hook payloads and appends turn summaries to JSONL files under ~/.config/superset/data/sessions/.",
		SilenceUsage:  true, // do not dump usage on run-time errors
		SilenceErrors: true, // root prints errors itself with a clean format
		Version:       "0.2.0",
	}
	root.SetVersionTemplate("sessiond {{.Version}}\n")

	root.AddCommand(gosdkcmd.ConfigCmd)
	root.AddCommand(HookCmd)
	root.AddCommand(InstallCmd)
	root.AddCommand(UninstallCmd)
	root.AddCommand(PauseCmd)
	root.AddCommand(ResumeCmd)

	return root
}
