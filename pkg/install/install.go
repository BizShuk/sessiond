// Package install manages the sessiond lifecycle hook entries in the user's
// Claude and Codex config files. Changes are dry-run by default, scoped to
// sessiond-owned entries, and backed up before writes.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	claudeEvents = []string{"Stop", "StopFailure", "TaskCompleted"}
	codexEvents  = []string{"Stop", "SubagentStop"}
)

const (
	codexMarkerBegin = "# >>> superset sessiond hooks >>>"
	codexMarkerEnd   = "# <<< superset sessiond hooks <<<"
)

// Options is the dry-run/apply toggle plus optional I/O overrides.
type Options struct {
	Apply       bool
	Stdout      io.Writer // default: os.Stdout
	Stderr      io.Writer // default: os.Stderr
	Binary      string    // default: this process's os.Executable()
	WorkingDir  string    // default: os.Getwd()
	ProjectRoot string    // tests may bypass Git root discovery
}

// TargetStatus captures what happened to one config file.
type TargetStatus struct {
	Path       string
	Configured bool
	Changed    bool
	Written    bool
	Skipped    string
}

// Run installs the hooks. Errors for one target do not abort the other target.
func Run(opts Options) error {
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
	installClaude(stdout, stderr, filepath.Join(root, ".claude", "settings.json"), bin, opts.Apply)
	installCodex(stdout, stderr, filepath.Join(root, ".codex", "config.toml"), bin, opts.Apply)
	if !opts.Apply {
		fmt.Fprintln(stdout, "\n(dry-run) re-run with --apply to write these changes.")
	}
	return nil
}

func installClaude(stdout, stderr io.Writer, path, bin string, apply bool) TargetStatus {
	real := resolveSymlink(path)
	cmd := bin + " hook claude"
	status := TargetStatus{Path: real}
	root, exists, mode, err := readClaudeRoot(real)
	if err != nil {
		return skipTarget(stderr, "claude", status, err)
	}
	if !exists {
		root = map[string]any{}
	}

	hooks, err := claudeHooks(root, true)
	if err != nil {
		return skipTarget(stderr, "claude", status, err)
	}
	changed := false
	for _, event := range claudeEvents {
		has, err := hookHasCommand(hooks[event], cmd)
		if err != nil {
			return skipTarget(stderr, "claude", status, fmt.Errorf("invalid hooks.%s: %w", event, err))
		}
		if has {
			continue
		}
		entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": cmd}}}
		hooks[event] = append(asSlice(hooks[event]), entry)
		changed = true
	}
	root["hooks"] = hooks
	status.Changed = changed

	fmt.Fprintf(stdout, "claude  %s\n  events: %s\n  command: %s\n", path, strings.Join(claudeEvents, ", "), cmd)
	printSymlinkNote(stdout, path, real)
	if !changed {
		fmt.Fprintln(stdout, "  status: already registered")
		status.Configured = true
		return status
	}
	if !apply {
		fmt.Fprintln(stdout, "  status: would add (dry-run)")
		return status
	}
	if exists {
		if err := backup(real); err != nil {
			return skipTarget(stderr, "claude backup", status, err)
		}
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return skipTarget(stderr, "claude marshal", status, err)
	}
	if err := atomicWrite(real, out, mode); err != nil {
		return skipTarget(stderr, "claude write", status, err)
	}
	fmt.Fprintln(stdout, "  status: written")
	status.Written = true
	status.Configured = true
	return status
}

