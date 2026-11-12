package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 || os.Args[1] != "run" && os.Args[1] != "check" {
		fmt.Fprintln(os.Stderr, "usage: tiny-symphony run [--once] [--config path] | check [--config path]")
		return 2
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	once := fs.Bool("once", false, "run one tick, wait for its runs, exit")
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	_ = fs.Parse(os.Args[2:])
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		return 1
	}
	if os.Args[1] == "check" {
		if checkConfig(ctx, os.Stdout, cfg, newTracker(cfg, existingDir(cfg))) > 0 {
			return 1
		}
		return 0
	}
	if err = startProblems(ctx, cfg, newTracker(cfg, existingDir(cfg))); err != nil {
		log.Error("config", "err", err)
		return 1
	}
	lock, err := lockWorkspace(cfg.WorkspaceRoot)
	if err != nil {
		log.Error("lock", "err", err)
		return 1
	}
	defer func() { _ = lock.Close() }()
	reapLeftovers(cfg.WorkspaceRoot, log)
	s := newSched(cfg, newTracker(cfg, cfg.WorkspaceRoot), log)
	if s.loop(ctx, *once) > 0 && *once {
		return 1
	}
	return 0
}

// existingDir is workspace_root, or the temp dir while it is not created yet: tix needs an existing cwd.
func existingDir(cfg *Config) string {
	if _, err := os.Stat(cfg.WorkspaceRoot); err != nil {
		return os.TempDir()
	}
	return cfg.WorkspaceRoot
}

// newTracker is tix run in dir, which is outside any git repository, so no list is narrowed to a project.
func newTracker(cfg *Config, dir string) Tracker {
	return &Tix{Dir: dir, Actor: "tiny-symphony", Blocked: cfg.BlockedStatus, Reopened: cfg.ReopenStatus, Timeout: time.Minute}
}

// actorName is the name an agent run writes the tracker under.
func actorName(agent, id string) string { return "tiny-symphony:" + agent + ":" + id }

// runTask does one run and returns the task as the run ended. ctx is for the tracker and git; stop ends only the agent.
// The tracker is read and written on a context that a signal does not cancel, so a write is never cut short.
func (s *sched) runTask(ctx, stop context.Context, t TrackerTask, name string, a Agent, repo string, attempt int, lastErr string, l *slog.Logger) (TrackerTask, error) {
	wctx, actor := context.WithoutCancel(ctx), actorName(name, t.ID)
	cfg, err := withProjectLayer(s.cfg, repo) // read at each dispatch; a bad file is a failed attempt
	if err != nil {
		return t, err
	}
	t, err = s.tr.Get(wctx, t.ID)
	if err != nil {
		return t, fmt.Errorf("task %s: %w", t.ID, err)
	}
	if _, _, ok := s.agentFor(t); !ok || !s.watching(t.Status) || live(t) {
		l.Info("skipped", "outcome", "skipped", "reason", "tag, status or claim changed since the list")
		return t, nil
	}
	dir, branch, err := prepareWorkspace(ctx, cfg.WorkspaceRoot, repo, t)
	if err != nil {
		return t, err
	}
	prompt, err := renderPrompt(map[string]any{
		"id": t.ID, "title": t.Title, "branch": branch, "worktree": dir, "actor": actor,
		"handoff_status": cfg.HandoffStatus, "attempt": attempt, "last_error": lastErr,
	}, cfg.PromptAppend)
	if err != nil {
		return t, err
	}
	l.Info("agent starting", "dir", dir, "attempt", attempt, "actor", actor)
	logPath, runErr := runAgent(stop, cfg.WorkspaceRoot, dir, a, prompt, min(s.timeout, time.Duration(cfg.RunTimeoutMinutes)*time.Minute), s.grace)
	if errors.Is(context.Cause(stop), errMoved) {
		return t, errMoved
	}
	logRel, _ := filepath.Rel(cfg.WorkspaceRoot, logPath)
	var now TrackerTask
	if err = s.trackerRetry(func() (e error) { now, e = s.tr.Get(wctx, t.ID); return }); err != nil {
		return t, err
	}
	return s.judge(wctx, now, actor, runErr, logRel, l)
}

// judge is the outcome table: the tracker state after the run decides, not the exit code.
func (s *sched) judge(ctx context.Context, now TrackerTask, actor string, runErr error, logRel string, l *slog.Logger) (TrackerTask, error) {
	reason := "agent exited without handing the task over"
	if runErr != nil {
		reason = runErr.Error()
	}
	failed := fmt.Errorf("%s; log %s", reason, logRel)
	switch st := norm(now.Status); {
	case st != "in_progress" && !s.watching(st):
		l.Info("run done", "outcome", "success", "status", now.Status, "log", logRel)
		return now, nil
	case st != "in_progress":
		return now, failed
	case live(now) && now.Claim != actor:
		err := s.noteThen(ctx, now, fmt.Sprintf("left in_progress by %s; the agent may have missed --as. Log: %s", now.Claim, logRel), nil)
		return now, failedOr(err, failed)
	}
	err := s.noteThen(ctx, now, fmt.Sprintf("%s. Back to %s. Log: %s", reason, s.cfg.ReopenStatus, logRel), func() error { return s.tr.Reopen(ctx, now.ID) })
	if err == nil {
		now.Status, now.Claim = s.cfg.ReopenStatus, "" // what the move did, for the guard of a later Block
	}
	return now, failedOr(err, failed)
}

// failedOr is the error of the run after the daemon's writes: a changed task is errMoved (no attempt),
// a tracker error stays as it is, and a clean write leaves the failed attempt.
func failedOr(writeErr, failed error) error {
	switch {
	case errors.Is(writeErr, errChanged):
		return errMoved
	case writeErr != nil:
		return writeErr
	}
	return failed
}

// trackerRetry repeats a tracker call made after the agent finished: the paid result is already checked.
// If it still fails, the error is a failed attempt of the run: errTracker is dropped, so it counts.
func (s *sched) trackerRetry(f func() error) (err error) {
	for i := range 3 {
		if i > 0 {
			time.Sleep(s.trackerPause)
		}
		if err = f(); err == nil || !errors.Is(err, errTracker) {
			return err
		}
	}
	return errors.New("writing the result: " + err.Error())
}
