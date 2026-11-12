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
	if len(cfg.Agents) == 0 {
		report("agents", errors.New("none configured"))
	}
	_, err := tr.List(ctx, cfg.Watch)
	report("tracker "+cfg.Tracker, err)
	report("workspace_root "+cfg.WorkspaceRoot, checkRoot(cfg.WorkspaceRoot))
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