func installCodex(stdout, stderr io.Writer, path, bin string, apply bool) TargetStatus {
	real := resolveSymlink(path)
	cmd := bin + " hook codex"
	status := TargetStatus{Path: real}
	existing, exists, mode, err := readFile(real)
	if err != nil {
		return skipTarget(stderr, "codex", status, err)
	}
	marker, err := locateCodexBlock(existing)
	if err != nil {
		return skipTarget(stderr, "codex", status, err)
	}
	if marker.present {
		if _, err := validateCodexBlock(marker.block); err != nil {
			return skipTarget(stderr, "codex", status, err)
		}
		fmt.Fprintf(stdout, "codex   %s\n  status: already registered\n", real)
		status.Configured = true
		return status
	}

	block := codexHookBlock(cmd)
	fmt.Fprintf(stdout, "codex   %s\n  events: %s\n  command: %s\n", path, strings.Join(codexEvents, ", "), cmd)
	printSymlinkNote(stdout, path, real)
	status.Changed = true
	if !apply {
		fmt.Fprintln(stdout, "  status: would append block (dry-run):")
		fmt.Fprintln(stdout, indent(block, "    "))
		return status
	}
	if exists {
		if err := backup(real); err != nil {
			return skipTarget(stderr, "codex backup", status, err)
		}
	}
	out := append(append([]byte(nil), existing...), '\n')
	out = append(out, block...)
	if err := atomicWrite(real, out, mode); err != nil {
		return skipTarget(stderr, "codex write", status, err)
	}
	fmt.Fprintln(stdout, "  status: appended")
	status.Written = true
	status.Configured = true
	return status
}

func readClaudeRoot(path string) (map[string]any, bool, fs.FileMode, error) {
	data, exists, mode, err := readFile(path)
	if err != nil || !exists {
		return nil, exists, mode, err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, true, mode, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		return nil, true, mode, fmt.Errorf("parse %s: root must be an object", path)
	}
	return root, true, mode, nil
}

func claudeHooks(root map[string]any, create bool) (map[string]any, error) {
	value, exists := root["hooks"]
	if !exists {
		if create {
			return map[string]any{}, nil
		}
		return nil, nil
	}
	hooks, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("hooks must be an object")
	}
	return hooks, nil
}

func hookHasCommand(entries any, cmd string) (bool, error) {
	if entries == nil {
		return false, nil
	}
	items, ok := entries.([]any)
	if !ok {
		return false, errors.New("event must be an array")
	}
	for _, entry := range items {
		object, ok := entry.(map[string]any)
		if !ok {
			return false, errors.New("event entry must be an object")
		}
		nested, ok := object["hooks"].([]any)
		if !ok {
			return false, errors.New("entry hooks must be an array")
		}
		for _, hook := range nested {
			object, ok := hook.(map[string]any)
			if !ok {
				return false, errors.New("hook must be an object")
			}
			if object["command"] == cmd {
				return true, nil
			}
		}
	}
	return false, nil
}

func asSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func codexHookBlock(cmd string) string {
	var builder strings.Builder
	builder.WriteString(codexMarkerBegin + "\n")
	for _, event := range codexEvents {
		fmt.Fprintf(&builder, "[[hooks.%s]]\n", event)
		fmt.Fprintf(&builder, "[[hooks.%s.hooks]]\n", event)
		builder.WriteString("type = \"command\"\n")
		fmt.Fprintf(&builder, "command = %q\n", cmd)
	}
	builder.WriteString(codexMarkerEnd + "\n")
	return builder.String()
}

type codexBlockLocation struct {
	present bool
	start   int
	end     int
	block   []byte
}

func locateCodexBlock(data []byte) (codexBlockLocation, error) {
	text := string(data)
	begins := strings.Count(text, codexMarkerBegin)
	ends := strings.Count(text, codexMarkerEnd)
	if begins == 0 && ends == 0 {
		return codexBlockLocation{}, nil
	}
	if begins != 1 || ends != 1 {
		return codexBlockLocation{}, fmt.Errorf("ambiguous sessiond marker block: begin=%d end=%d", begins, ends)
	}
	start := strings.Index(text, codexMarkerBegin)
	endMarker := strings.Index(text, codexMarkerEnd)
	if start > endMarker {
		return codexBlockLocation{}, errors.New("invalid sessiond marker order")
	}
	if start > 0 && text[start-1] != '\n' {
		return codexBlockLocation{}, errors.New("sessiond begin marker must start a line")
	}
	end := endMarker + len(codexMarkerEnd)
	if end < len(text) && text[end] != '\n' {
		return codexBlockLocation{}, errors.New("sessiond end marker must occupy a full line")
	}
	if end < len(text) {
		end++
	}
	return codexBlockLocation{present: true, start: start, end: end, block: data[start:end]}, nil
}

