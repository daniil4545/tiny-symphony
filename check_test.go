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
		Agents:        map[string]Agent{"stub": {Cmd: []string{"stub", "-p", "{prompt}"}}},
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
