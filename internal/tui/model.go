// Package tui is the live terminal dashboard behind `agentctl watch`.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	eventsv1 "k8s.io/api/events/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/controller"
	"github.com/celestinotavioo/agentrun-operator/internal/protocol"
	"github.com/celestinotavioo/agentrun-operator/internal/scenarios"
)

// Options configures the dashboard.
type Options struct {
	Client      client.Client
	Namespace   string
	KubeContext string
	Remote      bool
}

type snapshot struct {
	runs   []agentsv1.AgentRun
	events []eventsv1.Event
	err    error
}

type tickMsg time.Time
type flashMsg string

// Model is the Bubble Tea model.
type Model struct {
	opts     Options
	snap     snapshot
	selected string
	details  bool
	frame    int
	width    int
	height   int
	flash    string
	flashAt  time.Time
	loaded   bool
}

// New builds the dashboard model.
func New(opts Options) Model {
	return Model{opts: opts, details: true, width: 120, height: 40}
}

// Run starts the dashboard in the alternate screen.
func Run(opts Options) error {
	_, err := tea.NewProgram(New(opts), tea.WithAltScreen()).Run()
	return err
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.fetch(), tick()) }

func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) fetch() tea.Cmd {
	c, ns := m.opts.Client, m.opts.Namespace
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		var s snapshot
		var runs agentsv1.AgentRunList
		if err := c.List(ctx, &runs, client.InNamespace(ns)); err != nil {
			s.err = err
			return s
		}
		s.runs = runs.Items
		sort.Slice(s.runs, func(i, j int) bool {
			return s.runs[i].CreationTimestamp.Before(&s.runs[j].CreationTimestamp) ||
				s.runs[i].CreationTimestamp.Equal(&s.runs[j].CreationTimestamp) && s.runs[i].Name < s.runs[j].Name
		})
		var evs eventsv1.EventList
		if err := c.List(ctx, &evs, client.InNamespace(ns)); err == nil {
			for _, e := range evs.Items {
				if e.Regarding.Kind == "AgentRun" {
					s.events = append(s.events, e)
				}
			}
			sort.Slice(s.events, func(i, j int) bool { return eventTime(s.events[i]).Before(eventTime(s.events[j])) })
		}
		return s
	}
}

func eventTime(e eventsv1.Event) time.Time {
	if e.Series != nil && !e.Series.LastObservedTime.IsZero() {
		return e.Series.LastObservedTime.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.frame++
		cmds := []tea.Cmd{tick()}
		if m.frame%4 == 0 {
			cmds = append(cmds, m.fetch())
		}
		return m, tea.Batch(cmds...)
	case snapshot:
		m.snap, m.loaded = msg, true
		if m.selected == "" && len(msg.runs) > 0 {
			m.selected = msg.runs[len(msg.runs)-1].Name
		}
	case flashMsg:
		m.flash, m.flashAt = string(msg), time.Now()
		return m, m.fetch()
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "enter", " ":
			m.details = !m.details
		case "x", "delete":
			return m, m.deleteSelected()
		case "1", "2", "3", "4", "5", "6":
			i := int(msg.String()[0] - '1')
			return m, m.launch(scenarios.All[i].Name)
		case "l":
			return m, m.launch(scenarios.All[m.frame%len(scenarios.All)].Name)
		case "r":
			return m, m.fetch()
		}
	}
	return m, nil
}

func (m *Model) move(delta int) {
	runs := m.snap.runs
	if len(runs) == 0 {
		return
	}
	idx := 0
	for i, r := range runs {
		if r.Name == m.selected {
			idx = i
		}
	}
	idx = min(max(idx+delta, 0), len(runs)-1)
	m.selected = runs[idx].Name
}

func (m Model) launch(scenario string) tea.Cmd {
	c, ns := m.opts.Client, m.opts.Namespace
	return func() tea.Msg {
		run := scenarios.Build(scenario, "", ns)
		if err := c.Create(context.Background(), run); err != nil {
			return flashMsg("launch failed: " + err.Error())
		}
		return flashMsg("launched " + run.Name)
	}
}

