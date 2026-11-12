package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadConfigDefaultsAndHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, `workspace_root = "~"
[agents.Claude]
cmd = ["claude", "{prompt}"]
`, 0o600)
	c, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkspaceRoot != home || c.RunTimeoutMinutes != 60 || c.Tracker != "tix" || !slices.Equal(c.Watch, []string{"open"}) ||
		c.HandoffStatus != "blocked" || c.BlockedStatus != "blocked" || c.ReopenStatus != "open" || len(c.Agents["claude"].Cmd) != 2 {
		t.Fatalf("%+v", c)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, body := range []string{"", `workspace_root = "y"
tracker = "jira"`, `workspace_root = "y"
[agents.a]
cmd = []`} {
		write(t, path, body, 0o600)
		if _, err := loadConfig(path); err == nil {
			t.Errorf("config %q must fail", body)
		}
	}
}

func TestExampleConfigLoads(t *testing.T) {
	if _, err := loadConfig("examples/config.toml"); err != nil {
		t.Fatal(err)
	}
}
