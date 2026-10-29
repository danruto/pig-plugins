package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/MichaelKinsy/PiG/extensions/sdk/tui"
)

type usageSummary struct {
	Input        int           `json:"input"`
	Output       int           `json:"output"`
	CacheRead    int           `json:"cacheRead"`
	CacheWrite   int           `json:"cacheWrite"`
	Cost         float64       `json:"cost"`
	Subscription bool          `json:"subscription"`
	Windows      []quotaWindow `json:"windows"`
}

type quotaWindow struct {
	Label       string   `json:"label"`
	UsedPercent *float64 `json:"usedPercent"`
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

func main() {
	ext, err := extension()
	if err == nil {
		err = ext.Run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func extension() (*sdk.Extension, error) {
	ext := sdk.New("pig-status")
	var mu sync.RWMutex
	state := snapshot{started: time.Now()}
	install := func(ctx sdk.Context) error {
		if !ctx.HasUI() || ctx.Mode() != "tui" {
			return nil
		}
		return ctx.SetFooterRenderer(func(width int) []string {
			mu.RLock()
			current := state
			mu.RUnlock()
			branch, _ := ctx.FooterData().GitBranch()
			return render(width, current, ctx.UITheme(), ctx.ModelQualified(), ctx.Cwd(),
				ctx.SessionName(), branch, ctx.FooterData().ExtensionStatuses(), time.Now())
		})
	}
	refresh := func(ctx sdk.Context, _ map[string]any) (any, error) {
		if !ctx.HasUI() || ctx.Mode() != "tui" {
			return nil, nil
		}
		usage, err := ctx.GetContextUsage()
		if err != nil {
			return nil, err
		}
		thinking, err := ctx.GetThinkingLevel()
		if err != nil {
			return nil, err
		}
		mu.Lock()
		state.usage, state.thinking = usage, thinking
		mu.Unlock()
		return nil, install(ctx)
	}
	for _, event := range []string{
		"session_start", "agent_start", "turn_end", "agent_end", "model_select",
		"session_info_changed", "session_compact", "session_compact_failed", "session_tree",
	} {
		ext.OnEvent(event, refresh)
	}
	_, err := ext.Events().On("pig-status:usage", func(ctx sdk.Context, data any) error {
		summary, err := decodeSummary(data)
		if err != nil {
			return err
		}
		mu.Lock()
		state.summary = summary
		mu.Unlock()
		return install(ctx)
	})
	return ext, err
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
		for _, window := range summary.Windows {
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
