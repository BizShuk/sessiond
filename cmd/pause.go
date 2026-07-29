package cmd

import (
	"fmt"
	"strconv"

	gosdkconfig "github.com/bizshuk/gosdk/cmd/config"
	"github.com/spf13/cobra"
)

// HOOKS_PAUSED_KEY is the dotted config path holding the global pause flag.
// It is read back through sessiond's own config package, which resolves it
// via viper after gosdk's config.Default() has merged every layer.
const HOOKS_PAUSED_KEY = "sessiond.hooks.paused"

// PauseCmd implements `sessiond pause`.
var PauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Pause hook ingestion",
	Long:  "Pauses hook ingestion globally without flushing existing sessions. Hook calls made while paused are ignored and are not replayed by resume. The state is stored in the app-level settings file, not project .claude or .codex configuration.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := setHooksPaused(true)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "hooks paused: %s\n", path)
		return nil
	},
}

// setHooksPaused writes the pause flag and returns the file it landed in.
//
// The settings files belong to gosdk: config.Default() loads them and
// cmd/config writes them. An empty WriteOptions targets LOCAL_SETTINGS_FILE
// under GetAppConfigDir(), which is the last layer before APP_ environment
// variables — so the flag wins over config.yaml and settings.json. sessiond
// never opens, parses or encodes those files itself.
func setHooksPaused(paused bool) (string, error) {
	report, err := gosdkconfig.Update(
		[]string{HOOKS_PAUSED_KEY + "=" + strconv.FormatBool(paused)},
		gosdkconfig.WriteOptions{},
	)
	if err != nil {
		return "", err
	}
	return report.Path, nil
}
