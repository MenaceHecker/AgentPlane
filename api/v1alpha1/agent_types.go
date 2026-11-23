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
	// Image is the agent container image. Phase 1 intent: must come from the
	// approved registry and not use the latest tag (FR-POL-1/3, enforced by
	// admission policy in Phase 3, not by this type).
	Image string `json:"image"`
	// Replicas is the desired pod count. Absent means the controller default;
	// an explicit 0 is a real value.
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
	// Model is the backend model name the gateway routes to, for example
	// "mock-large".
	Model string `json:"model"`
	// Tools is the Agent's tool allowlist. Phase 2 intent (FR-GW-6): a tool
	// call is permitted only if the tool is in both this list and the
	// Tenant's egress.allowedTools. Empty or absent allows no tools.
	// +optional
	Tools []string `json:"tools,omitempty"`
	// Budget is the Agent's token and request budget. Phase 1 intent: not
	// above its Tenant's budget (FR-POL-2, webhook); the gateway enforces
	// both the agent and the aggregate tenant scope (ADR-004).
	Budget Budget `json:"budget"`
	// Resources are the container resource requests and limits.
	Resources corev1.ResourceRequirements `json:"resources"`
	// SandboxClass is the isolation class, for example "hardened" (ADR-003).
	// +optional
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

// Agent is a namespaced resource created by a tenant.
//
// Phase 1 intent: namespaced CRD with a status subresource.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

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
