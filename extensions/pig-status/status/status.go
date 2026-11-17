package status

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/MichaelKinsy/PiG/extensions/sdk/tui"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

type Tokens struct {
	Input, Output, CacheRead, CacheWrite int
	Cost                                 float64
	Subscription                         bool
}

type Display struct {
	Theme    sdk.UITheme
	Model    string
	Cwd      string
	Session  string
	Branch   string
	Statuses map[string]string
}

type Options struct {
	Collect func(ext *sdk.Extension, state *State) error
}

type State struct {
	mu      sync.RWMutex
	current snapshot
}

type usageSummary struct {
	Input        int
	Output       int
	CacheRead    int
	CacheWrite   int
	Cost         float64
	Subscription bool
	Windows      []quotaWindow
	expiresAt    time.Time
}

type quotaWindow struct {
	Label       string
	UsedPercent *float64
	resetsAt    time.Time
}

type footerSegment struct {
	text     string
	priority int
}

type snapshot struct {
	usage    *sdk.ContextUsage
	summary  *usageSummary
	thinking string
	started  time.Time
}

func NewState(started time.Time) *State {
	return &State{current: snapshot{started: started}}
}

func (s *State) Quota(view usage.Cached) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.summary == nil && view.ExpiresAt.IsZero() {
		return
	}
	next := s.summaryCopy()
	if view.ExpiresAt.IsZero() {
		next.Windows, next.expiresAt = nil, time.Time{}
	} else {
		windows := make([]quotaWindow, 0, len(view.Windows))
		for _, window := range view.Windows {
			windows = append(windows, quotaWindow{Label: window.Label, UsedPercent: &window.UsedPercent, resetsAt: window.ResetsAt})
		}
		next.Windows, next.expiresAt = windows, view.ExpiresAt
	}
	s.current.summary = &next
}

func (s *State) Tokens(tokens Tokens) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.summaryCopy()
	next.Input, next.Output = tokens.Input, tokens.Output
	next.CacheRead, next.CacheWrite = tokens.CacheRead, tokens.CacheWrite
	next.Cost, next.Subscription = tokens.Cost, tokens.Subscription
	s.current.summary = &next
}

