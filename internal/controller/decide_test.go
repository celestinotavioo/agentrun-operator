package controller

import (
	"testing"
	"time"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/protocol"
)

func spec() *agentsv1.AgentRunSpec {
	return &agentsv1.AgentRunSpec{
		Model:          "gemini-pro-latest",
		FallbackModels: []string{"gemini-flash-latest"},
		Budget:         agentsv1.Budget{MaxTokens: 100_000, MaxCostUSD: "0.50"},
		RetryPolicy:    agentsv1.RetryPolicy{MaxAttempts: 3, QualityThreshold: 0.7, BackoffSeconds: 2},
	}
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name       string
		status     agentsv1.AgentRunStatus
		outcome    agentsv1.AttemptOutcome
		wantAction Action
		wantModel  int32
		wantDelay  time.Duration
	}{
		{"success", agentsv1.AgentRunStatus{Attempt: 1, Quality: "0.91"}, agentsv1.OutcomeSucceeded, ActSucceed, 0, 0},
		{"transient retries same model with backoff", agentsv1.AgentRunStatus{Attempt: 1}, agentsv1.OutcomeTransient, ActRetry, 0, 2 * time.Second},
		{"second transient doubles backoff", agentsv1.AgentRunStatus{Attempt: 2}, agentsv1.OutcomeTransient, ActRetry, 0, 4 * time.Second},
		{"transient with no attempts left fails", agentsv1.AgentRunStatus{Attempt: 3}, agentsv1.OutcomeTransient, ActFail, 0, 0},
		{"low quality falls back", agentsv1.AgentRunStatus{Attempt: 1, Quality: "0.35"}, agentsv1.OutcomeLowQuality, ActFallback, 1, 0},
		{"hard failure falls back", agentsv1.AgentRunStatus{Attempt: 1}, agentsv1.OutcomeFailed, ActFallback, 1, 0},
		{"no models left fails", agentsv1.AgentRunStatus{Attempt: 2, ModelIndex: 1}, agentsv1.OutcomeLowQuality, ActFail, 1, 0},
		{"fallback blocked by attempts", agentsv1.AgentRunStatus{Attempt: 3}, agentsv1.OutcomeFailed, ActFail, 0, 0},
		{"budget beats success", agentsv1.AgentRunStatus{Attempt: 1, CostMicroUSD: 600_000}, agentsv1.OutcomeSucceeded, ActBudget, 0, 0},
		{"token budget", agentsv1.AgentRunStatus{Attempt: 1, TokensUsed: 100_000}, agentsv1.OutcomeTransient, ActBudget, 0, 0},
		{"agent stopped itself on budget", agentsv1.AgentRunStatus{Attempt: 1}, agentsv1.OutcomeBudgetExceeded, ActBudget, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(spec(), &tt.status, tt.outcome, "reason")
			if d.Action != tt.wantAction {
				t.Fatalf("action = %s (%s), want %s", d.Action, d.Reason, tt.wantAction)
			}
			if d.ModelIndex != tt.wantModel {
				t.Errorf("model index = %d, want %d", d.ModelIndex, tt.wantModel)
			}
			if d.Backoff != tt.wantDelay {
				t.Errorf("backoff = %s, want %s", d.Backoff, tt.wantDelay)
			}
			if d.Reason == "" {
				t.Error("every decision must explain itself")
			}
		})
	}
}

func TestClassifyAppliesQualityGate(t *testing.T) {
	cases := map[protocol.Result]agentsv1.AttemptOutcome{
		{Outcome: protocol.OutcomeOK, Quality: 0.71}:     agentsv1.OutcomeSucceeded,
		{Outcome: protocol.OutcomeOK, Quality: 0.69}:     agentsv1.OutcomeLowQuality,
		{Outcome: protocol.OutcomeTransient}:             agentsv1.OutcomeTransient,
		{Outcome: protocol.OutcomeBudget}:                agentsv1.OutcomeBudgetExceeded,
		{Outcome: protocol.OutcomeFailed, Quality: 0.99}: agentsv1.OutcomeFailed,
		{Outcome: protocol.Outcome("something-else")}:    agentsv1.OutcomeFailed,
	}
	for res, want := range cases {
		if got := Classify(res, 0.7); got != want {
			t.Errorf("Classify(%+v) = %s, want %s", res, got, want)
		}
	}
}

func TestBackoffIsCapped(t *testing.T) {
	s := spec()
	s.RetryPolicy.BackoffSeconds = 10
	if got := Backoff(s, 10); got != maxBackoff {
		t.Fatalf("Backoff = %s, want cap %s", got, maxBackoff)
	}
	s.RetryPolicy.BackoffSeconds = 0
	if got := Backoff(s, 3); got != 0 {
		t.Fatalf("Backoff with base 0 = %s, want 0", got)
	}
}

func TestRemainingBudget(t *testing.T) {
	s := spec()
	tok, micro := Remaining(s, &agentsv1.AgentRunStatus{TokensUsed: 40_000, CostMicroUSD: 125_000})
	if tok != 60_000 || micro != 375_000 {
		t.Fatalf("Remaining = %d tokens, %d micro; want 60000, 375000", tok, micro)
	}
	tok, micro = Remaining(s, &agentsv1.AgentRunStatus{TokensUsed: 500_000, CostMicroUSD: 900_000})
	if tok != 1 || micro != 1 {
		t.Fatalf("overspent Remaining = %d, %d; want 1, 1 (never 0, which means unlimited)", tok, micro)
	}
}

func TestMaxCostMicro(t *testing.T) {
	for in, want := range map[string]int64{"0.50": 500_000, "1": 1_000_000, "0.000001": 1, "": 0} {
		s := spec()
		s.Budget.MaxCostUSD = in
		if got := MaxCostMicro(s); got != want {
			t.Errorf("MaxCostMicro(%q) = %d, want %d", in, got, want)
		}
	}
}
