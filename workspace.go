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

// branchName is T-<N>-<slug>; the id is made path-safe so the branch can name a directory.
func branchName(t Task) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(t.Title), "-"), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	id := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(t.ID), "-"), "-")
	return strings.TrimRight(strings.ToUpper(id[:1])+id[1:]+"-"+slug, "-")
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

// prepareWorkspace adds the task's worktree under root, or reuses it, and clears the previous result.
func prepareWorkspace(ctx context.Context, root, repo string, t Task) (dir, branch string, err error) {
	branch = branchName(t)
	dir = filepath.Join(root, branch)
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
	if err = excludeStateDir(ctx, dir); err != nil {
		return "", "", err
	}
	if err = os.Remove(filepath.Join(dir, stateDir, "result.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("clear result: %w", err)
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
