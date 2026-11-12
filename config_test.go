package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigExpandsHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, `backlog_dir = "~/bl"
workspace_root = "~"
[projects.Toy]
repo = "~/toy"
[agents.Claude]
cmd = ["claude", "{prompt}"]
`, 0o600)
	c, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.BacklogDir != filepath.Join(home, "bl") || c.WorkspaceRoot != home || c.RunTimeoutMinutes != 60 {
		t.Fatalf("%+v", c)
	}
	if c.Projects["toy"].Repo != filepath.Join(home, "toy") || len(c.Agents["claude"].Cmd) != 2 {
		t.Fatalf("%+v", c)
	}
}

func TestLoadConfigRejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, body := range []string{"", `backlog_dir = "x"
workspace_root = "y"
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
