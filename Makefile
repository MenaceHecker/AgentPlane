# AgentPlane developer entry points. Default target prints help and creates nothing.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Pinned tool versions. CI installs the same versions (see .github/workflows/ci.yml).
GOLANGCI_LINT_VERSION ?= 2.15.0
GITLEAKS_VERSION ?= 8.30.1
KIND_VERSION ?= 0.31.0
# Trivy. Not named TRIVY_*: Trivy reads TRIVY_* environment variables as flags.
IMAGE_SCANNER_VERSION ?= 0.76.2
MOCK_IMAGE ?= agentplane/mockmodel:p0

# Local kind cluster name. Only this named cluster is ever created or deleted.
KIND_CLUSTER_NAME ?= agentplane

# Values a caller supplies are DATA, never Make or shell syntax (review P0-T5-r1, finding 3).
# They used to be pasted into shell recipes between quotes, so a quote in a value ran as shell
# code, and Make re-reads a value ($(shell ...), $(VAR)) whenever it expands or exports it.
# Each is now captured as raw text ($(value ...) is not scanned again) into a simple variable
# and handed to recipes only through the environment ("$$VAR"): the shell expands it once and
# never parses it. Scripts validate the values before any tool runs.
override GOLANGCI_LINT_VERSION := $(value GOLANGCI_LINT_VERSION)
override GITLEAKS_VERSION := $(value GITLEAKS_VERSION)
override KIND_VERSION := $(value KIND_VERSION)
override IMAGE_SCANNER_VERSION := $(value IMAGE_SCANNER_VERSION)
override MOCK_IMAGE := $(value MOCK_IMAGE)
override KIND_CLUSTER_NAME := $(value KIND_CLUSTER_NAME)
override MOCK_ARGS := $(value MOCK_ARGS)
export GOLANGCI_LINT_VERSION GITLEAKS_VERSION KIND_VERSION IMAGE_SCANNER_VERSION MOCK_IMAGE KIND_CLUSTER_NAME MOCK_ARGS

.PHONY: help
help: ## List available targets
	@echo "AgentPlane make targets:"
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo ""
	@echo "Variables: KIND_CLUSTER_NAME (default: agentplane)"

.PHONY: kind-up
kind-up: ## Create the labeled local kind cluster, or accept a compatible one (never recreates)
	@scripts/kind-cluster.sh up "$$KIND_CLUSTER_NAME"

.PHONY: kind-down
kind-down: ## Delete exactly the named local kind cluster (destroys its workloads; needs owner approval)
	@scripts/kind-cluster.sh down "$$KIND_CLUSTER_NAME"

.PHONY: make-args-test
make-args-test: ## Test that Make variables reach the scripts as data: quotes, substitutions and metacharacters execute nothing (stub tools)
	@scripts/test-make-args.sh

.PHONY: kind-config-test
kind-config-test: ## Test the kind topology, node-shape checks and cluster targeting with stub tools (no Docker, no cluster)
	@scripts/test-kind-cluster.sh

.PHONY: fmt-check
fmt-check: ## Check Go formatting without modifying files
	@scripts/check-go.sh fmt-check

.PHONY: lint
lint: ## Run golangci-lint (pinned version) on the Go module
	@scripts/check-go.sh lint

.PHONY: test
test: ## Run Go unit tests with the race detector
	@scripts/check-go.sh test

.PHONY: secrets
secrets: ## Scan tracked working-tree files with gitleaks (redacted output)
	@scripts/scan-secrets.sh

.PHONY: mock-run
mock-run: ## Run the deterministic mock model on loopback (Ctrl-C to stop; MOCK_ARGS for flags)
	@go run ./cmd/mockmodel $$MOCK_ARGS

.PHONY: mock-smoke
mock-smoke: ## Start a disposable mock model, check determinism and streaming, stop it (needs go, curl, python3)
	@scripts/mock-smoke.sh

.PHONY: mock-image
mock-image: ## Build the nonroot local mock model image (agentplane/mockmodel:p0; needs Docker; pushes nothing)
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: docker not found." >&2; exit 1; }
	@docker info >/dev/null 2>&1 || { echo "ERROR: the Docker daemon is not reachable. Start Docker and retry." >&2; exit 1; }
	docker build --provenance=false --sbom=false -f mockmodel/Dockerfile -t "$$MOCK_IMAGE" .

.PHONY: mock-image-scan
mock-image-scan: ## Scan the mock image with pinned Trivy; fail on fixable HIGH/CRITICAL (needs network for the database)
	@SCAN_TRIVY_VERSION="$$IMAGE_SCANNER_VERSION" scripts/scan-mock-image.sh "$$MOCK_IMAGE"

.PHONY: image-scan-test
image-scan-test: ## Test the image scan gate with fixture reports and stub tools (no Docker, no network)
	@scripts/test-image-scan.sh

.PHONY: smoke-cleanup-test
smoke-cleanup-test: ## Test the owned-process helper used by mock-smoke (needs bash and python3)
	@scripts/test-owned-process.sh

.PHONY: secrets-history
secrets-history: ## Scan every line ever added to go.sum (gitleaks' default history scan skips go.sum)
	@scripts/scan-secrets.sh --go-sum-history

.PHONY: secrets-selftest
secrets-selftest: ## Regression-test the secret scanner itself (needs gitleaks)
	@scripts/test-secrets.sh

.PHONY: contracts-check
contracts-check: ## Validate gateway and audit contract fixtures (needs go and python3)
	@scripts/check-contracts.sh
