package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// checkConfig is `tiny-symphony check`: one line per check, nothing is run, written or created.
// It returns the number of failed checks.
func checkConfig(ctx context.Context, w io.Writer, cfg *Config, tr Tracker) int {
	failed := 0
	report := func(name string, err error) {
		if err != nil {
			failed++
			_, _ = fmt.Fprintf(w, "FAIL %s: %v\n", name, err)
			return
		}
		_, _ = fmt.Fprintf(w, "ok   %s\n", name)
	}
	for _, err := range configRules(cfg) {
		report("config", err)
	}
	if len(cfg.Agents) == 0 {
		report("agents", errors.New("none configured"))
	}
	_, err := tr.List(ctx, cfg.Watch)
	report("tracker "+cfg.Tracker, err)
	report("workspace_root "+cfg.WorkspaceRoot, checkRoot(cfg.WorkspaceRoot))
	repos, err := projectRepos(ctx, cfg, tr)
	report("repos", err)
	for _, name := range slices.Sorted(maps.Keys(repos)) {
		report("repo "+name, checkRepo(cfg, repos[name]))
		eff, err := withProjectLayer(cfg, repos[name])
		report("project "+name+" "+repos[name], err)
		if err == nil {
			_, _ = fmt.Fprintf(w, "     max_attempts=%d run_timeout_minutes=%d prompt_append=%q\n",
				eff.MaxAttempts, eff.RunTimeoutMinutes, eff.PromptAppend)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Agents)) {
		cmd := cfg.Agents[name].Cmd
		_, err = exec.LookPath(cmd[0])
		if err == nil && !slices.ContainsFunc(cmd, func(a string) bool { return strings.Contains(a, "{prompt}") }) {
			err = errors.New("cmd has no {prompt}")
		}
		report("agent "+name, err)
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

// configRules are the settings that would make the daemon loop or never start work.
// check reports each one and run refuses to start on any.
func configRules(cfg *Config) []error {
	var errs []error
	if err := rootInGit(cfg.WorkspaceRoot); err != nil {
		errs = append(errs, err)
	}
	if len(cfg.Watch) == 0 {
		errs = append(errs, errors.New("watch is empty"))
	}
	for _, s := range cfg.Watch {
		if !slices.Contains(trackerStatuses, s) {
			errs = append(errs, fmt.Errorf("watch status %q is not a tix status (%s)", s, strings.Join(trackerStatuses, ", ")))
		}
		if slices.Contains([]string{cfg.HandoffStatus, cfg.BlockedStatus, "in_progress"}, s) {
			errs = append(errs, fmt.Errorf("watch contains %q: every handover or take would look like a failure and loop", s))
		}
	}
	if !slices.Contains(cfg.Watch, cfg.ReopenStatus) {
		errs = append(errs, fmt.Errorf("reopen_status %q is not in watch", cfg.ReopenStatus))
	}
	for _, st := range []string{cfg.HandoffStatus, cfg.BlockedStatus, cfg.ReopenStatus} {
		if !slices.Contains(trackerStatuses, st) {
			errs = append(errs, fmt.Errorf("status %q is not a tix status", st))
		}
	}
	return errs
}

var trackerStatuses = []string{"idea", "open", "in_progress", "blocked", "closed"}

// projectRepos is the repository of each project: the tracker's paths, then [repos] over them.
func projectRepos(ctx context.Context, cfg *Config, tr Tracker) (map[string]string, error) {
	repos, err := tr.Repos(ctx)
	if repos == nil {
		repos = map[string]string{}
	}
	maps.Copy(repos, cfg.Repos)
	if err != nil {
		return repos, fmt.Errorf("tracker projects: %w", err)
	}
	return repos, nil
}

func checkRepo(cfg *Config, repo string) error {
	if strings.ContainsAny(repo, " \t") {
		return fmt.Errorf("path %q has a space", repo)
	}
	if rel, err := filepath.Rel(repo, cfg.WorkspaceRoot); err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("workspace_root %s is inside the project path", cfg.WorkspaceRoot)
	}
	return nil
}

// rootInGit finds a git repository around workspace_root: a worktree there would be taken for part of it.
func rootInGit(root string) error {
	for p := root; ; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return fmt.Errorf("workspace_root %s is inside the git repository %s", root, p)
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

// startProblems is what makes `run` refuse to start: the config rules and the project paths.
// A tracker that does not answer is not one: the tick is skipped and tried again.
func startProblems(ctx context.Context, cfg *Config, tr Tracker) error {
	errs := configRules(cfg)
	repos, _ := projectRepos(ctx, cfg, tr)
	for _, name := range slices.Sorted(maps.Keys(repos)) {
		if err := checkRepo(cfg, repos[name]); err != nil {
			errs = append(errs, fmt.Errorf("repo %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}
