# Local development quickstart

This is the local (Phase 0) workflow: build, test, run the mock model, create a
labeled kind cluster, and build and scan the mock image. Nothing here uses
Azure, a registry credential or a real model provider. The tested host is stated
below; other hosts are not claimed to work.

## What was tested

| Item | Tested value |
| --- | --- |
| Host | macOS (Darwin 25.6.0), arm64 (Apple silicon) |
| Docker | Docker Desktop 29.9.3, aarch64, cgroup v2, overlayfs storage driver; the Docker VM had 11 CPUs and about 8.3 GB of memory |
| kind | v0.31.0 (Homebrew), default CNI |
| kubectl | v1.36.0 client |
| Go | 1.27.3 (measured in the earlier recorded runs; historical). `go.mod` now declares 1.27.4, which is not recorded here as a tested-host value until a recorded run supports it |
| golangci-lint / gitleaks / pre-commit | 2.15.0 / 8.30.1 / 4.7.0 |
| Trivy | 0.76.2 |
| bash / python3 | 3.2.57 (macOS `/bin/bash`; the scripts avoid newer features) / 3.14 |

CI runs the unit, contract, smoke, scanner, kind-topology (stub-based) and
image checks on `ubuntu-latest` (linux/amd64). **No kind cluster is created in
CI**, and creating one on Linux or on amd64 hardware has not been tested.

## Prerequisites

| Tool | Pin | Install |
| --- | --- | --- |
| Git, GNU Make, curl, python3 | any recent; the sources state Python 3.8 or newer (standard library only), but 3.8 was **not tested**: only the versions in the table above were | OS package manager |
| A POSIX host (macOS or Linux) | any recent | The smoke runner and the kind script are Python programs that start and collect their own child processes directly. They do not use `ps`, the shell job table, a PID file or a process-group signal ([ADR-007](adr/ADR-007-owned-process-supervision.md)), so there is no process-listing prerequisite |
| Go | the `go` directive in `go.mod` (1.27.4; the fixed patch for GO-2026-6859 / CVE-2026-97054 in the 1.27 line). | https://go.dev/dl |
| Docker (Desktop or Engine) | running daemon; cgroup v2 | https://docs.docker.com/get-docker |
| kind | **v0.31.0 exactly** (the script refuses any other) | `brew install kind` or the release binary, see [`deploy/kind/README.md`](../deploy/kind/README.md) |
| kubectl | within one minor version of Kubernetes 1.35 | `brew install kubectl` |
| golangci-lint | 2.15.0 | https://github.com/golangci/golangci-lint |
| gitleaks | 8.30.1 | `brew install gitleaks` or a release binary |
| Trivy (only for `make mock-image-scan`) | **0.76.2 exactly** | release tarball from https://github.com/aquasecurity/trivy/releases/tag/v0.76.2, verified against `trivy_0.76.2_checksums.txt` |
| pre-commit (optional) | 4.7.0 | `pip install pre-commit==4.7.0` |

Hardware: the cluster is three kind node containers plus Docker's VM, so give
Docker enough memory for three Kubernetes nodes (the tested VM had about 8.3 GB).
The topology is never reduced automatically; if there are not enough resources,
the create fails and you must raise Docker's limits.

Installing Trivy from the release (macOS arm64 shown; use `Linux-64bit` on
Linux amd64):

```bash
v=0.76.2; d="$(mktemp -d)"; cd "$d"
curl -fsSLO https://github.com/aquasecurity/trivy/releases/download/v$v/trivy_${v}_macOS-ARM64.tar.gz
curl -fsSLO https://github.com/aquasecurity/trivy/releases/download/v$v/trivy_${v}_checksums.txt
grep " trivy_${v}_macOS-ARM64.tar.gz\$" trivy_${v}_checksums.txt | shasum -a 256 -c -
tar -xzf trivy_${v}_macOS-ARM64.tar.gz trivy   # then put ./trivy on your PATH
```

