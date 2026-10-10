package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/llm"
	"github.com/celestinotavioo/agentrun-operator/internal/protocol"
	"github.com/celestinotavioo/agentrun-operator/internal/scenarios"
)

// Act is one beat of the on-stage story.
type Act struct {
	Run, Scenario, Short, Title, Narration string
	Color                                  lipgloss.Color
}

// Acts tell one story: an incident, five things that go wrong, one operator handling all of them.
var Acts = []Act{
	{"golden-path", llm.ScenarioHappy, "golden path", "The golden path", "One AgentRun in → one sandboxed Job → answer scored by an eval → Succeeded.", cGreen},
	{"rate-limited", llm.ScenarioFlaky, "rate limit", "The API rate-limits us", "429 twice → the controller retries the SAME model with exponential backoff. Bounded, not forever.", cYellow},
	{"hallucination", llm.ScenarioHallucinate, "hallucination", "The model invents a kubectl flag", "The eval scores it low → quality gate fails → fallback to the next model. The bad answer never ships.", cPink},
	{"runaway-agent", llm.ScenarioBudgetBuster, "runaway agent", "An agent loops on its own context", "Tokens are a resource: it hits its dollar budget and the kill switch stops it.", cRed},
	{"broken-models", llm.ScenarioDoomed, "broken models", "Every model is broken", "Fallback after fallback until no models are left → Failed. A decision, not a crash loop.", cBlue},
}

const actInterval = 9 * time.Second

type actLaunched struct {
	idx int
	err error
}

// Show is the presenter mode: the dashboard plus a story, a live scoreboard and a finale.
type Show struct {
	Model
	launched  int
	auto      bool
	nextAt    time.Time
	report    bool
	reportSet bool
}

// RunShow starts the presenter mode.
func RunShow(opts Options) error {
	m := Show{Model: New(opts), auto: true}
	m.details = false
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (s Show) Init() tea.Cmd { return tea.Batch(s.Model.Init(), s.launchNext()) }

func (s Show) launchNext() tea.Cmd {
	idx := s.launched
	if idx >= len(Acts) {
		return nil
	}
	c, ns := s.opts.Client, s.opts.Namespace
	act := Acts[idx]
	return func() tea.Msg {
		run := scenarios.Build(act.Scenario, act.Run, ns)
		err := c.Create(context.Background(), run)
		if apierrors.IsAlreadyExists(err) {
			err = nil
		}
		return actLaunched{idx: idx, err: err}
	}
}

func (s Show) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case actLaunched:
		if msg.err != nil {
			s.flash, s.flashAt = "could not launch "+Acts[msg.idx].Run+": "+msg.err.Error(), time.Now()
			return s, nil
		}
		if msg.idx+1 > s.launched {
			s.launched = msg.idx + 1
			s.selected = Acts[msg.idx].Run
		}
		s.nextAt = time.Now().Add(actInterval)
		return s, s.fetch()
	case tickMsg:
		var cmds []tea.Cmd
		if s.auto && s.launched > 0 && s.launched < len(Acts) && time.Now().After(s.nextAt) {
			s.nextAt = time.Now().Add(actInterval)
			cmds = append(cmds, s.launchNext())
		}
		if !s.reportSet && s.launched == len(Acts) && s.allDone() {
			s.report, s.reportSet = true, true
		}
		mm, cmd := s.Model.Update(msg)
		s.Model = mm.(Model)
		return s, tea.Batch(append(cmds, cmd)...)
	case tea.KeyMsg:
		switch msg.String() {
		case " ", "n", "right":
			if s.launched < len(Acts) {
				return s, s.launchNext()
			}
			s.report = !s.report
			return s, nil
		case "p":
			s.auto = !s.auto
			s.nextAt = time.Now().Add(actInterval)
			return s, nil
		case "r":
			s.report = !s.report
			return s, nil
		case "enter":
			s.details = !s.details
			return s, nil
		case "q", "ctrl+c", "esc":
			return s, tea.Quit
		case "up", "k", "down", "j":
			mm, cmd := s.Model.Update(msg)
			s.Model = mm.(Model)
			return s, cmd
		}
		return s, nil
	}
	mm, cmd := s.Model.Update(msg)
	s.Model = mm.(Model)
	return s, cmd
}

func (s Show) run(name string) *agentsv1.AgentRun {
	for i := range s.snap.runs {
		if s.snap.runs[i].Name == name {
			return &s.snap.runs[i]
		}
	}
	return nil
}

func (s Show) allDone() bool {
	for _, a := range Acts {
		r := s.run(a.Run)
		if r == nil || !r.Status.Phase.IsTerminal() {
			return false
		}
	}
	return true
}

// ---------- view ----------

