#!/usr/bin/env python3
"""Fails when a scenario or rule ID of the specs has no test marked `spec: <ID>`."""

# ruff: noqa: T201 - a CLI script, stdout is its output

import re
import shutil
import subprocess
import sys
from pathlib import Path

GIT = shutil.which("git") or "git"
# S<n> - scenario in the first table column, R<n> - rule as a list item `R<n>:`.
TABLE_ID = re.compile(r"^\|\s*([SR]\d+)\s*\|", re.MULTILINE)
LIST_ID = re.compile(r"^\s*(?:[-*]|\d+\.)\s+\**([SR]\d+)\**\s*[:.]", re.MULTILINE)
# A dot joins ID parts (`export.S1`) but never ends one: `spec: S1.` closes a sentence.
MARK_ID = r"[\w-]+(?:\.[\w-]+)*"
MARK = re.compile(rf"spec:\s*({MARK_ID}(?:\s*,\s*{MARK_ID})*)")
TEST_FILE = re.compile(r"(_test\.go|_test\.py|(^|/)test_[^/]*\.py|(^|/)tests/.*\.py)$")


def run_git(root: Path, *args: str) -> str:
    return subprocess.run([GIT, *args], cwd=root, capture_output=True, text=True, check=True).stdout  # noqa: S603 - fixed git subcommands


def spec_docs(root: Path) -> list[Path]:
    docs = [root / "docs" / "spec.md"]
    for folder in ("specs", "plans"):
        docs += sorted((root / "docs" / folder).glob("*.md"))
    return [doc for doc in docs if doc.is_file()]


def spec_ids(root: Path, docs: list[Path]) -> dict[str, str]:
    """docs/spec.md keeps IDs bare; docs/specs and docs/plans prefix them with the file stem."""
    ids = {}
    for doc in docs:
        prefix = "" if doc == root / "docs" / "spec.md" else f"{doc.stem}."
        text = doc.read_text(encoding="utf-8")
        for found in TABLE_ID.findall(text) + LIST_ID.findall(text):
            ids[prefix + found] = str(doc.relative_to(root))
    return ids


def test_marks(root: Path) -> set[str]:
    marks = set()
    for name in run_git(root, "ls-files", "-z").split("\0"):
        if TEST_FILE.search(name) and (root / name).is_file():
            for group in MARK.findall((root / name).read_text(encoding="utf-8", errors="replace")):
                marks.update(part.strip() for part in group.split(","))
    return marks


def main() -> int:
    root = Path(run_git(Path.cwd(), "rev-parse", "--show-toplevel").strip())
    docs = spec_docs(root)
    ids = spec_ids(root, docs)
    if docs and not ids:
        print(f"{docs[0].relative_to(root)}: spec found, no S<n>/R<n> IDs parsed; fix the ID format")
        return 1
    marks = test_marks(root)
    missing = sorted(i for i in ids if i not in marks)
    for spec_id in missing:
        print(f"{ids[spec_id]}: {spec_id} has no test; mark its test with `spec: {spec_id}`")
    for mark in sorted(marks - ids.keys()):
        # A closed task deletes its plan, so its marks stay behind: a note, not a failure.
        print(f"note: tests name {mark}, no spec defines it")
    print(f"spec-trace: {len(ids)} IDs, {len(ids) - len(missing)} with tests")
    return 1 if missing else 0


if __name__ == "__main__":
    sys.exit(main())