Do not use Trivy v0.69.4 (binary or images), the Docker Hub images 0.69.5 and
0.69.6, or the `trivy-action` and `setup-trivy` tags from before their fixes:
they were compromised in March 2026 (GHSA-69fq-xp46-6x23). 0.76.2 is after that and after the fixes for
GHSA-mcj4-mphf-j9ff and GHSA-8rc5-4fr6-64pw.

## Clone and check

```bash
git clone https://github.com/MenaceHecker/AgentPlane.git && cd AgentPlane
make fmt-check lint test secrets contracts-check   # quality checks
make smoke-cleanup-test make-args-test kind-config-test image-scan-test   # harness tests: no cluster, no network
make mock-smoke                                    # builds and runs a disposable mock model on loopback
```

`make` alone lists every target and creates nothing.

## The labeled kind cluster

```bash
make kind-up KIND_CLUSTER_NAME=agentplane-p0-t5    # default name: agentplane
kubectl --context kind-agentplane-p0-t5 wait --for=condition=Ready nodes --all --timeout=180s
kubectl --context kind-agentplane-p0-t5 get nodes -L agentplane.io/pool
```

Expected: one control plane, one worker with `agentplane.io/pool=system`, one
worker with `agentplane.io/pool=tenant` carrying the taint
`agentplane.io/pool=tenant:NoSchedule`. The labels and the taint express
placement **intent**; kind's default CNI does not enforce NetworkPolicy, so this
is **not tenant isolation**.

Rules the scripts enforce:

- **Always pass `--context kind-<name>` to kubectl.** `kind` switches your current
  context on create; `make kind-up` puts your previous current context back, and
  never uses the current context for any operation. Check
  `kubectl config current-context` before running other kubectl commands. If it
  points at an unrelated cluster, keep using explicit `--context`.
- **`make kind-up` fails if it cannot put your kubectl context back.** It reads the current context back after restoring it, and exits nonzero (reporting the context you actually have and the command to fix it) if it is not what it was. `make kind-down` does the same check.
- **Make variables are data.** These supported inputs reach the scripts through the environment, never as shell text, so quotes, `$(...)` and `$$` in a value execute nothing and a literal dollar sign arrives unchanged: `KIND_CLUSTER_NAME`, `KIND_VERSION`, `KIND_CONFIG`, `KINDSHAPE_BIN`, `KUBECONFIG`, `MOCK_IMAGE`, `MOCK_ARGS`, `IMAGE_SCANNER_VERSION`, `GOLANGCI_LINT_VERSION`, `GITLEAKS_VERSION`, `SCAN_EVIDENCE_DIR` and `TMPDIR` (the `DATA_VARS` list in the `Makefile`). Names and image references are validated before any tool runs. **This is not a sandbox for Make itself:** the Makefile, `-f`/`--file`, `--eval`, `include`, Make flags (`MAKEFLAGS`), an immediate assignment on the command line (`VAR:=...`, `VAR::=...`) and a command-line value for a variable that is not in that list are executable Make syntax, and whoever writes the `make` command line controls them.
- **`make kind-up` and `make kind-down` exit 0 only after the whole run is verified.** The kind script is a persistent Python owner (`scripts/kind_cluster.py`): it holds every tool it starts and its private temp directory for the whole run, and returns 0 only when the operation succeeded, your kubectl current context was restored and read back, every child was collected and its own directory was removed. Any failure exits nonzero (Make reports a failed recipe as exit 2) and a directory it cannot safely remove is kept (never recursively deleted) with its path in the error; inspect it and delete it yourself. A failed tool keeps its own status and message. An interrupt (Ctrl-C) stops the running tool, restores the context and exits 130.
- **`make kind-up` never recreates or deletes a cluster.** If `<name>` already
  exists it is accepted only when its nodes match
  [`deploy/kind/config.yaml`](../deploy/kind/config.yaml) (count, roles, pool
  labels, taint, Kubernetes version). Otherwise it stops and prints recovery
  instructions.
- **Deleting needs an explicit owner decision:** `make kind-down
  KIND_CLUSTER_NAME=<name>` deletes exactly that named cluster and its workloads.
  Agents must not run it without the owner's authorization for that exact name.

## The mock image

