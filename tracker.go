package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Criterion struct {
	Index   int
	Text    string
	Checked bool
}

// Task is the normalized task: the one seam to the tracker.
type Task struct {
	ID, Title, Description string
	Criteria               []Criterion
	Status                 string
	Labels                 []string
	Priority, Project      string
	Created                time.Time
	Ready                  bool
	Plan                   string
	Comments               []string
}

// Result is the agent's result.json.
type Result struct {
	Status    string `json:"status"`
	Plan      string `json:"plan"`
	Notes     string `json:"notes"`
	Summary   string `json:"summary"`
	CheckedAC []int  `json:"checked_ac"`
	Comment   string `json:"comment"`
}

// transitions is the Pipeline table: the agent statuses and where a run may end.
var transitions = map[string][]string{
	"to do":       {"Spec Review", "Review", "Blocked"},
	"in progress": {"Review", "Blocked"},
	"rework":      {"Review", "Blocked"},
}

var priorityRank = map[string]int{"high": 0, "medium": 1, "low": 2}

type Backlog struct {
	run func(ctx context.Context, args ...string) ([]byte, error)
}

func NewBacklog(dir string) *Backlog {
	return &Backlog{run: func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "backlog", args...) //nolint:gosec // fixed binary, args built by this package
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "BACKLOG_CWD="+dir)
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("backlog %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return out.Bytes(), nil
	}}
}

type jsonTask struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	Project     string   `json:"project"`
	Labels      []string `json:"labels"`
	CreatedAt   string   `json:"createdAt"`
	IsReady     bool     `json:"isReady"`
	Description string   `json:"description"`
	Plan        string   `json:"implementationPlan"`
	Comments    []struct {
		Body string `json:"body"`
	} `json:"comments"`
	AcceptanceCriteria []struct {
		Index   int    `json:"index"`
		Text    string `json:"text"`
		Checked bool   `json:"checked"`
	} `json:"acceptanceCriteria"`
}

func (j jsonTask) task() Task {
	t := Task{
		ID: j.ID, Title: j.Title, Description: j.Description, Status: j.Status,
		Priority: j.Priority, Project: j.Project, Ready: j.IsReady, Plan: j.Plan,
	}
	for _, c := range j.Comments {
		t.Comments = append(t.Comments, c.Body)
	}
	for _, l := range j.Labels {
		t.Labels = append(t.Labels, norm(l))
	}
	var err error
	if t.Created, err = time.Parse(time.RFC3339, j.CreatedAt); err != nil {
		slog.Warn("bad createdAt, task sorts first", "task", j.ID, "value", j.CreatedAt)
	}
	for _, c := range j.AcceptanceCriteria {
		t.Criteria = append(t.Criteria, Criterion(c))
	}
	return t
}

// agentName is the name in the task's first `agent:<name>` label, or "".
func (t Task) agentName() string {
	for _, l := range t.Labels {
		if name, ok := strings.CutPrefix(l, "agent:"); ok && name != "" {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

func (t Task) hasLabel(l string) bool { return slices.Contains(t.Labels, l) }

func idNumber(id string) int {
	n, _ := strconv.Atoi(id[strings.LastIndex(id, "-")+1:])
	return n
}

func rank(p string) int {
	if r, ok := priorityRank[norm(p)]; ok {
		return r
	}
	return len(priorityRank)
}

// parseList keeps the ready tasks in an active status that carry an agent label, in dispatch order.
func parseList(data []byte) ([]Task, error) {
	var doc struct {
		Tasks []jsonTask `json:"tasks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("task list json: %w", err)
	}
	var tasks []Task
	for _, j := range doc.Tasks {
		t := j.task()
		if _, active := transitions[norm(t.Status)]; active && t.agentName() != "" && t.Ready {
			tasks = append(tasks, t)
		}
	}
	slices.SortFunc(tasks, func(a, b Task) int {
		if r := rank(a.Priority) - rank(b.Priority); r != 0 {
			return r
		}
		if c := a.Created.Compare(b.Created); c != 0 {
			return c
		}
		return idNumber(a.ID) - idNumber(b.ID)
	})
	return tasks, nil
}

func (b *Backlog) List(ctx context.Context) ([]Task, error) {
	out, err := b.run(ctx, "task", "list", "--json")
	if err != nil {
		return nil, err
	}
	return parseList(out)
}

// ListDone returns the Done tasks that carry an agent label: their worktrees are due for cleanup.
func (b *Backlog) ListDone(ctx context.Context) ([]Task, error) {
	out, err := b.run(ctx, "task", "list", "-s", "Done", "--json")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Tasks []jsonTask `json:"tasks"`
	}
	if err = json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("task list json: %w", err)
	}
	var tasks []Task
	for _, j := range doc.Tasks {
		if t := j.task(); t.agentName() != "" {
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}

// Load reads one task with its description and criteria.
func (b *Backlog) Load(ctx context.Context, id string) (Task, error) {
	out, err := b.run(ctx, "task", "view", id, "--json")
	if err != nil {
		return Task{}, err
	}
	var doc struct {
		Task jsonTask `json:"task"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return Task{}, fmt.Errorf("task view json: %w", err)
	}
	return doc.Task.task(), nil
}

// checkResult returns the canonical target status, or an error if the transition or the result is not valid.
func checkResult(r Result, t Task) (string, error) {
	for _, to := range transitions[norm(t.Status)] {
		if norm(to) != norm(r.Status) || (to == "Review" && norm(t.Status) == "to do" && !t.hasLabel("quick")) {
			continue
		}
		if to == "Blocked" && strings.TrimSpace(r.Comment) == "" {
			return "", errors.New("result Blocked needs a comment with the question")
		}
		if to == "Spec Review" && strings.TrimSpace(r.Plan) == "" {
			return "", errors.New("result Spec Review needs a plan")
		}
		for _, i := range r.CheckedAC {
			if i < 1 || i > len(t.Criteria) {
				return "", fmt.Errorf("checked_ac %d: the task has %d criteria", i, len(t.Criteria))
			}
		}
		return to, nil
	}
	return "", fmt.Errorf("transition %q to %q is not in the pipeline", t.Status, r.Status)
}

func editArgs(id string, r Result) []string {
	args := []string{"task", "edit", id}
	for _, f := range [][2]string{
		{"--plan", r.Plan}, {"--append-notes", r.Notes}, {"--final-summary", r.Summary}, {"--comment", r.Comment},
	} {
		if f[1] != "" {
			args = append(args, f[0]+"="+f[1])
		}
	}
	for _, i := range r.CheckedAC {
		args = append(args, "--check-ac="+strconv.Itoa(i))
	}
	return args
}

// Apply writes the checked result to the task: the fields first, the status last.
func (b *Backlog) Apply(ctx context.Context, id string, r Result, status string) error {
	if args := editArgs(id, r); len(args) > 3 {
		if _, err := b.run(ctx, args...); err != nil {
			return err
		}
	}
	_, err := b.run(ctx, "task", "edit", id, "--status="+status)
	return err
}
