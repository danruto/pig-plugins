package usage

import (
	"slices"
	"testing"
)

func TestFormatOutcomeDistinguishesGroupsWindowsAndTarget(t *testing.T) {
	window := func(used, seconds float64) map[string]any {
		return map[string]any{"used_percent": used, "limit_window_seconds": seconds}
	}
	plan := map[string]any{"rate_limit": map[string]any{"primary_window": window(30, 18000), "secondary_window": window(30, 604800)}}
	appBuckets := make([]Bucket, 0, 2)
	for _, id := range []string{"app:0", "app:1"} {
		bucket, err := openAIWindow(window(30, 18000), id, "chatgpt-app", "App limits")
		if err != nil {
			t.Fatalf("app window: %v", err)
		}
		appBuckets = append(appBuckets, bucket)
	}
	report, err := openAICompanionReport(plan, appBuckets, nil, 1)
	if err != nil {
		t.Fatalf("companion report: %v", err)
	}
	report.AccountLabel = "Team"

	lines := FormatOutcome(Outcome{ProviderID: "openai", TargetID: "acct-7", Status: "ok", Report: &report})

	want := []string{
		"OpenAI: ok",
		"OpenAI account: Team (acct-7)",
		"Plan limits · Subscription limit · 5h window: 30 / 100 percent",
		"Plan limits · Subscription limit · Weekly window: 30 / 100 percent",
		"App limits · Subscription limit · 5h window: 30 / 100 percent",
		"App limits · Subscription limit · 5h window (app:1): 30 / 100 percent",
	}
	if !slices.Equal(want, lines) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}
