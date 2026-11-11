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
	"syscall"
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
	if len(cfg.Agents) == 0 {
		report("agents", errors.New("none configured"))
	}
	if len(cfg.Projects) == 0 {
		report("projects", errors.New("none configured"))
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
		report("project "+name, checkProject(ctx, cfg.Projects[name]))
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
			if err = syscall.Access(p, 2); err != nil {
				return fmt.Errorf("%s is not writable: %w", p, err)
			}
			return nil
		}
		if !os.IsNotExist(err) || filepath.Dir(p) == p {
			return fmt.Errorf("workspace_root: %w", err)
		}
	}
}

func checkProject(ctx context.Context, p Project) error {
	if _, err := os.Stat(filepath.Join(p.Repo, ".git")); err != nil {
		return fmt.Errorf("%s is not a git repository", p.Repo)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git not in PATH")
	}
	url, _ := git(ctx, p.Repo, "remote", "get-url", "origin")
	tool, _ := forgeTool(p.Forge, url)
	if _, err := exec.LookPath(tool); err != nil {
		return fmt.Errorf("%s not in PATH", tool)
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
