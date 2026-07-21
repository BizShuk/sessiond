package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	sessiondcfg "github.com/bizshuk/sessiond/config"
)

func newPauseCmd() *cobra.Command {
	return newHooksStateCmd(
		"pause",
		"Pause hook ingestion",
		"Pauses hook ingestion globally without flushing existing sessions. Hook calls made while paused are ignored and are not replayed by resume.",
		true,
	)
}

func newResumeCmd() *cobra.Command {
	return newHooksStateCmd(
		"resume",
		"Resume hook ingestion",
		"Resumes global hook ingestion. Hook calls ignored while paused are not replayed.",
		false,
	)
}

func newHooksStateCmd(use, short, long string, paused bool) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long + " The state is stored in the app-level settings file, not project .claude or .codex configuration.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sessiondcfg.SetHooksPaused(paused); err != nil {
				return err
			}
			state := "resumed"
			if paused {
				state = "paused"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "hooks %s: %s\n", state, sessiondcfg.SettingsPath())
			return nil
		},
	}
}
