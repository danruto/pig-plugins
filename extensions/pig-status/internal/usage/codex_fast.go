package usage

import (
	"maps"
	"regexp"
)

const (
	CodexFastServiceTier     = "priority"
	CodexStandardServiceTier = "default"
	CodexFastUsageWarning    = "Fast is about 1.5× faster and uses more of your plan allowance."
)

const (
	FastSupported   = "supported"
	FastUnsupported = "unsupported"
	FastUnknown     = "unknown"
)

const (
	FastAvailable   = "available"
	FastNotCodex    = "not-codex"
	FastUnavailable = "unavailable"
)

//nolint:gochecknoglobals // a shared sentinel value; Go has no struct constants
var UnknownFastCapability = FastCapability{Kind: FastUnknown, Reason: "Codex Fast capability information has not been fetched."}

var codexStatusPattern = regexp.MustCompile(`^codex(\s|$)`)

type ModelCost struct{ Input, Output, CacheRead, CacheWrite float64 }

type CodexModel struct {
	Provider string
	ID       string
	API      string
	BaseURL  string
	Cost     ModelCost
}

type FastCapability struct {
	Kind   string
	Reason string
}

type FastAvailability struct {
	Kind    string
	Enabled bool
	Reason  string
}

func CodexModelFrom(model map[string]any) (CodexModel, bool) {
	provider, ok := model["provider"].(string)
	if !ok {
		return CodexModel{}, false
	}
	id, ok := model["id"].(string)
	if !ok {
		return CodexModel{}, false
	}
	result := CodexModel{Provider: provider, ID: id}
	result.API, _ = model["api"].(string)
	result.BaseURL, _ = model["baseUrl"].(string)
	cost, _ := model["cost"].(map[string]any)
	result.Cost = ModelCost{
		Input:      costField(cost, "input"),
		Output:     costField(cost, "output"),
		CacheRead:  costField(cost, "cacheRead"),
		CacheWrite: costField(cost, "cacheWrite"),
	}
	return result, true
}

func costField(cost map[string]any, key string) float64 {
	number, _ := finiteNumber(cost[key])
	return number
}

func IsOfficialCodexModel(model *CodexModel) bool {
	return model != nil && providerID(model.Provider) == providerOpenAICodex &&
		model.API == "openai-codex-responses" &&
		sameHTTPSOrigin(model.BaseURL, "https://chatgpt.com")
}

func CodexFastAvailability(model *CodexModel, enabled bool, capability FastCapability) FastAvailability {
	if model == nil || providerID(model.Provider) != providerOpenAICodex {
		return FastAvailability{Kind: FastNotCodex}
	}
	if !IsOfficialCodexModel(model) {
		return FastAvailability{Kind: FastUnavailable, Reason: "Fast mode requires the official OpenAI Codex Responses endpoint."}
	}
	if capability.Kind == FastUnknown {
		return FastAvailability{Kind: FastUnknown, Reason: capability.Reason}
	}
	if capability.Kind == FastUnsupported {
		return FastAvailability{Kind: FastUnavailable, Reason: sanitize(model.ID) + " does not advertise Codex Fast support."}
	}
	return FastAvailability{Kind: FastAvailable, Enabled: enabled}
}

func CodexFastIsEffective(model *CodexModel, enabled bool, capability FastCapability) bool {
	return CodexFastAvailability(model, enabled, capability).Kind == FastAvailable && enabled
}

func CodexFastRequestTier(model *CodexModel, enabled bool, capability FastCapability) string {
	switch {
	case !IsOfficialCodexModel(model):
		return ""
	case enabled && capability.Kind == FastSupported:
		return CodexFastServiceTier
	default:
		return CodexStandardServiceTier
	}
}

func RewriteCodexFastPayload(payload any, model *CodexModel, enabled bool, capability FastCapability) (map[string]any, bool) {
	tier := CodexFastRequestTier(model, enabled, capability)
	if tier == "" {
		return nil, false
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, false
	}
	rewritten := maps.Clone(object)
	rewritten["service_tier"] = tier
	return rewritten, true
}

func CorrectCodexFastMessageCost(message any, model *CodexModel, fastRequested bool, capability FastCapability) (map[string]any, bool) {
	effective := CodexFastIsEffective(model, fastRequested, capability)
	object, ok := message.(map[string]any)
	if !effective || !ok || !codexFastAssistantMessage(object, model) {
		return nil, false
	}
	usage, ok := object["usage"].(map[string]any)
	if !ok {
		return nil, false
	}
	existing, ok := usage["cost"].(map[string]any)
	if !ok {
		return nil, false
	}
	corrected, ok := codexFastCost(model, usage)
	if !ok || sameCodexFastCost(existing, corrected) {
		return nil, false
	}
	copiedUsage := maps.Clone(usage)
	copiedUsage["cost"] = corrected
	copied := maps.Clone(object)
	copied["usage"] = copiedUsage
	return copied, true
}

func codexFastAssistantMessage(message map[string]any, model *CodexModel) bool {
	return message["role"] == "assistant" && message["provider"] == model.Provider && message["model"] == model.ID
}

func codexFastCost(model *CodexModel, usage map[string]any) (map[string]any, bool) {
	usageKeys := [4]string{"input", "output", "cacheRead", "cacheWrite"}
	var tokens [4]float64
	for index, key := range usageKeys {
		number, ok := finiteNumber(usage[key])
		if !ok {
			return nil, false
		}
		tokens[index] = number
	}
	multiplier := 2.0
	if model.ID == "gpt-5.5" {
		multiplier = 2.5
	}
	rates := [4]float64{model.Cost.Input, model.Cost.Output, model.Cost.CacheRead, model.Cost.CacheWrite}
	cost := make(map[string]any, len(usageKeys)+1)
	total := 0.0
	for index, key := range usageKeys {
		value := rates[index] / 1e6 * tokens[index] * multiplier
		cost[key] = value
		total += value
	}
	cost["total"] = total
	return cost, true
}

func sameCodexFastCost(existing, corrected map[string]any) bool {
	for _, key := range [5]string{"input", "output", "cacheRead", "cacheWrite", "total"} {
		value, ok := existing[key].(float64)
		if !ok || value != corrected[key] {
			return false
		}
	}
	return true
}

func CodexFastStatusLabel(status string, enabled bool) string {
	switch {
	case !enabled || !codexStatusPattern.MatchString(status):
		return status
	default:
		return "codex fast" + status[len("codex"):]
	}
}
