package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const stateDir = ".tiny-symphony"

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// pathID is the task id made path-safe, so it can start a branch and a directory name.
func pathID(id string) string {
	id = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(id), "-"), "-")
	return strings.ToUpper(id[:1]) + id[1:]
}

// branchName is T-<N>-<slug>, used when the worktree is created.
func branchName(t TrackerTask) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(t.Title), "-"), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	return strings.TrimRight(pathID(t.ID)+"-"+slug, "-")
}

// findWorkspace is the worktree of the task; "" if none. The name starts with the id and the file
// .tiny-symphony/task-id must agree. Two matches (a parent and its subtask) are an error.
func findWorkspace(root, id string) (string, error) {
	dirs, _ := filepath.Glob(filepath.Join(root, pathID(id)+"-*", ".git"))
	var found []string
	for _, g := range dirs {
		dir := filepath.Dir(g)
		got, err := os.ReadFile(filepath.Join(dir, stateDir, "task-id")) //nolint:gosec // glob under workspace_root
		if err == nil && string(got) != id {
			continue
		}
		found = append(found, dir)
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("task %s matches %d worktrees under %s: %v", id, len(found), root, found)
}

// inside reports whether path is under root.
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // fixed binary, args built by this package
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// prepareWorkspace adds the task's worktree under root, or reuses it.
func prepareWorkspace(ctx context.Context, root, repo string, t TrackerTask) (dir, branch string, err error) {
	if dir, err = findWorkspace(root, t.ID); err != nil {
		return "", "", err
	}
	branch = filepath.Base(dir)
	if dir == "" {
		branch = branchName(t)
		dir = filepath.Join(root, branch)
	}
	if !inside(root, dir) {
		return "", "", fmt.Errorf("workspace %s is outside %s", dir, root)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git")); errors.Is(statErr, os.ErrNotExist) {
		args := []string{"worktree", "add", "-b", branch, dir}
		if _, e := git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); e == nil {
			args = []string{"worktree", "add", dir, branch}
		}
		if _, err = git(ctx, repo, args...); err != nil {
			return "", "", err
		}
	}
	if err = os.MkdirAll(filepath.Join(dir, stateDir), 0o750); err != nil {
		return "", "", fmt.Errorf("state dir: %w", err)
	}
	if err = os.WriteFile(filepath.Join(dir, stateDir, "task-id"), []byte(t.ID), 0o600); err != nil {
		return "", "", fmt.Errorf("task id: %w", err)
	}
	if err = excludeStateDir(ctx, dir); err != nil {
		return "", "", err
	}
	return dir, branch, nil
}

func excludeStateDir(ctx context.Context, dir string) error {
	path, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	old, err := os.ReadFile(path) //nolint:gosec // path comes from git itself
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read exclude: %w", err)
	}
	if strings.Contains(string(old), stateDir+"/") {
		return nil
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("exclude dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path comes from git itself
	if err != nil {
		return fmt.Errorf("open exclude: %w", err)
	}
	sep := ""
	if len(old) > 0 && !strings.HasSuffix(string(old), "\n") {
		sep = "\n"
	}
	_, err = f.WriteString(sep + stateDir + "/\n")
	return errors.Join(err, f.Close())
}
