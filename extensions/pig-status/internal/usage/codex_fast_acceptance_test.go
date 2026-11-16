package usage

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"testing"
)

const (
	acceptFastProvider  = "openai-codex"
	acceptFastAPI       = "openai-codex-responses"
	acceptFastBaseURL   = "https://chatgpt.com/backend-api/codex"
	acceptFastModelID   = "gpt-5.4"
	acceptFastPremiumID = "gpt-5.5"
)

func acceptFastCost() ModelCost {
	return ModelCost{Input: 1, Output: 2, CacheRead: 0.5, CacheWrite: 0.25}
}

func acceptFastModel(id string) *CodexModel {
	return &CodexModel{Provider: acceptFastProvider, ID: id, API: acceptFastAPI, BaseURL: acceptFastBaseURL, Cost: acceptFastCost()}
}

func acceptFastSnapshot(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	return decoded
}

func acceptFastMessageUsage(t *testing.T, message map[string]any) map[string]any {
	t.Helper()
	usage, ok := message["usage"].(map[string]any)
	if !ok {
		t.Fatalf("message usage = %T, want a usage object", message["usage"])
	}
	return usage
}

func acceptFastUsageCost(t *testing.T, usage map[string]any) map[string]any {
	t.Helper()
	cost, ok := usage["cost"].(map[string]any)
	if !ok {
		t.Fatalf("usage cost = %T, want a cost object", usage["cost"])
	}
	return cost
}

func acceptFastAssistantMessage(provider, model string, usage map[string]any) map[string]any {
	return map[string]any{"role": "assistant", "provider": provider, "model": model, "usage": usage}
}

func acceptFastZeroCost() map[string]any {
	return map[string]any{"input": float64(0), "output": float64(0), "cacheRead": float64(0), "cacheWrite": float64(0), "total": float64(0)}
}

func acceptFastUsage(tokens float64, cost map[string]any) map[string]any {
	return map[string]any{"input": tokens, "output": tokens, "cacheRead": tokens, "cacheWrite": tokens, "cost": cost}
}

func acceptFastExpectedCost(scale float64) map[string]any {
	return map[string]any{
		"input":      1 * scale,
		"output":     2 * scale,
		"cacheRead":  0.5 * scale,
		"cacheWrite": 0.25 * scale,
		"total":      3.75 * scale,
	}
}

func acceptFastCorrectedCost(t *testing.T, want map[string]any, corrected map[string]any, detail string) {
	t.Helper()
	if want == nil {
		if corrected != nil {
			t.Fatalf("%s = %#v, want no correction", detail, corrected)
		}
		return
	}
	cost := acceptFastUsageCost(t, acceptFastMessageUsage(t, corrected))
	acceptEqual(t, slices.Sorted(maps.Keys(want)), slices.Sorted(maps.Keys(cost)), detail+" keys")
	for _, key := range slices.Sorted(maps.Keys(want)) {
		wantValue, _ := want[key].(float64)
		gotValue, ok := cost[key].(float64)
		if !ok || math.Abs(gotValue-wantValue) > 1e-9 {
			t.Fatalf("%s[%s] = %v, want %v", detail, key, cost[key], wantValue)
		}
	}
}

func TestIsOfficialCodexModelRejectsForeignEndpoints(t *testing.T) {
	for _, tt := range []struct {
		name  string
		model *CodexModel
		want  bool
	}{
		{name: "canonical backend endpoint", model: acceptFastModel(acceptFastModelID), want: true},
		{name: "uppercase host", model: &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: "https://CHATGPT.com/backend-api/codex"}, want: true},
		{name: "explicit 443 port", model: &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: "https://chatgpt.com:443/backend-api/codex"}, want: true},
		{name: "plain http scheme", model: &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: "http://chatgpt.com/backend-api/codex"}, want: false},
		{name: "embedded user info", model: &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: "https://user@chatgpt.com/backend-api/codex"}, want: false},
		{name: "foreign port", model: &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: "https://chatgpt.com:8443/backend-api/codex"}, want: false},
		{name: "subdomain", model: &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: "https://api.chatgpt.com/backend-api/codex"}, want: false},
		{name: "foreign host", model: &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: "https://api.openai.com/v1"}, want: false},
		{name: "missing base url", model: &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI}, want: false},
		{name: "foreign responses api", model: &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: "openai-responses", BaseURL: acceptFastBaseURL}, want: false},
		{name: "missing model", model: nil, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			acceptEqual(t, tt.want, IsOfficialCodexModel(tt.model), "official codex endpoint")
		})
	}
}

