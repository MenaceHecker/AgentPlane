# Controller and API development (P1-T1)

This page covers the Kubernetes API scaffold: the generated Tenant and Agent CRDs, the schema
admission tests, and the controller manager startup scaffold. Nothing here reconciles anything.

## What exists, and what does not

| Exists (P1-T1) | Does not exist yet |
| --- | --- |
| `agentplane.io/v1alpha1` Go types with generated deepcopy code | Tenant or Agent reconciliation, finalizers, owned objects |
| Generated CRDs `config/crd/bases/` (Tenant cluster-scoped, Agent namespaced, status subresource) | Real status, conditions or events (the schema only defines their shape) |
| Schema admission: required fields, bounds, the sandbox enum, list-map conditions, printer columns | The cross-resource webhook (agent budget not above tenant budget, P1-T4), TLS or certificates |
| A manager that starts, serves loopback health and metrics, and stops | Any controller, watch, RBAC role, leader election (P1-T7) or deployment |

Schema admission only checks the shape of an object. Compute quantities are checked to be
quantity-shaped, not sensible: a negative CPU quantity or an empty `resources` object is accepted by
the schema, and the tests say so explicitly. Registry, latest-tag and quota policy belong to later
tasks. Labels and CRDs say nothing about tenant isolation.

## Pinned tools

| Tool | Pin |
| --- | --- |
| Go | 1.27.4 (`go.mod`; `GOTOOLCHAIN=local` is forced by the scripts) |
| controller-gen | controller-tools v0.23.0 |
| setup-envtest | controller-runtime tools/setup-envtest at commit `18149e6fafab1fe3966fd13a65c1d4273558afef` (module v0.25.3) |
| controller-runtime / Kubernetes Go modules | v0.25.3 / v0.37.0 |
| envtest Kubernetes versions | exactly 1.35.0 (the kind API target) and 1.37.0 (the library minor) |
| Kubebuilder | v4.16.0 (used once, in a scratch directory, to derive `PROJECT`; `kubebuilder init` is never run in this repository) |

`make tools` installs controller-gen and setup-envtest into the ignored `bin/` with
`go install <module>@<version>` (Go's checksum database verifies the modules) and checks the
resolved versions. Nothing is installed globally.

## Everyday commands

```bash
make tools          # once: pinned controller-gen and setup-envtest into ./bin
make generate       # deepcopy code
make manifests      # CRDs into config/crd/bases
make check-generated  # regenerate and fail if api/ or config/crd/ differ from the committed files
make test-envtest ENVTEST_K8S_VERSION=1.35.0
make test-envtest ENVTEST_K8S_VERSION=1.37.0
make build          # ./bin/manager only; it is never run or deployed by make
```

Edit the markers in `api/v1alpha1/*_types.go`, run `make generate manifests`, and commit the
generated files together with the change; CI runs `make check-generated`.

## The envtest tests

`make test-envtest` starts a disposable kube-apiserver and etcd on 127.0.0.1 from official envtest
assets, installs the generated CRDs into it, and runs `internal/apitest`. Details that matter:

- **Never an existing cluster.** The target refuses `USE_EXISTING_CLUSTER`, unsets `KUBECONFIG`, the
  fixture checks that the API server address is loopback, and the tests use only the `rest.Config`
  envtest returns.
- **Assets are verified.** setup-envtest downloads the archive for the host; the script then checks
  the SHA-512 of `kube-apiserver` and `etcd` against values pinned from the official release (taken
  after verifying each archive against its published `.sha512`). A platform without a pinned value
  is reported as blocked, not run. Pinned today: darwin/arm64 and linux/amd64.
- **A skipped test is a failure.** Without `AGENTPLANE_ENVTEST=1` (set only by the make target)
  every admission test reports a skip, so plain `make test` passes quickly and the real evidence
  comes from `make test-envtest`, which fails on any skip or when nothing passed.
- **Raw requests.** Negative cases post raw JSON so a field that is omitted really is omitted; a
  typed Go value would serialize a zero in its place.
- **Teardown.** The API server is stopped by `TestMain` on every normal exit, a stop error fails the
  run, and every test converts a panic into a failure so a crashing test cannot skip the stop. These
  processes are children of the `go test` binary, **not** of the ownership library behind the
  smoke runner (ADR-007), so the ADR's guarantees do not apply to them. If the test binary is killed
  (SIGKILL, a crash outside a test, a host failure) the API server and etcd can keep running. The
  script runs the tests under a private temporary directory and, afterwards, looks (read-only) for
  processes whose command line contains it; it reports and fails but does **not** kill anything. If
  it names processes, check them and stop them yourself.

## The manager scaffold

```bash
make build
bin/manager --kubeconfig <path>      # or run in-cluster
```

`bin/manager` never reads `$KUBECONFIG` or `~/.kube/config`: it needs an explicit `--kubeconfig`
or an in-cluster configuration, so starting it by accident cannot reach whichever cluster your
current context names. Do not point it at a cluster you care about: it installs nothing and
reconciles nothing, but it is not a supported deployment.

- Metrics (`--metrics-bind-address`, default `127.0.0.1:8082`, plain HTTP) and health
  (`--health-probe-bind-address`, default `127.0.0.1:8083`, `/healthz` and `/readyz`) accept only a
  loopback address; the two must differ. A port that is already in use fails the start.
- `/readyz` reports that the manager and its cache have started. It says nothing about any Tenant
  or Agent being reconciled, because nothing reconciles them.
- Leader election is off. It is enabled and tested with the deployed manager in P1-T7.
- No webhook, certificate, Role, ClusterRole or Deployment is created by this scaffold.
