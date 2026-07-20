package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootIncludesUninstall(t *testing.T) {
	root := NewRootCmd()
	command, _, err := root.Find([]string{"uninstall"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Name() != "uninstall" {
		t.Fatalf("got command %q", command.Name())
	}
	if root.Version != "0.2.0" {
		t.Fatalf("version = %q, want 0.2.0", root.Version)
	}
	if command.Flags().Lookup("apply") == nil {
		t.Fatal("uninstall missing --apply")
	}
}

func TestInstallCommandsDescribeProjectTargets(t *testing.T) {
	for _, command := range []*cobra.Command{newInstallCmd(), newUninstallCmd()} {
		if !strings.Contains(command.Long, "<project>/.claude/settings.json") ||
			!strings.Contains(command.Long, "<project>/.codex/config.toml") {
			t.Errorf("%s help omits project targets: %s", command.Name(), command.Long)
		}
		if strings.Contains(command.Long, "~/.claude") || strings.Contains(command.Long, "~/.codex") {
			t.Errorf("%s help still mentions user targets: %s", command.Name(), command.Long)
		}
	}
}

func TestUninstallRejectsArguments(t *testing.T) {
	command := newUninstallCmd()
	if err := command.Args(command, []string{"extra"}); err == nil {
		t.Fatal("uninstall accepted positional argument")
	}
}

func TestRootIncludesStop(t *testing.T) {
	root := NewRootCmd()
	command, _, err := root.Find([]string{"stop"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Name() != "stop" {
		t.Fatalf("got command %q", command.Name())
	}
	for _, name := range []string{"workspace", "session", "agent", "dry-run"} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("stop missing --%s", name)
		}
	}
}

func TestStopRejectsArguments(t *testing.T) {
	command := newStopCmd()
	if err := command.Args(command, []string{"extra"}); err == nil {
		t.Fatal("stop accepted positional argument")
	}
}
