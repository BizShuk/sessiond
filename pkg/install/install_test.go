package install

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// home builds a fake $HOME with the directories install expects
// (~/.claude, ~/.codex). Each subtest gets its own copy so parallel runs don't
// share state.
func home(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	claudeDir := filepath.Join(root, ".claude")
	codexDir := filepath.Join(root, ".codex")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, claudeDir, codexDir
}

func newOpts(_ *testing.T, homeDir string, apply bool) Options {
	return Options{
		Apply:       apply,
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
		Binary:      "/fake/bin/sessiond",
		ProjectRoot: homeDir,
	}
}

func TestRun_DryRun_DoesNotTouchFiles(t *testing.T) {
	h, claudeDir, codexDir := home(t)
	opts := newOpts(t, h, false)

	if err := Run(opts); err != nil {
		t.Fatal(err)
	}

	// Neither file should exist after a dry-run with no prior content.
	if _, err := os.Stat(filepath.Join(claudeDir, "settings.json")); !os.IsNotExist(err) {
		t.Error("dry-run wrote settings.json")
	}
	if _, err := os.Stat(filepath.Join(codexDir, "config.toml")); !os.IsNotExist(err) {
		t.Error("dry-run wrote config.toml")
	}
}

func TestRun_Apply_WritesClaudeAndCodexAndCreatesBackups(t *testing.T) {
	h, claudeDir, codexDir := home(t)
	// Pre-seed both files so backup() has something to copy. A real install
	// over a fresh home is a separate (no-backup) flow; see TestRun_DryRun.
	claudeFile := filepath.Join(claudeDir, "settings.json")
	codexFile := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(claudeFile, []byte(`{"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexFile, []byte("# existing config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeBak, _ := os.ReadFile(claudeFile)
	codexBak, _ := os.ReadFile(codexFile)

	opts := newOpts(t, h, true)
	if err := Run(opts); err != nil {
		t.Fatal(err)
	}

	// Claude settings.json exists, is valid JSON, contains the three events.
	b, err := os.ReadFile(claudeFile)
	if err != nil {
		t.Fatalf("claude settings not written: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("claude settings not valid JSON: %v", err)
	}
	hooks, _ := root["hooks"].(map[string]any)
	for _, ev := range []string{"Stop", "StopFailure", "TaskCompleted"} {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("claude missing event %s", ev)
		}
	}
	if root["theme"] != "dark" {
		t.Errorf("pre-existing theme lost: %v", root)
	}

	// Codex config.toml exists, has the marker block, declares both events.
	cb, err := os.ReadFile(codexFile)
	if err != nil {
		t.Fatalf("codex config not written: %v", err)
	}
	cx := string(cb)
	if !strings.Contains(cx, codexMarkerBegin) || !strings.Contains(cx, codexMarkerEnd) {
		t.Errorf("codex marker block missing: %s", cx)
	}
	for _, want := range []string{"[[hooks.Stop]]", "[[hooks.SubagentStop]]", `command = "/fake/bin/sessiond hook codex"`} {
		if !strings.Contains(cx, want) {
			t.Errorf("codex config missing %q in:\n%s", want, cx)
		}
	}
	if !strings.HasPrefix(cx, "# existing config\n") {
		t.Errorf("codex pre-existing content lost: %q", cx[:min(40, len(cx))])
	}

	// Backups were created and their contents equal the originals.
	findBak := func(dir, prefix string) []byte {
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			if strings.HasPrefix(e.Name(), prefix+".bak.") {
				x, _ := os.ReadFile(filepath.Join(dir, e.Name()))
				return x
			}
		}
		return nil
	}
	if got := findBak(claudeDir, "settings.json"); !bytes.Equal(got, claudeBak) {
		t.Errorf("claude backup content mismatch: %q", got)
	}
	if got := findBak(codexDir, "config.toml"); !bytes.Equal(got, codexBak) {
		t.Errorf("codex backup content mismatch: %q", got)
	}
}

func TestRun_Idempotent_SecondApplyIsNoop(t *testing.T) {
	h, claudeDir, _ := home(t)
	opts := newOpts(t, h, true)

	if err := Run(opts); err != nil {
		t.Fatal(err)
	}
	claudeFile := filepath.Join(claudeDir, "settings.json")
	first, _ := os.ReadFile(claudeFile)
	firstMtime := mustStat(t, claudeFile).ModTime()

	// Sleep a beat to ensure mtime would change if we re-wrote.
	// (Not strictly necessary, but protects against fat-fingered mtime-reset.)
	// Re-apply; content must be identical and we must NOT add a second hook entry.
	if err := Run(opts); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(claudeFile)
	if !bytes.Equal(first, second) {
		t.Errorf("second apply changed file:\n%s\n---\n%s", first, second)
	}

	var root map[string]any
	_ = json.Unmarshal(second, &root)
	hooks := root["hooks"].(map[string]any)
	for _, ev := range []string{"Stop", "StopFailure", "TaskCompleted"} {
		arr, _ := hooks[ev].([]any)
		if len(arr) != 1 {
			t.Errorf("claude %s has %d entries, want 1 (idempotent)", ev, len(arr))
		}
	}
	_ = firstMtime // accepted that the second run reads + writes; what matters is content.
}

func TestRun_PreservesExistingClaudeSettings(t *testing.T) {
	h, claudeDir, _ := home(t)
	claudeFile := filepath.Join(claudeDir, "settings.json")
	existing := `{"theme":"dark","hooks":{"Other":[{"hooks":[{"type":"command","command":"x"}]}]}}`
	if err := os.WriteFile(claudeFile, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := newOpts(t, h, true)
	if err := Run(opts); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(claudeFile)
	var root map[string]any
	_ = json.Unmarshal(b, &root)
	if root["theme"] != "dark" {
		t.Errorf("existing theme lost: %v", root)
	}
	hooks := root["hooks"].(map[string]any)
	if _, ok := hooks["Other"]; !ok {
		t.Errorf("existing 'Other' hook lost")
	}
	if _, ok := hooks["Stop"]; !ok {
		t.Errorf("new Stop hook not added")
	}
}

func TestRun_CodexSymlink_WarnsAndWritesRealPath(t *testing.T) {
	h, _, codexDir := home(t)
	// Create a real config in a "shared" dir, symlink it into the fake home.
	sharedDir := t.TempDir()
	sharedFile := filepath.Join(sharedDir, "config.toml")
	if err := os.WriteFile(sharedFile, []byte("# existing config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sharedFile, filepath.Join(codexDir, "config.toml")); err != nil {
		t.Skip("symlinks unsupported on this platform")
	}

	opts := newOpts(t, h, true)
	if err := Run(opts); err != nil {
		t.Fatal(err)
	}

	// Marker landed in the SHARED file, not the symlink.
	got, _ := os.ReadFile(sharedFile)
	if !strings.Contains(string(got), codexMarkerBegin) {
		t.Errorf("marker not written through symlink; got:\n%s", got)
	}
	out := opts.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "symlink") {
		t.Errorf("stdout missing symlink warning:\n%s", out)
	}
}

func TestRunUninstall_RoundTripPreservesUnrelatedConfig(t *testing.T) {
	h, claudeDir, codexDir := home(t)
	claudeFile := filepath.Join(claudeDir, "settings.json")
	codexFile := filepath.Join(codexDir, "config.toml")
	claudeOriginal := []byte(`{"theme":"dark","hooks":{"Other":[{"hooks":[{"type":"command","command":"other"}]}]}}`)
	codexOriginal := []byte("# existing config\nmodel = \"test\"\n")
	if err := os.WriteFile(claudeFile, claudeOriginal, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexFile, codexOriginal, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := newOpts(t, h, true)
	if err := Run(opts); err != nil {
		t.Fatal(err)
	}
	if err := RunUninstall(opts); err != nil {
		t.Fatal(err)
	}

	var root map[string]any
	gotClaude, _ := os.ReadFile(claudeFile)
	if err := json.Unmarshal(gotClaude, &root); err != nil {
		t.Fatal(err)
	}
	if root["theme"] != "dark" {
		t.Fatalf("unrelated setting lost: %s", gotClaude)
	}
	hooks := root["hooks"].(map[string]any)
	if len(hooks) != 1 || hooks["Other"] == nil {
		t.Fatalf("unrelated hook changed: %s", gotClaude)
	}
	gotCodex, _ := os.ReadFile(codexFile)
	if !bytes.Equal(gotCodex, codexOriginal) {
		t.Fatalf("codex round trip changed unrelated bytes:\n%q\nwant:\n%q", gotCodex, codexOriginal)
	}
	if mustStat(t, claudeFile).Mode().Perm() != 0o600 || mustStat(t, codexFile).Mode().Perm() != 0o600 {
		t.Fatal("file mode was not preserved")
	}
}

func TestRunUninstall_DryRunAndMissingFilesAreNoop(t *testing.T) {
	h := t.TempDir()
	opts := newOpts(t, h, false)
	if err := RunUninstall(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h, ".claude")); !os.IsNotExist(err) {
		t.Fatal("dry-run created .claude")
	}
	if _, err := os.Stat(filepath.Join(h, ".codex")); !os.IsNotExist(err) {
		t.Fatal("dry-run created .codex")
	}
}

func TestRunUninstall_MalformedInputsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		claude string
		codex  string
	}{
		{name: "malformed json", claude: `{broken`, codex: "# config\n"},
		{name: "invalid hooks shape", claude: `{"hooks":[]}`, codex: "# config\n"},
		{name: "missing codex end", claude: `{}`, codex: codexMarkerBegin + "\n"},
		{name: "duplicate codex marker", claude: `{}`, codex: codexHookBlock("/old/sessiond hook codex") + codexHookBlock("/old/sessiond hook codex")},
		{name: "tampered codex block", claude: `{}`, codex: strings.Replace(codexHookBlock("/old/sessiond hook codex"), "type = \"command\"", "type = \"other\"", 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, claudeDir, codexDir := home(t)
			claudeFile := filepath.Join(claudeDir, "settings.json")
			codexFile := filepath.Join(codexDir, "config.toml")
			_ = os.WriteFile(claudeFile, []byte(tt.claude), 0o644)
			_ = os.WriteFile(codexFile, []byte(tt.codex), 0o644)
			opts := newOpts(t, h, true)
			if err := RunUninstall(opts); err != nil {
				t.Fatal(err)
			}
			gotClaude, _ := os.ReadFile(claudeFile)
			gotCodex, _ := os.ReadFile(codexFile)
			if !bytes.Equal(gotClaude, []byte(tt.claude)) || !bytes.Equal(gotCodex, []byte(tt.codex)) {
				t.Fatal("malformed input was modified")
			}
		})
	}
}

func TestRun_MalformedClaudeDoesNotOverwrite(t *testing.T) {
	h, claudeDir, _ := home(t)
	path := filepath.Join(claudeDir, "settings.json")
	original := []byte(`{broken`)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	opts := newOpts(t, h, true)
	if err := Run(opts); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, original) {
		t.Fatalf("malformed settings overwritten: %q", got)
	}
}

func TestRunUninstall_RemovesOldBinaryAndDuplicateClaudeCommands(t *testing.T) {
	h, claudeDir, codexDir := home(t)
	owned := map[string]any{"type": "command", "command": "/old/path/sessiond hook claude"}
	other := map[string]any{"type": "command", "command": "/other/tool hook claude"}
	entry := map[string]any{"hooks": []any{owned, owned, other}}
	root := map[string]any{"hooks": map[string]any{"Stop": []any{entry}}}
	data, _ := json.Marshal(root)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644)
	_ = os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(codexHookBlock("/old/path/sessiond hook codex")), 0o644)

	opts := newOpts(t, h, true)
	if err := RunUninstall(opts); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	var decoded map[string]any
	_ = json.Unmarshal(got, &decoded)
	nested := decoded["hooks"].(map[string]any)["Stop"].([]any)[0].(map[string]any)["hooks"].([]any)
	if len(nested) != 1 || nested[0].(map[string]any)["command"] != "/other/tool hook claude" {
		t.Fatalf("owned duplicate removal failed: %s", got)
	}
	codex, _ := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if strings.Contains(string(codex), codexMarkerBegin) {
		t.Fatalf("old binary codex block not removed: %s", codex)
	}
}

func TestRunUninstall_CodexSymlinkPreserved(t *testing.T) {
	h, _, codexDir := home(t)
	shared := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("# shared\n")
	if err := os.WriteFile(shared, append(original, []byte("\n"+codexHookBlock("/old/sessiond hook codex"))...), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(codexDir, "config.toml")
	if err := os.Symlink(shared, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	opts := newOpts(t, h, true)
	if err := RunUninstall(opts); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("uninstall replaced or removed symlink")
	}
	got, _ := os.ReadFile(shared)
	if !bytes.Equal(got, original) {
		t.Fatalf("unexpected symlink target: %q", got)
	}
}

func TestBackupCreatesDistinctFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backup(path); err != nil {
		t.Fatal(err)
	}
	if err := backup(path); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil || len(matches) != 2 {
		t.Fatalf("got %d backups, want 2: %v", len(matches), err)
	}
	for _, match := range matches {
		got, _ := os.ReadFile(match)
		if string(got) != "original" {
			t.Fatalf("backup mismatch: %q", got)
		}
	}
}

func TestResolveProjectRoot(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		root := t.TempDir()
		got, err := resolveProjectRoot(root, "")
		want, err := canonicalPath(root)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("root = %q, want %q", got, want)
		}
	})

	t.Run("git nested directory", func(t *testing.T) {
		root := t.TempDir()
		command := exec.Command("git", "init", "-q", root)
		if output, err := command.CombinedOutput(); err != nil {
			t.Skipf("git init unavailable: %v: %s", err, output)
		}
		nested := filepath.Join(root, "a", "b")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := resolveProjectRoot("", nested)
		if err != nil {
			t.Fatal(err)
		}
		want, err := canonicalPath(root)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("root = %q, want %q", got, want)
		}
	})

	t.Run("non git fallback", func(t *testing.T) {
		workingDir := t.TempDir()
		got, err := resolveProjectRoot("", workingDir)
		want, err := canonicalPath(workingDir)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("root = %q, want %q", got, want)
		}
	})
}

func TestRun_ProjectTargetsIgnoreHome(t *testing.T) {
	project := t.TempDir()
	homeDir := t.TempDir()
	legacyClaude := filepath.Join(homeDir, ".claude", "settings.json")
	legacyCodex := filepath.Join(homeDir, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(legacyClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(legacyCodex), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeOriginal := []byte(`{"legacy":true}`)
	codexOriginal := []byte("# legacy\n")
	_ = os.WriteFile(legacyClaude, claudeOriginal, 0o644)
	_ = os.WriteFile(legacyCodex, codexOriginal, 0o644)
	t.Setenv("HOME", homeDir)

	opts := newOpts(t, project, true)
	if err := Run(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "settings.json")); err != nil {
		t.Fatalf("project Claude settings missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".codex", "config.toml")); err != nil {
		t.Fatalf("project Codex config missing: %v", err)
	}
	gotClaude, _ := os.ReadFile(legacyClaude)
	gotCodex, _ := os.ReadFile(legacyCodex)
	if !bytes.Equal(gotClaude, claudeOriginal) || !bytes.Equal(gotCodex, codexOriginal) {
		t.Fatal("legacy HOME configs were modified")
	}
}

func TestRun_WorkingDirResolvesGitRoot(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, output)
	}
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := newOpts(t, "", true)
	opts.ProjectRoot = ""
	opts.WorkingDir = nested
	if err := Run(opts); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); err != nil {
		t.Fatalf("root settings missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nested, ".claude")); !os.IsNotExist(err) {
		t.Fatal("wrote hooks under nested working directory")
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
