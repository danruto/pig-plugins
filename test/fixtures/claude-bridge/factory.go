// Package claudebridge is a disposable offline provider fixture for the footer check.
package claudebridge

import sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

func Extension() *sdk.Extension {
	ext := sdk.New("claude-bridge")
	ext.RegisterProvider("claude-bridge", sdk.ProviderConfig{
		"api":     "offline-fixture",
		"apiKey":  "fixture",
		"baseUrl": "http://127.0.0.1:1",
		"models": []map[string]any{{
			"id":            "fixture",
			"name":          "Offline fixture",
			"reasoning":     false,
			"input":         []string{"text"},
			"contextWindow": 200000,
			"maxTokens":     4096,
			"cost":          map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
		}},
	})
	ext.OnSessionStart(func(ctx sdk.Context, _ map[string]any) (any, error) {
		ctx.SetStatus("claude-usage", "Claude 5h 6% ↻3h · 7d 76% ↻8h · Fable 99% ↻8h")
		return nil, nil
	})
	return ext
}
