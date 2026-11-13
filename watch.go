package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// watchLast is the time of the last round of the PR watch per scheduler, so loop.go needs no field.
var watchLast sync.Map // *sched -> time.Time

// ghTimeout bounds one gh call.
const ghTimeout = 30 * time.Second

var failedStates = []string{"FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "STARTUP_FAILURE", "ACTION_REQUIRED"}

// marks is what the watch has already said about a task, kept in .tiny-symphony/watch next to attempts.
type marks struct {
	Merged  bool `json:"merged"`
	Closed  bool `json:"closed"`
	Limit   bool `json:"limit"`
	Reopens int  `json:"reopens"`
}

func marksFile(dir string) string { return filepath.Join(dir, stateDir, "watch") }

// loadMarks reads the file; a damaged one means the limit is reached, so no more reopens.
func loadMarks(dir string) marks {
	var m marks
	data, err := os.ReadFile(marksFile(dir))
	if err != nil {
		return m
	}
	if json.Unmarshal(data, &m) != nil || m.Reopens < 0 {
		return marks{Limit: true}
	}
	return m
}

func saveMarks(dir string, m marks) error {
	data, err := json.Marshal(m)
	if err == nil {
		err = os.WriteFile(marksFile(dir), data, 0o600)
	}
	if err != nil {
		return fmt.Errorf("watch marks: %w", err)
	}
	return nil
}

type prInfo struct {
	State       string `json:"state"`
	MergedAt    string `json:"mergedAt"`
	MergeCommit struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	MergeStateStatus string `json:"mergeStateStatus"`
	Checks           []struct {
		Name       string `json:"name"`
		Context    string `json:"context"`
		State      string `json:"state"`
		Conclusion string `json:"conclusion"`
	} `json:"statusCheckRollup"`
}

// viewPR asks gh about the PR with the repository as cwd. No shell (R3).
func viewPR(ctx context.Context, repo, url string) (prInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--json", "state,mergedAt,mergeCommit,mergeStateStatus,statusCheckRollup", "--", url) //nolint:gosec // fixed binary
	cmd.Dir = repo
	cmd.WaitDelay = 5 * time.Second
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	var p prInfo
	if err := cmd.Run(); err != nil {
		return p, fmt.Errorf("gh pr view: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		return p, fmt.Errorf("gh pr view: %w", err)
	}
	return p, nil
}

// failure is the reason the PR needs the agent again, or "".
func (p prInfo) failure() string {
	for _, c := range p.Checks {
		if st := strings.ToUpper(c.Conclusion + c.State); slices.ContainsFunc(failedStates, func(f string) bool { return strings.Contains(st, f) }) {
			return fmt.Sprintf("check %s is %s", strings.TrimSpace(c.Name+c.Context), strings.ToLower(c.Conclusion+c.State))
		}
	}
	if strings.ToUpper(p.MergeStateStatus) == "DIRTY" {
		return "the PR conflicts with its base"
	}
	return ""
}

func hasPR(t TrackerTask) bool { return strings.HasPrefix(t.External, "http") }

// watchPRs looks, once per poll_minutes, at the PR of every closed task that has a worktree and no run.
// It never merges and never switches auto-merge on (R5).
func (s *sched) watchPRs(ctx context.Context) {
	if v, ok := watchLast.Load(s); ok {
		if last, _ := v.(time.Time); time.Since(last) < time.Duration(s.cfg.WatchPR.PollMinutes)*time.Minute {
			return
		}
	}
	watchLast.Store(s, time.Now())
	tasks, err := s.tr.List(ctx, []string{"closed"})
	var repos map[string]string
	if err == nil {
		repos, err = projectRepos(ctx, s.cfg, s.tr)
	}
	if err != nil {
		s.log.Warn("watch: tracker read failed, round skipped", "err", err)
		return
	}
	for _, t := range tasks {
		if _, busy := s.running[t.ID]; busy || !hasPR(t) || repos[t.Project] == "" || ctx.Err() != nil {
			continue
		}
		if _, _, ok := s.agentFor(t); !ok {
			continue
		}
		dir, err := findWorkspace(s.cfg.WorkspaceRoot, t.ID)
		if err != nil || dir == "" {
			continue
		}
		if m := loadMarks(dir); !m.Merged && !m.Closed {
			s.watchOne(ctx, t, dir, repos[t.Project], m)
		}
	}
}

func (s *sched) watchOne(ctx context.Context, t TrackerTask, dir, repo string, m marks) {
	p, err := viewPR(ctx, repo, t.External)
	if err != nil {
		s.log.Warn("watch: gh failed, next round", "task", t.ID, "err", err)
		return
	}
	var text string
	var move func() error
	switch reason := p.failure(); {
	case strings.ToUpper(p.State) == "MERGED" || p.MergedAt != "":
		text, m.Merged = "PR merged "+p.MergeCommit.Oid, true
	case strings.ToUpper(p.State) == "CLOSED":
		text, m.Closed = "PR closed without merge. The task stays closed, the branch stays.", true
	case reason == "" || m.Limit:
		return
	case m.Reopens >= s.cfg.WatchPR.MaxReopens:
		text, m.Limit = fmt.Sprintf("PR does not land (%s), reopened %d times. Needs a human.", reason, m.Reopens), true
	default:
		m.Reopens++
		text = fmt.Sprintf("PR needs work: %s. Back to %s (reopen %d of %d).", reason, s.cfg.ReopenStatus, m.Reopens, s.cfg.WatchPR.MaxReopens)
		move = func() error { return s.tr.Reopen(ctx, t.ID) }
	}
	switch err = s.noteThen(ctx, t, text, move); {
	case errors.Is(err, errChanged):
	default: // a failed move may follow a written note: keep the marks, so the note is not repeated
		if err != nil {
			s.log.Error("watch: tracker write failed", "task", t.ID, "err", err)
		}
		if err = saveMarks(dir, m); err != nil {
			s.log.Warn("watch: marks not saved", "task", t.ID, "err", err)
		}
	}
}
