#!/usr/bin/env python3
"""Fails when a change weakens an existing test without a stated reason."""

# ruff: noqa: T201 - a CLI script, stdout is its output

import argparse
import os
import re
import shutil
import subprocess
import sys
from collections import Counter

GIT = shutil.which("git") or "git"
TEST_FILE = re.compile(r"(_test\.go|_test\.py|(^|/)test_[^/]*\.py|(^|/)tests/.*\.py)$")
ASSERTION = re.compile(
    r"\b(assert|require)\.[A-Z]\w*\(|\bt\.(Error|Errorf|Fatal|Fatalf)\(|^\s*assert\b"
    r"|pytest\.raises\(|\.assert_\w+\(|self\.assert\w+\("
)
TEST_DEF = re.compile(r"^\s*func Test\w*\(|^\s*(async\s+)?def test_\w*\(")
SKIP = re.compile(r"\bt\.Skip(f|Now)?\(|pytest\.mark\.(skip|xfail)|pytest\.skip\(")
REASON = re.compile(r"^Test-change:\s*\S", re.MULTILINE)


def git(*args: str) -> str:
    return subprocess.run([GIT, *args], capture_output=True, text=True, check=True).stdout  # noqa: S603 - fixed git subcommands


def weakened(diff: str) -> list[str]:
    """Net removed assertions and tests and net added skips; a moved line cancels out, a new file is skipped."""
    found = []
    for chunk in re.split(r"^diff --git ", diff, flags=re.MULTILINE)[1:]:
        header, _, body = chunk.partition("\n@@")
        name = header.split(" b/", 1)[-1].splitlines()[0]
        if not TEST_FILE.search(name) or "\nnew file mode" in header:
            continue
        removed, added = Counter(), Counter()
        for line in ("@@" + body).splitlines():
            if line.startswith("-") and not line.startswith("---"):
                removed[line[1:].strip()] += 1
            elif line.startswith("+") and not line.startswith("+++"):
                added[line[1:].strip()] += 1
        for text, count in (removed - added).items():
            if ASSERTION.search(text) or TEST_DEF.search(text):
                found += [f"{name}: removed `{text}`"] * count
        for text, count in (added - removed).items():
            if SKIP.search(text):
                found += [f"{name}: added `{text}`"] * count
    return found


def check_tree() -> int:
    """Working tree against HEAD; the reason comes from TEST_CHANGE."""
    found = weakened(git("diff", "HEAD", "--no-color", "-U0"))
    if not found:
        return 0
    print("\n".join(found))
    reason = os.environ.get("TEST_CHANGE", "").strip()
    if reason:
        print(f"test-guard: put `Test-change: {reason}` into the commit message")
        return 0
    print("test-guard: existing tests weakened; set TEST_CHANGE='<reason>' and add `Test-change: <reason>`")
    return 1


def check_commits(base: str) -> int:
    """Every commit since the merge base; the reason is a `Test-change:` line in its message."""
    start = git("merge-base", "HEAD", base).strip()
    failed = 0
    for commit in git("rev-list", "--no-merges", f"{start}..HEAD").split():
        found = weakened(git("show", "--no-color", "-U0", "--format=", commit))
        if found and not REASON.search(git("log", "-1", "--format=%B", commit)):
            print(f"{commit[:9]}: weakens tests without a `Test-change:` line")
            print("\n".join(f"  {line}" for line in found))
            failed = 1
    return failed


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", help="check every commit since the merge base with this branch")
    args = parser.parse_args()
    return check_commits(args.base) if args.base else check_tree()


if __name__ == "__main__":
    sys.exit(main())
