# AgentPlane developer entry points. Default target prints help and creates nothing.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Pinned tool versions. CI installs the same versions (see .github/workflows/ci.yml).
GOLANGCI_LINT_VERSION ?= 2.15.0
GITLEAKS_VERSION ?= 8.30.1
KIND_VERSION ?= 0.31.0
export GOLANGCI_LINT_VERSION GITLEAKS_VERSION KIND_VERSION

# Local kind cluster name. Only this named cluster is ever created or deleted.
KIND_CLUSTER_NAME ?= agentplane

.PHONY: help
help: ## List available targets
	@echo "AgentPlane make targets:"
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo ""
	@echo "Variables: KIND_CLUSTER_NAME (default: agentplane)"

.PHONY: kind-up
kind-up: ## Create the labeled local kind cluster, or accept a compatible one (never recreates)
	@KIND_VERSION='$(KIND_VERSION)' scripts/kind-cluster.sh up '$(KIND_CLUSTER_NAME)'

.PHONY: kind-down
kind-down: ## Delete exactly the named local kind cluster (destroys its workloads; needs owner approval)
	@KIND_VERSION='$(KIND_VERSION)' scripts/kind-cluster.sh down '$(KIND_CLUSTER_NAME)'

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
	@go run ./cmd/mockmodel $(MOCK_ARGS)

.PHONY: mock-smoke
mock-smoke: ## Start a disposable mock model, check determinism and streaming, stop it (needs go, curl, python3)
	@scripts/mock-smoke.sh

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
