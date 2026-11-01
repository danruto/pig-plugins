package usage

import (
	"errors"
	"fmt"
	"math"
)

const chatGPTUsageSettingsURL = "https://chatgpt.com/settings/usage"

func openAIAuthReport(enabled bool, capturedAt int64) Report {
	note := "Codex companion usage is disabled. Enable Use Codex login for OpenAI usage in /usage Settings only when both logins use the same ChatGPT account and workspace."
	if enabled {
		note = "Numerical usage requires a companion /login openai-codex with the same ChatGPT account/workspace; native inference stays on openai."
	}
	return Report{ProviderID: "openai", ProviderName: "OpenAI", CapturedAt: capturedAt, Source: "openai-chatgpt-auth", Semantics: Semantics{Kind: "consumer-subscription", Label: "ChatGPT plan authentication status"}, Buckets: []Bucket{}, Metrics: []Metric{{ID: "plan-auth", Label: "ChatGPT plan authentication", Value: "Connected (native OAuth)"}}, Notes: []string{note, "Manage usage: " + chatGPTUsageSettingsURL, "Native OpenAI has no supported numerical usage API. Optional companion usage uses undocumented ChatGPT endpoints; registration matching is not independent identity proof.", "Fast mode and earned reset redemption remain legacy openai-codex features."}}
}

func normalizeOpenAI(payload map[string]any, capturedAt int64) (Report, error) {
	apps, hasApps := payload["apps"]
	if !hasApps {
		enabled, _ := payload["useCodexUsage"].(bool)
		return openAIAuthReport(enabled, capturedAt), nil
	}
	clientID, _ := payload["clientId"].(string)
	buckets, allowance, err := matchingOpenAIAppBuckets(apps, clientID)
	if err != nil {
		return Report{}, err
	}
	return openAICompanionReport(object(payload["plan"]), buckets, allowance, capturedAt)
}

func matchingOpenAIAppBuckets(payload any, clientID string) ([]Bucket, *float64, error) {
	root := object(payload)
	items, ok := root["items"].([]any)
	complete := root != nil && ok && len(items) <= 128
	for key := range root {
		if key != "items" && key != "has_more" && key != "next_cursor" {
			complete = false
		}
	}
	if more, present := root["has_more"]; present && more != false {
		complete = false
	}
	if cursor, present := root["next_cursor"]; present && cursor != nil && cursor != "" {
		complete = false
	}
	if !complete {
		return nil, nil, errors.New("ChatGPT app usage requires a complete, bounded registration list; pagination is unsupported.")
	}
	var match map[string]any
	matches := 0
	for _, raw := range items {
		app := object(raw)
		id, ok := app["id"].(string)
		if !ok || jsTrim(id) == "" {
			return nil, nil, errors.New("ChatGPT app usage returned an unreadable registration identity; uniqueness cannot be established.")
		}
		if id == clientID {
			match = app
			matches++
		}
	}
	if clientID == "" || matches != 1 {
		return nil, nil, errors.New("The native registration was not uniquely found in the companion Codex account; use the same ChatGPT account/workspace for both logins.")
	}
	windows, ok := match["windows"].([]any)
	if !ok || len(windows) == 0 || len(windows) > 32 {
		return nil, nil, errors.New("ChatGPT app usage returned no bounded, displayable windows.")
	}
	var allowance *float64
	if raw, present := match["allowed_usage_percent"]; present {
		number, ok := percentage(raw)
		if !ok {
			return nil, nil, errors.New("ChatGPT app allowance was invalid.")
		}
		allowance = new(number)
	}
	buckets := make([]Bucket, 0, len(windows))
	for index, raw := range windows {
		bucket, err := openAIWindow(raw, fmt.Sprintf("app:%d", index), "chatgpt-app", "App limits")
		if err != nil {
			return nil, nil, err
		}
		buckets = append(buckets, bucket)
	}
	return buckets, allowance, nil
}

func openAICompanionReport(plan map[string]any, appBuckets []Bucket, allowance *float64, capturedAt int64) (Report, error) {
	buckets := []Bucket{}
	limits := object(plan["rate_limit"])
	for _, position := range []string{"primary", "secondary"} {
		raw := limits[position+"_window"]
		if raw == nil {
			continue
		}
		bucket, err := openAIWindow(raw, "plan:"+position, "chatgpt-plan", "Plan limits")
		if err != nil {
			return Report{}, err
		}
		buckets = append(buckets, bucket)
	}
	if len(buckets) == 0 {
		return Report{}, errors.New("ChatGPT plan usage returned no displayable windows.")
	}
	report := Report{ProviderID: "openai", ProviderName: "OpenAI", CapturedAt: capturedAt, Source: "openai-chatgpt-companion", Semantics: Semantics{Kind: "consumer-subscription", Label: "ChatGPT plan and app limits (experimental companion source)"}, Buckets: append(buckets, appBuckets...), Metrics: []Metric{}, Notes: []string{"Source: companion Codex OAuth; registration matching is not independent proof of the same user/workspace.", "App allowance caps shared plan usage; it is not remaining or reserved quota.", "Manage usage: " + chatGPTUsageSettingsURL}}
	if allowance != nil {
		report.Metrics = append(report.Metrics, Metric{ID: "app-allowance", Label: "App allowance", Value: *allowance, Unit: "percent"})
	}
	return report, nil
}

func openAIWindow(raw any, id, groupID, groupLabel string) (Bucket, error) {
	window := object(raw)
	used, validUsed := percentage(window["used_percent"])
	remaining := 100 - used
	validRemaining := true
	if raw, present := window["remaining_percent"]; present {
		remaining, validRemaining = percentage(raw)
		validRemaining = validRemaining && math.Abs(used+remaining-100) <= 1
	}
	seconds, validSeconds := finiteNumber(window["limit_window_seconds"], false)
	validSeconds = validSeconds && safeInteger(seconds) && seconds > 0
	var reset *float64
	validReset := true
	if raw, present := window["reset_at"]; present {
		value, ok := finiteNumber(raw, false)
		validReset = ok && safeInteger(value) && value >= 0 && value <= 8_640_000_000_000
		reset = new(value)
	}
	if !validUsed || !validRemaining || !validSeconds || !validReset {
		return Bucket{}, errors.New("ChatGPT usage returned an invalid or contradictory window.")
	}
	return Bucket{ID: id, Label: "Subscription limit", GroupID: groupID, GroupLabel: groupLabel, Used: new(used), Remaining: new(remaining), Limit: new(100.0), Unit: "percent", WindowMinutes: new(math.Ceil(seconds / 60)), ResetsAt: reset}, nil
}

func percentage(raw any) (float64, bool) {
	value, ok := finiteNumber(raw, false)
	return value, ok && value >= 0 && value <= 100
}