func validateCodexBlock(block []byte) (string, error) {
	lines := strings.Split(strings.TrimSuffix(string(block), "\n"), "\n")
	if len(lines) != 10 || lines[0] != codexMarkerBegin || lines[9] != codexMarkerEnd {
		return "", errors.New("sessiond marker block has unexpected content")
	}
	commands := make([]string, 0, len(codexEvents))
	line := 1
	for _, event := range codexEvents {
		if lines[line] != "[[hooks."+event+"]]" ||
			lines[line+1] != "[[hooks."+event+".hooks]]" ||
			lines[line+2] != "type = \"command\"" ||
			!strings.HasPrefix(lines[line+3], "command = ") {
			return "", errors.New("sessiond marker block has unexpected content")
		}
		cmd, err := strconv.Unquote(strings.TrimPrefix(lines[line+3], "command = "))
		if err != nil {
			return "", fmt.Errorf("invalid sessiond command: %w", err)
		}
		commands = append(commands, cmd)
		line += 4
	}
	if commands[0] != commands[1] || !ownedCommand(commands[0], "codex", "") {
		return "", errors.New("sessiond marker block command is not owned by sessiond")
	}
	return commands[0], nil
}

func ownedCommand(command, agent, current string) bool {
	if current != "" && command == current+" hook "+agent {
		return true
	}
	suffix := " hook " + agent
	if !strings.HasSuffix(command, suffix) {
		return false
	}
	binary := strings.TrimSuffix(command, suffix)
	return filepath.IsAbs(binary) && filepath.Base(binary) == "sessiond"
}

func readFile(path string) ([]byte, bool, fs.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, 0o644, nil
	}
	if err != nil {
		return nil, false, 0, fmt.Errorf("read %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, true, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return data, true, info.Mode().Perm(), nil
}

func backup(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	stamp := time.Now().UnixNano()
	for attempt := 0; attempt < 100; attempt++ {
		candidate := fmt.Sprintf("%s.bak.%d", path, stamp)
		if attempt > 0 {
			candidate = fmt.Sprintf("%s.%d", candidate, attempt)
		}
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(candidate)
		}
		return err
	}
	return errors.New("could not allocate a unique backup path")
}

func atomicWrite(path string, data []byte, mode fs.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".sessiond-*")
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

func skipTarget(stderr io.Writer, label string, status TargetStatus, err error) TargetStatus {
	fmt.Fprintf(stderr, "%s: %v\n", label, err)
	status.Skipped = err.Error()
	return status
}

func printSymlinkNote(stdout io.Writer, path, real string) {
	if real != path {
		fmt.Fprintf(stdout, "  note: config is a symlink → %s (the target will be modified)\n", real)
	}
}

func resolveSymlink(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

func resolveBinary(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	binary, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(binary)
}

func resolveProjectRoot(override, workingDir string) (string, error) {
	if override != "" {
		return canonicalPath(override)
	}
	if workingDir == "" {
		var err error
		workingDir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	workingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return "", err
	}
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	command.Dir = workingDir
	if output, commandErr := command.Output(); commandErr == nil {
		root := strings.TrimSpace(string(output))
		if root != "" {
			return canonicalPath(root)
		}
	}
	return canonicalPath(workingDir)
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(absolute); err == nil {
		return real, nil
	}
	return filepath.Clean(absolute), nil
}

func resolveIO(opts Options) (io.Writer, io.Writer) {
	stdout, stderr := opts.Stdout, opts.Stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return stdout, stderr
}

func indent(value, padding string) string {
	lines := strings.Split(strings.TrimRight(value, "\n"), "\n")
	for index := range lines {
		lines[index] = padding + lines[index]
	}
	return strings.Join(lines, "\n")
}
