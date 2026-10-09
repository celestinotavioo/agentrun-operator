package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Provider selects which LLM backend the sandboxed agent talks to.
// +kubebuilder:validation:Enum=mock;gemini
type Provider string

const (
	ProviderMock   Provider = "mock"
	ProviderGemini Provider = "gemini"
)

// AgentRunPhase is a coarse summary of where a run is in its lifecycle.
// +kubebuilder:validation:Enum=Pending;Running;Retrying;FallingBack;Succeeded;Failed;BudgetExceeded
type AgentRunPhase string

const (
	PhasePending        AgentRunPhase = "Pending"
	PhaseRunning        AgentRunPhase = "Running"
	PhaseRetrying       AgentRunPhase = "Retrying"
	PhaseFallingBack    AgentRunPhase = "FallingBack"
	PhaseSucceeded      AgentRunPhase = "Succeeded"
	PhaseFailed         AgentRunPhase = "Failed"
	PhaseBudgetExceeded AgentRunPhase = "BudgetExceeded"
)

// IsTerminal reports whether the controller has nothing left to converge.
func (p AgentRunPhase) IsTerminal() bool {
	return p == PhaseSucceeded || p == PhaseFailed || p == PhaseBudgetExceeded
}

// MCPTool is an allow-listed MCP server the agent may call.
type MCPTool struct {
	// Name of the MCP server, e.g. "github" or "k8s-readonly".
	// +kubebuilder:validation:MinLength=1
	Name string `json:"mcp"`
}

// Budget caps what a run may spend, across all attempts.
type Budget struct {
	// MaxTokens is the total token budget (input + output) for the whole run.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=200000
	// +optional
	MaxTokens int64 `json:"maxTokens,omitempty"`

	// MaxCostUSD is the dollar ceiling for the whole run, as a decimal string.
	// +kubebuilder:validation:Pattern=`^\d+(\.\d{1,6})?$`
	// +kubebuilder:default="0.50"
	// +optional
	MaxCostUSD string `json:"maxCostUSD,omitempty"`
}

// RetryPolicy bounds attempts and defines the quality gate.
type RetryPolicy struct {
	// MaxAttempts across all models (retries and fallbacks both consume one).
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +kubebuilder:default=3
	// +optional
	MaxAttempts int32 `json:"maxAttempts,omitempty"`

	// QualityThreshold is the minimum eval score (0..1) for an answer to count as a success.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +kubebuilder:default=0.7
	// +optional
	QualityThreshold float64 `json:"qualityThreshold,omitempty"`

	// BackoffSeconds is the base for exponential backoff between transient retries.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=3
	// +optional
	BackoffSeconds int32 `json:"backoffSeconds,omitempty"`
}

// Sandbox tunes the isolation of the attempt Jobs.
type Sandbox struct {
	// RuntimeClassName, e.g. "gvisor". Leave empty on clusters without one (Docker Desktop, kind).
	// +optional
	RuntimeClassName *string `json:"runtimeClassName,omitempty"`
}

// AgentRunSpec is the desired state: what to ask, with which model, under which limits.
type AgentRunSpec struct {
	// +kubebuilder:default=mock
	// +optional
	Provider Provider `json:"provider,omitempty"`

	// Model is the primary model.
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// FallbackModels are tried in order when quality drops or the model fails hard.
	// +optional
	FallbackModels []string `json:"fallbackModels,omitempty"`

	// +kubebuilder:validation:MinLength=1
	Prompt string `json:"prompt"`

	// +optional
	Tools []MCPTool `json:"tools,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Budget Budget `json:"budget,omitempty"`

	// Timeout for the whole run, across all attempts.
	// +kubebuilder:default="10m"
	// +optional
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// +kubebuilder:default={}
	// +optional
	RetryPolicy RetryPolicy `json:"retryPolicy,omitempty"`

	// +optional
	Sandbox Sandbox `json:"sandbox,omitempty"`
}

// AttemptOutcome classifies how a single attempt ended.
type AttemptOutcome string

const (
	OutcomeSucceeded      AttemptOutcome = "Succeeded"
	OutcomeLowQuality     AttemptOutcome = "LowQuality"
	OutcomeTransient      AttemptOutcome = "TransientError"
	OutcomeFailed         AttemptOutcome = "Failed"
	OutcomeBudgetExceeded AttemptOutcome = "BudgetExceeded"
	OutcomeTimedOut       AttemptOutcome = "TimedOut"
)

// AttemptRecord is one append-only entry of the run ledger.
type AttemptRecord struct {
	Number  int32          `json:"number"`
	Model   string         `json:"model"`
	Job     string         `json:"job"`
	Outcome AttemptOutcome `json:"outcome,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	Tokens int64 `json:"tokens,omitempty"`
	// +optional
	CostUSD string `json:"costUSD,omitempty"`
	// +optional
	Quality string `json:"quality,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// AgentRunStatus is the observed state, written only by the controller.
type AgentRunStatus struct {
	// +optional
	Phase AgentRunPhase `json:"phase,omitempty"`
	// +optional
	Attempt int32 `json:"attempt,omitempty"`
	// +optional
	ActiveModel string `json:"activeModel,omitempty"`
	// +optional
	ModelIndex int32 `json:"modelIndex,omitempty"`
	// +optional
	TokensUsed int64 `json:"tokensUsed,omitempty"`
	// CostMicroUSD is the exact spend in millionths of a dollar; CostUSD is its display form.
	// +optional
	CostMicroUSD int64 `json:"costMicroUSD,omitempty"`
	// +optional
	CostUSD string `json:"costUSD,omitempty"`
	// +optional
	Quality string `json:"quality,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	Answer string `json:"answer,omitempty"`
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// +optional
	NextAttemptAt *metav1.Time `json:"nextAttemptAt,omitempty"`
	// +optional
	Attempts []AttemptRecord `json:"attempts,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ar;ars,categories=agents
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.status.activeModel`
// +kubebuilder:printcolumn:name="Attempt",type=integer,JSONPath=`.status.attempt`
// +kubebuilder:printcolumn:name="Tokens",type=integer,JSONPath=`.status.tokensUsed`
// +kubebuilder:printcolumn:name="Cost",type=string,JSONPath=`.status.costUSD`
// +kubebuilder:printcolumn:name="Quality",type=string,JSONPath=`.status.quality`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentRun is a single, bounded, observable execution of an AI agent.
type AgentRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRunSpec   `json:"spec,omitempty"`
	Status AgentRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentRunList contains a list of AgentRun.
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRun{}, &AgentRunList{})
}

// Models returns the primary model followed by the fallback chain.
func (s *AgentRunSpec) Models() []string {
	return append([]string{s.Model}, s.FallbackModels...)
}
