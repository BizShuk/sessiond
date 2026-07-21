package cmd

import (
	"fmt"
	"path/filepath"
	"strconv"

	gosdkcmd "github.com/bizshuk/gosdk/cmd"
	gosdkcfg "github.com/bizshuk/gosdk/config"
	"github.com/spf13/cobra"
)

// PauseCmd implements `sessiond pause`.
var PauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Pause hook ingestion",
	Long:  "Pauses hook ingestion globally without flushing existing sessions. Hook calls made while paused are ignored and are not replayed by resume. The state is stored in the app-level settings file, not project .claude or .codex configuration.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := setHooksPaused(true); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "hooks paused: %s\n", hooksSettingsPath())
		return nil
	},
}

func setHooksPaused(paused bool) error {
	_, err := gosdkcmd.RunConfigUpdate([]string{"sessiond.hooks.paused=" + strconv.FormatBool(paused)})
	return err
}

func hooksSettingsPath() string {
	return filepath.Join(gosdkcfg.GetAppConfigDir(), gosdkcmd.LOCAL_SETTINGS_FILE)
}
