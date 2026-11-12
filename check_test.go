package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listStub is a Tracker whose List gives err; check calls nothing else.
type listStub struct {
	Tracker
	err error
}

func (l listStub) List(context.Context, []string) ([]TrackerTask, error) { return nil, l.err }
func (l listStub) Repos(context.Context) (map[string]string, error)      { return map[string]string{}, nil }

// checkEnv builds a config that passes every check: a stub agent in a private PATH.
func checkEnv(t *testing.T) (*Config, *listStub) {
	t.Helper()
	bin := t.TempDir()
	write(t, filepath.Join(bin, "stub"), "#!/bin/sh\n", 0o700)
	t.Setenv("PATH", bin)
	cfg := &Config{
		Tracker:       "tix",
		WorkspaceRoot: filepath.Join(t.TempDir(), "not", "yet"),
		Watch:         []string{"open"},
		HandoffStatus: "blocked", BlockedStatus: "blocked", ReopenStatus: "open",
		Agents: map[string]Agent{"stub": {Cmd: []string{"stub", "-p", "{prompt}"}}},
	}
	return cfg, &listStub{}
}

func runCheck(cfg *Config, tr Tracker) (int, string) {
	var out bytes.Buffer
	n := checkConfig(context.Background(), &out, cfg, tr)
	return n, out.String()
}

func TestCheckAllOK(t *testing.T) {
	cfg, tr := checkEnv(t)
	n, out := runCheck(cfg, tr)
	if n != 0 || strings.Contains(out, "FAIL") {
		t.Fatalf("want all ok, got %d:\n%s", n, out)
	}
	if _, err := os.Stat(cfg.WorkspaceRoot); err == nil {
		t.Fatal("check created workspace_root")
	}
}

func TestCheckFailures(t *testing.T) {
	cases := map[string]struct {
		break_ func(cfg *Config, tr *listStub)
		want   string
	}{
		"agent not in PATH": {func(c *Config, _ *listStub) {
			c.Agents["stub"] = Agent{Cmd: []string{"no-such-agent", "{prompt}"}}
		}, "FAIL agent stub"},
		"no {prompt}": {func(c *Config, _ *listStub) {
			c.Agents["stub"] = Agent{Cmd: []string{"stub", "-p"}}
		}, "cmd has no {prompt}"},
		"no agents": {func(c *Config, _ *listStub) {
			c.Agents = map[string]Agent{}
		}, "FAIL agents: none configured"},
		"tracker silent": {func(_ *Config, tr *listStub) { tr.err = errors.New("boom") }, "FAIL tracker tix"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, tr := checkEnv(t)
			tc.break_(cfg, tr)
			n, out := runCheck(cfg, tr)
			if n != 1 || !strings.Contains(out, tc.want) {
				t.Fatalf("want 1 failure with %q, got %d:\n%s", tc.want, n, out)
			}
		})
	}
}

func TestCheckRootNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		return // root can write anywhere, nothing to check
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	cfg, tr := checkEnv(t)
	cfg.WorkspaceRoot = filepath.Join(dir, "work")
	n, out := runCheck(cfg, tr)
	if n != 1 || !strings.Contains(out, "is not writable") {
		t.Fatalf("want 1 failure, got %d:\n%s", n, out)
	}
}

// spec: S4
// Acceptance 10: each rule fails check and stops run.
func TestCheckRules(t *testing.T) {
	cases := map[string]struct {
		break_ func(cfg *Config)
		want   string
	}{
		"watch empty":           {func(c *Config) { c.Watch = nil }, "watch is empty"},
		"watch unknown status":  {func(c *Config) { c.Watch = []string{"open", "todo"} }, `"todo" is not a tix status`},
		"watch has handoff":     {func(c *Config) { c.Watch = []string{"open", "blocked"}; c.HandoffStatus = "blocked" }, "watch contains"},
		"watch has blocked":     {func(c *Config) { c.Watch = []string{"open", "idea"}; c.BlockedStatus = "idea" }, "watch contains"},
		"watch has in_progress": {func(c *Config) { c.Watch = []string{"open", "in_progress"} }, "watch contains"},
		"reopen not in watch":   {func(c *Config) { c.ReopenStatus = "idea" }, "reopen_status"},
		"root in git repo": {func(c *Config) {
			repo := t.TempDir()
			write(t, filepath.Join(repo, ".git"), "", 0o600)
			c.WorkspaceRoot = filepath.Join(repo, "work")
		}, "inside the git repository"},
		"root in project": {func(c *Config) {
			c.Repos = map[string]string{"p": filepath.Dir(c.WorkspaceRoot)}
		}, "inside the project path"},
		"space in path": {func(c *Config) { c.Repos = map[string]string{"p": "/a b/c"} }, "has a space"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, tr := checkEnv(t)
			cfg.HandoffStatus, cfg.BlockedStatus, cfg.ReopenStatus = "blocked", "closed", "open"
			tc.break_(cfg)
			if n, out := runCheck(cfg, tr); n < 1 || !strings.Contains(out, tc.want) {
				t.Fatalf("check: want %q, got %d:\n%s", tc.want, n, out)
			}
			if err := startProblems(t.Context(), cfg, tr); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run must refuse with %q, got %v", tc.want, err)
			}
		})
	}
}

// check prints the merged config of every project and fails on a bad project file.
func TestCheckPrintsProjects(t *testing.T) {
	cfg, tr := checkEnv(t)
	cfg.HandoffStatus, cfg.BlockedStatus, cfg.ReopenStatus = "blocked", "closed", "open"
	cfg.MaxAttempts, cfg.RunTimeoutMinutes, cfg.PromptAppend = 3, 60, "central"
	good, bad := t.TempDir(), t.TempDir()
	write(t, filepath.Join(good, ".tiny-symphony.toml"), "prompt_append = \"mine\"\nmax_attempts = 2\n", 0o600)
	write(t, filepath.Join(bad, ".tiny-symphony.toml"), "max_attempts = 10\n", 0o600)
	cfg.Repos = map[string]string{"good": good, "bad": bad, "plain": t.TempDir()}
	n, out := runCheck(cfg, tr)
	t.Log("\n" + out)
	if n != 1 || !strings.Contains(out, `max_attempts=2 run_timeout_minutes=60 prompt_append="central\nmine"`) ||
		!strings.Contains(out, "FAIL project bad") || !strings.Contains(out, "ok   project plain") {
		t.Fatalf("got %d:\n%s", n, out)
	}
}
