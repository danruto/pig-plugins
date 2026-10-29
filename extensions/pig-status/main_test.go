package main

import (
	"math"
	"strings"
	"testing"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/MichaelKinsy/PiG/extensions/sdk/tui"
)

func fixture(now time.Time) snapshot {
	percent, tokens := 76.0, 152_000
	return snapshot{
		usage: &sdk.ContextUsage{Percent: &percent, Tokens: &tokens, ContextWindow: 200_000},
		summary: &usageSummary{Input: 100_000, Output: 2_000, Cost: 0.216, Windows: []quotaWindow{
			{Label: "5h", UsedPercent: new(39.0)}, {Label: "Week", UsedPercent: new(13.0)},
		}},
		started: now.Add(-93 * time.Minute),
	}
}

func TestRenderIsOneLineAtEveryTerminalWidth(t *testing.T) {
	now := time.Now()
	state := fixture(now)
	statuses := map[string]string{
		"subscription-usage": "↑100k ↓2k · ctx 76% · $0.216 · 5h 61% left · Week 87% left",
		"pi-lens-lsp":        "\x1b[32mLSP ✓ 中文 👨‍👩‍👧‍👦 e\u0301\x1b[0m\nready",
	}
	for width := 1; width <= 240; width++ {
		lines := render(width, state, sdk.UITheme{}, "openai/gpt-4.1", "/projects/中文/👨‍👩‍👧‍👦", "a long session", "feature/test", statuses, now)
		if len(lines) != 1 {
			t.Fatalf("width %d: expected one line, got %q", width, lines)
		}
		if got := tui.VisibleWidth(lines[0]); got > width {
			t.Fatalf("width %d: %q occupies %d columns", width, lines[0], got)
		}
		if strings.ContainsAny(lines[0], "\r\n\t") {
			t.Fatalf("unsafe line: %q", lines[0])
		}
	}
	if got := render(0, state, sdk.UITheme{}, "", "", "", "", nil, now); len(got) != 0 {
		t.Fatalf("unknown width must not push unsized lines: %q", got)
	}
}

func TestCompactLayoutPrioritizesUsage(t *testing.T) {
	now := time.Now()
	state := fixture(now)
	line := render(80, state, sdk.UITheme{}, "openai/gpt-4.1", "/projects/example", "a session", "main", nil, now)[0]
	for _, want := range []string{"ctx [━━━━━─] 76%", "left 5h 61% Week 87%", "gpt-4.1", "↑100k ↓2k", "$0.216"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in compact line %q", want, line)
		}
	}
	line = render(40, state, sdk.UITheme{}, "openai/gpt-4.1", "/projects/example", "a session", "main", nil, now)[0]
	if !strings.Contains(line, "left 5h 61% Week 87%") || strings.Contains(line, "session") {
		t.Fatalf("quota must take priority over session metadata: %q", line)
	}
}

func TestWideLayoutIncludesOtherStatusesWithoutDuplicatingUsage(t *testing.T) {
	now := time.Now()
	state := fixture(now)
	state.thinking = "high"
	line := render(240, state, sdk.UITheme{}, "openai/gpt-4.1", "/projects/example", "session name", "main", map[string]string{
		"subscription-usage": "must not duplicate",
		"z-status":           "last status", "a-status": "first status",
	}, now)[0]
	for _, want := range []string{"high", "git main", "example", "session name", "open 1h33m", "first status · last status", "openai"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %q", want, line)
		}
	}
	if strings.Contains(line, "must not duplicate") {
		t.Fatalf("duplicated subscription status: %q", line)
	}
}

func TestLayoutAnchorsProjectLeftAndModelStatusesRight(t *testing.T) {
	now := time.Now()
	state := fixture(now)
	state.thinking = "high"
	left := "example · git main · left 5h 61% Week 87% · session name · open 1h33m"
	right := "ctx [━━━━━─] 76% · gpt-4.1 · high · ↑100k ↓2k · $0.216 · openai · LSP ready"
	for _, width := range []int{160, 200, 240} {
		line := render(width, state, sdk.UITheme{}, "openai/gpt-4.1", "/projects/example", "session name", "main", map[string]string{
			"pi-lens-lsp": "LSP ready",
		}, now)[0]
		gap := width - tui.VisibleWidth(left) - tui.VisibleWidth(right)
		want := left + strings.Repeat(" ", gap) + right
		if line != want {
			t.Errorf("width %d: want left/right groups separated by %d spaces, got %q", width, gap, line)
		}
	}
}

func TestLayoutAlignmentUsesDisplayColumns(t *testing.T) {
	now := time.Now()
	for _, width := range []int{80, 160, 240} {
		line := render(width, fixture(now), sdk.UITheme{}, "openai/gpt-4.1", "/projects/中文", "", "main", map[string]string{
			"pi-lens-lsp": "LSP \x1b[32m✓\x1b[0m",
		}, now)[0]
		if !strings.HasPrefix(line, "中文 · ") || tui.VisibleWidth(line) != width {
			t.Errorf("width %d: groups are not anchored to the terminal edges: %q", width, line)
		}
		if width >= 160 && !strings.HasSuffix(line, "LSP ✓") {
			t.Errorf("width %d: LSP status is not right-aligned: %q", width, line)
		}
	}
}