func TestCodexFastAvailabilityClassifiesModels(t *testing.T) {
	for _, tt := range []struct {
		name       string
		model      *CodexModel
		enabled    bool
		capability FastCapability
		want       FastAvailability
	}{
		{
			name:       "no selected model",
			capability: FastCapability{Kind: FastSupported},
			want:       FastAvailability{Kind: FastNotCodex},
		},
		{
			name:       "other provider",
			model:      &CodexModel{Provider: "deepseek", ID: "deepseek-chat", API: acceptFastAPI, BaseURL: acceptFastBaseURL},
			enabled:    true,
			capability: FastCapability{Kind: FastSupported},
			want:       FastAvailability{Kind: FastNotCodex},
		},
		{
			name:       "unofficial endpoint",
			model:      &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: "https://proxy.invalid/backend-api/codex"},
			enabled:    true,
			capability: FastCapability{Kind: FastSupported},
			want:       FastAvailability{Kind: FastUnavailable, Reason: "Fast mode requires the official OpenAI Codex Responses endpoint."},
		},
		{
			name:       "unfetched capability",
			model:      acceptFastModel(acceptFastModelID),
			enabled:    true,
			capability: FastCapability{Kind: FastUnknown, Reason: "synthetic capability gap"},
			want:       FastAvailability{Kind: FastUnknown, Reason: "synthetic capability gap"},
		},
		{
			name:       "unsupported model",
			model:      acceptFastModel(acceptFastModelID),
			enabled:    true,
			capability: FastCapability{Kind: FastUnsupported},
			want:       FastAvailability{Kind: FastUnavailable, Reason: "gpt-5.4 does not advertise Codex Fast support."},
		},
		{
			name:       "supported and enabled",
			model:      acceptFastModel(acceptFastModelID),
			enabled:    true,
			capability: FastCapability{Kind: FastSupported},
			want:       FastAvailability{Kind: FastAvailable, Enabled: true},
		},
		{
			name:       "supported and disabled",
			model:      acceptFastModel(acceptFastModelID),
			enabled:    false,
			capability: FastCapability{Kind: FastSupported},
			want:       FastAvailability{Kind: FastAvailable, Enabled: false},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			acceptEqual(t, tt.want, CodexFastAvailability(tt.model, tt.enabled, tt.capability), "availability")
		})
	}
}

func TestCodexFastRequestTierForcesStandardUnlessEffective(t *testing.T) {
	for _, tt := range []struct {
		name       string
		model      *CodexModel
		enabled    bool
		capability FastCapability
		want       string
	}{
		{name: "other provider", model: &CodexModel{Provider: "deepseek", ID: "deepseek-chat", API: acceptFastAPI, BaseURL: acceptFastBaseURL}, enabled: true, capability: FastCapability{Kind: FastSupported}, want: ""},
		{name: "official but unofficial endpoint", model: &CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: "https://api.openai.com/v1"}, enabled: true, capability: FastCapability{Kind: FastSupported}, want: ""},
		{name: "enabled and supported", model: acceptFastModel(acceptFastModelID), enabled: true, capability: FastCapability{Kind: FastSupported}, want: "priority"},
		{name: "disabled but supported", model: acceptFastModel(acceptFastModelID), enabled: false, capability: FastCapability{Kind: FastSupported}, want: "default"},
		{name: "enabled but unsupported", model: acceptFastModel(acceptFastModelID), enabled: true, capability: FastCapability{Kind: FastUnsupported}, want: "default"},
		{name: "enabled but unknown", model: acceptFastModel(acceptFastModelID), enabled: true, capability: FastCapability{Kind: FastUnknown, Reason: "synthetic capability gap"}, want: "default"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			acceptEqual(t, tt.want, CodexFastRequestTier(tt.model, tt.enabled, tt.capability), "request tier")
		})
	}
}

