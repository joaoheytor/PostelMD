# Local parity with CI: `make ci` runs what the `required` check runs, except signing and
# publishing (docs/ci-cd.md). Tools: Go (go.mod), Node 24, golangci-lint, actionlint,
# shellcheck, yamllint.
.DEFAULT_GOAL := help
SHELL := /bin/sh

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: help ci build web-build test test-go test-web test-scripts e2e fuzz lint lint-go lint-web lint-repo vuln web-deps clean

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

ci: lint test vuln build ## Everything the CI `required` check runs

build: web-build ## Build the postelmd binary, with the web app embedded, into bin/
	CGO_ENABLED=0 go build -trimpath -tags webui -ldflags "$(LDFLAGS)" -o bin/postelmd ./cmd/postelmd

web-build: web-deps
	cd web && npm run build

test: test-go test-web test-scripts ## Go, web and script tests

test-go:
	go test -race ./...

test-web: web-deps
	cd web && npm test

e2e: build ## Playwright smoke tests against bin/postelmd (needs `npx playwright install chromium`)
	cd web && npx playwright test

test-scripts: ## Tests for release scripts (throwaway repos and SSH keys)
	scripts/verify-release-tag_test.sh

fuzz: ## Every Go fuzz target for FUZZTIME (default 30s)
	scripts/fuzz.sh

lint: lint-go lint-web lint-repo ## All linters and the file-size check

lint-go:
	golangci-lint run ./...

lint-web: web-deps
	cd web && npm run typecheck && npm run lint

lint-repo:
	scripts/check-file-size.sh
	actionlint
	shellcheck scripts/*.sh .githooks/*
	yamllint --strict .

vuln: web-deps ## govulncheck and npm audit (shipped dependencies)
	go tool govulncheck ./...
	cd web && npm audit --omit=dev

web-deps: web/node_modules/.package-lock.json

web/node_modules/.package-lock.json: web/package-lock.json
	cd web && npm ci --ignore-scripts --no-fund --no-audit

clean: ## Remove build outputs
	rm -rf bin web/dist
