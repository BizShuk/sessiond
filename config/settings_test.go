package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetHooksPausedPreservesSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"other":{"keep":1},"sessiond":{"summarizer":{"provider":"heuristic"},"hooks":{"note":"keep"}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := setHooksPausedAt(path, true); err != nil {
		t.Fatal(err)
	}
	paused, err := hooksPausedAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if !paused {
		t.Fatal("hooks are not paused")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["other"].(map[string]any)["keep"] != float64(1) {
		t.Fatal("top-level settings were not preserved")
	}
	sessiond := settings["sessiond"].(map[string]any)
	if sessiond["summarizer"].(map[string]any)["provider"] != "heuristic" {
		t.Fatal("nested settings were not preserved")
	}
	if sessiond["hooks"].(map[string]any)["note"] != "keep" {
		t.Fatal("hook settings were not preserved")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}

	if err := setHooksPausedAt(path, false); err != nil {
		t.Fatal(err)
	}
	paused, err = hooksPausedAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if paused {
		t.Fatal("hooks are still paused")
	}
}

func TestHooksPausedReadsLatestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := setHooksPausedAt(path, false); err != nil {
		t.Fatal(err)
	}
	paused, err := hooksPausedAt(path)
	if err != nil || paused {
		t.Fatalf("first read = %v, %v", paused, err)
	}
	if err := setHooksPausedAt(path, true); err != nil {
		t.Fatal(err)
	}
	paused, err = hooksPausedAt(path)
	if err != nil || !paused {
		t.Fatalf("second read = %v, %v", paused, err)
	}
}

func TestSetHooksPausedRejectsMalformedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setHooksPausedAt(path, true); err == nil {
		t.Fatal("malformed settings accepted")
	}
}

func TestSetHooksPausedRejectsNonObjectParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"sessiond":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setHooksPausedAt(path, true); err == nil {
		t.Fatal("non-object sessiond key accepted")
	}
}
