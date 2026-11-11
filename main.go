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
	if len(os.Args) < 2 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: tiny-symphony run --once [--config path]")
		return 2
	}
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	once := fs.Bool("once", false, "run one tick, wait for its runs, exit")
	cfgPath := fs.String("config", defaultConfigPath, "config file")
	_ = fs.Parse(os.Args[2:])
	if !*once {
		fmt.Fprintln(os.Stderr, "only run --once exists yet; the loop comes in S2")
		return 2
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		return 1
	}
	if runOnce(ctx, cfg, filepath.Dir(expandHome(*cfgPath)), NewBacklog(cfg.BacklogDir), log) > 0 {
		return 1
	}
	return 0
}

// runOnce runs every candidate task one after another and returns the number of failed runs.
func runOnce(ctx context.Context, cfg *Config, configDir string, bl *Backlog, log *slog.Logger) int {
	tasks, err := bl.List(ctx)
	if err != nil {
		log.Error("tracker read failed, tick skipped", "err", err)
		return 1
	}
	failed := 0
	for _, t := range tasks {
		l := log.With("task", t.ID, "status", t.Status)
		agent, ok := cfg.Agents[t.agentName()]
		proj, known := cfg.Projects[norm(t.Project)]
		if !ok || !known {
			l.Info("skipped", "agent", t.agentName(), "project", t.Project, "reason", "agent or project not in config")
			continue
		}
		if err := runTask(ctx, cfg, configDir, bl, t, agent, proj, l); err != nil {
			l.Error("run failed", "outcome", "failed", "err", err)
			failed++
		}
	}
	return failed
}

func runTask(ctx context.Context, cfg *Config, configDir string, bl *Backlog, t Task, a Agent, p Project, l *slog.Logger) error {
	t, err := bl.Load(ctx, t.ID)
	if err != nil {
		return err
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
	logPath, err := runAgent(ctx, cfg.WorkspaceRoot, dir, a, prompt, time.Duration(cfg.RunTimeoutMinutes)*time.Minute)
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
	if norm(now.Status) != norm(t.Status) {
		return errors.New("task status changed during the run, result not applied")
	}
	if err = bl.Apply(ctx, t.ID, res, to); err != nil {
		return err
	}
	l.Info("run done", "outcome", "applied", "to", to, "log", logPath)
	return nil
}
