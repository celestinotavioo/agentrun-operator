// Package scenarios builds ready-made AgentRuns for demos and workshops.
package scenarios

import (
	"fmt"
	"math/rand/v2"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/llm"
	"github.com/celestinotavioo/agentrun-operator/internal/protocol"
)

// Info describes a scenario for help texts.
type Info struct {
	Name, Story, Expect string
}

// All scenarios, in the order a demo tells the story.
var All = []Info{
	{llm.ScenarioHappy, "the golden path", "Succeeded on attempt 1"},
	{llm.ScenarioFlaky, "the API rate-limits us twice (429)", "Retrying ×2 with backoff → Succeeded"},
	{llm.ScenarioHallucinate, "the primary model invents a kubectl flag", "QualityGate fails → FallingBack → Succeeded on flash"},
	{llm.ScenarioBudgetBuster, "the agent loops on its own context", "kill switch → BudgetExceeded"},
	{llm.ScenarioDoomed, "every model returns broken tool calls", "FallingBack until no models left → Failed"},
	{llm.ScenarioRandom, "real life: non-deterministic", "anything can happen"},
}

// Lookup finds a scenario by name.
func Lookup(name string) (Info, bool) {
	for _, s := range All {
		if s.Name == name {
			return s, true
		}
	}
	return Info{}, false
}

// Build returns an AgentRun for the scenario. An empty name generates one.
func Build(scenario, name, namespace string) *agentsv1.AgentRun {
	if name == "" {
		name = fmt.Sprintf("%s-%04x", scenario, rand.IntN(0xffff))
	}
	return &agentsv1.AgentRun{
		TypeMeta: metav1.TypeMeta{APIVersion: agentsv1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: map[string]string{protocol.ScenarioAnnotation: scenario},
			Labels:      map[string]string{"agents.devfest.dev/scenario": scenario},
		},
		Spec: agentsv1.AgentRunSpec{
			Provider:       agentsv1.ProviderMock,
			Model:          "gemini-pro-latest",
			FallbackModels: []string{"gemini-flash-latest", "gemini-flash-lite-latest"},
			Prompt:         "Triage GitHub issue 4211: payments API returns 500 after the last deploy.",
			Tools:          []agentsv1.MCPTool{{Name: "github"}, {Name: "k8s-readonly"}},
			Budget:         agentsv1.Budget{MaxTokens: 200000, MaxCostUSD: "0.50"},
			Timeout:        metav1.Duration{Duration: 10 * time.Minute},
			RetryPolicy:    agentsv1.RetryPolicy{MaxAttempts: 4, QualityThreshold: 0.7, BackoffSeconds: 3},
		},
	}
}
