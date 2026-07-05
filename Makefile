# caddy-geo-ops — common developer tasks.
#
# This repo has two Go modules — the plugin (root) and ./e2e — plus the
# MaxMind-DB git submodule, which provides the test fixtures the ops package
# reads. Targets that touch tests ensure the submodule is initialised first.
#
# Requires a POSIX shell (Git Bash / WSL on Windows), Go, golangci-lint, and —
# for the `caddy` target — xcaddy. The `secrets`/`security` targets also need
# gitleaks; govulncheck is fetched on demand via `go run`.

GO            ?= go
GOLANGCI_LINT ?= golangci-lint
XCADDY        ?= xcaddy
# Run on demand (no prior install); pin by overriding, e.g. GOVULNCHECK="govulncheck".
GOVULNCHECK   ?= $(GO) run golang.org/x/vuln/cmd/govulncheck@latest
GITLEAKS      ?= $(GO) run github.com/zricethezav/gitleaks/v8@latest
COVERPROFILE  ?= coverage.out
MODULE        := github.com/ubiuser/caddy-geo-ops
SUBMODULE     := MaxMind-DB

.DEFAULT_GOAL := help

.PHONY: help
help: ## List available targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ---- setup ----

.PHONY: submodule
submodule: ## Init/refresh the MaxMind-DB fixtures submodule to its pinned commit
	git submodule update --init --recursive

.PHONY: submodule-update
submodule-update: ## Bump the MaxMind-DB submodule to the latest upstream commit
	git submodule update --remote $(SUBMODULE)

# ---- build ----

.PHONY: build
build: ## Build both modules
	$(GO) build ./...
	cd e2e && $(GO) build ./...

.PHONY: caddy
caddy: ## Build a Caddy binary with the geo_ops plugin via xcaddy
	$(XCADDY) build --with $(MODULE)=.

# ---- test ----

.PHONY: test
test: submodule ## Run unit tests (root module) with the race detector
	$(GO) test -race ./...

.PHONY: e2e
e2e: ## Run the end-to-end tests (e2e module) with the race detector
	cd e2e && $(GO) test -race ./...

.PHONY: test-all
test-all: test e2e ## Run unit + e2e tests

.PHONY: cover
cover: submodule ## Run unit tests with coverage; print the per-function + total summary
	$(GO) test -covermode=atomic -coverprofile=$(COVERPROFILE) ./...
	$(GO) tool cover -func=$(COVERPROFILE)

.PHONY: cover-html
cover-html: cover ## Write an annotated HTML coverage report to coverage.html
	$(GO) tool cover -html=$(COVERPROFILE) -o coverage.html
	@echo "open coverage.html"

# ---- lint / format ----

.PHONY: lint
lint: ## Run golangci-lint on both modules
	$(GOLANGCI_LINT) run ./...
	cd e2e && $(GOLANGCI_LINT) run ./...

.PHONY: fmt
fmt: ## Apply golangci-lint formatters (gci, gofumpt, golines, ...) to both modules
	$(GOLANGCI_LINT) fmt ./...
	cd e2e && $(GOLANGCI_LINT) fmt ./...

# ---- dependencies ----

.PHONY: tidy
tidy: ## Run `go mod tidy` for both modules
	$(GO) mod tidy
	cd e2e && $(GO) mod tidy

.PHONY: update-deps
update-deps: ## Update Go deps (both modules) + the MaxMind-DB submodule, tidy, then verify the build
	$(GO) get -u ./...
	cd e2e && $(GO) get -u ./...
	$(MAKE) tidy
	$(MAKE) submodule-update
	# Verify both modules still compile: `go get -u` can bump a transitive dep
	# (e.g. cel-go) past what the pinned Caddy tolerates, breaking the build.
	$(MAKE) build

# ---- security ----
#
# Local subset of the Security workflow. gosec is already run by `make lint`
# (golangci-lint). CodeQL, OpenSSF Scorecard, and dependency-review are
# GitHub-side only and have no local equivalent.

.PHONY: vuln
vuln: ## Scan both modules for known vulnerabilities (govulncheck, reachability-based)
	$(GOVULNCHECK) ./...
	cd e2e && $(GOVULNCHECK) ./...

.PHONY: secrets
secrets: ## Scan the working tree + git history for committed secrets (gitleaks)
	@command -v $(GITLEAKS) >/dev/null 2>&1 || { \
		echo "gitleaks not found — install from https://github.com/gitleaks/gitleaks (or set GITLEAKS=...)"; \
		exit 1; }
	$(GITLEAKS) detect --no-banner --redact

.PHONY: security
security: vuln secrets ## Run all local security checks (govulncheck + gitleaks)

# ---- aggregate ----

.PHONY: check
check: lint test-all ## Fast pre-push gate: lint + all tests (both modules)

.PHONY: check-all
check-all: check security ## Everything check runs, plus the security scans (slower; needs network)
