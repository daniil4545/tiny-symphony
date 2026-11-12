package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// argv puts the prompt into every {prompt} of the command; no shell is involved.
func argv(cmd []string, prompt string) []string {
	out := make([]string, len(cmd))
	for i, a := range cmd {
		out[i] = strings.ReplaceAll(a, "{prompt}", prompt)
	}
	return out
}

// errRunTimeout tells a run killed for its time from an agent that crashed.
var errRunTimeout = errors.New("run timeout")

// runAgent runs the agent in dir, in its own process group, output to a log file; returns the log path.
// Cancelling ctx sends SIGTERM to the group, grace later SIGKILL.
func runAgent(ctx context.Context, root, dir string, a Agent, prompt string, timeout, grace time.Duration) (string, error) {
	if !inside(root, dir) {
		return "", fmt.Errorf("refusing to start an agent in %s: outside %s", dir, root)
	}
	logPath := filepath.Join(dir, stateDir, time.Now().UTC().Format("20060102T150405Z")+".log")
	logFile, err := os.Create(logPath) //nolint:gosec // the log lives in the worktree state folder
	if err != nil {
		return "", fmt.Errorf("create log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errRunTimeout)
	defer cancel()
	args := argv(a.Cmd, prompt)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // the argv comes from the config, by design
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = grace
	if err = cmd.Start(); err != nil {
		return logPath, fmt.Errorf("start agent: %w", err)
	}
	pidFile := filepath.Join(dir, stateDir, "pid")
	if err = os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"+procStart(ctx, cmd.Process.Pid)+"\n"), 0o600); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return logPath, fmt.Errorf("write pid file: %w", err)
	}
	err = cmd.Wait()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = os.Remove(pidFile)
	if err != nil {
		if errors.Is(context.Cause(ctx), errRunTimeout) {
			return logPath, fmt.Errorf("agent killed after %s: %w", timeout, errRunTimeout)
		}
		return logPath, fmt.Errorf("agent exited: %w", err)
	}
	return logPath, nil
}
