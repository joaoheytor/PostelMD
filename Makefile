# Local parity with CI: `make ci` runs what the `required` check runs, except signing and
# publishing (docs/ci-cd.md). Tools: Go (go.mod), Node 24, Docker with buildx, golangci-lint,
# actionlint, shellcheck, yamllint.
.DEFAULT_GOAL := help
SHELL := /bin/sh

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
IMAGE     ?= postelmd:dev
PLATFORMS ?= linux/amd64,linux/arm64
# Trivy v0.76.1, its cosign signature verified against aquasecurity/trivy's release workflow.
TRIVY_IMAGE ?= ghcr.io/aquasecurity/trivy:0.76.1@sha256:790fd66d1697395d8693d1acaa1ea702406a20f109b6fc7bac8db623eca6c06c
# image-push and image-gate (candidate.yaml)
REPO        ?=
DIGEST_FILE ?= bin/image-digest
GATE_DIR    ?= bin/candidate

.PHONY: help ci perf build web-build image-bins image image-multiarch image-smoke image-scan image-push image-gate print-trivy-image test test-go test-web test-scripts e2e fuzz lint lint-go lint-web lint-repo vuln web-deps clean

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

ci: lint test vuln build e2e image-multiarch image-smoke image-scan ## Everything the CI `required` check runs (needs Docker)

build: web-build ## Build the postelmd binary, with the web app embedded, into bin/
	CGO_ENABLED=0 go build -trimpath -tags webui -ldflags "$(LDFLAGS)" -o bin/postelmd ./cmd/postelmd

web-build: web-deps
	cd web && npm run build

# The image takes these binaries as they are (Dockerfile), like the release archives will.
image-bins: web-build
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -tags webui -ldflags "$(LDFLAGS)" \
			-o bin/linux-$$arch/postelmd ./cmd/postelmd || exit 1; \
	done
	mkdir -p bin/empty

image: image-bins ## Build the image for this machine and load it as $(IMAGE)
	docker buildx build --load -t $(IMAGE) .

image-multiarch: image-bins ## Build the image for every release platform (not loaded or pushed)
	docker buildx build --platform $(PLATFORMS) --output type=cacheonly .

image-smoke: image ## `docker run` smoke test of $(IMAGE) (ADR-0009), and adopting a notes folder
	scripts/image-smoke.sh $(IMAGE)
	scripts/image-smoke-adopt.sh $(IMAGE)

image-scan: image ## Trivy gate of ADR-0015 on $(IMAGE): no critical, no high with a fix
	TRIVY_IMAGE=$(TRIVY_IMAGE) scripts/image-scan.sh $(IMAGE)

# Push the multi-arch image of the binaries already in bin/linux-<arch>/ (image-bins, or
# candidate.yaml's artifact) to $(REPO), untagged, by digest; write the digest to
# $(DIGEST_FILE). Timestamps are clamped to the commit's time, so the same binaries give the
# same digest.
image-push: ## Push the image untagged, by digest, to REPO=<registry>/<name> (candidate.yaml)
	@test -n "$(REPO)" || { echo 'usage: make image-push REPO=<registry>/<name>' >&2; exit 1; }
	test -f bin/linux-amd64/postelmd && test -f bin/linux-arm64/postelmd
	mkdir -p bin/empty
	SOURCE_DATE_EPOCH=$$(git log -1 --format=%ct) docker buildx build --platform $(PLATFORMS) \
		--provenance=false --sbom=false --metadata-file bin/image-push.json \
		--output 'type=image,name=$(REPO),push-by-digest=true,name-canonical=true,push=true,oci-mediatypes=true,rewrite-timestamp=true' .
	jq -r '."containerimage.digest"' bin/image-push.json >$(DIGEST_FILE)
	grep -qE '^sha256:[0-9a-f]{64}$$' $(DIGEST_FILE)
	@echo "pushed $(REPO)@$$(cat $(DIGEST_FILE))"

image-gate: ## Candidate gates (scripts/candidate-gate.sh) on REPO@$(DIGEST_FILE): smoke, Trivy, SBOMs
	TRIVY_IMAGE=$(TRIVY_IMAGE) scripts/candidate-gate.sh $(REPO) "$$(cat $(DIGEST_FILE))" $(GATE_DIR)

print-trivy-image: ## Print the digest-pinned Trivy image (release.yaml rescans the RC with it)
	@echo '$(TRIVY_IMAGE)'

test: test-go test-web test-scripts ## Go, web and script tests

test-go:
	go test -race ./...

test-web: web-deps
	cd web && npm test

e2e: build ## Playwright smoke tests against bin/postelmd (once: `cd web && npx playwright install --with-deps chromium`)
	cd web && npx playwright test

test-scripts: ## Tests for release scripts and git hooks (throwaway repos and SSH keys)
	scripts/verify-release-tag_test.sh
	scripts/release-meta_test.sh
	scripts/pre-commit-adr_test.sh

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

perf: build ## PRD budgets for one user against bin/postelmd (SAMPLES sessions per mode, default 30)
	cd test/perf/app && npm ci --ignore-scripts && npm run --silent bench

vuln: web-deps ## govulncheck and npm audit (shipped dependencies)
	go tool govulncheck ./...
	cd web && npm audit --omit=dev

web-deps: web/node_modules/.package-lock.json

web/node_modules/.package-lock.json: web/package-lock.json
	cd web && npm ci --ignore-scripts --no-fund --no-audit

clean: ## Remove build outputs
	rm -rf bin web/dist
