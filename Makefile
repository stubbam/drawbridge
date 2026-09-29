# Drawbridge build. Run `make help` to list the targets.

GO  ?= go
NPM ?= npm

# The build-time version constant is a bare string (CLAUDE.md, "Conventions"): the exact
# vX.Y.Z tag on HEAD without its "v", or 0.0.0-dev between releases.
GIT_TAG := $(shell git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null)
VERSION ?= $(if $(GIT_TAG),$(patsubst v%,%,$(GIT_TAG)),0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)$(if $(shell git status --porcelain 2>/dev/null),-dirty)

PKG     := github.com/stuffam/drawbridge
# Drawbridge's own Go packages. Not ./..., which would include Go files that npm
# packages ship inside web/node_modules.
GO_PKGS := ./cmd/... ./internal/...
# The integration tests have a build tag, so they're linted with it.
GO_LINT := --build-tags integration $(GO_PKGS) ./test/...
# Run the integration tests as root: directly if we are root, otherwise through sudo.
SUDO_EXEC := $(if $(filter 0,$(shell id -u)),,-exec 'sudo -E')
LDFLAGS := -s -w -X $(PKG)/internal/version.Version=$(VERSION) -X $(PKG)/internal/version.Commit=$(COMMIT)
GOBUILD := CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)'

# Pinned build tools, installed into ./bin by `make tools`.
BIN                   := $(CURDIR)/bin
GOLANGCI_LINT_VERSION := v2.14.0
NFPM_VERSION          := v2.47.0
MISSPELL_VERSION      := v0.8.0

ARCHES := arm64 amd64

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

.PHONY: tools
tools: $(BIN)/golangci-lint $(BIN)/nfpm $(BIN)/misspell ## Install the pinned build tools into ./bin.

# Each tool installs on first use. After changing a version above, run `make clean-tools`.
$(BIN)/golangci-lint:
	GOBIN=$(BIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
$(BIN)/nfpm:
	GOBIN=$(BIN) $(GO) install github.com/goreleaser/nfpm/v2/cmd/nfpm@$(NFPM_VERSION)
$(BIN)/misspell:
	GOBIN=$(BIN) $(GO) install github.com/golangci/misspell/cmd/misspell@$(MISSPELL_VERSION)

.PHONY: clean-tools
clean-tools: ## Delete ./bin, so the next build installs the pinned tool versions again.
	rm -rf $(BIN)

web/node_modules: web/package.json web/package-lock.json
	cd web && $(NPM) ci
	@touch $@

.PHONY: web
web: web-build embed ## Build the web app and embed it (web-build, then embed).

.PHONY: web-build
web-build: web/node_modules ## Build the web app into web/build/.
	cd web && $(NPM) run build

.PHONY: embed
embed: ## Copy web/build/ into the Go embed directory, internal/webui/dist/.
	@test -f web/build/200.html || { echo "web/build/ is missing; run make web-build first" >&2; exit 1; }
	find internal/webui/dist -mindepth 1 ! -name .keep -delete
	cp -R web/build/. internal/webui/dist/

.PHONY: build
build: ## Build drawbridge for this machine into dist/.
	$(GOBUILD) -o dist/drawbridge ./cmd/drawbridge

.PHONY: build-linux
build-linux: ## Build drawbridge for linux/arm64 and linux/amd64 into dist/linux-<arch>/.
	@for arch in $(ARCHES); do \
		echo "GOARCH=$$arch"; \
		GOOS=linux GOARCH=$$arch $(GOBUILD) -o dist/linux-$$arch/drawbridge ./cmd/drawbridge || exit 1; \
	done

.PHONY: deb
deb: web ## Build the web app, then the .deb packages for arm64 and amd64 into dist/.
	$(MAKE) package

.PHONY: package
package: $(BIN)/nfpm ## Build binaries and .deb packages from the already-embedded web app.
	@test -f internal/webui/dist/200.html || { echo "the web app isn't embedded; run make web first" >&2; exit 1; }
	$(MAKE) build-linux
	@mkdir -p dist/package
	@for arch in $(ARCHES); do \
		cp dist/linux-$$arch/drawbridge dist/package/drawbridge && \
		VERSION=$(VERSION) ARCH=$$arch $(BIN)/nfpm package --config packaging/nfpm.yaml \
			--packager deb --target dist/ || exit 1; \
	done
	@rm -rf dist/package

.PHONY: test
test: test-go test-web ## Run all tests.

.PHONY: test-go
test-go: ## Run the Go tests with the race detector.
	$(GO) test -race $(GO_PKGS)

.PHONY: test-integration
test-integration: build ## Run the kernel WireGuard tests in network namespaces (root, IPv6, wireguard module).
	test/integration/preflight.sh
	DRAWBRIDGE_INTEGRATION=1 DRAWBRIDGE_BIN=$(CURDIR)/dist/drawbridge \
		$(GO) test -tags integration -count=1 -v $(SUDO_EXEC) ./test/integration/

.PHONY: test-web
test-web: web/node_modules ## Run the web app's unit tests.
	cd web && $(NPM) test

.PHONY: test-e2e
test-e2e: web build ## Drive the real web app in a browser, against `serve --backend fake` (Playwright).
	cd web && DRAWBRIDGE_BIN=$(CURDIR)/dist/drawbridge npx playwright test

.PHONY: lint
lint: lint-go lint-web spell ## Run every linter and the spelling check.

.PHONY: lint-go
lint-go: $(BIN)/golangci-lint ## Lint and format-check the Go code.
	$(BIN)/golangci-lint run $(GO_LINT)
	$(BIN)/golangci-lint fmt --diff $(GO_PKGS) ./test/...

.PHONY: lint-web
lint-web: web/node_modules ## Lint, format-check, and type-check the web app.
	cd web && $(NPM) run lint && $(NPM) run check

# Lockfiles and SVGs hold package names and path data, not prose.
SPELL_FILES = git ls-files -z --cached --others --exclude-standard | \
	grep -zv -e 'package-lock\.json$$' -e 'go\.sum$$' -e '\.svg$$'

.PHONY: spell
spell: $(BIN)/misspell ## Check every tracked text file for British spellings (U.S. English only).
	$(SPELL_FILES) | xargs -0 $(BIN)/misspell -locale US -error

.PHONY: fmt
fmt: $(BIN)/golangci-lint web/node_modules ## Format the Go code and the web app.
	$(BIN)/golangci-lint fmt $(GO_PKGS) ./test/...
	cd web && $(NPM) run format

.PHONY: check
check: lint test ## Run everything CI runs, except the package build.

.PHONY: clean
clean: ## Delete build output (not ./bin or node_modules).
	rm -rf dist web/build
	find internal/webui/dist -mindepth 1 ! -name .keep -delete
