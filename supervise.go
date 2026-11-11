package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// checkWork is the "work not finished" row of Supervision: a Review result needs a clean tree,
// a pushed branch, an open PR or MR and a passing verify command.
func checkWork(ctx context.Context, dir, branch string, p Project) error {
	if st, err := git(ctx, dir, "status", "--porcelain"); err != nil || st != "" {
		return errors.Join(err, errors.New("worktree has uncommitted changes"))
	}
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	remote, err := git(ctx, dir, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(remote, head) {
		return fmt.Errorf("branch %s is not pushed (HEAD %.8s)", branch, head)
	}
	tool, kind := "gh", "pr"
	url, _ := git(ctx, dir, "remote", "get-url", "origin")
	if norm(p.Forge) == "gitlab" || norm(p.Forge) == "" && strings.Contains(url, "gitlab") {
		tool, kind = "glab", "mr"
	}
	if out, err := runIn(ctx, dir, tool, kind, "view", branch); err != nil {
		return fmt.Errorf("no PR or MR for %s: %w: %s", branch, err, tail(out))
	}
	if p.Verify != "" {
		if out, err := runIn(ctx, dir, "sh", "-c", p.Verify); err != nil {
			return fmt.Errorf("verify failed: %w: %s", err, tail(out))
		}
	}
	return nil
}

func runIn(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the verify command and forge tools come from the config
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	return s[max(0, len(s)-300):]
}

// reapLeftovers kills the process groups that a crashed daemon left behind (pid files in worktrees).
func reapLeftovers(root string, log *slog.Logger) {
	files, _ := filepath.Glob(filepath.Join(root, "*", stateDir, "pid"))
	for _, f := range files {
		data, _ := os.ReadFile(f) //nolint:gosec // glob under workspace_root
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if pg, e := syscall.Getpgid(pid); err == nil && pid > 1 && e == nil && pg == pid {
			log.Warn("killing a leftover agent group", "pid", pid, "file", f)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = os.Remove(f)
	}
}

// housekeeping removes the worktrees of Done tasks (and their branches if merged) and old run logs.
func (s *sched) housekeeping(ctx context.Context) {
	done, err := s.bl.ListDone(ctx)
	if err != nil {
		s.log.Warn("cleanup: tracker read failed", "err", err)
	}
	root := s.cfg.WorkspaceRoot
	for _, t := range done {
		p, known := s.cfg.Projects[norm(t.Project)]
		branch := branchName(t)
		dir := filepath.Join(root, branch)
		if _, e := os.Stat(dir); !known || e != nil || !inside(root, dir) {
			continue
		}
		if _, e := git(ctx, p.Repo, "worktree", "remove", dir); e != nil {
			s.log.Warn("cleanup: worktree kept", "task", t.ID, "err", e)
			continue
		}
		if _, e := git(ctx, p.Repo, "branch", "-d", branch); e != nil {
			s.log.Info("cleanup: branch kept, not merged", "task", t.ID, "branch", branch)
		}
	}
	logs, _ := filepath.Glob(filepath.Join(root, "*", stateDir, "*.log"))
	cutoff := time.Now().AddDate(0, 0, -s.cfg.LogDays)
	for _, f := range logs {
		if fi, e := os.Stat(f); e == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(f)
		}
	}
}

// tries is what the scheduler remembers about a task's failed attempts; a restart forgets it.
type tries struct {
	n            int
	last, status string
	next         time.Time
}

// failure records a failed attempt and returns true when the task has used up max_attempts.
func (s *sched) failure(id, status string, err error) bool {
	tr := s.tries[id]
	if tr == nil {
		tr = &tries{status: status}
		s.tries[id] = tr
	}
	tr.n++
	tr.last = err.Error()
	tr.next = time.Now().Add(s.backoff[min(tr.n, len(s.backoff))-1])
	return tr.n >= s.cfg.MaxAttempts
}

// block gives up on a task: the reason and the log path go into a comment, the status becomes Blocked.
func (s *sched) block(ctx context.Context, id string) {
	tr := s.tries[id]
	delete(s.tries, id)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	msg := fmt.Sprintf("tiny-symphony gave up after %d failed attempts. Last error: %s", tr.n, tr.last)
	if err := s.bl.Apply(ctx, id, Result{Comment: msg}, "Blocked"); err != nil {
		s.log.Error("could not block the task", "task", id, "err", err)
		return
	}
	s.log.Warn("task blocked", "task", id, "outcome", "blocked", "attempts", tr.n)
}

// attempt is the number of the next run of the task and the last error; wait is true inside the pause.
func (s *sched) attempt(t Task) (n int, lastErr string, wait bool) {
	tr := s.tries[t.ID]
	if tr != nil && norm(tr.status) != norm(t.Status) {
		delete(s.tries, t.ID) // a human moved the card: a fresh start
		tr = nil
	}
	if tr == nil {
		return 1, "", false
	}
	return tr.n + 1, tr.last, time.Now().Before(tr.next)
}
