// Package protocol is the contract between the sandboxed agent and the controller.
//
// The agent reports its result through the Pod termination message (/dev/termination-log),
// so no sidecar, volume or extra network access is needed to get usage back to the controller.
package protocol

import (
	"encoding/json"
	"fmt"
)

// Exit codes of the agent container.
const (
	ExitOK        = 0
	ExitFailed    = 1
	ExitTransient = 75 // EX_TEMPFAIL: worth retrying on the same model
	ExitBudget    = 76 // the agent stopped itself because it hit the budget
)

// Outcome as reported by the agent. Quality gating happens in the controller.
type Outcome string

const (
	OutcomeOK        Outcome = "ok"
	OutcomeTransient Outcome = "transient"
	OutcomeFailed    Outcome = "failed"
	OutcomeBudget    Outcome = "budget"
)

// Env vars the controller sets on every attempt Job.
const (
	EnvRunName      = "AGENTRUN_NAME"
	EnvProvider     = "AGENTRUN_PROVIDER"
	EnvModel        = "AGENTRUN_MODEL"
	EnvModelIndex   = "AGENTRUN_MODEL_INDEX"
	EnvAttempt      = "AGENTRUN_ATTEMPT"
	EnvPrompt       = "AGENTRUN_PROMPT"
	EnvTools        = "AGENTRUN_TOOLS"
	EnvMaxTokens    = "AGENTRUN_REMAINING_TOKENS"
	EnvMaxCostMicro = "AGENTRUN_REMAINING_COST_MICRO_USD"
	EnvScenario     = "AGENTRUN_MOCK_SCENARIO"
	EnvSpeed        = "AGENTRUN_MOCK_SPEED"
)

// ScenarioAnnotation lets a demo pick a deterministic mock behaviour.
const ScenarioAnnotation = "agents.devfest.dev/mock-scenario"

// TerminationLogPath is where Kubernetes reads the termination message from.
const TerminationLogPath = "/dev/termination-log"

// maxMessage stays below the 4096-byte termination message limit.
const maxMessage = 3800

// Result is what one attempt produced.
type Result struct {
	Outcome      Outcome `json:"o"`
	Reason       string  `json:"r,omitempty"`
	Model        string  `json:"m"`
	InputTokens  int64   `json:"in"`
	OutputTokens int64   `json:"out"`
	CostMicroUSD int64   `json:"c"`
	Quality      float64 `json:"q"`
	Answer       string  `json:"a,omitempty"`
}

// Tokens is the total token usage of the attempt.
func (r Result) Tokens() int64 { return r.InputTokens + r.OutputTokens }

// Encode serialises the result, trimming the answer so it fits the termination message.
func (r Result) Encode() []byte {
	for {
		b, _ := json.Marshal(r)
		if len(b) <= maxMessage || r.Answer == "" {
			return b
		}
		cut := len(r.Answer) - (len(b) - maxMessage) - 3
		if cut <= 0 {
			r.Answer = ""
			continue
		}
		r.Answer = truncateRunes(r.Answer, cut) + "…"
	}
}

// Decode parses a termination message.
func Decode(msg string) (Result, error) {
	var r Result
	if err := json.Unmarshal([]byte(msg), &r); err != nil {
		return r, fmt.Errorf("decoding agent result: %w", err)
	}
	if r.Outcome == "" {
		return r, fmt.Errorf("agent result has no outcome")
	}
	return r, nil
}

func truncateRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && (s[maxBytes]&0xC0) == 0x80 {
		maxBytes--
	}
	return s[:maxBytes]
}

// FormatUSD renders micro-dollars as a short dollar string.
func FormatUSD(micro int64) string {
	return fmt.Sprintf("$%.4f", float64(micro)/1e6)
}
