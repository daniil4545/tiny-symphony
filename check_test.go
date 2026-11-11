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

// checkEnv builds a config that passes every check: stub agent and gh in a private PATH, a fake repo.
func checkEnv(t *testing.T) (*Config, string, *Backlog) {
	t.Helper()
	bin, cfgDir, repo := t.TempDir(), t.TempDir(), t.TempDir()
	for _, name := range []string{"stub", "gh"} {
		write(t, filepath.Join(bin, name), "#!/bin/sh\n", 0o700)
	}
	t.Setenv("PATH", bin)
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"to-do", "in-progress", "rework"} {
		write(t, filepath.Join(cfgDir, "prompts", f+".md"), "{{.id}} {{.title}}", 0o600)
	}
	cfg := &Config{
		BacklogDir:    t.TempDir(),
		WorkspaceRoot: filepath.Join(t.TempDir(), "not", "yet"),
		Projects:      map[string]Project{"toy": {Repo: repo}},
		Agents:        map[string]Agent{"stub": {Cmd: []string{"stub", "-p", "{prompt}"}}},
	}
	bl := &Backlog{run: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"tasks":[]}`), nil
	}}
	return cfg, cfgDir, bl
}

func runCheck(cfg *Config, dir string, bl *Backlog) (int, string) {
	var out bytes.Buffer
	n := checkConfig(context.Background(), &out, cfg, dir, bl)
	return n, out.String()
}

func TestCheckAllOK(t *testing.T) {
	cfg, dir, bl := checkEnv(t)
	n, out := runCheck(cfg, dir, bl)
	if n != 0 || strings.Contains(out, "FAIL") {
		t.Fatalf("want all ok, got %d:\n%s", n, out)
	}
	if _, err := os.Stat(cfg.WorkspaceRoot); err == nil {
		t.Fatal("check created workspace_root")
	}
}

func TestCheckFailures(t *testing.T) {
	cases := map[string]struct {
		break_ func(cfg *Config, dir string, bl *Backlog)
		want   string
	}{
		"agent not in PATH": {func(c *Config, _ string, _ *Backlog) {
			c.Agents["stub"] = Agent{Cmd: []string{"no-such-agent", "{prompt}"}}
		}, "FAIL agent stub"},
		"no {prompt}": {func(c *Config, _ string, _ *Backlog) {
			c.Agents["stub"] = Agent{Cmd: []string{"stub", "-p"}}
		}, "cmd has no {prompt}"},
		"repo is not git": {func(c *Config, _ string, _ *Backlog) {
			c.Projects["toy"] = Project{Repo: t.TempDir()}
		}, "is not a git repository"},
		"no prompt": {func(_ *Config, d string, _ *Backlog) {
			_ = os.Remove(filepath.Join(d, "prompts", "rework.md"))
		}, "FAIL prompt"},
		"tracker silent": {func(_ *Config, _ string, b *Backlog) {
			b.run = func(context.Context, ...string) ([]byte, error) { return nil, errors.New("boom") }
		}, "FAIL tracker"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, dir, bl := checkEnv(t)
			tc.break_(cfg, dir, bl)
			n, out := runCheck(cfg, dir, bl)
			if n != 1 || !strings.Contains(out, tc.want) {
				t.Fatalf("want 1 failure with %q, got %d:\n%s", tc.want, n, out)
			}
		})
	}
}
