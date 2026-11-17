package nativeusage_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/MichaelKinsy/PiG/extensions/sdk/tui"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
	"github.com/danruto/pig-plugins/extensions/pig-status/nativeusage"
	"github.com/danruto/pig-plugins/extensions/pig-status/status"
)

const (
	acceptAssistantOneHundred = `{"type":"message","message":{"role":"assistant","usage":{"input":100,"output":20,"cacheRead":30,"cacheWrite":5,"cost":{"total":0.5}}}}`
	acceptAssistantFifty      = `{"type":"message","message":{"role":"assistant","usage":{"input":50,"output":10,"cacheRead":15,"cacheWrite":2,"cost":{"total":0.25}}}}`
	acceptAssistantFull       = `{"type":"message","message":{"role":"assistant","usage":{"input":100000,"output":2000,"cacheRead":50000,"cacheWrite":10000,"cost":{"total":0.216}}}}`
	acceptUserMessage         = `{"type":"message","message":{"role":"user","usage":{"input":900,"output":900,"cacheRead":900,"cacheWrite":900,"cost":{"total":9}}}}`
	acceptToolEntry           = `{"type":"tool","message":{"role":"assistant","usage":{"input":900,"output":900,"cacheRead":900,"cacheWrite":900,"cost":{"total":9}}}}`
	acceptSessionEntry        = `{"type":"session"}`
	acceptAssistantWithoutUse = `{"type":"message","message":{"role":"assistant"}}`
	acceptBrokenJSON          = `{"type":"message",`
)

func acceptEntries(values ...string) []json.RawMessage {
	entries := make([]json.RawMessage, len(values))
	for i, value := range values {
		entries[i] = json.RawMessage(value)
	}
	return entries
}

func TestSessionTokensSumsOnlyAssistantMessages(t *testing.T) {
	tests := []struct {
		name    string
		entries []json.RawMessage
		want    status.Tokens
	}{
		{
			name:    "sums assistant token classes with cache kept separate",
			entries: acceptEntries(acceptAssistantOneHundred, acceptAssistantFifty),
			want:    status.Tokens{Input: 150, Output: 30, CacheRead: 45, CacheWrite: 7, Cost: 0.75},
		},
		{
			name:    "ignores user messages",
			entries: acceptEntries(acceptUserMessage),
			want:    status.Tokens{},
		},
		{
			name:    "ignores tool and non-message entries",
			entries: acceptEntries(acceptToolEntry, acceptSessionEntry),
			want:    status.Tokens{},
		},
		{
			name:    "skips invalid json",
			entries: acceptEntries(acceptBrokenJSON, acceptAssistantOneHundred),
			want:    status.Tokens{Input: 100, Output: 20, CacheRead: 30, CacheWrite: 5, Cost: 0.5},
		},
		{
			name:    "missing usage counts as zero",
			entries: acceptEntries(acceptAssistantWithoutUse),
			want:    status.Tokens{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nativeusage.SessionTokens(tt.entries, "openai/gpt-4.1")

			if got != tt.want {
				t.Fatalf("SessionTokens = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSessionTokensMarksClaudeBridgeSubscription(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		{name: "claude bridge model is a subscription", model: "claude-bridge/claude-sonnet-4-5", expected: true},
		{name: "openai model is not a subscription", model: "openai/gpt-4.1"},
		{name: "anthropic model is not a subscription", model: "anthropic/claude-sonnet-4-5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nativeusage.SessionTokens(acceptEntries(acceptAssistantOneHundred), tt.model)

			if got.Subscription != tt.expected {
				t.Fatalf("Subscription = %t, want %t", got.Subscription, tt.expected)
			}
			if got.Input != 100 {
				t.Fatalf("Input = %d, want 100", got.Input)
			}
		})
	}
}

func TestExtensionReturnsNativeFactory(t *testing.T) {
	ext := nativeusage.Extension()

	if ext == nil {
		t.Fatal("Extension returned nil")
	}
}

func TestFooterRendersNativeCollectionAtWideAndNarrowWidths(t *testing.T) {
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
			state := status.NewState(now.Add(-30 * time.Minute))
			state.Tokens(nativeusage.SessionTokens(acceptEntries(acceptAssistantFull), "openai/gpt-4.1"))
			state.Quota(usage.Cached{
				ExpiresAt: now.Add(30 * time.Minute),
				Windows: []usage.Window{
					{Label: "5h", UsedPercent: 39, ResetsAt: now.Add(time.Hour)},
					{Label: "Week", UsedPercent: 13, ResetsAt: now.Add(48 * time.Hour)},
				},
			})
			display := status.Display{
				Theme:   sdk.UITheme{},
				Model:   "openai/gpt-4.1",
				Cwd:     "/projects/example",
				Session: "a session",
				Branch:  "main",
			}

			lines := state.Render(tt.width, display, now)

			if len(lines) != 1 {
				t.Fatalf("render at width %d: %d lines %q, want one", tt.width, len(lines), lines)
			}
			if got := tui.VisibleWidth(lines[0]); got > tt.width {
				t.Fatalf("render at width %d: %q occupies %d columns", tt.width, lines[0], got)
			}
			if !strings.Contains(lines[0], "left 5h 61% Week 87%") {
				t.Fatalf("missing quota in %q", lines[0])
			}
			if !strings.Contains(lines[0], "↑160k ↓2k") {
				t.Fatalf("missing cache-inclusive tokens in %q", lines[0])
			}
			if !strings.Contains(lines[0], "$0.216") {
				t.Fatalf("missing cost in %q", lines[0])
			}
		})
	}
}
