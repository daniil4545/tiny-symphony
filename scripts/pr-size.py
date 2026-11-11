#!/usr/bin/env python3
"""Fails when a branch changes more lines of code than a PR may carry (~/.claude/docs/git.md)."""

# ruff: noqa: T201 - a CLI script, stdout is its output

import argparse
import os
import re
import shutil
import subprocess
import sys

GIT = shutil.which("git") or "git"
LIMIT = 400
# Tests, fixtures, golden files, cassettes, documents, lock files and generated code do not count.
NOT_CODE = re.compile(
    r"(_test\.go|_test\.py|(^|/)test_[^/]*\.py|(^|/)(tests|Tests|testdata|fixtures|golden|cassettes|docs)/"
    r"|\.golden$|\.(md|txt|rst)$|(^|/)(go\.sum|uv\.lock|package-lock\.json|pnpm-lock\.yaml|yarn\.lock)$"
    r"|\.pb\.go$|_gen\.go$|\.gen\.go$)"
)


def git(*args: str) -> str:
    return subprocess.run([GIT, *args], capture_output=True, text=True, check=True).stdout  # noqa: S603 - fixed git subcommands


def default_base() -> str:
    """origin/dev when the remote branch exists, else dev."""
    probe = subprocess.run([GIT, "rev-parse", "--verify", "-q", "origin/dev"], capture_output=True, check=False)  # noqa: S603 - fixed git subcommand
    return "origin/dev" if probe.returncode == 0 else "dev"


def code_lines(base: str) -> list[tuple[int, str]]:
    """Added plus deleted lines per code file since the merge base; binary files show `-` and are skipped."""
    rows = []
    for line in git("diff", "--numstat", "--no-renames", f"{base}...HEAD").splitlines():
        added, deleted, name = line.split("\t", 2)
        if added == "-" or NOT_CODE.search(name):
            continue
        rows.append((int(added) + int(deleted), name))
    return sorted(rows, reverse=True)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", help="target branch; default origin/dev or dev")
    args = parser.parse_args()
    limit = int(os.environ.get("PR_SIZE_MAX") or LIMIT)
    rows = code_lines(args.base or default_base())
    total = sum(count for count, _ in rows)
    print(f"pr-size: {total} lines of code, limit {limit}")
    if total <= limit:
        return 0
    print("\n".join(f"  {count}\t{name}" for count, name in rows[:10]))
    print("pr-size: split the task; the user may allow more with PR_SIZE_MAX=<n>")
    return 1


if __name__ == "__main__":
    sys.exit(main())
