package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types shared by Tenant and Agent status (requirements 4.3).
// A condition's presence in status.conditions describes controller-observed
// state; in Phase 0 no controller exists, so these are names only and do not
// establish any real runtime status.
const (
	// ConditionReady reports that the resource's owned objects are ready.
	ConditionReady = "Ready"
	// ConditionReconciled reports that the latest spec generation was reconciled.
	ConditionReconciled = "Reconciled"
	// ConditionBudgetExhausted reports that a token budget is exhausted.
	ConditionBudgetExhausted = "BudgetExhausted"
	// ConditionPolicyViolation reports an admission or policy violation.
	ConditionPolicyViolation = "PolicyViolation"
)

// Budget is a token and request budget (requirements 4.1 and 4.2).
//
// Semantics from ADR-005: TokensPerDay is counted per fixed UTC day, and
// 0 is a real limit of zero (all requests rejected), never "unlimited". Both
// fields are always serialized, so an explicit 0 survives a round trip and an
// absent field is distinguishable in the raw JSON.
//
// Phase 1 intent: both fields required and non-negative; an Agent's budget
// must not exceed its Tenant's (FR-POL-2).
type Budget struct {
	// TokensPerDay is the daily token cap for the fixed UTC budget window.
	TokensPerDay int64 `json:"tokensPerDay"`
	// RequestsPerMinute is the request rate limit (token-bucket refill rate
	// per minute; burst semantics are in ADR-005).
	RequestsPerMinute int32 `json:"requestsPerMinute"`
}

// TenantQuota is the compute quota a Tenant controller would turn into a
// ResourceQuota (Phase 1).
type TenantQuota struct {
	// CPU is the total CPU quota, for example "4".
	CPU resource.Quantity `json:"cpu"`
	// Memory is the total memory quota, for example "8Gi".
	Memory resource.Quantity `json:"memory"`
	// Pods is the maximum number of pods.
	Pods int32 `json:"pods"`
}

// TenantEgress lists what a Tenant's agents may reach through the gateway.
type TenantEgress struct {
	// AllowedTools is the Tenant's tool allowlist. An empty or absent list
	// allows no tools. Phase 2 intent (FR-GW-6): a tool call is permitted only
	// if the tool is in both the Tenant's AllowedTools and the Agent's Tools.
	// +optional
	AllowedTools []string `json:"allowedTools,omitempty"`
}

// TenantSpec is the desired state of a Tenant.
type TenantSpec struct {
	// DisplayName is a human-readable name.
	DisplayName string `json:"displayName"`
	// Quota is the compute quota.
	Quota TenantQuota `json:"quota"`
	// Budget is the Tenant-wide token and request budget; agent budgets are
	// bounded by it and the gateway also enforces the aggregate (ADR-004).
	Budget Budget `json:"budget"`
	// Egress is the tool allowlist.
	Egress TenantEgress `json:"egress"`
}

// TenantStatus is the observed state of a Tenant. In Phase 0 nothing writes
// it.
type TenantStatus struct {
	// ObservedGeneration is the metadata.generation the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions use the Condition types defined in this package.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Tenant is a cluster-scoped resource created by a platform admin.
//
// Phase 1 intent: CRD scope Cluster with a status subresource.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type Tenant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TenantSpec   `json:"spec,omitempty"`
	Status TenantStatus `json:"status,omitempty"`
}

// TenantList is a list of Tenants.
//
// +kubebuilder:object:root=true
type TenantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Tenant `json:"items"`
}