func TestContextUnknownAndBoundaryValues(t *testing.T) {
	for _, usage := range []*sdk.ContextUsage{nil, {}, {Percent: new(math.NaN())}, {Percent: new(math.Inf(1))}} {
		if got := contextLabel(usage, true, sdk.UITheme{}); got != "ctx --" {
			t.Fatalf("unknown usage reported as known: %q", got)
		}
	}
	for _, percent := range []float64{-10, 0, 70, 90, 100, 120} {
		got := contextLabel(&sdk.ContextUsage{Percent: &percent}, true, sdk.UITheme{})
		if strings.Count(got, "━")+strings.Count(got, "─") != 6 {
			t.Fatalf("unbounded gauge: %q", got)
		}
	}
}

func TestClaudeUsageIsPrioritizedWithoutParsingOrDuplicatingBridgeText(t *testing.T) {
	now := time.Now()
	status := "Claude 5h 6% ↻3h · 7d 76% ↻8h · Fable 99% ↻8h · reset available"
	for _, width := range []int{40, 80, 240} {
		line := render(width, fixture(now), sdk.UITheme{}, "claude-bridge/fixture", "/example", "", "", map[string]string{"claude-usage": status}, now)[0]
		if !strings.Contains(line, "used Claude 5h 6%") || strings.Count(line, "Claude") != 1 || tui.VisibleWidth(line) > width || strings.Contains(line, "left ") {
			t.Fatalf("width %d: bridge usage missing or mislabeled: %q", width, line)
		}
	}
	line := render(240, fixture(now), sdk.UITheme{}, "openai-codex/fixture", "/example", "", "", map[string]string{"claude-usage": status}, now)[0]
	if strings.Contains(line, "Claude") || !strings.Contains(line, "left 5h") {
		t.Fatalf("previous provider's quota leaked: %q", line)
	}
}

func TestSubscriptionIncludesCacheTokensWithoutPretendingToKnowBilledCost(t *testing.T) {
	now := time.Now()
	state := fixture(now)
	state.summary.CacheRead = 50_000
	state.summary.CacheWrite = 10_000
	state.summary.Subscription = true
	line := render(240, state, sdk.UITheme{}, "claude-bridge/fixture", "/example", "", "", map[string]string{"usage": "duplicate backend status"}, now)[0]
	if !strings.Contains(line, "↑160k ↓2k") || !strings.Contains(line, "plan") || strings.Contains(line, "$0.216") || strings.Contains(line, "duplicate backend") {
		t.Fatalf("incorrect subscription accounting: %q", line)
	}
}

func TestUsageEventDecoding(t *testing.T) {
	summary, err := decodeSummary(map[string]any{
		"input": 100_000.0, "output": 2_000.0, "cost": 0.216,
		"windows": []any{map[string]any{"label": "5h", "usedPercent": 39.0}},
	})
	if err != nil || summary.Input != 100_000 || summary.Output != 2_000 || summary.Cost != 0.216 || *summary.Windows[0].UsedPercent != 39 {
		t.Fatalf("invalid summary: %+v, %v", summary, err)
	}
	if summary, err := decodeSummary(nil); err != nil || summary != nil {
		t.Fatalf("null is not known zero usage: %+v, %v", summary, err)
	}
	if _, err := decodeSummary(map[string]any{"input": "invalid"}); err == nil {
		t.Fatal("malformed summary accepted")
	}
}

func TestQuotaBoundsAndUnknownWindows(t *testing.T) {
	now := time.Now()
	state := fixture(now)
	state.summary.Windows = []quotaWindow{
		{Label: "low", UsedPercent: new(-10.0)}, {Label: "high", UsedPercent: new(120.0)},
		{Label: "unknown"}, {Label: "invalid", UsedPercent: new(math.NaN())},
	}
	line := render(240, state, sdk.UITheme{}, "model", "/example", "", "", nil, now)[0]
	if !strings.Contains(line, "left low 100% high 0%") || strings.Contains(line, "unknown") || strings.Contains(line, "invalid") {
		t.Fatalf("invalid quota display: %q", line)
	}
}

func TestDisabledThinkingLevel(t *testing.T) {
	for _, level := range []string{"", "none", "off"} {
		now := time.Now()
		lines := render(180, snapshot{thinking: level, started: now}, sdk.UITheme{}, "model", "/example", "", "", nil, now)
		if strings.Contains(strings.Join(lines, "\n"), " · "+level+" · ") {
			t.Fatalf("disabled thinking should not get a label: %q", lines)
		}
	}
}

func TestCleanTerminalControls(t *testing.T) {
	if got := clean("\x1b[31mname\x1b[0m\nline\tend\u2028next\x07"); got != "name line end next" {
		t.Fatalf("unexpected sanitized text: %q", got)
	}
}
