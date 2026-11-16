package status

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"slices"
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
	Input        int           `json:"input"`
	Output       int           `json:"output"`
	CacheRead    int           `json:"cacheRead"`
	CacheWrite   int           `json:"cacheWrite"`
	Cost         float64       `json:"cost"`
	Subscription bool          `json:"subscription"`
	Windows      []quotaWindow `json:"windows"`
	expiresAt    time.Time     `json:"-"`
}

type quotaWindow struct {
	Label       string   `json:"label"`
	UsedPercent *float64 `json:"usedPercent"`
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

func (s *State) Quota(view usage.CachedUsage) {
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

func (s *State) Render(width int, display Display, now time.Time) []string {
	s.mu.RLock()
	current := s.current
	s.mu.RUnlock()
	return render(width, current, display.Theme, display.Model, display.Cwd,
		display.Session, display.Branch, display.Statuses, now)
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
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var summary *usageSummary
	err = json.Unmarshal(encoded, &summary)
	return summary, err
}

func render(width int, state snapshot, theme sdk.UITheme, model, cwd, name, branch string, statuses map[string]string, now time.Time) []string {
	if width <= 0 {
		return nil
	}
	provider, modelID, qualified := strings.Cut(model, "/")
	if !qualified {
		modelID, provider = model, ""
	}
	tokens, cost, quota := "", "", ""
	if summary := state.summary; summary != nil {
		tokens = fmt.Sprintf("↑%s ↓%s", count(summary.Input+summary.CacheRead+summary.CacheWrite), count(summary.Output))
		cost = fmt.Sprintf("$%.3f", summary.Cost)
		if summary.Subscription {
			cost = "plan"
		}
		windows := []string{}
		for _, window := range liveWindows(summary, now) {
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
		tokens = clean(statuses["subscription-usage"])
	}
	if provider == "claude-bridge" {
		if status := clean(statuses["claude-usage"]); status != "" {
			quota = "used " + status
		}
	}
	thinking := ""
	if state.thinking != "" && state.thinking != "none" && state.thinking != "off" {
		thinking = clean(state.thinking)
	}
	git := ""
	if branch != "" {
		git = "git " + clean(branch)
	}
	left := []footerSegment{
		{clean(filepath.Base(cwd)), 6},
		{git, 7},
		{theme.Fg("muted", quota), 1},
		{clean(name), 9},
		{"open " + duration(now.Sub(state.started)), 8},
	}
	right := []footerSegment{
		{contextLabel(state.usage, width >= 70, theme), 0},
		{theme.Fg("accent", clean(modelID)), 2},
		{thinking, 3},
		{tokens, 4},
		{cost, 5},
		{clean(provider), 10},
	}
	keys := make([]string, 0, len(statuses))
	for key := range statuses {
		if key != "subscription-usage" && key != "usage" && key != "claude-usage" {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		right = append(right, footerSegment{theme.Fg("muted", clean(statuses[key])), 11})
	}
	return []string{fit(width, left, right)}
}

func liveWindows(summary *usageSummary, now time.Time) []quotaWindow {
	if !summary.expiresAt.IsZero() && !now.Before(summary.expiresAt) {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(summary.Windows), func(window quotaWindow) bool {
		return !window.resetsAt.IsZero() && !window.resetsAt.After(now)
	})
}

func contextLabel(usage *sdk.ContextUsage, gauge bool, theme sdk.UITheme) string {
	if usage == nil || usage.Percent == nil || math.IsNaN(*usage.Percent) || math.IsInf(*usage.Percent, 0) {
		return theme.Fg("muted", "ctx --")
	}
	percent := *usage.Percent
	label := fmt.Sprintf("ctx %.0f%%", percent)
	if gauge {
		filled := int(math.Round(math.Max(0, math.Min(100, percent)) * 6 / 100))
		label = fmt.Sprintf("ctx [%s%s] %.0f%%", strings.Repeat("━", filled), strings.Repeat("─", 6-filled), percent)
	}
	color := "success"
	if percent >= 90 {
		color = "error"
	} else if percent >= 70 {
		color = "warning"
	}
	return theme.Fg(color, label)
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
	return fmt.Sprint(n)
}

func duration(elapsed time.Duration) string {
	minutes := max(0, int(elapsed.Minutes()))
	if minutes >= 60 {
		return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
	}
	return fmt.Sprintf("%dm", minutes)
}
