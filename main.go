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

// runOnce is one tick; it waits for its runs and returns the number of failed ones.
func runOnce(ctx context.Context, cfg *Config, configDir string, bl *Backlog, log *slog.Logger) int {
	return newSched(cfg, configDir, bl, log).loop(ctx, true)
}

// runTask does one run. ctx is for the tracker and git; stop ends only the agent.
func (s *sched) runTask(ctx, stop context.Context, t Task, a Agent, p Project, l *slog.Logger) error {
	cfg, configDir, bl := s.cfg, s.configDir, s.bl
	name, started := t.agentName(), t.Status
	t, err := bl.Load(ctx, t.ID)
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
	prompt, err := renderPrompt(promptFile(configDir, t.Status), t, branch, 1, "")
	if err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	l.Info("agent starting", "dir", dir)
	logPath, err := runAgent(stop, cfg.WorkspaceRoot, dir, a, prompt, s.timeout, s.grace)
	if errors.Is(context.Cause(stop), errMoved) {
		return errMoved
	}
	if err != nil {
		return fmt.Errorf("agent (log %s): %w", logPath, err)
	}
	res, err := readResult(dir)
	if err != nil {
		return err
	}
	to, err := checkResult(res, t)
	if err != nil {
		return err
	}
	now, err := bl.Load(ctx, t.ID)
	if err != nil {
		return err
	}
	if !stillOurs(now, name, started) {
		return errors.New("task label or status changed during the run, result not applied")
	}
	if err = bl.Apply(ctx, t.ID, res, to); err != nil {
		return err
	}
	l.Info("run done", "outcome", "applied", "to", to, "log", logPath)
	return nil
}
