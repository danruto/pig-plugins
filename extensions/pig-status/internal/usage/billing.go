package usage

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func basetenBillingURL(at time.Time) string {
	query := url.Values{}
	query.Set("start_date", at.Add(-30*24*time.Hour).UTC().Format("2006-01-02T15:04:05.000Z"))
	query.Set("end_date", at.UTC().Format("2006-01-02T15:04:05.000Z"))
	return "https://api.baseten.co/v1/billing/usage_summary?" + query.Encode()
}

func normalizeBaseten(payload map[string]any, capturedAt int64) (Report, error) {
	report := Report{
		ProviderID: "baseten", ProviderName: "Baseten", CapturedAt: capturedAt,
		Source: "baseten-billing-usage-summary", Semantics: Semantics{Kind: "api-key", Label: "Organization Model APIs spend"},
		Buckets: []Bucket{}, Metrics: []Metric{},
	}
	if payload["model_apis_usage"] == nil {
		report.Notes = []string{"Baseten returned no Model APIs usage for the last 30 days."}
		return report, nil
	}
	data := object(payload["model_apis_usage"])
	if data == nil {
		return Report{}, errors.New("Baseten Model APIs usage was not an object.")
	}
	for _, field := range [][3]string{
		{"total", "gross-usage", "Gross usage"},
		{"credits_used", "credits-used", "Credits used"},
		{"subtotal", "net-subtotal", "Net subtotal"},
	} {
		value := data[field[0]]
		if number, ok := finiteNumber(value, false); ok {
			value = jsNumber(number)
		}
		if !validDecimal(value, false) {
			return Report{}, fmt.Errorf("Baseten %s was not a valid nonnegative amount.", strings.ToLower(field[2]))
		}
		report.Metrics = append(report.Metrics, Metric{ID: field[1], Label: field[2], Value: value, Unit: "currency", Currency: "USD"})
	}
	return report, nil
}

func normalizeOpenRouter(payload map[string]any, capturedAt int64) (Report, error) {
	data := object(payload["data"])
	if data == nil {
		return Report{}, errors.New("OpenRouter key response data was not an object.")
	}
	report := Report{
		ProviderID: "openrouter", ProviderName: "OpenRouter", CapturedAt: capturedAt,
		Source: "openrouter-key", Semantics: Semantics{Kind: "api-key", Label: "API-key spend limits"},
		AccountLabel: displayString(data["label"], 80), Buckets: []Bucket{}, Metrics: []Metric{},
	}
	if limit, ok := nonnegativeNumber(data["limit"]); ok {
		bucket := Bucket{ID: "key-limit", Label: "Key limit", Limit: &limit, Unit: "usd", Period: displayString(data["limit_reset"], 80)}
		if remaining, ok := nonnegativeNumber(data["limit_remaining"]); ok {
			used := max(0, limit-remaining)
			bucket.Used, bucket.Remaining = &used, &remaining
		}
		report.Buckets = append(report.Buckets, bucket)
	}
	for _, field := range [][3]string{
		{"usage_daily", "usage-daily", "Usage today"},
		{"usage_weekly", "usage-weekly", "Usage this week"},
		{"usage_monthly", "usage-monthly", "Usage this month"},
		{"usage", "usage-total", "All-time usage"},
	} {
		if amount, ok := nonnegativeNumber(data[field[0]]); ok {
			report.Metrics = append(report.Metrics, Metric{ID: field[1], Label: field[2], Value: amount, Unit: "usd"})
		}
	}
	if len(report.Buckets) == 0 && len(report.Metrics) == 0 {
		return Report{}, errors.New("OpenRouter key response returned no displayable usage data.")
	}
	if value, present := data["limit"]; present && value == nil {
		report.Notes = append(report.Notes, "No per-key spend cap")
	} else if _, ok := nonnegativeNumber(value); !ok {
		report.Notes = append(report.Notes, "Per-key spend cap unavailable")
	}
	if data["is_free_tier"] == true {
		report.Notes = append(report.Notes, "Free-tier API key")
	}
	return report, nil
}
