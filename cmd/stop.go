package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bizshuk/sessiond/pkg/stop"
)

// newStopCmd builds `sessiond stop`. It re-fires the Stop hook for sessions in
// the JSONL store — useful when an agent session ended without firing hooks (a
// crash, hooks uninstalled mid-session, etc.) or when the operator wants to
// force a manual flush from a different cwd. Works across every workspace by
// default; --workspace, --session, and --agent narrow the scope.
func newStopCmd() *cobra.Command {
	var (
		workspace string
		sessionID string
		agent     string
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Re-fire the Stop hook for sessions in the store",
		Long: "Walks the JSONL store under <dataDir>/sessions/ and re-fires the per-agent " +
			"Stop hook for every session whose transcript still holds unsummarized turns. " +
			"Runs regardless of the current working directory — by default the scope is " +
			"every workspace sessiond has captured. Pass --workspace, --session, or " +
			"--agent to narrow the scope. --dry-run lists what would be processed " +
			"without writing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			summary, err := stop.Run(stop.Options{
				Scope: stop.Scope{
					Workspace: workspace,
					SessionID: sessionID,
					Agent:     agent,
				},
				DryRun: dryRun,
			})
			if err != nil {
				return err
			}
			printStopSummary(cmd.OutOrStdout(), summary, dryRun)
			return nil
		},
	}
	cmd.Flags().StringVar(&workspace, "workspace", "", "limit to one workspace (absolute path)")
	cmd.Flags().StringVar(&sessionID, "session", "", "limit to one session id")
	cmd.Flags().StringVar(&agent, "agent", "", "limit to one agent (claude|codex)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "discover targets without syncing")
	return cmd
}

// printStopSummary writes a single human-readable block. The text format is the
// only user-facing surface of this command and is what downstream scripts may
// grep for, so it stays line-based and stable.
func printStopSummary(out io.Writer, s stop.Summary, dryRun bool) {
	if dryRun {
		fmt.Fprintf(out, "dry-run: %d session(s) match the scope\n", s.Scanned)
		return
	}
	if s.Scanned == 0 {
		fmt.Fprintln(out, "no sessions in store match the scope")
		return
	}
	fmt.Fprintf(out, "scanned=%d advanced=%d appended=%d\n",
		s.Scanned, s.Advanced, s.Appended)
	for _, reason := range s.Skipped {
		fmt.Fprintf(out, "skipped: %s\n", reason)
	}
}
