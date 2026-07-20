package install

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
)

// RunUninstall removes only sessiond-owned hooks. Missing files are no-ops and
// malformed or ambiguous files are left untouched.
func RunUninstall(opts Options) error {
	bin, err := resolveBinary(opts.Binary)
	if err != nil {
		return err
	}
	stdout, stderr := resolveIO(opts)
	root, err := resolveProjectRoot(opts.ProjectRoot, opts.WorkingDir)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "binary: %s\nproject: %s\n\n", bin, root)
	uninstallClaude(stdout, stderr, filepath.Join(root, ".claude", "settings.json"), bin, opts.Apply)
	uninstallCodex(stdout, stderr, filepath.Join(root, ".codex", "config.toml"), opts.Apply)
	if !opts.Apply {
		fmt.Fprintln(stdout, "\n(dry-run) re-run with --apply to remove these hooks.")
	}
	return nil
}

func uninstallClaude(stdout, stderr io.Writer, path, bin string, apply bool) TargetStatus {
	real := resolveSymlink(path)
	status := TargetStatus{Path: real}
	root, exists, mode, err := readClaudeRoot(real)
	fmt.Fprintf(stdout, "claude  %s\n", path)
	printSymlinkNote(stdout, path, real)
	if err != nil {
		return skipTarget(stderr, "claude", status, err)
	}
	if !exists {
		fmt.Fprintln(stdout, "  status: not present")
		return status
	}
	hooks, err := claudeHooks(root, false)
	if err != nil {
		return skipTarget(stderr, "claude", status, err)
	}
	if hooks == nil {
		fmt.Fprintln(stdout, "  status: already absent")
		return status
	}

	removed := 0
	for _, event := range claudeEvents {
		value, exists := hooks[event]
		if !exists {
			continue
		}
		entries, ok := value.([]any)
		if !ok {
			return skipTarget(stderr, "claude", status, fmt.Errorf("invalid hooks.%s: event must be an array", event))
		}
		updated := make([]any, 0, len(entries))
		for _, entry := range entries {
			object, ok := entry.(map[string]any)
			if !ok {
				return skipTarget(stderr, "claude", status, fmt.Errorf("invalid hooks.%s: event entry must be an object", event))
			}
			nestedValue, exists := object["hooks"]
			if !exists {
				updated = append(updated, entry)
				continue
			}
			nested, ok := nestedValue.([]any)
			if !ok {
				return skipTarget(stderr, "claude", status, fmt.Errorf("invalid hooks.%s: entry hooks must be an array", event))
			}
			kept := make([]any, 0, len(nested))
			for _, hook := range nested {
				hookObject, ok := hook.(map[string]any)
				if !ok {
					return skipTarget(stderr, "claude", status, fmt.Errorf("invalid hooks.%s: hook must be an object", event))
				}
				command, _ := hookObject["command"].(string)
				typeName, _ := hookObject["type"].(string)
				if typeName == "command" && ownedCommand(command, "claude", bin) {
					removed++
					continue
				}
				kept = append(kept, hook)
			}
			if len(kept) == 0 {
				continue
			}
			object["hooks"] = kept
			updated = append(updated, object)
		}
		if len(updated) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = updated
		}
	}
	if removed == 0 {
		fmt.Fprintln(stdout, "  status: already absent")
		return status
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooks
	}
	status.Changed = true
	fmt.Fprintf(stdout, "  hooks: %d sessiond command(s)\n", removed)
	if !apply {
		fmt.Fprintln(stdout, "  status: would remove (dry-run)")
		return status
	}
	if err := backup(real); err != nil {
		return skipTarget(stderr, "claude backup", status, err)
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return skipTarget(stderr, "claude marshal", status, err)
	}
	if err := atomicWrite(real, out, mode); err != nil {
		return skipTarget(stderr, "claude write", status, err)
	}
	fmt.Fprintln(stdout, "  status: removed")
	status.Written = true
	return status
}

func uninstallCodex(stdout, stderr io.Writer, path string, apply bool) TargetStatus {
	real := resolveSymlink(path)
	status := TargetStatus{Path: real}
	fmt.Fprintf(stdout, "codex   %s\n", path)
	printSymlinkNote(stdout, path, real)
	data, exists, mode, err := readFile(real)
	if err != nil {
		return skipTarget(stderr, "codex", status, err)
	}
	if !exists {
		fmt.Fprintln(stdout, "  status: not present")
		return status
	}
	location, err := locateCodexBlock(data)
	if err != nil {
		return skipTarget(stderr, "codex", status, err)
	}
	if !location.present {
		fmt.Fprintln(stdout, "  status: already absent")
		return status
	}
	if _, err := validateCodexBlock(location.block); err != nil {
		return skipTarget(stderr, "codex", status, err)
	}
	status.Changed = true
	if !apply {
		fmt.Fprintln(stdout, "  status: would remove marker block (dry-run)")
		return status
	}
	if err := backup(real); err != nil {
		return skipTarget(stderr, "codex backup", status, err)
	}
	out := removeCodexBlock(data, location)
	if err := atomicWrite(real, out, mode); err != nil {
		return skipTarget(stderr, "codex write", status, err)
	}
	fmt.Fprintln(stdout, "  status: removed")
	status.Written = true
	return status
}

func removeCodexBlock(data []byte, location codexBlockLocation) []byte {
	start := location.start
	end := location.end
	if start > 0 && data[start-1] == '\n' {
		start--
	}
	out := append([]byte(nil), data[:start]...)
	out = append(out, data[end:]...)
	return out
}