```bash
make mock-image                                    # builds agentplane/mockmodel:p0 locally; pushes nothing
make mock-image-scan                               # pinned Trivy; needs network for its database
kind load docker-image agentplane/mockmodel:p0 --name agentplane-p0-t5
docker run --rm agentplane/mockmodel:p0 --error-rate=2          # exits 2 with a configuration error
docker run --rm --network=none --read-only --cap-drop=ALL --security-opt=no-new-privileges agentplane/mockmodel:p0 --help
```

The image is a static binary on distroless `static-debian13:nonroot`, running as
user `65532:65532`, with no shell and no credentials. It has no default
command, so the server stays on loopback inside the container; to serve:

```bash
docker run --rm -p 127.0.0.1:18081:8081 agentplane/mockmodel:p0 --listen 0.0.0.0:8081
```

**Scan gate.** `make mock-image-scan` fails on any *fixable* HIGH or CRITICAL
finding and on any scan problem (database not downloaded or loaded, Trivy error,
invalid or empty report, nothing analyzed). Unfixed findings and lower
severities are always printed, never filtered: there is no ignore file, no
`--ignore-unfixed`, and any `TRIVY_*` variable that could change results is
refused. The JSON report is kept in `$TMPDIR/agentplane-p0-t5-evidence` (override
with `SCAN_EVIDENCE_DIR`). The recorded results of the last verified run are in
[`agents/runs/P0-T5.md`](../agents/runs/P0-T5.md).

**Base image pins.** The Dockerfile pins `golang:1.27.4-bookworm` and
`gcr.io/distroless/static-debian13:nonroot` by the digest of their multi-arch OCI
index. The builder digest (`sha256:ce59b41415e2524ae99926fe1f37ce48ba8c194dcbd2a56a65436cdcefa2bd34`,
amd64 and arm64 entries present) was resolved with
`docker buildx imagetools inspect golang:1.27.4-bookworm` on 2026-11-27; the
runtime digest is unchanged from its resolution on 2026-11-25. To update, resolve
the new digest the same way, change the Dockerfile, rebuild, rescan and record
the digests in the run report.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `the Docker daemon is not reachable` | Start Docker Desktop (or the Docker service) and retry. |
| `kind 0.31.0 is required (found ...)` | Install exactly v0.31.0; the node image pinned in the config is only supported by that release. |
| `the existing cluster '<name>' was NOT changed` plus a list of differences | The cluster does not match the config. Use another name, or, with the owner's approval, `make kind-down KIND_CLUSTER_NAME=<name>` and then `make kind-up`. |
| `refusing to recreate '<name>' because its state could not be read` | The cluster exists but kubectl cannot read it (Docker paused, cluster stopped). Fix that first; nothing was changed. |
| kubectl talks to the wrong cluster | Always pass `--context kind-<name>`; check `kubectl config current-context`. |
| `could not restore your kubectl current context` | kubectl refused `use-context` or `unset`. Run the command the message prints, then check `kubectl config current-context`. The cluster itself was created or checked. |
| Smoke runner: `some child is lost, uncollected or unregistered, so the workdir <path> was kept for inspection` | The runner could not confirm that every child it started was collected, so it kept its private directory and did not delete it. Read the `children:` line in the output for each child's state. Before you stop anything or remove the directory, see "Manual inspection" below. |
| Kind script: `some child is lost, uncollected or unregistered, so the private workdir <path> was kept for inspection`, together with `<tool>: lost ownership (<reason>); it was not signalled again and may still be running` or `<tool>: not collected within the reap deadline; cleanup is incomplete and it may still be running` | Same meaning for the kind script's own tools: ownership was lost or collection was not confirmed, the tool may still be running, and the directory was kept. See "Manual inspection" below. |
| `<name> ignored SIGTERM and had to be killed` (kind script; the smoke runner reports the same condition in its failure list) | This is **timeout escalation, not lost ownership**: the runner sent SIGTERM, waited its bounded interval, and because the child was still running sent SIGKILL and then collected it. Escalation happens only when needed; a child that exits after SIGTERM is never sent SIGKILL. The run fails, but the child is collected. |
| Kind script: `<command> did not finish within <N> s and was stopped`, optionally followed by `; it could not be collected, so cleanup is incomplete` | The command exceeded its deadline and the runner stopped it with bounded escalation (SIGTERM first, SIGKILL only if it was still running). The suffix confirms that collection was **not** completed. **Its absence does not prove the child was collected**: a child whose ownership was lost is also finished from the runner's point of view and carries no suffix. Always read the final ownership diagnostics; a `lost ownership (...)` or `not collected within the reap deadline` line overrides any "was stopped" wording, and the directory-kept message means collection was not confirmed. Treat those as the lost/uncollected case above. |
| `Trivy 0.76.2 is required` or `SCAN_TRIVY_VERSION is not set` | Install the pinned Trivy and run through `make mock-image-scan`. |
| scan: `NOT a pass` / `INVALID REPORT` | The database could not be downloaded or the report is unusable. Check network access to `mirror.gcr.io`/`ghcr.io` and rerun; a failed scan is never reported as clean. |
| scan: `GATE FAILED` | A HIGH or CRITICAL finding has a fix. Update the base image or Go version and rebuild. |
| `$VAR is set; it could change what Trivy reports` | Unset that `TRIVY_*` variable (only `TRIVY_CACHE_DIR` and `TRIVY_NO_PROGRESS` are allowed). |
| `kind create` fails or nodes stay NotReady | Too little Docker memory or CPU, or cgroup v1. Raise Docker's limits and retry; do not edit the topology to make it fit. |

