package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const defaultConfigPath = "~/.config/tiny-symphony/config.toml"

type Config struct {
	Tracker           string           `toml:"tracker"`
	WorkspaceRoot     string           `toml:"workspace_root"`
	Watch             []string         `toml:"watch"`
	HandoffStatus     string           `toml:"handoff_status"`
	BlockedStatus     string           `toml:"blocked_status"`
	ReopenStatus      string           `toml:"reopen_status"`
	PromptAppend      string           `toml:"prompt_append"`
	PollSeconds       int              `toml:"poll_seconds"`
	MaxAgents         int              `toml:"max_agents"`
	RunTimeoutMinutes int              `toml:"run_timeout_minutes"`
	MaxAttempts       int              `toml:"max_attempts"`
	LogDays           int              `toml:"log_days"`
	Agents            map[string]Agent `toml:"agents"`
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
	var c Config
	if _, err := toml.DecodeFile(expandHome(path), &c); err != nil {
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
	if len(c.Watch) == 0 {
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
	if c.LogDays <= 0 {
		c.LogDays = 14
	}
	agents := map[string]Agent{}
	for name, a := range c.Agents {
		if len(a.Cmd) == 0 {
			return nil, fmt.Errorf("config: agent %q has an empty cmd", name)
		}
		agents[norm(name)] = a
	}
	c.Agents = agents
	return &c, nil
}
