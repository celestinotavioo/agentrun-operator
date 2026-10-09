package llm

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

// Scenarios make demos deterministic. "random" is the honest one: LLMs are non-deterministic.
const (
	ScenarioRandom       = "random"
	ScenarioHappy        = "happy"
	ScenarioFlaky        = "flaky"
	ScenarioHallucinate  = "hallucinate"
	ScenarioBudgetBuster = "budget-buster"
	ScenarioDoomed       = "doomed"
)

// Scenarios lists every mock scenario, for help texts and validation.
var Scenarios = []string{ScenarioRandom, ScenarioHappy, ScenarioFlaky, ScenarioHallucinate, ScenarioBudgetBuster, ScenarioDoomed}

// Mock simulates an LLM agent loop: thinking, MCP tool calls, an answer and an eval score.
type Mock struct {
	// Rand is injectable for tests.
	Rand *rand.Rand
}

type profile struct {
	quality   float64
	outTokens int64
	think     time.Duration
}

func profileFor(model string) profile {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "flash-lite"):
		return profile{0.58, 700, 600 * time.Millisecond}
	case strings.Contains(m, "flash"):
		return profile{0.80, 900, 900 * time.Millisecond}
	case strings.Contains(m, "pro"):
		return profile{0.88, 1600, 1600 * time.Millisecond}
	default:
		return profile{0.72, 1000, 1 * time.Second}
	}
}

func (m *Mock) rng() *rand.Rand {
	if m.Rand == nil {
		m.Rand = rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 42))
	}
	return m.Rand
}

func (m *Mock) sleep(ctx context.Context, req Request, d time.Duration) error {
	speed := req.Speed
	if speed <= 0 {
		speed = 1
	}
	t := time.NewTimer(time.Duration(float64(d) / speed))
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (m *Mock) step(req Request, icon, f string, a ...any) {
	if req.Step != nil {
		req.Step(icon, f, a...)
	}
}

// Run executes one simulated attempt.
func (m *Mock) Run(ctx context.Context, req Request) (Response, error) {
	r := m.rng()
	p := profileFor(req.Model)
	scenario := req.Scenario
	if scenario == "" {
		scenario = ScenarioRandom
	}
	usage := Response{InputTokens: int64(len(req.Prompt)/4) + 1200}

	m.step(req, "🧠", "planning with %s (scenario: %s)", req.Model, scenario)
	if err := m.sleep(ctx, req, p.think); err != nil {
		return usage, Transient("timed out while planning", usage)
	}

	// Failures that happen before any tool call.
	switch scenario {
	case ScenarioFlaky:
		if req.Attempt <= 2 {
			return usage, Transient("429 RESOURCE_EXHAUSTED: quota exceeded for "+req.Model, usage)
		}
	case ScenarioDoomed:
		return usage, Failed("model returned an invalid tool call (schema mismatch)", usage)
	case ScenarioRandom:
		switch x := r.Float64(); {
		case x < 0.15:
			return usage, Transient("503 UNAVAILABLE: the model is overloaded", usage)
		case x < 0.22:
			return usage, Failed("model refused to call tools (safety block)", usage)
		}
	}

	for _, tool := range req.Tools {
		call := toolCall(tool, r)
		m.step(req, "🔧", "mcp:%s → %s", tool, call)
		if err := m.sleep(ctx, req, 500*time.Millisecond+time.Duration(r.IntN(600))*time.Millisecond); err != nil {
			return usage, Transient("timed out during tool call", usage)
		}
		usage.InputTokens += 600 + int64(r.IntN(900))
		m.step(req, "📦", "mcp:%s returned %d results", tool, 1+r.IntN(12))
	}

	m.step(req, "✍️ ", "drafting the answer")
	chunks := 4
	burn := int64(1)
	if scenario == ScenarioBudgetBuster {
		chunks, burn = 12, 40
	}
	for i := 0; i < chunks; i++ {
		if err := m.sleep(ctx, req, p.think/2); err != nil {
			return usage, Transient("timed out while generating", usage)
		}
		usage.OutputTokens += (p.outTokens / 4) * burn
		if scenario == ScenarioBudgetBuster {
			usage.InputTokens += 6000
			m.step(req, "🔁", "re-reading context… %d tokens so far", usage.InputTokens+usage.OutputTokens)
		}
		spent := CostMicroUSD(req.Model, usage.InputTokens, usage.OutputTokens)
		if (req.RemainingTok > 0 && usage.InputTokens+usage.OutputTokens >= req.RemainingTok) ||
			(req.RemainingMicro > 0 && spent >= req.RemainingMicro) {
			m.step(req, "🛑", "budget hit mid-generation — stopping myself")
			return usage, Budget(fmt.Sprintf("stopped at %d tokens / $%.4f: budget exhausted", usage.InputTokens+usage.OutputTokens, float64(spent)/1e6), usage)
		}
	}

	usage.Text = answerFor(req, r)
	m.step(req, "⚖️ ", "running the eval (LLM-as-judge)")
	if err := m.sleep(ctx, req, 400*time.Millisecond); err != nil {
		return usage, Transient("timed out during eval", usage)
	}
	usage.InputTokens += 350
	usage.OutputTokens += 20

	switch scenario {
	case ScenarioHallucinate:
		if req.ModelIndex == 0 {
			usage.Quality = 0.31 + r.Float64()*0.1
			usage.Text = "Run `kubectl agents --fix-everything` and the issue resolves itself. (hallucinated flag)"
		} else {
			usage.Quality = 0.84 + r.Float64()*0.08
		}
	case ScenarioHappy, ScenarioFlaky, ScenarioBudgetBuster:
		usage.Quality = math.Max(p.quality, 0.82) + r.Float64()*0.08
	default:
		usage.Quality = clamp(p.quality + r.NormFloat64()*0.12)
	}
	return usage, nil
}

func clamp(q float64) float64 { return math.Min(0.99, math.Max(0.05, q)) }

func toolCall(tool string, r *rand.Rand) string {
	calls := map[string][]string{
		"github":       {"search_issues(label=bug)", "get_issue(#4211)", "list_comments(#4211)"},
		"k8s-readonly": {"get_pods(ns=payments)", "get_events(ns=payments)", "describe(deploy/api)"},
		"docs":         {"search(\"CrashLoopBackOff\")", "fetch(runbook/api.md)"},
	}
	if c, ok := calls[tool]; ok {
		return c[r.IntN(len(c))]
	}
	return "call(default)"
}

func answerFor(req Request, r *rand.Rand) string {
	answers := []string{
		"Root cause: the api Deployment lost its DB secret after the last rollout. Labelled the issue as `bug`, `p1`, assigned to on-call and linked the runbook.",
		"Triage: duplicate of #4180 (connection pool exhaustion). Closed as duplicate with a pointer to the fix in v2.3.1.",
		"Triage: needs-info. The stack trace points at the payments client; asked the reporter for the request ID and region.",
	}
	return fmt.Sprintf("[%s] %s", req.Model, answers[r.IntN(len(answers))])
}
