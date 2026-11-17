package status_test

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/MichaelKinsy/PiG/extensions/sdk/tui"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
	"github.com/danruto/pig-plugins/extensions/pig-status/status"
)

const (
	acceptQuotaSegment  = "left 5h 61% Week 87%"
	acceptCountSegment  = "↑160k ↓2k"
	acceptCostSegment   = "$0.216"
	acceptBridgeSegment = "Claude 5h 6% ↻3h · 7d 76% ↻8h"
)

type acceptFooter struct {
	state   *status.State
	display status.Display
	now     time.Time
}

func acceptDisplay(statuses map[string]string) status.Display {
	return status.Display{
		Theme:    sdk.UITheme{},
		Model:    "openai/gpt-4.1",
		Cwd:      "/projects/example",
		Session:  "a session",
		Branch:   "main",
		Statuses: statuses,
	}
}

func acceptQuota(now time.Time) usage.Cached {
	return usage.Cached{
		ExpiresAt: now.Add(30 * time.Minute),
		Windows: []usage.Window{
			{Label: "5h", UsedPercent: 39, ResetsAt: now.Add(time.Hour)},
			{Label: "Week", UsedPercent: 13, ResetsAt: now.Add(48 * time.Hour)},
		},
	}
}

func acceptTokens() status.Tokens {
	return status.Tokens{Input: 100_000, Output: 2_000, CacheRead: 50_000, CacheWrite: 10_000, Cost: 0.216}
}

func acceptUsagePayload(t *testing.T) any {
	t.Helper()
	const payload = `{"input":100000,"output":2000,"cost":0.216,"windows":[{"label":"5h","usedPercent":39},{"label":"Week","usedPercent":13}]}`
	var decoded any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return decoded
}

func (f acceptFooter) line(t *testing.T, width int) string {
	t.Helper()
	lines := f.state.Render(width, f.display, f.now)
	if len(lines) != 1 {
		t.Fatalf("render at width %d: %d lines %q, want one", width, len(lines), lines)
	}
	if got := tui.VisibleWidth(lines[0]); got > width {
		t.Fatalf("render at width %d: %q occupies %d columns", width, lines[0], got)
	}
	if strings.ContainsAny(lines[0], "\r\n\t") {
		t.Fatalf("render at width %d: %q carries terminal controls", width, lines[0])
	}
	return lines[0]
}

func acceptContains(t *testing.T, line, want string) {
	t.Helper()
	if !strings.Contains(line, want) {
		t.Fatalf("missing %q in %q", want, line)
	}
}

func acceptAbsent(t *testing.T, line, unwanted string) {
	t.Helper()
	if strings.Contains(line, unwanted) {
		t.Fatalf("unexpected %q in %q", unwanted, line)
	}
}

func TestQuotaRendersKeptPercentWindows(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		windows []usage.Window
		want    string
		absent  []string
	}{
		{
			name: "percent windows inverted to remaining and joined once",
			windows: []usage.Window{
				{Label: "5h", UsedPercent: 39, ResetsAt: now.Add(time.Hour)},
				{Label: "Week", UsedPercent: 13, ResetsAt: now.Add(48 * time.Hour)},
			},
			want: acceptQuotaSegment,
		},
		{
			name: "nonfinite usage skipped",
			windows: []usage.Window{
				{Label: "invalid", UsedPercent: math.NaN()},
				{Label: "5h", UsedPercent: 39},
				{Label: "infinite", UsedPercent: math.Inf(1)},
			},
			want:   "left 5h 61%",
			absent: []string{"invalid", "infinite"},
		},
		{
			name:    "used zero renders full remaining",
			windows: []usage.Window{{Label: "zero", UsedPercent: 0}},
			want:    "left zero 100%",
		},
		{
			name:    "used above full renders empty remaining",
			windows: []usage.Window{{Label: "high", UsedPercent: 120}},
			want:    "left high 0%",
		},
		{
			name:    "negative used clamps to full remaining",
			windows: []usage.Window{{Label: "low", UsedPercent: -5}},
			want:    "left low 100%",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := status.NewState(now)
			state.Quota(usage.Cached{ExpiresAt: now.Add(30 * time.Minute), Windows: tt.windows})
			f := acceptFooter{state: state, display: acceptDisplay(nil), now: now}

			line := f.line(t, 200)

			acceptContains(t, line, tt.want)
			for _, unwanted := range tt.absent {
				acceptAbsent(t, line, unwanted)
			}
		})
	}
}