func (m Model) deleteSelected() tea.Cmd {
	c, ns, name := m.opts.Client, m.opts.Namespace, m.selected
	return func() tea.Msg {
		if name == "" {
			return flashMsg("nothing selected")
		}
		run := &agentsv1.AgentRun{}
		run.Name, run.Namespace = name, ns
		if err := c.Delete(context.Background(), run); err != nil {
			return flashMsg("delete failed: " + err.Error())
		}
		return flashMsg("deleted " + name)
	}
}

// Describe renders the detail panel of one run, for `agentctl describe`.
func Describe(r *agentsv1.AgentRun) string {
	m := New(Options{})
	m.width = 130
	return phaseBadge(r.Status.Phase, 0) + sSubtle.Render(fmt.Sprintf("  %s · %d tokens · %d attempt(s)", r.Status.CostUSD, r.Status.TokensUsed, r.Status.Attempt)) + "\n" + m.detail(r)
}

// ---------- view ----------

const (
	wName    = 26
	wPhase   = 17
	wModel   = 26
	wAttempt = 11
	wBar     = 12
	wCost    = 17
	wTokens  = 8
	wQual    = 6
	wAge     = 5
)

func pad(s string, w int) string {
	if gap := w - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

func trunc(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:max(w-1, 0)]) + "…"
}

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")

	if m.snap.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(cRed).Render("  ✘ " + m.snap.err.Error()))
		b.WriteString("\n" + sSubtle.Render("  is the cluster up and the CRD installed? try: make doctor") + "\n")
		return b.String()
	}
	if !m.loaded {
		b.WriteString(sSubtle.Render("  connecting…\n"))
		return b.String()
	}
	b.WriteString(m.table())
	if sel := m.selectedRun(); sel != nil && m.details {
		b.WriteString("\n")
		b.WriteString(m.detail(sel))
	}
	b.WriteString("\n")
	b.WriteString(m.eventFeed())
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m Model) header() string {
	counts := map[agentsv1.AgentRunPhase]int{}
	var cost int64
	for _, r := range m.snap.runs {
		counts[r.Status.Phase]++
		cost += r.Status.CostMicroUSD
	}
	active := counts[agentsv1.PhaseRunning] + counts[agentsv1.PhaseRetrying] + counts[agentsv1.PhaseFallingBack] + counts[agentsv1.PhasePending] + counts[""]
	ctxStyle := lipgloss.NewStyle().Foreground(cGreen)
	if m.opts.Remote {
		ctxStyle = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	}
	left := sTitle.Render("⎈ AgentRun Operator") + "  " +
		sSubtle.Render("ctx ") + ctxStyle.Render(m.opts.KubeContext) +
		sSubtle.Render("  ns ") + sBold.Render(m.opts.Namespace)
	right := lipgloss.NewStyle().Foreground(cBlue).Render(fmt.Sprintf("◐ %d active", active)) + "  " +
		lipgloss.NewStyle().Foreground(cGreen).Render(fmt.Sprintf("✔ %d", counts[agentsv1.PhaseSucceeded])) + "  " +
		lipgloss.NewStyle().Foreground(cRed).Render(fmt.Sprintf("✘ %d", counts[agentsv1.PhaseFailed])) + "  " +
		lipgloss.NewStyle().Foreground(cRed).Render(fmt.Sprintf("$ %d", counts[agentsv1.PhaseBudgetExceeded])) + "  " +
		sBold.Render("spent "+protocol.FormatUSD(cost))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 2 {
		return " " + left + "\n " + right
	}
	return " " + left + strings.Repeat(" ", gap) + right
}

type column struct {
	title string
	width int
	prio  int // lower survives longer on narrow terminals
	cell  func(r *agentsv1.AgentRun) string
}

