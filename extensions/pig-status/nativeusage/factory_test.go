package nativeusage

import (
	"encoding/json"
	"testing"

	"github.com/danruto/pig-plugins/extensions/pig-status/status"
)

func TestSessionTokensSumsAssistantUsageAndSkipsEveryOtherEntry(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		model   string
		want    status.Tokens
	}{
		{
			name: "sums assistant usage including cache tokens and cost",
			entries: []string{
				`{"type":"message","message":{"role":"assistant","usage":{"input":10,"output":2,"cacheRead":3,"cacheWrite":4,"cost":{"total":0.5}}}}`,
				`{"type":"message","message":{"role":"assistant","usage":{"input":1,"output":1,"cost":{"total":0.25}}}}`,
			},
			model: "openai/gpt-4.1",
			want:  status.Tokens{Input: 11, Output: 3, CacheRead: 3, CacheWrite: 4, Cost: 0.75},
		},
		{
			name: "skips user messages, other entry types, malformed entries and missing usage",
			entries: []string{
				`{"type":"message","message":{"role":"user","usage":{"input":99}}}`,
				`{"type":"summary","message":{"role":"assistant","usage":{"input":99}}}`,
				`{"type":"message","message":{"role":"assistant","usage":{"input":"invalid"}}}`,
				`not json`,
				`{"type":"message","message":{"role":"assistant"}}`,
			},
			model: "openai/gpt-4.1",
			want:  status.Tokens{},
		},
		{
			name:    "claude-bridge provider reports the subscription plan",
			entries: []string{`{"type":"message","message":{"role":"assistant","usage":{"output":5}}}`},
			model:   "claude-bridge/anthropic",
			want:    status.Tokens{Output: 5, Subscription: true},
		},
		{
			name:  "an unqualified model name never claims the plan",
			model: "claude-bridge",
			want:  status.Tokens{},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			entries := make([]json.RawMessage, 0, len(test.entries))
			for _, entry := range test.entries {
				entries = append(entries, json.RawMessage(entry))
			}
			if got := SessionTokens(entries, test.model); got != test.want {
				t.Fatalf("tokens=%+v want %+v", got, test.want)
			}
		})
	}
}