func TestQuotaClearsWindowsAndHidesReasonWithoutExpiry(t *testing.T) {
	now := time.Now()
	state := status.NewState(now)
	state.Quota(acceptQuota(now))
	f := acceptFooter{state: state, display: acceptDisplay(nil), now: now}
	acceptContains(t, f.line(t, 200), acceptQuotaSegment)

	state.Quota(usage.Cached{Reason: "claude credentials unavailable"})

	line := f.line(t, 200)
	acceptAbsent(t, line, "left")
	acceptAbsent(t, line, "claude credentials unavailable")
}

func TestQuotaReasonOnlyViewKeepsBackendTokenStatus(t *testing.T) {
	now := time.Now()
	state := status.NewState(now)
	display := acceptDisplay(map[string]string{"subscription-usage": "↑100k ↓2k · $0.216 · 5h 61% left"})

	state.Quota(usage.Cached{Reason: "claude credentials unavailable"})

	line := acceptFooter{state: state, display: display, now: now}.line(t, 200)
	acceptContains(t, line, "↑100k ↓2k")
	acceptAbsent(t, line, "↑0 ↓0")
	acceptAbsent(t, line, "$0.000")
}

func TestQuotaHidesWindowsOnceReportExpires(t *testing.T) {
	now := time.Now()
	view := acceptQuota(now)
	state := status.NewState(now)
	state.Quota(view)
	acceptContains(t, acceptFooter{state: state, display: acceptDisplay(nil), now: now}.line(t, 200), acceptQuotaSegment)

	line := acceptFooter{state: state, display: acceptDisplay(nil), now: view.ExpiresAt}.line(t, 200)

	acceptAbsent(t, line, "left")
	acceptAbsent(t, line, "5h")
}

func TestQuotaDropsWindowWhoseResetPassed(t *testing.T) {
	now := time.Now()
	state := status.NewState(now)
	state.Quota(usage.Cached{ExpiresAt: now.Add(30 * time.Minute), Windows: []usage.Window{
		{Label: "5h", UsedPercent: 39, ResetsAt: now.Add(time.Hour)},
		{Label: "Week", UsedPercent: 13, ResetsAt: now.Add(-time.Hour)},
	}})
	f := acceptFooter{state: state, display: acceptDisplay(nil), now: now}

	line := f.line(t, 200)

	acceptContains(t, line, "left 5h 61%")
	acceptAbsent(t, line, "Week")
}

func TestTokensRendersCacheInclusiveTotalsAndLabels(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name   string
		tokens status.Tokens
		want   []string
		absent []string
	}{
		{
			name:   "counts cache tokens towards input",
			tokens: acceptTokens(),
			want:   []string{acceptCountSegment, acceptCostSegment},
			absent: []string{"plan"},
		},
		{
			name:   "subscription labelled plan instead of cost",
			tokens: status.Tokens{Input: 100_000, Output: 2_000, CacheRead: 50_000, CacheWrite: 10_000, Subscription: true},
			want:   []string{acceptCountSegment, "plan"},
			absent: []string{acceptCostSegment},
		},
		{
			name:   "millions formatted with an M suffix",
			tokens: status.Tokens{Input: 2_000_000, Output: 500},
			want:   []string{"↑2M ↓500"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := status.NewState(now)
			state.Tokens(tt.tokens)
			f := acceptFooter{state: state, display: acceptDisplay(nil), now: now}

			line := f.line(t, 200)

			for _, want := range tt.want {
				acceptContains(t, line, want)
			}
			for _, unwanted := range tt.absent {
				acceptAbsent(t, line, unwanted)
			}
		})
	}
}

