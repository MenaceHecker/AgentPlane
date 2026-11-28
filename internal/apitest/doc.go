// Package apitest holds the schema admission tests for the agentplane.io/v1alpha1 CRDs (P1-T1).
//
// The tests live in _test files and run against a disposable local kube-apiserver and etcd started
// by controller-runtime's envtest (make test-envtest, with ENVTEST_K8S_VERSION 1.35.0 or 1.37.0).
// They never contact an existing cluster and never read a kubeconfig. Without AGENTPLANE_ENVTEST=1
// every test reports a skip; make test-envtest sets it and treats any skip as a failure.
//
// What these tests establish is schema admission only: required fields, bounds, enums, scope, the
// status subresource and list semantics. They say nothing about reconciliation, webhooks, quota or
// registry policy, or runtime budget enforcement, none of which exist in this task.
package apitest