func TestRewriteCodexFastPayloadCopiesThePayload(t *testing.T) {
	supported := FastCapability{Kind: FastSupported}
	userInput := []any{map[string]any{"role": "user", "content": "synthetic"}}
	standardPayload := func() map[string]any {
		return map[string]any{"model": acceptFastModelID, "input": userInput, "service_tier": "default"}
	}
	expectedPayload := func(tier string) map[string]any {
		return map[string]any{"model": acceptFastModelID, "input": userInput, "service_tier": tier}
	}

	for _, tt := range []struct {
		name       string
		payload    any
		model      *CodexModel
		enabled    bool
		capability FastCapability
		want       map[string]any
	}{
		{name: "enabled official model", payload: standardPayload(), model: acceptFastModel(acceptFastModelID), enabled: true, capability: supported, want: expectedPayload("priority")},
		{name: "disabled official model", payload: standardPayload(), model: acceptFastModel(acceptFastModelID), enabled: false, capability: supported, want: expectedPayload("default")},
		{name: "unsupported official model falls back", payload: standardPayload(), model: acceptFastModel(acceptFastModelID), enabled: true, capability: FastCapability{Kind: FastUnsupported}, want: expectedPayload("default")},
		{name: "other provider untouched", payload: standardPayload(), model: &CodexModel{Provider: "deepseek", ID: "deepseek-chat", API: acceptFastAPI, BaseURL: acceptFastBaseURL}, enabled: true, capability: supported, want: nil},
		{name: "non object payload untouched", payload: []any{"item"}, model: acceptFastModel(acceptFastModelID), enabled: true, capability: supported, want: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := acceptFastSnapshot(t, tt.payload)

			rewritten, ok := RewriteCodexFastPayload(tt.payload, tt.model, tt.enabled, tt.capability)

			acceptEqual(t, tt.want != nil, ok, "rewrite applied")
			acceptEqual(t, tt.want, rewritten, "rewritten payload")
			acceptEqual(t, snapshot, tt.payload, "payload after rewrite")
		})
	}
}

func TestCorrectCodexFastMessageCostScalesAssistantUsage(t *testing.T) {
	zeroCost := func() map[string]any { return acceptFastZeroCost() }
	incompleteUsage := map[string]any{"input": float64(1e6), "output": float64(1e6), "cacheRead": float64(1e6), "cost": zeroCost()}
	missingUsage := map[string]any{"role": "assistant", "provider": acceptFastProvider, "model": acceptFastModelID}
	textCostUsage := map[string]any{"input": float64(1e6), "output": float64(1e6), "cacheRead": float64(1e6), "cacheWrite": float64(1e6), "cost": "synthetic"}

	for _, tt := range []struct {
		name          string
		message       any
		model         *CodexModel
		fastRequested bool
		capability    FastCapability
		wantCost      map[string]any
	}{
		{
			name:          "premium model scaled by two point five",
			message:       acceptFastAssistantMessage(acceptFastProvider, acceptFastPremiumID, acceptFastUsage(1e6, zeroCost())),
			model:         acceptFastModel(acceptFastPremiumID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      acceptFastExpectedCost(2.5),
		},
		{
			name:          "standard model scaled by two",
			message:       acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, acceptFastUsage(1e6, zeroCost())),
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      acceptFastExpectedCost(2),
		},
		{
			name:          "provider mismatch",
			message:       acceptFastAssistantMessage("deepseek", acceptFastModelID, acceptFastUsage(1e6, zeroCost())),
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      nil,
		},
		{
			name:          "model mismatch",
			message:       acceptFastAssistantMessage(acceptFastProvider, acceptFastPremiumID, acceptFastUsage(1e6, zeroCost())),
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      nil,
		},
		{
			name:          "user message",
			message:       map[string]any{"role": "user", "provider": acceptFastProvider, "model": acceptFastModelID, "usage": acceptFastUsage(1e6, zeroCost())},
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      nil,
		},
		{
			name:          "incomplete usage",
			message:       acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, incompleteUsage),
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      nil,
		},
		{
			name:          "missing usage",
			message:       missingUsage,
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      nil,
		},
		{
			name:          "non object cost",
			message:       acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, textCostUsage),
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      nil,
		},
		{
			name:          "already matching cost",
			message:       acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, acceptFastUsage(0, zeroCost())),
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      nil,
		},
		{
			name:          "unsupported capability",
			message:       acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, acceptFastUsage(1e6, zeroCost())),
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastUnsupported},
			wantCost:      nil,
		},
		{
			name:          "standard request",
			message:       acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, acceptFastUsage(1e6, zeroCost())),
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: false,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      nil,
		},
		{
			name:          "non object message",
			message:       "synthetic",
			model:         acceptFastModel(acceptFastModelID),
			fastRequested: true,
			capability:    FastCapability{Kind: FastSupported},
			wantCost:      nil,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := acceptFastSnapshot(t, tt.message)

			corrected, ok := CorrectCodexFastMessageCost(tt.message, tt.model, tt.fastRequested, tt.capability)

			acceptEqual(t, tt.wantCost != nil, ok, "cost correction applied")
			acceptFastCorrectedCost(t, tt.wantCost, corrected, "corrected cost")
			acceptEqual(t, snapshot, tt.message, "message after correction")
		})
	}
}

