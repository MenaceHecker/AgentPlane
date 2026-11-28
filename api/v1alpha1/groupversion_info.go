// Package v1alpha1 contains the Go types for the agentplane.io/v1alpha1 API
// group: the Tenant and Agent resources described in section 4 of the
// requirements.
//
// CRD generation (P1-T1): the +kubebuilder markers on these types are turned
// into the Tenant and Agent CRDs under config/crd/bases by the pinned
// controller-gen (make manifests). The schema validates the shape of each
// object. Reconciliation, the cross-resource webhook, quota and registry
// policy, and any runtime enforcement of budgets are later work; nothing in
// this package enforces them.
//
// +kubebuilder:object:generate=true
// +groupName=agentplane.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group and version used to register these types.
	GroupVersion = schema.GroupVersion{Group: "agentplane.io", Version: "v1alpha1"}

	// SchemeBuilder collects the functions that add this group's types to a
	// runtime.Scheme. Only k8s.io/apimachinery is needed (the API package keeps
	// minimal dependencies, as controller-runtime now recommends); the
	// +kubebuilder markers stay compatible with a Phase 1 scaffold.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group and version to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&Tenant{}, &TenantList{},
		&Agent{}, &AgentList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