func TestQuotaAndTokensKeepEachOther(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		apply func(*status.State)
	}{
		{
			name: "tokens set after quota keep the quota",
			apply: func(s *status.State) {
				s.Quota(acceptQuota(now))
				s.Tokens(acceptTokens())
			},
		},
		{
			name: "quota set after tokens keep the tokens",
			apply: func(s *status.State) {
				s.Tokens(acceptTokens())
				s.Quota(acceptQuota(now))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := status.NewState(now)

			tt.apply(state)

			line := acceptFooter{state: state, display: acceptDisplay(nil), now: now}.line(t, 200)
			acceptContains(t, line, acceptQuotaSegment)
			acceptContains(t, line, acceptCountSegment)
		})
	}
}

func TestStatusSlotShowsSubscriptionUsageBeforeAnyFeed(t *testing.T) {
	now := time.Now()
	state := status.NewState(now)
	display := acceptDisplay(map[string]string{"subscription-usage": "↑100k ↓2k · $0.216 · 5h 61% left"})

	line := acceptFooter{state: state, display: display, now: now}.line(t, 200)

	acceptContains(t, line, "↑100k ↓2k")
	acceptContains(t, line, acceptCostSegment)
	acceptContains(t, line, "5h 61% left")
}

func TestFeedRendersTypeScriptPayload(t *testing.T) {
	now := time.Now()
	state := status.NewState(now)

	err := state.Feed(acceptUsagePayload(t))

	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	line := acceptFooter{state: state, display: acceptDisplay(nil), now: now}.line(t, 200)
	acceptContains(t, line, "↑100k ↓2k")
	acceptContains(t, line, acceptCostSegment)
	acceptContains(t, line, acceptQuotaSegment)
}

func TestFeedRejectsMalformedPayloadAndKeepsState(t *testing.T) {
	now := time.Now()
	state := status.NewState(now)
	if err := state.Feed(acceptUsagePayload(t)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	f := acceptFooter{state: state, display: acceptDisplay(nil), now: now}
	before := f.line(t, 200)

	err := state.Feed(map[string]any{"input": "invalid"})

	if err == nil {
		t.Fatal("malformed feed accepted")
	}
	after := f.line(t, 200)
	if after != before {
		t.Fatalf("state changed after rejected feed: %q -> %q", before, after)
	}
}

func TestClaudeBridgeStatusRendersUsedVerbatim(t *testing.T) {
	now := time.Now()
	state := status.NewState(now)
	state.Quota(acceptQuota(now))
	display := acceptDisplay(map[string]string{
		"claude-usage":       acceptBridgeSegment,
		"subscription-usage": "backend duplicate",
		"usage":              "backend duplicate",
	})
	display.Model = "claude-bridge/claude-sonnet-4-5"
	f := acceptFooter{state: state, display: display, now: now}

	line := f.line(t, 240)

	acceptContains(t, line, "used "+acceptBridgeSegment)
	if got := strings.Count(line, "Claude"); got != 1 {
		t.Fatalf("bridge text rendered %d times in %q", got, line)
	}
	acceptAbsent(t, line, "left")
	acceptAbsent(t, line, "backend duplicate")
}

func TestClaudeStatusDoesNotLeakToOtherProviders(t *testing.T) {
	now := time.Now()
	state := status.NewState(now)
	state.Quota(acceptQuota(now))
	display := acceptDisplay(map[string]string{"claude-usage": acceptBridgeSegment})
	f := acceptFooter{state: state, display: display, now: now}

	line := f.line(t, 240)

	acceptContains(t, line, acceptQuotaSegment)
	acceptAbsent(t, line, "Claude")
}

func TestClaudeBridgeWithoutStatusKeepsQuota(t *testing.T) {
	now := time.Now()
	state := status.NewState(now)
	state.Quota(acceptQuota(now))
	display := acceptDisplay(nil)
	display.Model = "claude-bridge/claude-sonnet-4-5"

	line := acceptFooter{state: state, display: display, now: now}.line(t, 200)

	acceptContains(t, line, acceptQuotaSegment)
	acceptAbsent(t, line, "used")
}

func TestRenderIsOneLineWithinWidthAtEveryWidth(t *testing.T) {
	now := time.Now()
	state := status.NewState(now.Add(-93 * time.Minute))
	state.Quota(acceptQuota(now))
	state.Tokens(acceptTokens())
	f := acceptFooter{state: state, display: acceptDisplay(map[string]string{
		"subscription-usage": "backend duplicate",
		"pi-lens-lsp":        "\x1b[32mLSP ✓ 中文 👨‍👩‍👧‍👦 e\u0301\x1b[0m\nready",
	}), now: now}

	for width := 1; width <= 240; width++ {
		f.line(t, width)
	}

	if lines := f.state.Render(0, f.display, f.now); len(lines) != 0 {
		t.Fatalf("width 0 returned %q, want no lines", lines)
	}
}

func TestQuotaAndTokensVisibleAtNarrowWidths(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		width int
	}{
		{name: "eighty columns", width: 80},
		{name: "one hundred twenty columns", width: 120},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := status.NewState(now)
			state.Quota(acceptQuota(now))
			state.Tokens(acceptTokens())
			f := acceptFooter{state: state, display: acceptDisplay(nil), now: now}

			line := f.line(t, tt.width)

			acceptContains(t, line, acceptQuotaSegment)
			acceptContains(t, line, acceptCountSegment)
			acceptContains(t, line, acceptCostSegment)
		})
	}
}