func TestCorrectCodexFastMessageCostKeepsOtherUsageFields(t *testing.T) {
	message := acceptFastAssistantMessage(acceptFastProvider, acceptFastModelID, map[string]any{
		"input":       float64(1e6),
		"output":      float64(1e6),
		"cacheRead":   float64(1e6),
		"cacheWrite":  float64(1e6),
		"totalTokens": float64(4e6),
		"cost":        map[string]any{"input": float64(0), "output": float64(0), "cacheRead": float64(0), "cacheWrite": float64(0), "total": float64(0), "synthetic": float64(1)},
	})

	corrected, ok := CorrectCodexFastMessageCost(message, acceptFastModel(acceptFastModelID), true, FastCapability{Kind: FastSupported})

	acceptEqual(t, true, ok, "cost correction applied")
	usage := acceptFastMessageUsage(t, corrected)
	acceptEqual(t, []string{"cacheRead", "cacheWrite", "cost", "input", "output", "totalTokens"}, slices.Sorted(maps.Keys(usage)), "usage keys")
	acceptEqual[any](t, float64(4e6), usage["totalTokens"], "retained token total")
	acceptEqual(t, []string{"cacheRead", "cacheWrite", "input", "output", "total"}, slices.Sorted(maps.Keys(acceptFastUsageCost(t, usage))), "cost keys")
}

func TestCodexFastStatusLabelRewritesCodexStatuses(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  string
		enabled bool
		want    string
	}{
		{name: "bare codex", status: "codex", enabled: true, want: "codex fast"},
		{name: "codex with detail", status: "codex weekly", enabled: true, want: "codex fast weekly"},
		{name: "disabled", status: "codex weekly", enabled: false, want: "codex weekly"},
		{name: "other provider", status: "deepseek", enabled: true, want: "deepseek"},
		{name: "codex prefix without separator", status: "codexfast", enabled: true, want: "codexfast"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			acceptEqual(t, tt.want, CodexFastStatusLabel(tt.status, tt.enabled), "status label")
		})
	}
}

func TestCodexModelFromReadsHostModel(t *testing.T) {
	costed := map[string]any{"input": float64(1), "output": float64(2), "cacheRead": float64(0.5), "cacheWrite": float64(0.25)}
	for _, tt := range []struct {
		name  string
		model map[string]any
		want  CodexModel
		ok    bool
	}{
		{
			name:  "complete host model",
			model: map[string]any{"provider": acceptFastProvider, "id": acceptFastModelID, "api": acceptFastAPI, "baseUrl": acceptFastBaseURL, "cost": costed, "maxTokens": float64(64)},
			want:  CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: acceptFastBaseURL, Cost: acceptFastCost()},
			ok:    true,
		},
		{
			name:  "host model without cost",
			model: map[string]any{"provider": acceptFastProvider, "id": acceptFastModelID, "api": acceptFastAPI, "baseUrl": acceptFastBaseURL},
			want:  CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: acceptFastBaseURL},
			ok:    true,
		},
		{
			name:  "cost fields of the wrong type read as zero",
			model: map[string]any{"provider": acceptFastProvider, "id": acceptFastModelID, "api": acceptFastAPI, "baseUrl": acceptFastBaseURL, "cost": map[string]any{"input": "1", "output": float64(2), "cacheRead": float64(0.5), "cacheWrite": float64(0.25)}},
			want:  CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID, API: acceptFastAPI, BaseURL: acceptFastBaseURL, Cost: ModelCost{Output: 2, CacheRead: 0.5, CacheWrite: 0.25}},
			ok:    true,
		},
		{
			name:  "non string endpoint fields and non object cost",
			model: map[string]any{"provider": acceptFastProvider, "id": acceptFastModelID, "api": float64(5), "baseUrl": false, "cost": "synthetic"},
			want:  CodexModel{Provider: acceptFastProvider, ID: acceptFastModelID},
			ok:    true,
		},
		{
			name:  "numeric provider",
			model: map[string]any{"provider": float64(5), "id": acceptFastModelID},
			ok:    false,
		},
		{
			name:  "missing model id",
			model: map[string]any{"provider": acceptFastProvider},
			ok:    false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := CodexModelFrom(tt.model)
			acceptEqual(t, tt.ok, ok, "model parsed")
			acceptEqual(t, tt.want, got, "parsed model")
		})
	}
}
