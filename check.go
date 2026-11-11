package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// checkConfig is `tiny-symphony check`: one line per check, nothing is run, written or created.
// It returns the number of failed checks.
func checkConfig(ctx context.Context, w io.Writer, cfg *Config, configDir string, bl *Backlog) int {
	failed := 0
	report := func(name string, err error) {
		if err != nil {
			failed++
			_, _ = fmt.Fprintf(w, "FAIL %s: %v\n", name, err)
			return
		}
		_, _ = fmt.Fprintf(w, "ok   %s\n", name)
	}
	_, err := os.Stat(cfg.BacklogDir)
	if err == nil {
		_, err = bl.List(ctx)
	}
	report("tracker "+cfg.BacklogDir, err)
	report("workspace_root "+cfg.WorkspaceRoot, checkRoot(cfg.WorkspaceRoot))
	for _, name := range sortedKeys(cfg.Agents) {
		cmd := cfg.Agents[name].Cmd
		_, err = exec.LookPath(cmd[0])
		if err == nil && !slices.ContainsFunc(cmd, func(a string) bool { return strings.Contains(a, "{prompt}") }) {
			err = errors.New("cmd has no {prompt}")
		}
		report("agent "+name, err)
	}
	for _, name := range sortedKeys(cfg.Projects) {
		report("project "+name, checkProject(cfg.Projects[name]))
	}
	for _, status := range []string{"To Do", "In Progress", "Rework"} {
		file := promptFile(configDir, status)
		_, err = renderPrompt(file, Task{}, "", 1, "")
		report("prompt "+file, err)
	}
	return failed
}

func checkRoot(root string) error {
	for p := root; ; p = filepath.Dir(p) {
		st, err := os.Stat(p)
		if err == nil {
			if !st.IsDir() {
				return fmt.Errorf("%s is not a directory", p)
			}
			return nil
		}
		if !os.IsNotExist(err) || filepath.Dir(p) == p {
			return fmt.Errorf("workspace_root: %w", err)
		}
	}
}

func checkProject(p Project) error {
	if _, err := os.Stat(filepath.Join(p.Repo, ".git")); err != nil {
		return fmt.Errorf("%s is not a git repository", p.Repo)
	}
	tools := map[string][]string{"github": {"gh"}, "gitlab": {"glab"}}[norm(p.Forge)]
	if tools == nil {
		tools = []string{"gh", "glab"}
	}
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err == nil {
			return nil
		}
	}
	return fmt.Errorf("none of %s in PATH", strings.Join(tools, ", "))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