func (m Model) columns() []column {
	nameW := wName
	if m.width < 90 {
		nameW = 20
	}
	all := []column{
		{"NAME", nameW, 0, func(r *agentsv1.AgentRun) string { return sBold.Render(trunc(r.Name, nameW-2)) }},
		{"PHASE", wPhase, 1, func(r *agentsv1.AgentRun) string { return phaseBadge(r.Status.Phase, m.frame) }},
		{"MODEL", wModel, 4, func(r *agentsv1.AgentRun) string { return trunc(orDash(r.Status.ActiveModel), wModel-2) }},
		{"ATTEMPTS", wAttempt, 2, func(r *agentsv1.AgentRun) string { return attemptDots(r, m.frame) }},
		{"BUDGET", wBar + 1, 3, func(r *agentsv1.AgentRun) string {
			return budgetBar(r.Status.CostMicroUSD, controller.MaxCostMicro(&r.Spec), wBar)
		}},
		{"COST", wCost, 6, func(r *agentsv1.AgentRun) string {
			return sSubtle.Render(fmt.Sprintf("%s/%s", trimUSD(r.Status.CostMicroUSD), trimUSD(controller.MaxCostMicro(&r.Spec))))
		}},
		{"TOKENS", wTokens, 7, func(r *agentsv1.AgentRun) string { return humanTokens(r.Status.TokensUsed) }},
		{"QUAL", wQual, 5, func(r *agentsv1.AgentRun) string {
			return qualityText(r.Status.Quality, r.Spec.RetryPolicy.QualityThreshold)
		}},
		{"AGE", wAge, 8, func(r *agentsv1.AgentRun) string { return sDim.Render(age(r.CreationTimestamp.Time)) }},
	}
	byPrio := make([]column, len(all))
	copy(byPrio, all)
	sort.SliceStable(byPrio, func(i, j int) bool { return byPrio[i].prio < byPrio[j].prio })
	keep := map[string]bool{}
	used := 2
	for _, c := range byPrio {
		if used+c.width > m.width && len(keep) >= 2 {
			break
		}
		keep[c.title] = true
		used += c.width
	}
	var out []column
	for _, c := range all {
		if keep[c.title] {
			out = append(out, c)
		}
	}
	return out
}

func (m Model) table() string {
	var b strings.Builder
	cols := m.columns()
	head := "  "
	for _, c := range cols {
		head += pad(c.title, c.width)
	}
	b.WriteString(m.fit(sHeader.Render(head)) + "\n")
	if len(m.snap.runs) == 0 {
		b.WriteString(m.fit(sSubtle.Render("  no AgentRuns yet — press ")+sKey.Render("1-6")+sSubtle.Render(" to launch a scenario, or run ")+sKey.Render("make samples")) + "\n")
		return b.String()
	}
	for i := range m.snap.runs {
		r := &m.snap.runs[i]
		row := "  "
		if r.Name == m.selected {
			row = lipgloss.NewStyle().Foreground(cCyan).Render("▸ ")
		}
		for _, c := range cols {
			row += pad(c.cell(r), c.width)
		}
		b.WriteString(m.fit(row) + "\n")
	}
	return b.String()
}

// fit cuts a styled line to the terminal width without breaking ANSI sequences.
func (m Model) fit(s string) string {
	if m.width <= 0 {
		return s
	}
	return ansi.Truncate(s, m.width-1, "…")
}

func (m Model) selectedRun() *agentsv1.AgentRun {
	for i := range m.snap.runs {
		if m.snap.runs[i].Name == m.selected {
			return &m.snap.runs[i]
		}
	}
	return nil
}

