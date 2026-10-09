package controller

import (
	"fmt"
	"math"
	"strconv"
	"time"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/protocol"
)

// Action is what the controller does after an attempt finishes.
type Action string

const (
	ActSucceed  Action = "Succeed"
	ActRetry    Action = "Retry"
	ActFallback Action = "Fallback"
	ActFail     Action = "Fail"
	ActBudget   Action = "BudgetExceeded"
)

// Decision is the outcome of Decide.
type Decision struct {
	Action     Action
	Outcome    agentsv1.AttemptOutcome
	Reason     string
	ModelIndex int32
	Backoff    time.Duration
}

const maxBackoff = 60 * time.Second

// Classify turns the agent's raw result into an attempt outcome, applying the quality gate.
func Classify(res protocol.Result, threshold float64) agentsv1.AttemptOutcome {
	switch res.Outcome {
	case protocol.OutcomeOK:
		if res.Quality < threshold {
			return agentsv1.OutcomeLowQuality
		}
		return agentsv1.OutcomeSucceeded
	case protocol.OutcomeTransient:
		return agentsv1.OutcomeTransient
	case protocol.OutcomeBudget:
		return agentsv1.OutcomeBudgetExceeded
	default:
		return agentsv1.OutcomeFailed
	}
}

// Decide is the whole policy of the talk, as a pure function:
// budget first, then quality, then retry (transient) vs. fallback (everything else).
// status must already include the usage of the attempt that just finished.
func Decide(spec *agentsv1.AgentRunSpec, status *agentsv1.AgentRunStatus, outcome agentsv1.AttemptOutcome, reason string) Decision {
	d := Decision{Outcome: outcome, ModelIndex: status.ModelIndex}

	if over, why := OverBudget(spec, status); over || outcome == agentsv1.OutcomeBudgetExceeded {
		if why == "" {
			why = reason
		}
		d.Action, d.Reason = ActBudget, why
		return d
	}

	attemptsLeft := status.Attempt < maxAttempts(spec)
	models := spec.Models()
	hasNextModel := int(status.ModelIndex)+1 < len(models)

	switch outcome {
	case agentsv1.OutcomeSucceeded:
		d.Action, d.Reason = ActSucceed, fmt.Sprintf("quality %s ≥ %.2f", status.Quality, threshold(spec))
		return d

	case agentsv1.OutcomeTransient, agentsv1.OutcomeTimedOut:
		if !attemptsLeft {
			d.Action, d.Reason = ActFail, fmt.Sprintf("attempts exhausted (%d/%d): %s", status.Attempt, maxAttempts(spec), reason)
			return d
		}
		d.Action, d.Reason, d.Backoff = ActRetry, reason, Backoff(spec, status.Attempt)
		return d

	default: // LowQuality, Failed
		why := reason
		if outcome == agentsv1.OutcomeLowQuality {
			why = fmt.Sprintf("quality %s < %.2f", status.Quality, threshold(spec))
		}
		switch {
		case !hasNextModel:
			d.Action, d.Reason = ActFail, "no models left: "+why
		case !attemptsLeft:
			d.Action, d.Reason = ActFail, fmt.Sprintf("attempts exhausted (%d/%d): %s", status.Attempt, maxAttempts(spec), why)
		default:
			d.Action, d.Reason, d.ModelIndex = ActFallback, why, status.ModelIndex+1
		}
		return d
	}
}

// OverBudget reports whether the run already spent its token or dollar budget.
func OverBudget(spec *agentsv1.AgentRunSpec, status *agentsv1.AgentRunStatus) (bool, string) {
	if max := spec.Budget.MaxTokens; max > 0 && status.TokensUsed >= max {
		return true, fmt.Sprintf("spent %d of %d tokens", status.TokensUsed, max)
	}
	if max := MaxCostMicro(spec); max > 0 && status.CostMicroUSD >= max {
		return true, fmt.Sprintf("spent %s of %s", protocol.FormatUSD(status.CostMicroUSD), protocol.FormatUSD(max))
	}
	return false, ""
}

// Remaining returns how many tokens and micro-dollars the next attempt may spend (0 = unlimited).
func Remaining(spec *agentsv1.AgentRunSpec, status *agentsv1.AgentRunStatus) (tokens, micro int64) {
	if spec.Budget.MaxTokens > 0 {
		tokens = max(spec.Budget.MaxTokens-status.TokensUsed, 1)
	}
	if m := MaxCostMicro(spec); m > 0 {
		micro = max(m-status.CostMicroUSD, 1)
	}
	return tokens, micro
}

// MaxCostMicro parses the dollar budget. Invalid values are rejected by the CRD schema.
func MaxCostMicro(spec *agentsv1.AgentRunSpec) int64 {
	if spec.Budget.MaxCostUSD == "" {
		return 0
	}
	f, err := strconv.ParseFloat(spec.Budget.MaxCostUSD, 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(f * 1e6))
}

// Backoff is exponential in the attempt number, capped at a minute.
func Backoff(spec *agentsv1.AgentRunSpec, attempt int32) time.Duration {
	base := time.Duration(spec.RetryPolicy.BackoffSeconds) * time.Second
	if base <= 0 {
		return 0
	}
	d := base << max(attempt-1, 0)
	if d > maxBackoff || d <= 0 {
		return maxBackoff
	}
	return d
}

func maxAttempts(spec *agentsv1.AgentRunSpec) int32 {
	if spec.RetryPolicy.MaxAttempts <= 0 {
		return 3
	}
	return spec.RetryPolicy.MaxAttempts
}

func threshold(spec *agentsv1.AgentRunSpec) float64 {
	return spec.RetryPolicy.QualityThreshold
}
