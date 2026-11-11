package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const defaultConfigPath = "~/.config/tiny-symphony/config.toml"

type Config struct {
	BacklogDir        string             `toml:"backlog_dir"`
	WorkspaceRoot     string             `toml:"workspace_root"`
	PollSeconds       int                `toml:"poll_seconds"`
	MaxAgents         int                `toml:"max_agents"`
	RunTimeoutMinutes int                `toml:"run_timeout_minutes"`
	Projects          map[string]Project `toml:"projects"`
	Agents            map[string]Agent   `toml:"agents"`
}

type Project struct {
	Repo string `toml:"repo"`
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
	if c.BacklogDir == "" || c.WorkspaceRoot == "" {
		return nil, errors.New("config: backlog_dir and workspace_root are required")
	}
	c.BacklogDir = expandHome(c.BacklogDir)
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
	projects := map[string]Project{}
	for name, p := range c.Projects {
		p.Repo = expandHome(p.Repo)
		projects[norm(name)] = p
	}
	c.Projects = projects
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
