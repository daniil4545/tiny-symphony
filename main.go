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
)

func main() { os.Exit(run()) }

func run() int {
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: tiny-symphony run [--once] [--config path]")
		return 2
	}
	fs := flag.NewFlagSet("run", flag.ExitOnError)
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
	lock, err := lockWorkspace(cfg.WorkspaceRoot)
	if err != nil {
		log.Error("lock", "err", err)
		return 1
	}
	defer func() { _ = lock.Close() }()
	reapLeftovers(cfg.WorkspaceRoot, log)
	s := newSched(cfg, filepath.Dir(expandHome(*cfgPath)), NewBacklog(cfg.BacklogDir), log)
	if s.loop(ctx, *once) > 0 && *once {
		return 1
	}
	return 0
}

// stillOurs reports whether the task still carries the agent label and sits in the status it was taken in.
func stillOurs(t Task, agent, status string) bool {
	return t.agentName() == agent && norm(t.Status) == norm(status)
}

// runTask does one run. ctx is for the tracker and git; stop ends only the agent.
// The tracker is read and written on a context that a signal does not cancel, so a write is never cut short.
func (s *sched) runTask(ctx, stop context.Context, t Task, a Agent, p Project, attempt int, lastErr string, l *slog.Logger) (err error) {
	cfg, configDir, bl := s.cfg, s.configDir, s.bl
	name, started := t.agentName(), t.Status
	wctx := context.WithoutCancel(ctx)
	t, err = bl.Load(wctx, t.ID)
	if err != nil {
		return err
	}
	if !stillOurs(t, name, started) {
		l.Info("skipped", "outcome", "skipped", "reason", "label or status changed since the list")
		return nil
	}
	dir, branch, err := prepareWorkspace(ctx, cfg.WorkspaceRoot, p.Repo, t)
	if err != nil {
		return err
	}
	prompt, err := renderPrompt(promptFile(configDir, t.Status), t, branch, attempt, lastErr)
	if err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	l.Info("agent starting", "dir", dir, "attempt", attempt)
	logPath, err := runAgent(stop, cfg.WorkspaceRoot, dir, a, prompt, s.timeout, s.grace)
	if errors.Is(context.Cause(stop), errMoved) {
		return errMoved
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w (log %s)", err, logPath)
		}
	}()
	if err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	res, err := readResult(dir)
	if err != nil {
		return err
	}
	to, err := checkResult(res, t)
	if err != nil {
		return err
	}
	if to == "Review" {
		if err = checkWork(ctx, dir, branch, p); err != nil {
			return err
		}
	}
	now, err := bl.Load(wctx, t.ID)
	if err != nil {
		return err
	}
	if !stillOurs(now, name, started) {
		return errors.New("task label or status changed during the run, result not applied")
	}
	if err = bl.Apply(wctx, t.ID, res, to); err != nil {
		return err
	}
	l.Info("run done", "outcome", "applied", "to", to, "log", logPath)
	return nil
}
