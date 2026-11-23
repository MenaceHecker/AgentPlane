# AgentPlane developer entry points. Default target prints help and creates nothing.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# Pinned tool versions. CI installs the same versions (see .github/workflows/ci.yml).
GOLANGCI_LINT_VERSION ?= 2.15.0
GITLEAKS_VERSION ?= 8.30.1
export GOLANGCI_LINT_VERSION GITLEAKS_VERSION

# Local kind cluster name. Only this named cluster is ever created or deleted.
KIND_CLUSTER_NAME ?= agentplane

.PHONY: help
help: ## List available targets
	@echo "AgentPlane make targets:"
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo ""
	@echo "Variables: KIND_CLUSTER_NAME (default: agentplane)"

.PHONY: check-kind-prereqs
check-kind-prereqs:
	@command -v docker >/dev/null 2>&1 || { echo "ERROR: docker not found. Install Docker Desktop or Docker Engine." >&2; exit 1; }
	@command -v kind >/dev/null 2>&1 || { echo "ERROR: kind not found. Install it, e.g. 'brew install kind'." >&2; exit 1; }
	@docker info >/dev/null 2>&1 || { echo "ERROR: Docker daemon is not reachable. Start Docker and retry." >&2; exit 1; }
	@case "$(KIND_CLUSTER_NAME)" in \
	  ""|*[!a-z0-9-]*|-*|*-) echo "ERROR: KIND_CLUSTER_NAME must be lowercase alphanumerics and '-' (got '$(KIND_CLUSTER_NAME)')." >&2; exit 1;; \
	esac

.PHONY: kind-up
kind-up: check-kind-prereqs ## Create the local kind cluster (no-op if it exists)
	@if kind get clusters 2>/dev/null | grep -qx '$(KIND_CLUSTER_NAME)'; then \
	  echo "kind cluster '$(KIND_CLUSTER_NAME)' already exists; nothing to do."; \
	else \
	  kind create cluster --name '$(KIND_CLUSTER_NAME)'; \
	fi

.PHONY: kind-down
kind-down: check-kind-prereqs ## Delete the named local kind cluster (destroys its workloads)
	@if kind get clusters 2>/dev/null | grep -qx '$(KIND_CLUSTER_NAME)'; then \
	  kind delete cluster --name '$(KIND_CLUSTER_NAME)'; \
	else \
	  echo "kind cluster '$(KIND_CLUSTER_NAME)' does not exist; nothing to do."; \
	fi

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