func (s *State) Feed(data any) error {
	summary, err := decodeSummary(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.summary = summary
	return nil
}

func (s *State) Context(context *sdk.ContextUsage, thinking string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.usage, s.current.thinking = context, thinking
}

type renderInput struct {
	width    int
	state    snapshot
	theme    sdk.UITheme
	model    string
	cwd      string
	session  string
	branch   string
	statuses map[string]string
	now      time.Time
}

func (s *State) Render(width int, display Display, now time.Time) []string {
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	return render(renderInput{width: width, state: current, theme: display.Theme, model: display.Model, cwd: display.Cwd,
		session: display.Session, branch: display.Branch, statuses: display.Statuses, now: now})
}

func (s *State) summaryCopy() usageSummary {
	if s.current.summary == nil {
		return usageSummary{}
	}
	return *s.current.summary
}

func (s *State) Install(ctx sdk.Context) error {
	if !ctx.HasUI() || ctx.Mode() != "tui" {
		return nil
	}
	return ctx.SetFooterRenderer(func(width int) []string {
		branch, _ := ctx.FooterData().GitBranch()
		return s.Render(width, Display{
			Theme:    ctx.UITheme(),
			Model:    ctx.ModelQualified(),
			Cwd:      ctx.Cwd(),
			Session:  ctx.SessionName(),
			Branch:   branch,
			Statuses: ctx.FooterData().ExtensionStatuses(),
		}, time.Now())
	})
}

func (s *State) refresh(ctx sdk.Context) error {
	if !ctx.HasUI() || ctx.Mode() != "tui" {
		return nil
	}
	current, err := ctx.GetContextUsage()
	if err != nil {
		return err
	}
	thinking, err := ctx.GetThinkingLevel()
	if err != nil {
		return err
	}
	s.Context(current, thinking)
	return s.Install(ctx)
}

func New(options Options) (*sdk.Extension, error) {
	ext := sdk.New("pig-status")
	state := NewState(time.Now())
	refresh := func(ctx sdk.Context, _ map[string]any) (any, error) { return nil, state.refresh(ctx) }
	for _, event := range []string{
		"session_start", "agent_start", "turn_end", "agent_end", "model_select",
		"session_info_changed", "session_compact", "session_compact_failed", "session_tree",
	} {
		ext.OnEvent(event, refresh)
	}
	if options.Collect != nil {
		if err := options.Collect(ext, state); err != nil {
			return nil, err
		}
		return ext, nil
	}
	if _, err := ext.Events().On("pig-status:usage", func(ctx sdk.Context, data any) error {
		if err := state.Feed(data); err != nil {
			return err
		}
		return state.Install(ctx)
	}); err != nil {
		return nil, err
	}
	return ext, nil
}

func decodeSummary(data any) (*usageSummary, error) {
	if data == nil {
		return nil, nil
	}
	payload, ok := data.(map[string]any)
	if !ok {
		return nil, errors.New("usage payload was not an object")
	}
	summary := &usageSummary{}
	for key, field := range map[string]*int{"input": &summary.Input, "output": &summary.Output, "cacheRead": &summary.CacheRead, "cacheWrite": &summary.CacheWrite} {
		number, err := summaryInteger(payload, key)
		if err != nil {
			return nil, err
		}
		*field = number
	}
	cost, err := summaryNumber(payload, "cost")
	if err != nil {
		return nil, err
	}
	summary.Cost = cost
	if value := payload["subscription"]; value != nil {
		flag, ok := value.(bool)
		if !ok {
			return nil, errors.New("usage payload subscription was not a boolean")
		}
		summary.Subscription = flag
	}
	windows, err := summaryWindows(payload["windows"])
	if err != nil {
		return nil, err
	}
	summary.Windows = windows
	return summary, nil
}

func summaryNumber(payload map[string]any, key string) (float64, error) {
	value := payload[key]
	if value == nil {
		return 0, nil
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("usage payload %s was not a number", key)
	}
	return number, nil
}

func summaryInteger(payload map[string]any, key string) (int, error) {
	number, err := summaryNumber(payload, key)
	if err != nil {
		return 0, err
	}
	if math.Trunc(number) != number || math.Abs(number) >= math.MaxInt64 {
		return 0, fmt.Errorf("usage payload %s was not an integer", key)
	}
	return int(number), nil
}

func summaryWindows(value any) ([]quotaWindow, error) {
	if value == nil {
		return nil, nil
	}
	rows, ok := value.([]any)
	if !ok {
		return nil, errors.New("usage payload windows was not an array")
	}
	windows := make([]quotaWindow, 0, len(rows))
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("usage payload window was not an object")
		}
		var window quotaWindow
		if label := row["label"]; label != nil {
			text, ok := label.(string)
			if !ok {
				return nil, errors.New("usage payload window label was not a string")
			}
			window.Label = text
		}
		if percent := row["usedPercent"]; percent != nil {
			number, ok := percent.(float64)
			if !ok {
				return nil, errors.New("usage payload window usedPercent was not a number")
			}
			window.UsedPercent = &number
		}
		windows = append(windows, window)
	}
	return windows, nil
}

func render(in renderInput) []string {
	if in.width <= 0 {
		return nil
	}
	provider, modelID, qualified := strings.Cut(in.model, "/")
	if !qualified {
		modelID, provider = in.model, ""
	}
	tokens, cost, quota := "", "", ""
	if summary := in.state.summary; summary != nil {
		tokens = fmt.Sprintf("↑%s ↓%s", count(summary.Input+summary.CacheRead+summary.CacheWrite), count(summary.Output))
		cost = fmt.Sprintf("$%.3f", summary.Cost)
		if summary.Subscription {
			cost = "plan"
		}
		windows := []string{}
		for _, window := range liveWindows(summary, in.now) {
			if window.UsedPercent == nil || math.IsNaN(*window.UsedPercent) || math.IsInf(*window.UsedPercent, 0) {
				continue
			}
			remaining := math.Max(0, math.Min(100, math.Round(100-*window.UsedPercent)))
			windows = append(windows, fmt.Sprintf("%s %.0f%%", clean(window.Label), remaining))
		}
		if len(windows) > 0 {
			quota = "left " + strings.Join(windows, " ")
		}
	} else {
		tokens = clean(in.statuses["subscription-usage"])
	}
	if provider == "claude-bridge" {
		if status := clean(in.statuses["claude-usage"]); status != "" {
			quota = "used " + status
		}
	}
	thinking := ""
	if in.state.thinking != "" && in.state.thinking != "none" && in.state.thinking != "off" {
		thinking = clean(in.state.thinking)
	}
	git := ""
	if in.branch != "" {
		git = "git " + clean(in.branch)
	}
	left := []footerSegment{
		{clean(filepath.Base(in.cwd)), 6},
		{git, 7},
		{in.theme.Fg("muted", quota), 1},
		{clean(in.session), 9},
		{"open " + duration(in.now.Sub(in.state.started)), 8},
	}
	right := []footerSegment{
		{contextSegment(in.state.usage, in.width, in.theme), 0},
		{in.theme.Fg("accent", clean(modelID)), 2},
		{thinking, 3},
		{tokens, 4},
		{cost, 5},
		{clean(provider), 10},
	}
	keys := make([]string, 0, len(in.statuses))
	for key := range in.statuses {
		if key != "subscription-usage" && key != "usage" && key != "claude-usage" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		right = append(right, footerSegment{in.theme.Fg("muted", clean(in.statuses[key])), 11})
	}
	return []string{fit(in.width, left, right)}
}

