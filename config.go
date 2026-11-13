package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

const defaultConfigPath = "~/.config/tiny-symphony/config.toml"

type Config struct {
	Tracker           string            `toml:"tracker"`
	WorkspaceRoot     string            `toml:"workspace_root"`
	Watch             []string          `toml:"watch"`
	HandoffStatus     string            `toml:"handoff_status"`
	BlockedStatus     string            `toml:"blocked_status"`
	ReopenStatus      string            `toml:"reopen_status"`
	PromptAppend      string            `toml:"prompt_append"`
	PollSeconds       int               `toml:"poll_seconds"`
	MaxAgents         int               `toml:"max_agents"`
	RunTimeoutMinutes int               `toml:"run_timeout_minutes"`
	MaxAttempts       int               `toml:"max_attempts"`
	LogDays           int               `toml:"log_days"` // the old place; [cleanup] log_days wins
	Repos             map[string]string `toml:"repos"`
	Agents            map[string]Agent  `toml:"agents"`
	WatchPR           WatchPR           `toml:"watch_pr"`
	Cleanup           Cleanup           `toml:"cleanup"`
}

// WatchPR and Cleanup carry the settings of the watch.go and cleanup.go modules.
type WatchPR struct {
	Enabled     bool `toml:"enabled"`
	PollMinutes int  `toml:"poll_minutes"`
	MaxReopens  int  `toml:"max_reopens"`
}

type Cleanup struct {
	Enabled bool `toml:"enabled"`
	LogDays int  `toml:"log_days"`
}

type Agent struct {
	Cmd []string `toml:"cmd"`
}

// norm is how statuses, labels and project names are compared: trimmed and lowercased.
func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func expandHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	}
	return p
}

func loadConfig(path string) (*Config, error) {
	c := Config{Cleanup: Cleanup{Enabled: true}}
	md, err := toml.DecodeFile(expandHome(path), &c)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if c.WorkspaceRoot == "" {
		return nil, errors.New("config: workspace_root is required")
	}
	if c.Tracker == "" {
		c.Tracker = "tix"
	}
	if c.Tracker != "tix" {
		return nil, fmt.Errorf("config: tracker %q is not supported, only tix", c.Tracker)
	}
	if !md.IsDefined("watch") {
		c.Watch = []string{"open"}
	}
	c.HandoffStatus = cmp.Or(norm(c.HandoffStatus), "blocked")
	c.BlockedStatus = cmp.Or(norm(c.BlockedStatus), "blocked")
	c.ReopenStatus = cmp.Or(norm(c.ReopenStatus), "open")
	for i, w := range c.Watch {
		c.Watch[i] = norm(w)
	}
	c.WorkspaceRoot = expandHome(c.WorkspaceRoot)
	if c.PollSeconds <= 0 {
		c.PollSeconds = 30
	}
	if c.MaxAgents <= 0 {
		c.MaxAgents = 2
	}
	if c.RunTimeoutMinutes <= 0 {
		c.RunTimeoutMinutes = 60
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	c.LogDays = cmp.Or(max(c.Cleanup.LogDays, 0), max(c.LogDays, 0), 14)
	c.Cleanup.LogDays = c.LogDays
	if c.WatchPR.PollMinutes <= 0 {
		c.WatchPR.PollMinutes = 5
	}
	if c.WatchPR.MaxReopens <= 0 {
		c.WatchPR.MaxReopens = 2
	}
	repos := map[string]string{}
	for name, p := range c.Repos {
		repos[norm(name)] = expandHome(p)
	}
	c.Repos = repos
	agents := map[string]Agent{}
	for name, a := range c.Agents {
		if len(a.Cmd) == 0 {
			return nil, fmt.Errorf("config: agent %q has an empty cmd", name)
		}
		a.Cmd = slices.Clone(a.Cmd)
		a.Cmd[0] = expandHome(a.Cmd[0])
		agents[norm(name)] = a
	}
	c.Agents = agents
	return &c, nil
}

// projectFile is the only thing a repository may say about how it is run.
type projectFile struct {
	PromptAppend      string `toml:"prompt_append"`
	MaxAttempts       int    `toml:"max_attempts"`
	RunTimeoutMinutes int    `toml:"run_timeout_minutes"`
}

// withProjectLayer returns base with the .tiny-symphony.toml of the repository applied.
// No file is no change. Any other key, a parse error or a limit above the central one is an error.
func withProjectLayer(base *Config, repo string) (*Config, error) {
	path := filepath.Join(repo, ".tiny-symphony.toml")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return base, nil
	}
	var p projectFile
	md, err := toml.DecodeFile(path, &p)
	if err != nil {
		return nil, fmt.Errorf("project config %s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("project config %s: key %s is not allowed, only prompt_append, max_attempts, run_timeout_minutes", path, u[0])
	}
	c := *base
	for _, l := range []struct {
		key      string
		val, cap int
		dst      *int
	}{
		{"max_attempts", p.MaxAttempts, base.MaxAttempts, &c.MaxAttempts},
		{"run_timeout_minutes", p.RunTimeoutMinutes, base.RunTimeoutMinutes, &c.RunTimeoutMinutes},
	} {
		if !md.IsDefined(l.key) {
			continue
		}
		if l.val <= 0 || l.val > l.cap {
			return nil, fmt.Errorf("project config %s: %s = %d must be from 1 to the central %d", path, l.key, l.val, l.cap)
		}
		*l.dst = l.val
	}
	c.PromptAppend = strings.TrimSpace(base.PromptAppend + "\n" + p.PromptAppend)
	return &c, nil
}