func (m Model) detail(r *agentsv1.AgentRun) string {
	var b strings.Builder
	// Model chain with the active one highlighted.
	var chain []string
	for i, mdl := range r.Spec.Models() {
		s := sDim.Render(mdl)
		if int32(i) == r.Status.ModelIndex {
			s = lipgloss.NewStyle().Foreground(cBlue).Bold(true).Render("[" + mdl + "]")
		} else if int32(i) < r.Status.ModelIndex {
			s = lipgloss.NewStyle().Foreground(cPink).Strikethrough(true).Render(mdl)
		}
		chain = append(chain, s)
	}
	b.WriteString(sBold.Render(r.Name) + sSubtle.Render("  models ") + strings.Join(chain, sDim.Render(" → ")) + "\n")
	b.WriteString(sSubtle.Render("prompt  ") + trunc(r.Spec.Prompt, 90) + "\n\n")

	if len(r.Status.Attempts) == 0 {
		b.WriteString(sDim.Render("no attempts yet") + "\n")
	}
	for _, a := range r.Status.Attempts {
		dot := outcomeDot(a.Outcome)
		outcome := string(a.Outcome)
		if outcome == "" {
			dot = lipgloss.NewStyle().Foreground(cBlue).Render([]string{"◉", "○"}[m.frame%2])
			outcome = "Running"
		}
		line := fmt.Sprintf("%s #%d  %s %s %s %s  %s",
			dot, a.Number,
			pad(trunc(a.Model, 24), 24),
			pad(outcome, 15),
			pad(sSubtle.Render(humanTokens(a.Tokens)+" tok"), 10),
			pad(sSubtle.Render(a.CostUSD), 9),
			sDim.Render(trunc(a.Reason, 48)))
		if a.Quality != "" {
			line += "  " + sSubtle.Render("q=") + qualityText(a.Quality, r.Spec.RetryPolicy.QualityThreshold)
		}
		b.WriteString(line + "\n")
	}
	msgColor := phaseColor(r.Status.Phase)
	if r.Status.Message != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(msgColor).Render("» "+trunc(r.Status.Message, 110)) + "\n")
	}
	if r.Status.Answer != "" {
		b.WriteString(sSubtle.Render("answer ") + r.Status.Answer + "\n")
	}
	w := min(m.width-2, 150)
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, max(w-4, 10), "…")
	}
	return sPanel.Width(w).Render(strings.Join(lines, "\n"))
}

func (m Model) eventFeed() string {
	var b strings.Builder
	b.WriteString(sHeader.Render("  EVENTS") + "\n")
	evs := m.snap.events
	maxLines := 8
	if m.height > 0 {
		maxLines = max(3, min(10, m.height-len(m.snap.runs)-22))
	}
	if len(evs) > maxLines {
		evs = evs[len(evs)-maxLines:]
	}
	if len(evs) == 0 {
		b.WriteString(sDim.Render("  waiting for the controller…") + "\n")
	}
	nameW := 23
	if m.width < 100 {
		nameW = 17
	}
	for _, e := range evs {
		icon, c := "•", cGray
		switch e.Reason {
		case "AttemptStarted":
			icon, c = "▶", cBlue
		case "Retrying":
			icon, c = "⟳", cYellow
		case "FallingBack":
			icon, c = "⤵", cPink
		case "Succeeded":
			icon, c = "✔", cGreen
		case "Failed":
			icon, c = "✘", cRed
		case "BudgetExceeded":
			icon, c = "$", cRed
		case "Accepted":
			icon, c = "✚", cCyan
		}
		st := lipgloss.NewStyle().Foreground(c)
		b.WriteString(m.fit(fmt.Sprintf("  %s %s %s %s %s",
			sDim.Render(eventTime(e).Local().Format("15:04:05")),
			st.Render(icon),
			pad(sBold.Render(trunc(e.Regarding.Name, nameW-1)), nameW),
			pad(st.Render(e.Reason), 15),
			e.Note)) + "\n")
	}
	return b.String()
}

func (m Model) footer() string {
	keys := []string{
		sKey.Render("↑↓") + " select", sKey.Render("enter") + " details", sKey.Render("1-6") + " launch scenario",
		sKey.Render("x") + " delete", sKey.Render("q") + " quit",
	}
	var sc []string
	for i, s := range scenarios.All {
		sc = append(sc, sKey.Render(fmt.Sprint(i+1))+sDim.Render(" "+s.Name))
	}
	line := m.fit("  "+strings.Join(keys, sDim.Render("  ·  "))) + "\n" + m.fit("  "+strings.Join(sc, "   "))
	if m.flash != "" && time.Since(m.flashAt) < 4*time.Second {
		line += "\n" + m.fit("  "+lipgloss.NewStyle().Foreground(cCyan).Render("» "+m.flash))
	}
	return line
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func trimUSD(micro int64) string {
	return fmt.Sprintf("$%.2f", float64(micro)/1e6)
}

func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprint(n)
	}
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}
