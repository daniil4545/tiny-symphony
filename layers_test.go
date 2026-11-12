package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func prompts(t *testing.T, e *env) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(e.cfg.WorkspaceRoot, "*", stateDir, "prompt"))
	var out []string
	for _, f := range files {
		data, _ := os.ReadFile(f)
		out = append(out, string(data))
	}
	return out
}

// spec: S4
// Acceptance 7: a project file adds to the prompt after the central one; one with [agents.x] fails the dispatch.
func TestProjectLayerDispatch(t *testing.T) {
	e := newEnv(t, "handoff")
	e.cfg.PromptAppend = "CENTRAL-LINE"
	write(t, filepath.Join(e.repo, ".tiny-symphony.toml"), "prompt_append = \"PROJECT-LINE\"\nmax_attempts = 2\n", 0o600)
	e.create(t, "Adds", "agent:stub")
	e.run(t)
	p := prompts(t, e)
	if len(p) != 1 || !strings.Contains(p[0], "CENTRAL-LINE\nPROJECT-LINE") {
		t.Fatalf("prompt: %q", p)
	}
}

// Acceptance 7 and 10: a foreign key, a broken file or a higher limit is a failed attempt, and the agent never starts.
func TestProjectLayerFailsDispatch(t *testing.T) {
	for name, body := range map[string]string{
		"agents":        "[agents.x]\ncmd = [\"rm\", \"{prompt}\"]\n",
		"other key":     "poll_seconds = 1\n",
		"broken":        "prompt_append = \n",
		"higher max":    "max_attempts = 9\n",
		"higher timout": "run_timeout_minutes = 61\n",
		"zero":          "max_attempts = 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, "handoff")
			write(t, filepath.Join(e.repo, ".tiny-symphony.toml"), body, 0o600)
			id := e.create(t, "Bad file", "agent:stub")
			s := e.sched()
			drive(t, s, 1)
			if len(e.starts(t)) != 0 || s.failed != 1 || s.tries[id] == nil || s.tries[id].N != 1 {
				t.Fatalf("starts %v, failed %d, tries %+v", e.starts(t), s.failed, s.tries[id])
			}
			if !strings.Contains(s.tries[id].Last, ".tiny-symphony.toml") {
				t.Fatalf("the reason must name the file: %q", s.tries[id].Last)
			}
		})
	}
}

func TestProjectLayerMerge(t *testing.T) {
	repo := t.TempDir()
	base := &Config{PromptAppend: "a", MaxAttempts: 3, RunTimeoutMinutes: 60}
	if c, err := withProjectLayer(base, repo); err != nil || c != base {
		t.Fatalf("no file must change nothing: %v", err)
	}
	write(t, filepath.Join(repo, ".tiny-symphony.toml"), "prompt_append = \"b\"\nmax_attempts = 1\nrun_timeout_minutes = 60\n", 0o600)
	c, err := withProjectLayer(base, repo)
	if err != nil || c.PromptAppend != "a\nb" || c.MaxAttempts != 1 || c.RunTimeoutMinutes != 60 || base.MaxAttempts != 3 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestLoadConfigNewFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, `workspace_root = "/w"
[repos]
Toy = "~/Dev/toy"
[agents.a]
cmd = ["~/bin/a", "{prompt}", "~/x"]
[cleanup]
log_days = 3
`, 0o600)
	c, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if c.Repos["toy"] != filepath.Join(home, "Dev/toy") || c.Agents["a"].Cmd[0] != filepath.Join(home, "bin/a") || c.Agents["a"].Cmd[2] != "~/x" {
		t.Fatalf("%+v", c)
	}
	if c.LogDays != 3 || !c.Cleanup.Enabled || c.WatchPR.Enabled || c.WatchPR.PollMinutes != 5 || c.WatchPR.MaxReopens != 2 {
		t.Fatalf("defaults: %+v %+v", c.Cleanup, c.WatchPR)
	}
}