func TestContextRendersStoredGaugeAndThinkingLevel(t *testing.T) {
	now := time.Now()
	percent, tokens := 76.0, 152_000
	state := status.NewState(now)
	state.Context(&sdk.ContextUsage{Percent: &percent, Tokens: &tokens, ContextWindow: 200_000}, "high")
	f := acceptFooter{state: state, display: acceptDisplay(nil), now: now}

	line := f.line(t, 200)

	acceptContains(t, line, "ctx [━━━━━─] 76%")
	acceptContains(t, line, "high")
}

func TestOpenSegmentRendersMinutesBelowAnHour(t *testing.T) {
	now := time.Now()
	state := status.NewState(now.Add(-45 * time.Minute))
	state.Tokens(acceptTokens())

	line := acceptFooter{state: state, display: acceptDisplay(nil), now: now}.line(t, 200)

	acceptContains(t, line, "open 45m")
}

func TestNewWithCollectorAttachesOnce(t *testing.T) {
	var (
		calls    int
		gotExt   *sdk.Extension
		gotState *status.State
	)

	ext, err := status.New(status.Options{Collect: func(e *sdk.Extension, s *status.State) error {
		calls++
		gotExt, gotState = e, s
		return nil
	}})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if calls != 1 {
		t.Fatalf("collector called %d times, want once", calls)
	}
	if gotExt == nil || ext != gotExt {
		t.Fatalf("New returned %p, collector received %p", ext, gotExt)
	}
	if gotState == nil {
		t.Fatal("collector received a nil state")
	}
}

func TestNewWithCollectorForwardsError(t *testing.T) {
	boom := errors.New("collector failed")

	ext, err := status.New(status.Options{Collect: func(*sdk.Extension, *status.State) error {
		return boom
	}})

	if ext != nil {
		t.Fatalf("New returned %p with a failed collector", ext)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("New error = %v, want the collector error", err)
	}
}

func TestNewWithoutCollectorRegistersExtension(t *testing.T) {
	ext, err := status.New(status.Options{})

	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ext == nil {
		t.Fatal("New returned a nil extension")
	}
}

func TestStateRendersDuringConcurrentWriters(t *testing.T) {
	now := time.Now()
	state := status.NewState(now.Add(-time.Hour))
	f := acceptFooter{state: state, display: acceptDisplay(nil), now: now}
	var writers sync.WaitGroup

	for range 4 {
		writers.Go(func() {
			for i := range 50 {
				state.Quota(usage.Cached{
					ExpiresAt: now.Add(time.Duration(i) * time.Minute),
					Windows:   []usage.Window{{Label: "5h", UsedPercent: float64(i)}},
				})
				state.Tokens(status.Tokens{Input: i * 100, Output: i, Cost: float64(i) / 100})
				_ = state.Feed(map[string]any{
					"input":  float64(i),
					"output": 1.0,
					"cost":   0.01,
					"windows": []any{
						map[string]any{"label": "5h", "usedPercent": float64(i % 100)},
					},
				})
			}
		})
	}

	for range 200 {
		f.line(t, 120)
	}

	writers.Wait()
}
