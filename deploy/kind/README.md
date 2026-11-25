# Local kind topology and pins

`config.yaml` defines the P0-T5 local cluster: one control plane, one `system`
worker and one `tenant` worker. The tenant worker carries the taint
`agentplane.io/pool=tenant:NoSchedule`. This simulates the placement model
(requirements section 3.3) and **is not tenant isolation**: kind's default CNI
does not enforce NetworkPolicy, and a taint only keeps pods without a toleration
off a node. Isolation is verified in later phases.

## Pins and provenance

| Item | Pin | Source | Checked |
| --- | --- | --- | --- |
| kind | v0.31.0 (`KIND_VERSION` in the Makefile; the script refuses any other) | https://github.com/kubernetes-sigs/kind/releases/tag/v0.31.0, published 2025-12-18 | see below |
| Node image | `kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f` | The "Images pre-built for this release" list in the v0.31.0 release notes; the notes state it is the default node image for that release, supports amd64 and arm64 (the host platform must match), and that the `@sha256` digest must be used | copied from the release notes through the GitHub API on 2026-11-25; the `internal/kindshape` tests require every node image to be digest-pinned and identical |
| kubeadm config API | `kubeadm.k8s.io/v1beta3` patch | kind v0.31.0 release notes: kind uses v1beta3 for Kubernetes 1.23+; a v1beta4 patch will be needed for a future kind | not testable without a newer kind |
| kubectl | v1.36.0 client on the tested host (within the supported one-minor skew of the 1.35.0 cluster) | `kubectl version --client` | recorded in `agents/runs/P0-T5.md` |

kind release asset checksum for `kind-darwin-arm64` published with the release:
`88bf554fe9da6311c9f8c2d082613c002911a476f6b5090e9420b35d84e70c5c`. The kind
binary on the tested host was installed with Homebrew (`kind 0.31.0`) and
reports `kind v0.31.0`, but **its SHA-256 differs** from the upstream release
asset (`19b50bfe8f348d9054076bb1e81c2da28d7cd0a56f8ba42b9d667b051c216109`),
because Homebrew builds it from source. The version, not the binary hash, is
what the script enforces. To use the upstream binary instead, download
`kind-<os>-<arch>` and its `.sha256sum` from the release page and verify with
`shasum -a 256 -c`.

Cgroup v2 is required for Kubernetes 1.35 node images (kind v0.31.0 release
notes); Docker Desktop and current Linux distributions provide it.

## Changing a pin

Pins are changed deliberately: update `KIND_VERSION`, the three `image:` lines in
`config.yaml` (they must all be identical, digest-pinned, from the new kind
release notes) and this table, then rerun `make kind-config-test`. The topology
(node count, pools, taint) is asserted by `TestCommittedConfigIsTheApprovedTopology`,
so changing it also requires changing that test, which is a visible decision.
