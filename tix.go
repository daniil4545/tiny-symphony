package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// noteLimit is the longest note tix accepts, in characters.
const noteLimit = 300

// TrackerTask is a task as the daemon reads it from a tracker.
type TrackerTask struct {
	ID, Title, Project string
	Status             string // trimmed, lowercased
	Tags               []string
	Claim              string // the holder, "" when nobody holds the task
	ClaimUntil         string // expiry as the tracker prints it, may be ""
	External           string
	Rank               float64
	Created            time.Time
	After              []string // ids from the "After:" lines of the body; only Get fills it
}

// Tracker is what the daemon needs from a task tracker.
type Tracker interface {
	List(ctx context.Context, statuses []string) ([]TrackerTask, error)
	Get(ctx context.Context, id string) (TrackerTask, error)
	// Repos maps a project name to its repository path.
	Repos(ctx context.Context) (map[string]string, error)
	Note(ctx context.Context, id, text string) error
	Block(ctx context.Context, id, reason string) error
	Reopen(ctx context.Context, id string) error
}

// Tix is the Tracker over the tix CLI.
type Tix struct {
	Dir      string // workspace_root: the cwd of every call
	Actor    string // the --as of every write
	Blocked  string // status for Block
	Reopened string // status for Reopen
	Timeout  time.Duration
}

var _ Tracker = (*Tix)(nil)

func (x *Tix) run(ctx context.Context, args ...string) ([]byte, error) {
	if x.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, x.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "tix", args...) //nolint:gosec // fixed binary, args built by this package
	cmd.Dir = x.Dir
	cmd.WaitDelay = 5 * time.Second // a child holding stdout must not outlive the timeout
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return nil, fmt.Errorf("%w: tix %s: %w: %s", errTracker, strings.Join(args, " "), err, msg)
	}
	return out.Bytes(), nil
}

type tixJSON struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Project  string   `json:"project"`
	Status   string   `json:"status"`
	Tags     []string `json:"tags"`
	Claim    string   `json:"claim"`
	External string   `json:"external"`
	Rank     float64  `json:"rank"`
	Created  string   `json:"created"`
	Body     string   `json:"body"`
}

var (
	afterLine = regexp.MustCompile(`(?m)^After:(.*)$`)
	taskID    = regexp.MustCompile(`T-\d+`)
	colSplit  = regexp.MustCompile(`\s{2,}`)
)

func (j tixJSON) task() (TrackerTask, error) {
	t := TrackerTask{
		ID: j.ID, Title: j.Title, Project: j.Project,
		Status: strings.ToLower(strings.TrimSpace(j.Status)),
		Tags:   j.Tags, External: j.External, Rank: j.Rank,
	}
	t.Claim, t.ClaimUntil, _ = strings.Cut(strings.TrimSpace(j.Claim), " until ")
	if j.Created != "" {
		c, err := time.ParseInLocation("2006-01-02T15:04:05", j.Created, time.Local)
		if err != nil {
			return t, fmt.Errorf("task %s: created %q: %w", j.ID, j.Created, err)
		}
		t.Created = c
	}
	for _, m := range afterLine.FindAllStringSubmatch(j.Body, -1) {
		t.After = append(t.After, taskID.FindAllString(m[1], -1)...)
	}
	return t, nil
}

func (x *Tix) List(ctx context.Context, statuses []string) ([]TrackerTask, error) {
	var all []TrackerTask
	for _, s := range statuses {
		out, err := x.run(ctx, "list", "--status", s, "--json")
		if err != nil {
			return nil, err
		}
		var js []tixJSON
		if err := json.Unmarshal(out, &js); err != nil {
			return nil, fmt.Errorf("tix list --status %s: %w", s, err)
		}
		for _, j := range js {
			t, err := j.task()
			if err != nil {
				return nil, err
			}
			all = append(all, t)
		}
	}
	return all, nil
}

func (x *Tix) Get(ctx context.Context, id string) (TrackerTask, error) {
	out, err := x.run(ctx, "show", id, "--json")
	if err != nil {
		return TrackerTask{}, err
	}
	var j tixJSON
	if err := json.Unmarshal(out, &j); err != nil {
		return TrackerTask{}, fmt.Errorf("tix show %s: %w", id, err)
	}
	return j.task()
}

// Repos parses the text of `tix projects`: name, path ("-" is none), count, a note.
// Projects without a path are left out.
func (x *Tix) Repos(ctx context.Context) (map[string]string, error) {
	out, err := x.run(ctx, "projects")
	if err != nil {
		return nil, err
	}
	repos := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		cols := colSplit.Split(strings.TrimSpace(line), -1)
		if len(cols) < 3 || cols[1] == "-" {
			continue
		}
		repos[cols[0]] = cols[1]
	}
	return repos, nil
}

func (x *Tix) Note(ctx context.Context, id, text string) error {
	if r := []rune(text); len(r) > noteLimit {
		text = string(r[:noteLimit-1]) + "…"
	}
	_, err := x.run(ctx, "note", id, "--as", x.Actor, "--", text)
	return err
}

func (x *Tix) Block(ctx context.Context, id, reason string) error {
	_, err := x.run(ctx, "move", id, x.Blocked, "-m", reason, "--as", x.Actor)
	return err
}

// Reopen has no reason: tix move takes -m only into blocked and closed, so the caller
// writes the reason with Note first.
func (x *Tix) Reopen(ctx context.Context, id string) error {
	_, err := x.run(ctx, "move", id, x.Reopened, "--as", x.Actor)
	return err
}