func (s Show) View() string {
	if s.report {
		return s.reportView()
	}
	var b strings.Builder
	b.WriteString(s.header() + "\n\n")
	if s.snap.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(cRed).Render("  ✘ "+s.snap.err.Error()) + "\n")
		b.WriteString(sSubtle.Render("  is the operator deployed? run: make demo") + "\n")
		return b.String()
	}
	b.WriteString(s.incident() + "\n")
	b.WriteString(s.actStrip() + "\n\n")
	if s.loaded {
		b.WriteString(s.table())
		if sel := s.selectedRun(); sel != nil && s.details {
			b.WriteString("\n" + s.detail(sel) + "\n")
		}
	}
	b.WriteString("\n" + s.scoreboard() + "\n\n")
	b.WriteString(s.eventFeed())
	b.WriteString("\n" + s.showFooter())
	return b.String()
}

func (s Show) incident() string {
	w := min(s.width-2, 150)
	title := lipgloss.NewStyle().Foreground(cRed).Bold(true).Render("🚨 INCIDENT #4211") +
		sBold.Render("  payments API returns 500 after the last deploy")
	body := sSubtle.Render("On-call asks AI agents to triage it. Five things go wrong — watch the operator handle each.")
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cRed).Padding(0, 1).Width(w)
	return box.Render(s.fitW(title, w-4) + "\n" + s.fitW(body, w-4))
}

func (s Show) fitW(str string, w int) string {
	m := s.Model
	m.width = w + 1
	return m.fit(str)
}

func (s Show) actStrip() string {
	var parts []string
	for i, a := range Acts {
		dot, style := "◌", sDim
		if i < s.launched {
			dot, style = "●", lipgloss.NewStyle().Foreground(a.Color).Bold(true)
			if r := s.run(a.Run); r != nil && !r.Status.Phase.IsTerminal() {
				dot = []string{"◐", "◓", "◑", "◒"}[s.frame%4]
			}
		}
		parts = append(parts, style.Render(fmt.Sprintf("%s %d %s", dot, i+1, a.Short)))
	}
	line := "  " + strings.Join(parts, sDim.Render(" › "))
	cur := s.launched - 1
	if cur < 0 {
		return s.fit(line) + "\n" + s.fit(sSubtle.Render("  starting the story…"))
	}
	a := Acts[cur]
	title := lipgloss.NewStyle().Foreground(a.Color).Bold(true).Render(fmt.Sprintf("ACT %d · %s", cur+1, strings.ToUpper(a.Title)))
	narr := lipgloss.NewStyle().Width(max(s.width-5, 20)).Render(title + "  " + a.Narration)
	var lines []string
	for _, l := range strings.Split(narr, "\n") {
		lines = append(lines, s.fit("  "+strings.TrimRight(l, " ")))
	}
	return s.fit(line) + "\n" + strings.Join(lines, "\n")
}

type score struct {
	retries, hallucinations, abandoned, attempts int
	stoppedMicro, spentMicro                     int64
	done                                         int
}

func (s Show) score() score {
	var sc score
	for _, r := range s.snap.runs {
		sc.spentMicro += r.Status.CostMicroUSD
		if r.Status.Phase.IsTerminal() {
			sc.done++
		}
		if r.Status.Phase == agentsv1.PhaseBudgetExceeded {
			sc.stoppedMicro += r.Status.CostMicroUSD
		}
		for _, a := range r.Status.Attempts {
			sc.attempts++
			switch a.Outcome {
			case agentsv1.OutcomeTransient, agentsv1.OutcomeTimedOut:
				sc.retries++
			case agentsv1.OutcomeLowQuality:
				sc.hallucinations++
			case agentsv1.OutcomeFailed:
				sc.abandoned++
			}
		}
	}
	return sc
}

func (s Show) scoreboard() string {
	sc := s.score()
	chip := func(c lipgloss.Color, n, label string) string {
		return lipgloss.NewStyle().Foreground(c).Bold(true).Render(n) + " " + sSubtle.Render(label)
	}
	line := "  " + strings.Join([]string{
		chip(cYellow, fmt.Sprint(sc.retries), "429s absorbed"),
		chip(cPink, fmt.Sprint(sc.hallucinations), "bad answers blocked"),
		chip(cRed, trimUSD(sc.stoppedMicro), "runaway spend stopped"),
		chip(cBlue, fmt.Sprint(sc.abandoned), "broken attempts dropped"),
		chip(cGreen, fmt.Sprint(sc.attempts), "sandboxed attempts"),
	}, sDim.Render("  │  "))
	return sHeader.Render("  WHAT THE OPERATOR HANDLED") + "\n" + s.fit(line)
}

func (s Show) showFooter() string {
	mode := lipgloss.NewStyle().Foreground(cGreen).Render("auto")
	if !s.auto {
		mode = lipgloss.NewStyle().Foreground(cYellow).Render("paused")
	}
	next := "space next act"
	if s.launched >= len(Acts) {
		next = "space report card"
	}
	keys := []string{sKey.Render("space") + strings.TrimPrefix(next, "space"), sKey.Render("p") + " auto/pause (" + mode + ")",
		sKey.Render("r") + " report", sKey.Render("enter") + " details", sKey.Render("q") + " quit"}
	line := s.fit("  " + strings.Join(keys, sDim.Render("  ·  ")))
	if s.flash != "" && time.Since(s.flashAt) < 5*time.Second {
		line += "\n" + s.fit("  "+lipgloss.NewStyle().Foreground(cRed).Render("» "+s.flash))
	}
	return line
}

