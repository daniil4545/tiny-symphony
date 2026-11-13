# Checks of the project. Needs go and golangci-lint; infra-check and workflows also need uv and node.
.PHONY: secrets infra-changed test-guard spec-trace pr-size push-check pr-check build test test-fast vet fmt lint infra-check security workflows ci-check paid-guard smoke

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

# Step 2: integration tests need git and tix on PATH.
test:
	go test -count=1 ./... $(if $(RUN),-run $(RUN),)

# The live tag compiles paid tests too, so their code is checked.
vet:
	go vet -tags live ./...

# gofumpt and goimports are set in .golangci.yml.
fmt:
	golangci-lint fmt

# All static checks: go vet, Go format, golangci-lint (v2.15.0), ruff and shellcheck on own files.
# ruff format --check is off: all 3 scripts/*.py files fail it; old code is not reformatted.
GOLANGCI := $(shell command -v golangci-lint 2>/dev/null || echo go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0)
RUFF := uvx ruff@0.17.2
SHELLCHECK := uvx --from shellcheck-py==0.11.0.1 shellcheck

lint: vet
	$(GOLANGCI) fmt --diff
	$(GOLANGCI) run --timeout 120s $(if $(LINT_BASE),--new-from-merge-base=$(LINT_BASE),)
	$(RUFF) check .
	$(SHELLCHECK) $$(git ls-files '*.sh')

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

# Every S<n> and R<n> of docs/spec.md has a test marked `spec: <ID>`.
spec-trace:
	scripts/spec-trace.py

# infra-check for changed and new files only; nothing changed - nothing to run.
infra-changed:
	@files=$$(git diff --name-only --diff-filter=d HEAD; git ls-files --others --exclude-standard); \
	[ -z "$$files" ] || scripts/lint-files.sh $$files


# Push to a task branch: step 1 and spec links on the Mac; commits since dev keep their tests.
push-check: lint infra-changed test-guard secrets test-fast spec-trace
	@$(MAKE) --no-print-directory test-guard BASE=$${BASE:-$$(git rev-parse -q --verify origin/dev >/dev/null && echo origin/dev || echo dev)}

# PR into main: the full check.
ci-check: lint infra-check security spec-trace test build
	@$(MAKE) --no-print-directory test-guard BASE=$${BASE:-$$(git rev-parse -q --verify origin/dev >/dev/null && echo origin/dev || echo dev)}

# PR into dev: fast tier on the whole tree plus the size limit, minutes; CI runs it on dev.
pr-check: lint infra-check secrets spec-trace test-fast build pr-size
	@$(MAKE) --no-print-directory test-guard BASE=$${BASE:-$$(git rev-parse -q --verify origin/dev >/dev/null && echo origin/dev || echo dev)}

# Every paid target depends on it. PAID_OK and MAX_USD are a manual consent; nothing enforces the ceiling.
paid-guard:
	@test "$$PAID_OK" = 1 || { echo "paid run: set PAID_OK=1 after approval"; exit 1; }
	@test -n "$$MAX_USD" || { echo "paid run: set MAX_USD"; exit 1; }

# Paid smoke test on the real agent CLI: one tiny task, a few cents; run after approval.
# AGENT picks the CLI from liveAgents in live_test.go: make smoke AGENT=codex.
AGENT ?= claude
smoke: paid-guard
	go test -tags live -count=1 -run '(?i)^TestLive$(AGENT)$$' -v ./...
