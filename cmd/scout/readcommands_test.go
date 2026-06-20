package main

import (
	"strings"
	"testing"
)

// Every registered read command must carry a usage string and (except the
// specially-handled "table") a run function, so the dispatcher can execute it.
func TestReadCommands_WellFormed(t *testing.T) {
	for name, cmd := range readCommands {
		if cmd.usage == "" {
			t.Errorf("read command %q has empty usage", name)
		}
		if !strings.Contains(cmd.usage, name) {
			t.Errorf("read command %q usage %q should mention the command name", name, cmd.usage)
		}
		if name != "table" && cmd.run == nil {
			t.Errorf("read command %q has nil run func", name)
		}
	}
}

// runReadCommand must report false for names it does not own, so main()'s
// switch can fall through to the unknown-command error.
func TestRunReadCommand_UnknownReturnsFalse(t *testing.T) {
	if runReadCommand("definitely-not-a-command", []string{"definitely-not-a-command"}) {
		t.Error("expected runReadCommand to return false for an unregistered name")
	}
}

// The one-shot read tier must cover the diagnostic capabilities the audit
// called out as missing from the CLI.
func TestReadCommands_CoversExpectedSurface(t *testing.T) {
	want := []string{
		"accessibility", "readable", "auto-extract", "table",
		"aria", "vitals", "console", "dialog", "auth-wall",
		"cookies", "app-state",
	}
	for _, n := range want {
		if _, ok := readCommands[n]; !ok {
			t.Errorf("read command registry missing %q", n)
		}
	}
}