func liveWindows(summary *usageSummary, now time.Time) []quotaWindow {
	if !summary.expiresAt.IsZero() && !now.Before(summary.expiresAt) {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(summary.Windows), func(window quotaWindow) bool {
		return !window.resetsAt.IsZero() && !window.resetsAt.After(now)
	})
}

func contextSegment(usage *sdk.ContextUsage, width int, theme sdk.UITheme) string {
	if width >= 70 {
		return contextGaugeLabel(usage, theme)
	}
	return contextLabel(usage, theme)
}

func contextLabel(usage *sdk.ContextUsage, theme sdk.UITheme) string {
	percent, ok := contextPercent(usage)
	if !ok {
		return theme.Fg("muted", "ctx --")
	}
	return theme.Fg(contextColor(percent), fmt.Sprintf("ctx %.0f%%", percent))
}

func contextGaugeLabel(usage *sdk.ContextUsage, theme sdk.UITheme) string {
	percent, ok := contextPercent(usage)
	if !ok {
		return theme.Fg("muted", "ctx --")
	}
	filled := int(math.Round(math.Max(0, math.Min(100, percent)) * 6 / 100))
	return theme.Fg(contextColor(percent), fmt.Sprintf("ctx [%s%s] %.0f%%", strings.Repeat("━", filled), strings.Repeat("─", 6-filled), percent))
}

func contextPercent(usage *sdk.ContextUsage) (float64, bool) {
	if usage == nil || usage.Percent == nil || math.IsNaN(*usage.Percent) || math.IsInf(*usage.Percent, 0) {
		return 0, false
	}
	return *usage.Percent, true
}

func contextColor(percent float64) string {
	switch {
	case percent >= 90:
		return "error"
	case percent >= 70:
		return "warning"
	default:
		return "success"
	}
}

func fit(width int, left, right []footerSegment) string {
	segments := append(left, right...)
	order := make([]int, len(segments))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return segments[a].priority - segments[b].priority
	})
	selected := make([]bool, len(segments))
	used, counts := 0, [2]int{}
	for _, i := range order {
		cells := tui.VisibleWidth(segments[i].text)
		if cells == 0 {
			continue
		}
		group := 0
		if i >= len(left) {
			group = 1
		}
		if used == 0 {
			segments[i].text = tui.TruncateToWidth(segments[i].text, width, "…", false)
			cells = tui.VisibleWidth(segments[i].text)
		} else if counts[group] > 0 {
			cells += 3
		} else {
			cells += 2
		}
		if segments[i].priority <= 1 && used > 0 && used+cells > width {
			gap := 2
			if counts[group] > 0 {
				gap = 3
			}
			if available := width - used - gap; available > 0 {
				segments[i].text = tui.TruncateToWidth(segments[i].text, available, "…", false)
				cells = tui.VisibleWidth(segments[i].text) + gap
			}
		}
		if used+cells <= width {
			selected[i] = true
			used += cells
			counts[group]++
		}
	}
	groups := [2][]string{}
	for i, segment := range segments {
		if selected[i] {
			group := 0
			if i >= len(left) {
				group = 1
			}
			groups[group] = append(groups[group], segment.text)
		}
	}
	l, r := strings.Join(groups[0], " · "), strings.Join(groups[1], " · ")
	if r == "" {
		return l
	}
	return l + strings.Repeat(" ", width-tui.VisibleWidth(l)-tui.VisibleWidth(r)) + r
}

func clean(text string) string {
	text = tui.StripTerminalSequences(text)
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, text))
}

func count(n int) string {
	if n >= 1_000_000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000_000), ".0") + "M"
	}
	if n >= 1_000 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1_000), ".0") + "k"
	}
	return strconv.Itoa(n)
}

func duration(elapsed time.Duration) string {
	minutes := max(0, int(elapsed.Minutes()))
	if minutes >= 60 {
		return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
	}
	return fmt.Sprintf("%dm", minutes)
}
