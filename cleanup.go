package main

import (
	"context"
	"path/filepath"
	"time"
)

// cleanup removes the worktree and the merged branch of closed tasks that have no run. With the PR watch
// on it waits until the watch has seen the PR merged or closed, or the task has no PR link.
// git refuses a dirty tree and an unmerged branch; both stay. Log rotation is housekeeping's.
func (s *sched) cleanup(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute) // a hung git must not stop the tick
	defer cancel()
	tasks, err := s.tr.List(ctx, []string{"closed"})
	var repos map[string]string
	if err == nil {
		repos, err = projectRepos(ctx, s.cfg, s.tr)
	}
	if err != nil {
		s.log.Warn("cleanup: tracker read failed, skipped", "err", err)
		return
	}
	for _, t := range tasks {
		if _, busy := s.running[t.ID]; busy || repos[t.Project] == "" || ctx.Err() != nil {
			continue
		}
		if _, _, ok := s.agentFor(t); !ok {
			continue
		}
		dir, err := findWorkspace(s.cfg.WorkspaceRoot, t.ID)
		if err != nil || dir == "" {
			continue
		}
		if m := loadMarks(dir); s.cfg.WatchPR.Enabled && hasPR(t) && !m.Merged && !m.Closed {
			continue
		}
		repo := repos[t.Project]
		if _, err = git(ctx, repo, "worktree", "remove", dir); err != nil {
			s.log.Info("cleanup: worktree kept", "task", t.ID, "err", err)
			continue
		}
		s.log.Info("cleanup: worktree removed", "task", t.ID)
		// branch -d accepts a branch merged only into its upstream, so ask HEAD first (R7).
		branch := filepath.Base(dir)
		if _, err = git(ctx, repo, "merge-base", "--is-ancestor", branch, "HEAD"); err == nil {
			_, err = git(ctx, repo, "branch", "-d", branch)
		}
		if err != nil {
			s.log.Info("cleanup: branch kept", "task", t.ID, "err", err)
		}
	}
}
