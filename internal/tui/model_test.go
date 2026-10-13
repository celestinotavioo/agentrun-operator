package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/scenarios"
)

func fixture() snapshot {
	var s snapshot
	for i, sc := range scenarios.All {
		r := scenarios.Build(sc.Name, "", "agents-demo")
		r.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Duration(i) * time.Minute))
		r.Status = agentsv1.AgentRunStatus{
			Phase: agentsv1.PhaseRunning, Attempt: 2, ActiveModel: "gemini-flash-latest", ModelIndex: 1,
			TokensUsed: 123456, CostMicroUSD: 312_000, CostUSD: "$0.3120", Quality: "0.42",
			Message: strings.Repeat("a very long controller message ", 10),
			Answer:  strings.Repeat("a very long answer ", 20),
			Attempts: []agentsv1.AttemptRecord{
				{Number: 1, Model: "gemini-pro-latest", Outcome: agentsv1.OutcomeLowQuality, Reason: strings.Repeat("why ", 40), Tokens: 4000, CostUSD: "$0.0200", Quality: "0.31"},
				{Number: 2, Model: "gemini-flash-latest"},
			},
		}
		s.runs = append(s.runs, *r)
		s.events = append(s.events, eventsv1.Event{
			Regarding: corev1.ObjectReference{Kind: "AgentRun", Name: r.Name},
			Reason:    "FallingBack", Note: strings.Repeat("note ", 50),
		})
	}
	return s
}

func TestViewNeverOverflowsTheTerminal(t *testing.T) {
	for _, width := range []int{60, 100, 160} {
		m := New(Options{Namespace: "agents-demo", KubeContext: "docker-desktop"})
		m.width, m.height, m.loaded, m.snap = width, 50, true, fixture()
		m.selected = m.snap.runs[0].Name
		for i, line := range strings.Split(m.View(), "\n") {
			if w := ansi.StringWidth(line); w > width {
				t.Errorf("width %d: line %d is %d cells wide: %q", width, i, w, ansi.Strip(line))
			}
		}
	}
}

func TestEmptyStateExplainsWhatToDo(t *testing.T) {
	m := New(Options{Namespace: "agents-demo", KubeContext: "docker-desktop"})
	m.loaded = true
	if v := ansi.Strip(m.View()); !strings.Contains(v, "make demo") {
		t.Fatalf("empty dashboard should tell the user how to start:\n%s", v)
	}
}