// ---------- finale ----------

func (s Show) reportView() string {
	w := min(s.width-4, 132)
	g := lipgloss.NewStyle().Foreground(cGreen).Bold(true)
	r := lipgloss.NewStyle().Foreground(cRed)
	col1 := 24
	col2 := (w - col1) * 2 / 5
	col3 := w - col1 - col2 - 2

	stacked := w < 110
	row := func(what, naive, op string) string {
		if stacked {
			return sBold.Render(what) + "\n" +
				"   " + r.Render("✘ ") + sSubtle.Render(naive) + "\n" +
				"   " + g.Render("✔ ") + op
		}
		return pad(sBold.Render(trunc(what, col1-1)), col1) +
			pad(r.Render("✘ ")+trunc(naive, col2-3), col2) + "  " +
			g.Render("✔ ") + trunc(op, col3-3)
	}

	rate, hall, runaway, broken := s.run("rate-limited"), s.run("hallucination"), s.run("runaway-agent"), s.run("broken-models")
	sc := s.score()

	rateOp := "…"
	if rate != nil && rate.Status.Phase.IsTerminal() {
		rateOp = fmt.Sprintf("%d retries with backoff → %s", countOutcome(rate, agentsv1.OutcomeTransient), rate.Status.Phase)
	}
	hallNaive, hallOp := "ships whatever the model said", "…"
	if hall != nil && len(hall.Status.Attempts) > 0 {
		if q := hall.Status.Attempts[0].Quality; q != "" {
			hallNaive = "ships the hallucinated answer (q=" + q + ")"
		}
		if hall.Status.Phase.IsTerminal() {
			hallOp = fmt.Sprintf("blocked it, fell back to %s (q=%s)", hall.Status.ActiveModel, orDash(hall.Status.Quality))
		}
	}
	runOp := "…"
	if runaway != nil && runaway.Status.Phase.IsTerminal() {
		runOp = fmt.Sprintf("killed at %s of a %s budget", trimUSD(runaway.Status.CostMicroUSD), "$"+runaway.Spec.Budget.MaxCostUSD)
	}
	brokenOp := "…"
	if broken != nil && broken.Status.Phase.IsTerminal() {
		brokenOp = fmt.Sprintf("tried %d models, then %s with a reason", broken.Status.Attempt, broken.Status.Phase)
	}

	var b strings.Builder
	b.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(cWhite).Background(cNavy).Padding(0, 2).Render("REPORT CARD · incident #4211") + "\n\n")
	if stacked {
		b.WriteString(r.Bold(true).Render("✘ the agent.py loop (slide 4)") + "   vs   " + g.Render("✔ the AgentRun operator") + "\n")
	} else {
		b.WriteString(pad("", col1) + pad(r.Bold(true).Render("the agent.py loop (slide 4)"), col2) + "  " + g.Render("the AgentRun operator") + "\n")
	}
	b.WriteString(sDim.Render(strings.Repeat("─", w)) + "\n")
	rows := [][3]string{
		{"API rate limit (429)", "retries every 5s, forever", rateOp},
		{"hallucinated answer", hallNaive, hallOp},
		{"runaway context loop", "no ceiling — keeps billing", runOp},
		{"every model broken", "crash-loops on the same model", brokenOp},
		{"blast radius", "your credentials, your network", fmt.Sprintf("%d attempts: non-root, read-only, no token, no egress", sc.attempts)},
		{"audit trail", "print()", "status ledger + Events + Prometheus"},
	}
	for _, rw := range rows {
		sep := "\n\n"
		if stacked {
			sep = "\n"
		}
		b.WriteString(row(rw[0], rw[1], rw[2]) + sep)
	}
	b.WriteString(sDim.Render(strings.Repeat("─", w)) + "\n")
	b.WriteString(fmt.Sprintf("%s %s   %s %d/%d runs reached a terminal phase   %s %s\n\n",
		sSubtle.Render("total spent"), sBold.Render(protocol.FormatUSD(sc.spentMicro)),
		sSubtle.Render("·"), sc.done, len(s.snap.runs),
		sSubtle.Render("·"), sSubtle.Render("every decision: kubectl get events")))
	b.WriteString(lipgloss.NewStyle().Italic(true).Render("  “Kubernetes won’t make your agent ") + lipgloss.NewStyle().Bold(true).Render("smarter") +
		lipgloss.NewStyle().Italic(true).Render(". It makes it ") + lipgloss.NewStyle().Bold(true).Foreground(cRed).Render("accountable") +
		lipgloss.NewStyle().Italic(true).Render(".”") + "\n\n")
	b.WriteString(sDim.Render("  r back to the dashboard · q quit"))

	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		lines[i] = s.fit("  " + l)
	}
	return strings.Join(lines, "\n")
}

func countOutcome(r *agentsv1.AgentRun, o agentsv1.AttemptOutcome) int {
	n := 0
	for _, a := range r.Status.Attempts {
		if a.Outcome == o {
			n++
		}
	}
	return n
}
