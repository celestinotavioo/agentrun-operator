// Command agent is the workload of every AgentRun attempt. It runs inside a sandboxed Job,
// talks to one model, and reports usage + quality through the Pod termination message.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/celestinotavioo/agentrun-operator/internal/llm"
	"github.com/celestinotavioo/agentrun-operator/internal/protocol"
)

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	blue   = "\033[38;5;33m"
	green  = "\033[38;5;35m"
	yellow = "\033[38;5;214m"
	red    = "\033[38;5;203m"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string) int64 {
	n, _ := strconv.ParseInt(os.Getenv(k), 10, 64)
	return n
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	attempt, _ := strconv.Atoi(env(protocol.EnvAttempt, "1"))
	modelIdx, _ := strconv.Atoi(env(protocol.EnvModelIndex, "0"))
	speed, _ := strconv.ParseFloat(env(protocol.EnvSpeed, "1"), 64)
	var tools []string
	if t := os.Getenv(protocol.EnvTools); t != "" {
		tools = strings.Split(t, ",")
	}
	req := llm.Request{
		Model:          env(protocol.EnvModel, "gemini-flash-latest"),
		Prompt:         env(protocol.EnvPrompt, "Say hello"),
		Tools:          tools,
		Attempt:        attempt,
		ModelIndex:     modelIdx,
		RemainingTok:   envInt(protocol.EnvMaxTokens),
		RemainingMicro: envInt(protocol.EnvMaxCostMicro),
		Scenario:       os.Getenv(protocol.EnvScenario),
		Speed:          speed,
		Step: func(icon, f string, a ...any) {
			fmt.Printf("  %s%s%s  %s %s\n", dim, time.Now().Format("15:04:05"), reset, icon, fmt.Sprintf(f, a...))
		},
	}
	providerName := env(protocol.EnvProvider, "mock")

	fmt.Printf("\n%s╭─ AgentRun %s · attempt %d%s\n", bold, env(protocol.EnvRunName, "?"), attempt, reset)
	fmt.Printf("%s│%s  model    %s%s%s\n", bold, reset, blue, req.Model, reset)
	fmt.Printf("%s│%s  provider %s · tools %v\n", bold, reset, providerName, tools)
	fmt.Printf("%s│%s  budget   %d tokens · %s left\n", bold, reset, req.RemainingTok, protocol.FormatUSD(req.RemainingMicro))
	fmt.Printf("%s╰─%s\n", bold, reset)

	res := protocol.Result{Model: req.Model}
	p, err := llm.New(providerName, os.Getenv(protocol.EnvGeminiKey))
	if err != nil {
		finish(res, protocol.OutcomeFailed, err.Error())
	}

	resp, err := p.Run(ctx, req)
	res.InputTokens, res.OutputTokens = resp.InputTokens, resp.OutputTokens
	res.Quality = resp.Quality
	res.Answer = resp.Text
	if err != nil {
		e := llm.Classify(err)
		res.InputTokens, res.OutputTokens = max(res.InputTokens, e.Usage.InputTokens), max(res.OutputTokens, e.Usage.OutputTokens)
		res.CostMicroUSD = llm.CostMicroUSD(req.Model, res.InputTokens, res.OutputTokens)
		switch e.Kind {
		case llm.KindTransient:
			finish(res, protocol.OutcomeTransient, e.Reason)
		case llm.KindBudget:
			finish(res, protocol.OutcomeBudget, e.Reason)
		default:
			finish(res, protocol.OutcomeFailed, e.Reason)
		}
	}
	res.CostMicroUSD = llm.CostMicroUSD(req.Model, res.InputTokens, res.OutputTokens)
	finish(res, protocol.OutcomeOK, "")
}

func finish(res protocol.Result, outcome protocol.Outcome, reason string) {
	res.Outcome, res.Reason = outcome, reason
	color, code, label := green, protocol.ExitOK, "DONE"
	switch outcome {
	case protocol.OutcomeTransient:
		color, code, label = yellow, protocol.ExitTransient, "TRANSIENT"
	case protocol.OutcomeBudget:
		color, code, label = red, protocol.ExitBudget, "BUDGET"
	case protocol.OutcomeFailed:
		color, code, label = red, protocol.ExitFailed, "FAILED"
	}
	fmt.Printf("\n  %s%s● %s%s", bold, color, label, reset)
	if reason != "" {
		fmt.Printf("  %s", reason)
	}
	fmt.Printf("\n  tokens %d in / %d out · cost %s · quality %.2f\n", res.InputTokens, res.OutputTokens, protocol.FormatUSD(res.CostMicroUSD), res.Quality)
	if res.Answer != "" {
		fmt.Printf("  %sanswer%s %s\n", dim, reset, res.Answer)
	}
	if err := os.WriteFile(protocol.TerminationLogPath, res.Encode(), 0o644); err != nil {
		// Running outside Kubernetes: print the result instead.
		fmt.Printf("  %sresult%s %s\n", dim, reset, res.Encode())
	}
	os.Exit(code)
}
