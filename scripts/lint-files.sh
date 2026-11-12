#!/usr/bin/env bash
# Lints infrastructure and UI files that the project linters (golangci-lint, ruff) do not cover.
# Usage: lint-files.sh [FILE...]; without files it checks every tracked one, migrations only since BASE.
set -uo pipefail

SHELLCHECK=(uvx --from shellcheck-py==0.11.0.1 shellcheck --format gcc)
HADOLINT=(uvx --from hadolint-bin==2.15.1 hadolint --failure-threshold warning)
SQUAWK=(uvx --from squawk-cli==2.70.0 squawk --pg-version "${PG_VERSION:-17}" --assume-in-transaction)
DCLINT=(npx -y dclint@3.1.0)
ACTIONLINT=(go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12)
OXLINT=(npx -y oxlint@1.89.0)
DJLINT=(uvx djlint@1.46.6 --lint)
YAMLLINT=(uvx yamllint==1.38.0)
MARKDOWNLINT=(npx -y markdownlint-cli2@0.24.0)
# 0.2.2 is the version inside cytopia/checkmake:latest, the image of the CI action.
CHECKMAKE=(go run github.com/mrtazz/checkmake/cmd/checkmake@v0.2.2)

root=$(git rev-parse --show-toplevel)
cd "$root" || exit 2

if [ "$#" -eq 0 ]; then
  base=$(git merge-base HEAD "${BASE:-main}" 2>/dev/null || git rev-parse HEAD)
  # Old migrations already ran in production; only new ones can still be fixed.
  mapfile -t files < <(git ls-files | grep -v '/migrations/.*\.sql$'; git diff --name-only "$base" -- '*/migrations/*.sql')
else
  files=("$@")
fi

failed=0
run() {
  local name=$1
  shift
  if ! out=$("$@" 2>&1); then
    printf '%s\n%s\n' "== $name" "$out"
    failed=1
  fi
}

for f in "${files[@]}"; do
  [ -f "$f" ] || continue
  # These three run only in a project that has their config, the way its CI workflows do:
  # markdownlint and checkmake look at the repository root only.
  case "$f" in
    *.yml | *.yaml) [ ! -f .yamllint.yml ] || run "yamllint $f" "${YAMLLINT[@]}" -c .yamllint.yml "$f" ;;
    */*) ;;
    *.md | *.markdown) [ ! -f .markdownlint.json ] || run "markdownlint $f" "${MARKDOWNLINT[@]}" "$f" ;;
    Makefile) [ ! -f checkmake.ini ] || run checkmake "${CHECKMAKE[@]}" --config=checkmake.ini "$f" ;;
  esac
  case "$f" in
    *.sh | *.bash) run shellcheck "${SHELLCHECK[@]}" "$f" ;;
    *Dockerfile* | *.dockerfile) run hadolint "${HADOLINT[@]}" "$f" ;;
    */migrations/*.sql) run squawk "${SQUAWK[@]}" "$f" ;;
    *compose*.yml | *compose*.yaml)
      # Coolify's own key exclude_from_hc breaks the compose schema, so it is dropped from a copy.
      tmp=$(mktemp -d)
      grep -vE '^[[:space:]]*exclude_from_hc:' "$f" >"$tmp/compose.yml"
      run "dclint $f" "${DCLINT[@]}" "$tmp/compose.yml" --quiet
      if command -v docker >/dev/null; then
        run "compose config $f" docker compose -f "$tmp/compose.yml" config --no-interpolate -q
      fi
      rm -r "$tmp"
      ;;
    .github/workflows/*.yml | .github/workflows/*.yaml) run actionlint "${ACTIONLINT[@]}" "$f" ;;
    *.js | *.mjs | *.cjs | *.jsx | *.ts | *.tsx) run oxlint "${OXLINT[@]}" "$f" ;;
    *.html | *.jinja | *.j2)
      if grep -qE '\{%|\{\{' "$f"; then profile=jinja; else profile=html; fi
      run djlint "${DJLINT[@]}" --profile "$profile" "$f"
      ;;
  esac
done
exit "$failed"
