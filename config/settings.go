package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	gosdkcfg "github.com/bizshuk/gosdk/config"
)

// SettingsPath returns the app-level JSON file changed by pause and resume.
func SettingsPath() string {
	return filepath.Join(gosdkcfg.GetAppConfigDir(), "settings.json")
}

// HooksPaused reads the persisted pause state on every call so hook processes
// observe pause and resume changes without relying on viper's startup cache.
func HooksPaused() (bool, error) {
	return hooksPausedAt(SettingsPath())
}

// SetHooksPaused atomically updates the app-level pause state while preserving
// unrelated settings.
func SetHooksPaused(paused bool) error {
	return setHooksPausedAt(SettingsPath(), paused)
}

func hooksPausedAt(path string) (bool, error) {
	settings, _, err := readSettings(path)
	if err != nil {
		return false, err
	}
	sessiond, ok := settings["sessiond"].(map[string]any)
	if !ok {
		return false, nil
	}
	hooks, ok := sessiond["hooks"].(map[string]any)
	if !ok {
		return false, nil
	}
	paused, _ := hooks["paused"].(bool)
	return paused, nil
}

func setHooksPausedAt(path string, paused bool) error {
	settings, mode, err := readSettings(path)
	if err != nil {
		return err
	}
	sessiond, err := objectAt(settings, "sessiond")
	if err != nil {
		return err
	}
	hooks, err := objectAt(sessiond, "hooks")
	if err != nil {
		return err
	}
	hooks["paused"] = paused

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	if err := atomicWrite(path, data, mode); err != nil {
		return fmt.Errorf("write settings %s: %w", path, err)
	}
	return nil
}

func readSettings(path string) (map[string]any, fs.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return make(map[string]any), 0o644, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read settings %s: %w", path, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, 0, fmt.Errorf("decode settings %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("stat settings %s: %w", path, err)
	}
	return settings, info.Mode().Perm(), nil
}

func objectAt(parent map[string]any, key string) (map[string]any, error) {
	value, exists := parent[key]
	if !exists {
		object := make(map[string]any)
		parent[key] = object
		return object, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("settings key %q must be an object", key)
	}
	return object, nil
}

func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".sessiond-settings-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
