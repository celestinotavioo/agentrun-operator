package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/scenarios"
)

func finishedStory() snapshot {
	var s snapshot
	mk := func(name, scenario string, phase agentsv1.AgentRunPhase, model, q string, cost int64, attempts ...agentsv1.AttemptRecord) {
		r := scenarios.Build(scenario, name, "agents-demo")
		r.Status = agentsv1.AgentRunStatus{Phase: phase, ActiveModel: model, Quality: q, CostMicroUSD: cost,
			Attempt: int32(len(attempts)), Attempts: attempts}
		s.runs = append(s.runs, *r)
	}
	a := func(o agentsv1.AttemptOutcome, q string) agentsv1.AttemptRecord {
		return agentsv1.AttemptRecord{Outcome: o, Quality: q, Model: "gemini-pro-latest"}
	}
	mk("golden-path", "happy", agentsv1.PhaseSucceeded, "gemini-pro-latest", "0.91", 20_000, a(agentsv1.OutcomeSucceeded, "0.91"))
	mk("rate-limited", "flaky", agentsv1.PhaseSucceeded, "gemini-pro-latest", "0.88", 22_000,
		a(agentsv1.OutcomeTransient, ""), a(agentsv1.OutcomeTransient, ""), a(agentsv1.OutcomeSucceeded, "0.88"))
	mk("hallucination", "hallucinate", agentsv1.PhaseSucceeded, "gemini-flash-latest", "0.87", 23_000,
		a(agentsv1.OutcomeLowQuality, "0.33"), a(agentsv1.OutcomeSucceeded, "0.87"))
	mk("runaway-agent", "budget-buster", agentsv1.PhaseBudgetExceeded, "gemini-pro-latest", "", 503_000, a(agentsv1.OutcomeBudgetExceeded, ""))
	mk("broken-models", "doomed", agentsv1.PhaseFailed, "gemini-flash-lite-latest", "", 2_000,
		a(agentsv1.OutcomeFailed, ""), a(agentsv1.OutcomeFailed, ""), a(agentsv1.OutcomeFailed, ""))
	return s
}

func newShow(width int) Show {
	s := Show{Model: New(Options{Namespace: "agents-demo", KubeContext: "docker-desktop"}), auto: true}
	s.width, s.height, s.loaded, s.snap, s.launched = width, 50, true, finishedStory(), len(Acts)
	return s
}

func TestShowNeverOverflows(t *testing.T) {
	for _, width := range []int{80, 120, 180} {
		for _, report := range []bool{false, true} {
			s := newShow(width)
			s.report = report
			for i, line := range strings.Split(s.View(), "\n") {
				if w := ansi.StringWidth(line); w > width {
					t.Errorf("width %d report=%v: line %d is %d wide: %q", width, report, i, w, ansi.Strip(line))
				}
			}
		}
	}
}

func TestScoreboardCountsWhatTheOperatorDid(t *testing.T) {
	sc := newShow(120).score()
	if sc.retries != 2 || sc.hallucinations != 1 || sc.abandoned != 3 || sc.attempts != 10 || sc.stoppedMicro != 503_000 || sc.done != 5 {
		t.Fatalf("unexpected score %+v", sc)
	}
}

func TestReportCardUsesRealNumbers(t *testing.T) {
	s := newShow(180)
	s.report = true
	v := ansi.Strip(s.View())
	for _, want := range []string{"q=0.33", "fallback to gemini-flash (q=0.87)", "killed at $0.50", "2 retries with backoff", "tried 3 models", "accountable"} {
		if !strings.Contains(v, want) {
			t.Errorf("report card misses %q:\n%s", want, v)
		}
	}
}

func TestReportOpensWhenTheStoryEnds(t *testing.T) {
	s := newShow(120)
	m, _ := s.Update(tickMsg{})
	if !m.(Show).report {
		t.Fatal("report card should open once every act is terminal")
	}
}

func TestPrintShow(t *testing.T) {
	if testing.Short() || !strings.Contains(strings.Join(os.Args, " "), "PrintShow") {
		t.Skip("run with -run PrintShow -v to eyeball the screens")
	}
	for _, w := range []int{90, 120, 140} {
		for _, report := range []bool{false, true} {
			s := newShow(w)
			s.report = report
			t.Logf("\n===== width %d report=%v =====\n%s", w, report, ansi.Strip(s.View()))
		}
	}
}