### Manual inspection after a kept directory or a lost child

The runner never recovers a child it no longer owns, never signals a process
number found in a log, and never deletes a directory recursively. If you decide to
act yourself, first **positively confirm** that the thing you are about to stop or
remove is the one the failed run created: for a process, that its command line and
start time match that run and it is not something else that reused the number; for
a directory, that it is exactly the path the message printed, under your temporary
directory, and holds only the files the tool writes. If you cannot confirm that,
leave it alone and report it. Do not stop a process or delete a directory on a
guess.

## Limitations

- Local only; one tested host (macOS arm64). No Linux or amd64 cluster claim.
- Labels and the taint are placement simulation, not isolation (no NetworkPolicy
  enforcement, no Pod Security, no policy engine yet).
- Process supervision is described in [ADR-007](adr/ADR-007-owned-process-supervision.md);
  this is a non-exhaustive summary and the ADR is authoritative. It covers only the
  **direct children** that the smoke runner or the kind script starts. Each runner is
  the only waiter and signaller for those children and never signals a child it has
  already collected or lost. Not covered: the runner itself being killed (SIGKILL, a
  crash, host failure or permanent suspension), after which nothing cleans up and
  its model servers may keep running until stopped by hand; a child still
  uncollectible after SIGKILL, which fails the run as incomplete cleanup;
  grandchildren, daemonizing processes, reparenting tricks, other processes that
  wait on the same children, an inherited SIGCHLD override, and hostile same-user
  code. The `go build` preflight is a separate step with its own deadline, and the
  Go compiler processes it starts are not tracked or given a kill guarantee. The
  bounded deadlines apply to responsive userspace operations, not to a stalled
  kernel.
- The private temporary directories are removed by file descriptor, with a
  nonrecursive `rmdir`, and only when exactly the expected files are present. Any
  surprise keeps the directory and fails the run. A hostile same-user rename
  between the final check and the `rmdir` is outside the supported threat model.
- The persistent Python kind owner (`scripts/kind_cluster.py`) was verified with
  stub tools only. A live kind lifecycle with the current Python owner is
  **pending acceptance**; earlier live evidence recorded for the original Bash
  script does not cover it. The smoke runner was verified with a real local mock
  model. CI creates no kind cluster.
- Scan results are a point-in-time record against the database downloaded at
  scan time and say nothing about future findings or about unscanned images.
- The kind binary from Homebrew is not byte-identical to the upstream release
  asset; the script enforces the version, not a binary hash
  ([`deploy/kind/README.md`](../deploy/kind/README.md)).
- The sigstore bundle published with the Trivy release was not verified (no
  `cosign` in the tested environment); the tarball checksum and the release's
  immutable flag were.
