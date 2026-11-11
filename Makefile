# Gate template for a Go project (~/.claude/rules/verification.md); copy into the module root.
# Needs go and golangci-lint; infra-check and workflows also need uv and node.
.PHONY: secrets doc-check comment-check infra-changed test-guard spec-trace pr-size push-check pr-check build test test-fast vet fmt fmt-check lint infra-check security workflows commit-check ci-check paid-guard smoke

# Scanners are pinned through go run so the machine PATH and CI do not matter.
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0
GITLEAKS := go run github.com/zricethezav/gitleaks/v8@v8.30.1
ACTIONLINT := go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
ZIZMOR := uvx zizmor@1.30.1

build:
	go build ./...

# Step 1: no database, no network.
test-fast:
	go test -short ./... $(if $(RUN),-run $(RUN) -count=1,)

# Step 2: integration tests need git and backlog on PATH.
test:
	go test -count=1 ./... $(if $(RUN),-run $(RUN),)

# The live tag compiles paid tests too, so their code is checked.
vet:
	go vet -tags live ./...

# gofumpt and goimports are set in .golangci.yml.
fmt:
	golangci-lint fmt

fmt-check:
	golangci-lint fmt --diff

# Strict ~/.claude/templates/golangci.yml; in an old project LINT_BASE=main lints only new code.
lint:
	golangci-lint run --timeout 120s $(if $(LINT_BASE),--new-from-merge-base=$(LINT_BASE),)

# Dockerfile, compose, shell, new migrations, CI, JS and HTML templates (~/.claude/templates/lint-files.sh).
infra-check:
	scripts/lint-files.sh

# Vulnerable dependencies and leaked secrets.
security: secrets
	$(GOVULNCHECK) ./...

# gitleaks ignores .gitignore, so it scans a snapshot of what git sees instead of local .env files.
# Runs before every push: a task branch gets no CI, and a pushed secret is already out.
secrets:
	@set -e; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	root=$$(git rev-parse --show-toplevel); \
	cp "$$(git rev-parse --git-path index)" "$$tmp/index"; \
	GIT_INDEX_FILE="$$tmp/index" git -C "$$root" add -A; \
	tree=$$(GIT_INDEX_FILE="$$tmp/index" git -C "$$root" write-tree); \
	mkdir "$$tmp/tree"; git -C "$$root" archive "$$tree" | tar -x -C "$$tmp/tree"; \
	cd "$$tmp/tree" && $(GITLEAKS) dir --no-banner --redact .

# Run when .github/workflows changes; not in ci-check because zizmor needs uv on the runner.
workflows:
	@root=$$(git rev-parse --show-toplevel); \
	if [ -d "$$root/.github/workflows" ]; then \
	  cd "$$root" && $(ACTIONLINT) && $(ZIZMOR) --no-progress .github/workflows; \
	else echo "no .github/workflows"; fi

# Existing tests: a weakened or deleted assertion needs TEST_CHANGE='<reason>' and a `Test-change:` line.
test-guard:
	scripts/test-guard.py $(if $(BASE),--base $(BASE),)

# Lines of code a PR into dev carries, limit 400 (~/.claude/docs/git.md); PR_SIZE_MAX raises it.
pr-size:
	scripts/pr-size.py $(if $(BASE),--base $(BASE),)

# Every S<n> and R<n> of docs/spec.md and docs/specs has a test marked `spec: <ID>`.
# Document classes of ~/.claude/rules/docs.md; skipped where the script is absent (CI).
doc-check:
	@f=$$HOME/.claude/scripts/doc-check.py; [ ! -f $$f ] || python3 -I $$f --all

spec-trace:
	scripts/spec-trace.py

# infra-check for changed and new files only; nothing changed - nothing to run.
infra-changed:
	@files=$$(git diff --name-only --diff-filter=d HEAD; git ls-files --others --exclude-standard); \
	[ -z "$$files" ] || scripts/lint-files.sh $$files

# Comment rule of code.md (optimize scan) on changed and new code files; scan.py exits 1 on a finding.
# Needs ~/.claude, so it is in commit-check only: CI has no such directory.
comment-check:
	@files=$$( (git -c core.quotepath=false diff --name-only --diff-filter=d HEAD; \
	  git -c core.quotepath=false ls-files --others --exclude-standard) \
	  | grep -E '\.(go|py|js|mjs|cjs|jsx|ts|tsx|html|sh|bash)$$' || true); \
	[ -z "$$files" ] || printf '%s\n' "$$files" | tr '\n' '\0' \
	  | xargs -0 python3 "$$HOME/.claude/skills/optimize/scan.py" --check-comments

# Commit: step 0 only, seconds; linters and the comment rule run here and in CI, not on every edit.
commit-check: fmt-check vet lint comment-check infra-changed test-guard

# Push to a task branch: step 1 and spec links on the Mac; commits since dev keep their tests.
push-check: commit-check secrets test-fast spec-trace
	@$(MAKE) --no-print-directory test-guard BASE=$${BASE:-$$(git rev-parse -q --verify origin/dev >/dev/null && echo origin/dev || echo dev)}

# PR into main: steps 0-2; the project adds release-smoke (release artifact installed the user's way and run).
ci-check: fmt-check vet lint infra-check security spec-trace test build
	@$(MAKE) --no-print-directory test-guard BASE=$${BASE:-$$(git rev-parse -q --verify origin/dev >/dev/null && echo origin/dev || echo dev)}

# PR into dev: fast tier on the whole tree plus the size limit, minutes; CI runs it on dev.
pr-check: fmt-check vet lint infra-check secrets spec-trace doc-check test-fast build pr-size
	@$(MAKE) --no-print-directory test-guard BASE=$${BASE:-$$(git rev-parse -q --verify origin/dev >/dev/null && echo origin/dev || echo dev)}

# Step 4 guard: every paid target depends on it; the run reads MAX_USD and stops at the ceiling.
# A paid run rewrites cassettes: `RECORD=1 go test -tags live -run Live ./...`.
paid-guard:
	@test "$$PAID_OK" = 1 || { echo "paid run: set PAID_OK=1 after approval"; exit 1; }
	@test -n "$$MAX_USD" || { echo "paid run: set MAX_USD"; exit 1; }

# Paid smoke test on the real agent CLI: one tiny task, a few cents; run after approval.
smoke: paid-guard
	go test -tags live -count=1 -run Live -v ./...
