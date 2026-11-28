package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// SandboxClassHardened is the baseline isolation class: hardened pods
	// (ADR-003). Kata is a Phase 3 timeboxed experiment and is not a defined
	// class here.
	SandboxClassHardened = "hardened"

	// RolloutStrategyCanary is the progressive delivery strategy named in
	// requirements 4.2 (implemented in Phase 5).
	RolloutStrategyCanary = "canary"
)

// RolloutSLO holds the service-level thresholds canary analysis compares
// against (requirements 4.2, FR-DEL-3). The Phase 0 types only carry the
// values; nothing evaluates them.
type RolloutSLO struct {
	// ErrorRate is the maximum tolerated error rate in the requirements'
	// format, for example "<1%". Parsing and validation are Phase 5 work.
	ErrorRate string `json:"errorRate"`
	// P95LatencyMs is the maximum tolerated p95 latency in milliseconds.
	P95LatencyMs int32 `json:"p95LatencyMs"`
}

// RolloutSpec is the progressive delivery configuration.
type RolloutSpec struct {
	// Strategy is the delivery strategy, for example "canary".
	Strategy string `json:"strategy"`
	// Steps are the traffic weights, in percent, a canary passes through, for
	// example [10, 50, 100].
	Steps []int32 `json:"steps"`
	// SLO are the analysis thresholds.
	SLO RolloutSLO `json:"slo"`
}

// AgentSpec is the desired state of an Agent (requirements 4.2).
type AgentSpec struct {
	// Image is the agent container image. The CRD requires a non-empty value;
	// the approved-registry and no-latest-tag rules (FR-POL-1/3) are admission
	// policy in Phase 3, not part of this schema.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`
	// Replicas is the desired pod count. Absent means the controller default
	// and the CRD sets no default; an explicit 0 is a real value. When present
	// it must be an integer from 0 to the int32 maximum.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	Replicas *int32 `json:"replicas,omitempty"`
	// Model is the backend model name the gateway routes to, for example
	// "mock-large". The CRD requires a non-empty value.
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`
	// Tools is the Agent's tool allowlist. Phase 2 intent (FR-GW-6): a tool
	// call is permitted only if the tool is in both this list and the
	// Tenant's egress.allowedTools. Empty or absent allows no tools.
	// +optional
	Tools []string `json:"tools,omitempty"`
	// Budget is the Agent's token and request budget, required and bounded as
	// described on Budget. That it is not above its Tenant's budget (FR-POL-2)
	// is a later webhook (P1-T4); the gateway enforces both the agent and the
	// aggregate tenant scope (ADR-004).
	Budget Budget `json:"budget"`
	// Resources are the container resource requests and limits. The field must
	// be present and its quantities must be quantity-shaped; whether requests
	// and limits are required, sensible or non-negative is later policy work.
	Resources corev1.ResourceRequirements `json:"resources"`
	// SandboxClass is the isolation class (ADR-003). When set it must be
	// "hardened"; omitted is allowed. Kata is not a class here.
	// +optional
	// +kubebuilder:validation:Enum=hardened
	SandboxClass string `json:"sandboxClass,omitempty"`
	// Rollout is the progressive delivery configuration.
	// +optional
	Rollout *RolloutSpec `json:"rollout,omitempty"`
}

// AgentStatus is the observed state of an Agent. In Phase 0 nothing writes
// it.
type AgentStatus struct {
	// ObservedGeneration is the metadata.generation the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions use the Condition types defined in this package (Ready,
	// Reconciled, BudgetExhausted, PolicyViolation).
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Agent is a namespaced resource created by a tenant. The CRD is
// Namespaced, serves and stores v1alpha1 and has a status subresource.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec   AgentSpec   `json:"spec,omitempty"`
	Status AgentStatus `json:"status,omitempty"`
}

// AgentList is a list of Agents.
//
// +kubebuilder:object:root=true
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}
