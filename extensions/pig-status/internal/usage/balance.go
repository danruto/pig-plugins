package usage

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var decimalAmount = regexp.MustCompile(`^-?(?:0|[1-9]\d*)(?:\.\d+)?$`)

func normalizeReport(providerID string, payload map[string]any, capturedAt int64) (Report, error) {
	switch providerID {
	case "command-code":
		return normalizeCommandCode(payload, capturedAt)
	case "opencode-go":
		return normalizeOpenCode(payload, capturedAt)
	case "kimi-coding":
		return normalizeKimi(payload, capturedAt)
	case "openai-codex":
		return normalizeCodex(payload, capturedAt)
	case "github-copilot":
		return normalizeCopilot(payload, capturedAt)
	case "openai":
		return normalizeOpenAI(payload, capturedAt)
	case "baseten":
		return normalizeBaseten(payload, capturedAt)
	case "openrouter":
		return normalizeOpenRouter(payload, capturedAt)
	case "deepseek":
		return normalizeDeepSeek(payload, capturedAt)
	case "vercel-ai-gateway":
		return normalizeVercel(payload, capturedAt)
	case "moonshotai", "moonshotai-cn":
		return normalizeMoonshot(providerID, payload, capturedAt)
	default:
		return Report{}, errors.New("unsupported usage provider")
	}
}

func normalizeDeepSeek(payload map[string]any, capturedAt int64) (Report, error) {
	available, ok := payload["is_available"].(bool)
	if !ok {
		return Report{}, errors.New("DeepSeek API balance response availability was not a boolean.")
	}
	rows, ok := payload["balance_infos"].([]any)
	if !ok || len(rows) == 0 {
		return Report{}, errors.New("DeepSeek API balance response returned no balance information.")
	}
	fields := [][3]string{
		{"total", "Total balance", "total_balance"},
		{"granted", "Granted balance", "granted_balance"},
		{"topped-up", "Topped-up balance", "topped_up_balance"},
	}
	balances := make(map[string]map[string]any)
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			return Report{}, errors.New("DeepSeek API balance row was not an object.")
		}
		currency, ok := row["currency"].(string)
		if !ok || (currency != "CNY" && currency != "USD") {
			return Report{}, errors.New("DeepSeek API balance row returned an unsupported currency.")
		}
		if _, exists := balances[currency]; exists {
			return Report{}, fmt.Errorf("DeepSeek API balance response repeated %s.", currency)
		}
		for _, field := range fields {
			if !validDecimal(row[field[2]], true) {
				return Report{}, fmt.Errorf("DeepSeek API balance %s was not a valid amount.", strings.ToLower(field[1]))
			}
		}
		balances[currency] = row
	}
	availability := "unavailable"
	if available {
		availability = "available"
	}
	metrics := []Metric{{ID: "api-availability", Label: "API calls", Value: availability}}
	for _, currency := range []string{"CNY", "USD"} {
		row, exists := balances[currency]
		if !exists {
			continue
		}
		for _, field := range fields {
			metrics = append(metrics, Metric{
				ID: strings.ToLower(currency) + "-" + field[0], Label: field[1],
				Value: row[field[2]], Unit: "currency", Currency: currency,
			})
		}
	}
	return Report{
		ProviderID: "deepseek", ProviderName: "DeepSeek", CapturedAt: capturedAt,
		Source: "deepseek-balance", Semantics: Semantics{Kind: "api-key", Label: "DeepSeek API balance"},
		Buckets: []Bucket{}, Metrics: metrics,
	}, nil
}

func normalizeVercel(payload map[string]any, capturedAt int64) (Report, error) {
	metrics := make([]Metric, 0, 2)
	for _, field := range [][4]string{
		{"balance", "balance", "credit-balance", "Credit balance"},
		{"total_used", "total used", "lifetime-spend", "Lifetime spend"},
	} {
		if !validDecimal(payload[field[0]], false) {
			return Report{}, fmt.Errorf("Vercel AI Gateway %s was not a valid nonnegative amount.", field[1])
		}
		metrics = append(metrics, Metric{ID: field[2], Label: field[3], Value: payload[field[0]], Unit: "currency", Currency: "USD"})
	}
	return Report{
		ProviderID: "vercel-ai-gateway", ProviderName: "Vercel AI Gateway", CapturedAt: capturedAt,
		Source: "vercel-ai-gateway-credits", Semantics: Semantics{Kind: "api-key", Label: "AI Gateway credits and lifetime spend"},
		Buckets: []Bucket{}, Metrics: metrics,
	}, nil
}

func normalizeMoonshot(providerID string, payload map[string]any, capturedAt int64) (Report, error) {
	code, validCode := payload["code"].(float64)
	status, validStatus := payload["status"].(bool)
	if !validCode || code != 0 || !validStatus || !status {
		return Report{}, errors.New("Moonshot AI balance response did not report success.")
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		return Report{}, errors.New("Moonshot AI balance response data was not an object.")
	}
	name, currency := "Moonshot AI", "USD"
	if providerID == "moonshotai-cn" {
		name, currency = "Moonshot AI CN", "CNY"
	}
	metrics := make([]Metric, 0, 3)
	for _, field := range [][3]string{
		{"available_balance", "available-balance", "Available balance"},
		{"voucher_balance", "voucher-balance", "Voucher balance"},
		{"cash_balance", "cash-balance", "Cash balance"},
	} {
		value, ok := data[field[0]].(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 9_007_199_254_740_991 || (field[0] != "cash_balance" && value < 0) {
			return Report{}, fmt.Errorf("Moonshot AI %s was not a valid amount.", strings.ToLower(field[2]))
		}
		metrics = append(metrics, Metric{ID: field[1], Label: field[2], Value: jsNumber(value), Unit: "currency", Currency: currency})
	}
	return Report{
		ProviderID: providerID, ProviderName: name, CapturedAt: capturedAt,
		Source: "moonshot-balance", Semantics: Semantics{Kind: "api-key", Label: "Moonshot API account balance"},
		Buckets: []Bucket{}, Metrics: metrics,
	}, nil
}

func validDecimal(value any, allowNegative bool) bool {
	text, ok := value.(string)
	return ok && len(text) <= 64 && decimalAmount.MatchString(text) && (allowNegative || !strings.HasPrefix(text, "-"))
}

func jsNumber(value float64) string {
	if value == 0 {
		return "0"
	}
	if math.Abs(value) >= 1e-6 && math.Abs(value) < 1e21 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	return strings.NewReplacer("e-0", "e-", "e+0", "e+").Replace(strconv.FormatFloat(value, 'e', -1, 64))
}
