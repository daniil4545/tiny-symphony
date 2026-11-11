package main

import (
	"context"
	"encoding/json"
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
func checkWork(ctx context.Context, dir, branch string, p Project, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
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
	url, _ := git(ctx, dir, "remote", "get-url", "origin")
	tool, kind := forgeTool(p.Forge, url)
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

// forgeTool picks the forge CLI: gitlab by config or by the origin URL, gh otherwise.
func forgeTool(forge, originURL string) (tool, kind string) {
	if norm(forge) == "gitlab" || norm(forge) == "" && strings.Contains(originURL, "gitlab") {
		return "glab", "mr"
	}
	return "gh", "pr"
}

func runIn(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // the verify command and forge tools come from the config
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	return s[max(0, len(s)-300):]
}

// procStart is the start time of a process as ps prints it, "" if it is gone: a reused pid has another one.
func procStart(ctx context.Context, pid int) string {
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // fixed binary, numeric arg
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// reapLeftovers kills the process groups that a crashed daemon left behind. A pid file holds the pid and the
// start time of the process; if the process is not provably the same one, the file is only removed.
func reapLeftovers(root string, log *slog.Logger) {
	files, _ := filepath.Glob(filepath.Join(root, "*", stateDir, "pid"))
	for _, f := range files {
		data, _ := os.ReadFile(f) //nolint:gosec // glob under workspace_root
		pidText, started, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
		pid, err := strconv.Atoi(pidText)
		pg, e := syscall.Getpgid(pid)
		switch {
		case err != nil || pid <= 1 || pid == os.Getpid() || pid == os.Getppid() || e != nil || pg != pid:
		case started == "" || procStart(context.Background(), pid) != started:
			log.Warn("pid file does not match the live process, not killing", "pid", pid, "file", f)
		default:
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
		dir := findWorkspace(root, t.ID)
		if _, busy := s.running[t.ID]; busy || !known || dir == "" || !inside(root, dir) {
			continue
		}
		branch := filepath.Base(dir)
		if _, e := git(ctx, p.Repo, "worktree", "remove", dir); e != nil {
			s.log.Warn("cleanup: worktree kept", "task", t.ID, "err", e)
			continue
		}
		// -d alone also accepts a branch that is merged only into its upstream.
		_, e := git(ctx, p.Repo, "merge-base", "--is-ancestor", branch, "HEAD")
		if e == nil {
			_, e = git(ctx, p.Repo, "branch", "-d", branch)
		}
		if e != nil {
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

// tries is the failed-attempt record of a task, saved in .tiny-symphony/attempts of its worktree.
// Only the pause before the next attempt is in memory.
type tries struct {
	N      int    `json:"n"`
	Last   string `json:"last"`
	Status string `json:"status"`
	next   time.Time
}

func (s *sched) triesFile(id string) string {
	if dir := findWorkspace(s.cfg.WorkspaceRoot, id); dir != "" {
		return filepath.Join(dir, stateDir, "attempts")
	}
	return ""
}

// getTries returns the record of the task, or nil; after a restart it comes from the file.
func (s *sched) getTries(id string) *tries {
	if tr := s.tries[id]; tr != nil {
		return tr
	}
	data, err := os.ReadFile(s.triesFile(id))
	tr := &tries{}
	if err != nil || json.Unmarshal(data, tr) != nil {
		return nil
	}
	s.tries[id] = tr
	return tr
}

func (s *sched) clearTries(id string) {
	delete(s.tries, id)
	if f := s.triesFile(id); f != "" {
		_ = os.Remove(f)
	}
}

// failure records a failed attempt and returns true when the task has used up max_attempts.
func (s *sched) failure(id, status string, err error) bool {
	tr := s.getTries(id)
	if tr == nil {
		tr = &tries{Status: status}
		s.tries[id] = tr
	}
	tr.N++
	tr.Last = err.Error()
	tr.next = time.Now().Add(s.backoff[min(tr.N, len(s.backoff))-1])
	if f := s.triesFile(id); f != "" {
		data, e := json.Marshal(tr)
		if e == nil {
			e = os.WriteFile(f, data, 0o600)
		}
		if e != nil {
			s.log.Warn("attempts not saved", "task", id, "err", e)
		}
	}
	return tr.N >= s.cfg.MaxAttempts
}

// block gives up on a task: the reason and the log path go into a comment, the status becomes Blocked.
// The task is read again first: a card a human moved meanwhile is left alone. The counter stays until the write works.
func (s *sched) block(ctx context.Context, id string) {
	tr := s.getTries(id)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	now, err := s.bl.Load(ctx, id)
	if err == nil && (now.agentName() == "" || norm(now.Status) != norm(tr.Status)) {
		s.clearTries(id)
		s.log.Info("not blocking, the card changed", "task", id)
		return
	}
	msg := fmt.Sprintf("tiny-symphony gave up after %d failed attempts. Last error: %s", tr.N, tr.Last)
	if err == nil {
		err = s.bl.Apply(ctx, id, Result{Comment: msg}, "Blocked")
	}
	if err != nil {
		s.log.Error("could not block the task", "task", id, "err", err)
		return
	}
	s.clearTries(id)
	s.log.Warn("task blocked", "task", id, "outcome", "blocked", "attempts", tr.N)
}

// attempt is the number of the next run of the task and the last error; wait is true inside the pause.
func (s *sched) attempt(ctx context.Context, t Task) (n int, lastErr string, wait bool) {
	tr := s.getTries(t.ID)
	if tr != nil && norm(tr.Status) != norm(t.Status) {
		s.clearTries(t.ID) // a human moved the card: a fresh start
		tr = nil
	}
	if tr == nil {
		return 1, "", false
	}
	if tr.N >= s.cfg.MaxAttempts { // an earlier write of Blocked failed
		s.block(ctx, t.ID)
		return 0, "", true
	}
	return tr.N + 1, tr.Last, time.Now().Before(tr.next)
}
