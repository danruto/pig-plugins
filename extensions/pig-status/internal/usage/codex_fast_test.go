package usage

import (
	"reflect"
	"testing"
)

func TestNormalizeDirectory(t *testing.T) {
	supported := true
	unsupported := false
	for _, tt := range []struct {
		name       string
		payload    map[string]any
		want       map[string]*bool
		wantReason string
	}{
		{
			name:    "priority tier is supported",
			payload: map[string]any{"models": []any{map[string]any{"slug": "gpt-5.5", "service_tiers": []any{map[string]any{"id": "default"}, map[string]any{"id": "priority"}}}}},
			want:    map[string]*bool{"gpt-5.5": &supported},
		},
		{
			name:    "legacy fast tier is supported",
			payload: map[string]any{"models": []any{map[string]any{"slug": "gpt-5.1", "additional_speed_tiers": []any{"standard", "fast"}}}},
			want:    map[string]*bool{"gpt-5.1": &supported},
		},
		{
			name:    "standard tiers only is unsupported",
			payload: map[string]any{"models": []any{map[string]any{"slug": "gpt-5.1", "service_tiers": []any{map[string]any{"id": "default"}}, "additional_speed_tiers": []any{"quick"}}}},
			want:    map[string]*bool{"gpt-5.1": &unsupported},
		},
		{
			name:    "listed without tier information carries no answer",
			payload: map[string]any{"models": []any{map[string]any{"slug": "gpt-5.1"}}},
			want:    map[string]*bool{"gpt-5.1": nil},
		},
		{
			name:       "models must be an array",
			payload:    map[string]any{"models": map[string]any{"slug": "gpt-5.5"}},
			wantReason: "The Codex model directory returned invalid models.",
		},
		{
			name:       "model entries must be objects",
			payload:    map[string]any{"models": []any{"gpt-5.5"}},
			wantReason: "The Codex model directory returned an invalid or duplicate model ID.",
		},
		{
			name:       "blank slug is rejected",
			payload:    map[string]any{"models": []any{map[string]any{"slug": "   "}}},
			wantReason: "The Codex model directory returned an invalid or duplicate model ID.",
		},
		{
			name:       "duplicate slug is rejected",
			payload:    map[string]any{"models": []any{map[string]any{"slug": "gpt-5.5"}, map[string]any{"slug": "gpt-5.5"}}},
			wantReason: "The Codex model directory returned an invalid or duplicate model ID.",
		},
		{
			name:       "service tiers need a string id",
			payload:    map[string]any{"models": []any{map[string]any{"slug": "gpt-5.5", "service_tiers": []any{map[string]any{"name": "priority"}}}}},
			wantReason: "The Codex model directory returned invalid service tiers.",
		},
		{
			name:       "service tiers must be an array",
			payload:    map[string]any{"models": []any{map[string]any{"slug": "gpt-5.5", "service_tiers": "priority"}}},
			wantReason: "The Codex model directory returned invalid service tiers.",
		},
		{
			name:       "legacy speed tiers must be strings",
			payload:    map[string]any{"models": []any{map[string]any{"slug": "gpt-5.5", "additional_speed_tiers": []any{1}}}},
			wantReason: "The Codex model directory returned invalid service tiers.",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			models, err := normalizeDirectory(tt.payload)
			if tt.wantReason != "" {
				if err == nil || codexFastFailureReason(err) != tt.wantReason {
					t.Fatalf("normalizeDirectory error = %v, want reason %q", err, tt.wantReason)
				}
				if models != nil {
					t.Fatalf("normalizeDirectory returned %v alongside an error", models)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeDirectory returned unexpected error %v", err)
			}
			if !reflect.DeepEqual(models, tt.want) {
				t.Fatalf("normalizeDirectory = %v, want %v", renderTiers(models), renderTiers(tt.want))
			}
		})
	}
}

func renderTiers(models map[string]*bool) map[string]string {
	rendered := map[string]string{}
	for id, supported := range models {
		switch {
		case supported == nil:
			rendered[id] = "no information"
		case *supported:
			rendered[id] = "supported"
		default:
			rendered[id] = "unsupported"
		}
	}
	return rendered
}

const (
	codexFastInputRate      = 1.25
	codexFastOutputRate     = 10.0
	codexFastCacheReadRate  = 0.125
	codexFastCacheWriteRate = 1.25
)

func codexFastTestCaseModel(id string) CodexModel {
	return CodexModel{
		Provider: "openai-codex",
		ID:       id,
		API:      "openai-codex-responses",
		BaseURL:  "https://chatgpt.com/backend-api/codex",
		Cost: ModelCost{
			Input:      codexFastInputRate,
			Output:     codexFastOutputRate,
			CacheRead:  codexFastCacheReadRate,
			CacheWrite: codexFastCacheWriteRate,
		},
	}
}

func codexFastTestCaseMessage(model CodexModel) map[string]any {
	return map[string]any{
		"role":     "assistant",
		"provider": model.Provider,
		"model":    model.ID,
		"usage": map[string]any{
			"input":       400.0,
			"output":      1000.0,
			"cacheRead":   2000.0,
			"cacheWrite":  400.0,
			"totalTokens": 3400.0,
			"cost":        map[string]any{"input": 0.5, "output": 0.5, "cacheRead": 0.5, "cacheWrite": 0.5, "total": 2.0},
		},
	}
}

func TestCorrectCodexFastMessageCostIsIdempotent(t *testing.T) {
	model := codexFastTestCaseModel("gpt-5.5")
	capability := FastCapability{Kind: FastSupported}
	corrected, ok := CorrectCodexFastMessageCost(codexFastTestCaseMessage(model), &model, true, capability)
	if !ok || corrected == nil {
		t.Fatalf("CorrectCodexFastMessageCost = %v, %v; want a corrected message", corrected, ok)
	}
	again, changed := CorrectCodexFastMessageCost(corrected, &model, true, capability)
	if changed || again != nil {
		t.Fatalf("re-correcting an already corrected message = %v, %v; want no change", again, changed)
	}
}
