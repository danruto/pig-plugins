package usage

import (
	"slices"
	"strings"
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
	report.Semantics.Label = "Synthetic companion source"
	report.Notes = []string{"Synthetic companion caveat."}

	lines := FormatOutcome(Outcome{ProviderID: "openai", TargetID: "acct-7", Status: "ok", Report: &report})

	want := []string{
		"OpenAI: ok",
		"OpenAI account: Team (acct-7)",
		"OpenAI source: Synthetic companion source",
		"Plan limits · Subscription limit · 5h window: 30 / 100 percent",
		"Plan limits · Subscription limit · Weekly window: 30 / 100 percent",
		"App limits · Subscription limit · 5h window: 30 / 100 percent",
		"App limits · Subscription limit · 5h window (app:1): 30 / 100 percent",
		"OpenAI note: Synthetic companion caveat.",
	}
	if !slices.Equal(want, lines) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}

func TestFormatOutcomeRendersSemanticsLabelAndNotes(t *testing.T) {
	window := func(used, seconds float64) map[string]any {
		return map[string]any{"used_percent": used, "limit_window_seconds": seconds}
	}
	plan := map[string]any{"rate_limit": map[string]any{"primary_window": window(30, 18000), "secondary_window": window(30, 604800)}}
	report, err := openAICompanionReport(plan, nil, nil, 1)
	acceptNoError(t, err, "companion report")
	report.Semantics.Label = "Synthetic\u001b[31m companion source"
	report.Notes = []string{
		"",
		"Registration matching is not independent proof of the same user.",
		"App allowance caps shared plan usage\u001b[31m",
	}

	lines := FormatOutcome(Outcome{ProviderID: "openai", Status: "ok", Report: &report})

	joined := strings.Join(lines, "\n")
	sourceAt := slices.Index(lines, "OpenAI source: Synthetic[31m companion source")
	acceptEqual(t, "OpenAI: ok", lines[0], "status line")
	acceptEqual(t, true, sourceAt > 0, "semantics label after the status line")
	acceptEqual(t, []string{
		"OpenAI note: Registration matching is not independent proof of the same user.",
		"OpenAI note: App allowance caps shared plan usage[31m",
	}, lines[len(lines)-2:], "note lines after the amounts")
	acceptEqual(t, 1, strings.Count(joined, "OpenAI source:"), "semantics label lines")
	acceptNotContains(t, joined, "\x1b", "control characters in caveats")
}
