package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
)

// Google palette, as on the slides.
var (
	cBlue   = lipgloss.Color("#4285F4")
	cRed    = lipgloss.Color("#EA4335")
	cYellow = lipgloss.Color("#F9AB00")
	cGreen  = lipgloss.Color("#34A853")
	cPink   = lipgloss.Color("#FF7DAF")
	cCyan   = lipgloss.Color("#57CAFF")
	cNavy   = lipgloss.Color("#20344B")
	cGray   = lipgloss.Color("#9AA0A6")
	cDim    = lipgloss.Color("#5F6368")
	cWhite  = lipgloss.Color("#FFFFFF")

	sTitle  = lipgloss.NewStyle().Bold(true).Foreground(cWhite).Background(cNavy).Padding(0, 1)
	sSubtle = lipgloss.NewStyle().Foreground(cGray)
	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sBold   = lipgloss.NewStyle().Bold(true)
	sHeader = lipgloss.NewStyle().Bold(true).Foreground(cGray)
	sPanel  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cDim).Padding(0, 1)
	sKey    = lipgloss.NewStyle().Foreground(cCyan).Bold(true)
)

func phaseColor(p agentsv1.AgentRunPhase) lipgloss.Color {
	switch p {
	case agentsv1.PhaseRunning:
		return cBlue
	case agentsv1.PhaseRetrying:
		return cYellow
	case agentsv1.PhaseFallingBack:
		return cPink
	case agentsv1.PhaseSucceeded:
		return cGreen
	case agentsv1.PhaseFailed, agentsv1.PhaseBudgetExceeded:
		return cRed
	default:
		return cGray
	}
}

func phaseIcon(p agentsv1.AgentRunPhase, frame int) string {
	switch p {
	case agentsv1.PhaseRunning:
		return []string{"◐", "◓", "◑", "◒"}[frame%4]
	case agentsv1.PhaseRetrying:
		return "⟳"
	case agentsv1.PhaseFallingBack:
		return "⤵"
	case agentsv1.PhaseSucceeded:
		return "✔"
	case agentsv1.PhaseFailed:
		return "✘"
	case agentsv1.PhaseBudgetExceeded:
		return "$"
	default:
		return "…"
	}
}

func phaseBadge(p agentsv1.AgentRunPhase, frame int) string {
	if p == "" {
		p = agentsv1.PhasePending
	}
	return lipgloss.NewStyle().Foreground(phaseColor(p)).Bold(true).Render(fmt.Sprintf("%s %s", phaseIcon(p, frame), p))
}

func outcomeDot(o agentsv1.AttemptOutcome) string {
	switch o {
	case agentsv1.OutcomeSucceeded:
		return lipgloss.NewStyle().Foreground(cGreen).Render("●")
	case agentsv1.OutcomeLowQuality:
		return lipgloss.NewStyle().Foreground(cPink).Render("●")
	case agentsv1.OutcomeTransient, agentsv1.OutcomeTimedOut:
		return lipgloss.NewStyle().Foreground(cYellow).Render("●")
	case agentsv1.OutcomeBudgetExceeded:
		return lipgloss.NewStyle().Foreground(cRed).Render("◆")
	case agentsv1.OutcomeFailed:
		return lipgloss.NewStyle().Foreground(cRed).Render("●")
	default:
		return ""
	}
}

func attemptDots(run *agentsv1.AgentRun, frame int) string {
	var b strings.Builder
	for _, a := range run.Status.Attempts {
		if a.Outcome == "" {
			b.WriteString(lipgloss.NewStyle().Foreground(cBlue).Render([]string{"◉", "○"}[frame%2]))
			continue
		}
		b.WriteString(outcomeDot(a.Outcome))
	}
	max := int(run.Spec.RetryPolicy.MaxAttempts)
	if max == 0 {
		max = 3
	}
	if !run.Status.Phase.IsTerminal() {
		for i := len(run.Status.Attempts); i < max; i++ {
			b.WriteString(sDim.Render("◌"))
		}
	}
	return b.String()
}

func budgetBar(spentMicro, maxMicro int64, width int) string {
	if maxMicro <= 0 {
		return sDim.Render(strings.Repeat("·", width))
	}
	ratio := float64(spentMicro) / float64(maxMicro)
	if ratio > 1 {
		ratio = 1
	}
	fill := int(ratio*float64(width) + 0.5)
	color := cGreen
	switch {
	case ratio >= 0.9:
		color = cRed
	case ratio >= 0.6:
		color = cYellow
	}
	return lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", fill)) +
		sDim.Render(strings.Repeat("░", width-fill))
}

func qualityText(q string, threshold float64) string {
	if q == "" {
		return sDim.Render("  –")
	}
	var f float64
	fmt.Sscanf(q, "%f", &f)
	c := cGreen
	if f < threshold {
		c = cPink
	}
	return lipgloss.NewStyle().Foreground(c).Render(q)
}
