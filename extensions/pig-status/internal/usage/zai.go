package usage

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

type zaiPlan struct {
	Name     string `json:"name"`
	RenewsAt string `json:"renewsAt,omitempty"`
}

func normalizeZai(providerID string, payload map[string]any, capturedAt int64, plan *zaiPlan) (Report, error) {
	if message := zaiPayloadError(payload); message != "" {
		return Report{}, errors.New(message)
	}
	data := object(payload["data"])
	if data == nil {
		return Report{}, errors.New("Z.AI quota response data was not an object.")
	}
	name := "Z.AI"
	if providerID == "zai-coding-cn" {
		name = "Z.AI Coding CN"
	}
	report := Report{ProviderID: providerID, ProviderName: name, CapturedAt: capturedAt, Source: "zai-quota", Semantics: Semantics{Kind: "consumer-subscription", Label: "GLM Coding Plan usage"}, Buckets: []Bucket{}, Metrics: []Metric{}}
	limits, _ := data["limits"].([]any)
	for _, raw := range limits {
		limit := object(raw)
		kind := displayString(limit["type"], 80)
		unit, _ := nonnegativeNumber(limit["unit"])
		bucket := Bucket{}
		switch {
		case kind == "TIME_LIMIT":
			bucket.ID, bucket.Label = "mcp-monthly", "MCP monthly allowance"
			details, _ := limit["usageDetails"].([]any)
			for _, raw := range details {
				detail := object(raw)
				label := displayString(detail["modelCode"], 80)
				usage, ok := nonnegativeNumber(detail["usage"])
				if label == "" || !ok {
					continue
				}
				id := strings.Trim(regionalSlug.ReplaceAllString(strings.ToLower(strings.ReplaceAll(label, "İ", "I\u0307")), "-"), "-")
				if id == "" {
					id = "tool"
				}
				report.Metrics = append(report.Metrics, Metric{ID: "mcp-" + id, Label: label, Value: usage, Unit: "count"})
			}
		case (kind == "TOKENS_LIMIT" || kind == "CREDIT_LIMIT") && unit == 3:
			minutes := zaiWindowMinutes(limit["number"], 60, 300)
			bucket.ID, bucket.Label, bucket.WindowMinutes, bucket.Unit = "five-hour", "5h window", new(minutes), "percent"
			if minutes != 300 {
				bucket.Label = jsNumber(math.Round(minutes/60)) + "h window"
			}
		case (kind == "TOKENS_LIMIT" || kind == "CREDIT_LIMIT") && unit == 6:
			minutes := zaiWindowMinutes(limit["number"], 10080, 10080)
			bucket.ID, bucket.Label, bucket.WindowMinutes = "weekly", "Weekly window", new(minutes)
			_, usedOK := nonnegativeNumber(limit["currentValue"])
			_, quotaOK := nonnegativeNumber(limit["usage"])
			if !usedOK || !quotaOK {
				bucket.Unit = "percent"
			}
		default:
			continue
		}
		if bucket.WindowMinutes != nil && math.IsInf(*bucket.WindowMinutes, 0) {
			return Report{}, errors.New("Z.AI quota window was not finite.")
		}
		if bucket.Unit == "percent" {
			used, ok := nonnegativeNumber(limit["percentage"])
			if !ok {
				continue
			}
			percent := math.Min(100, used)
			bucket.Used, bucket.Remaining, bucket.Limit = new(percent), new(100-percent), new(100.0)
		} else {
			used, usedOK := nonnegativeNumber(limit["currentValue"])
			quota, quotaOK := nonnegativeNumber(limit["usage"])
			if !usedOK || !quotaOK {
				continue
			}
			bucket.Used, bucket.Remaining, bucket.Limit, bucket.Unit = new(used), new(math.Max(0, quota-used)), new(quota), "count"
		}
		if reset, ok := nonnegativeNumber(limit["nextResetTime"]); ok {
			bucket.ResetsAt = new(math.Floor(reset / 1000))
		}
		report.Buckets = append(report.Buckets, bucket)
	}
	if len(report.Buckets) == 0 {
		return Report{}, errors.New("Z.AI quota endpoint returned no displayable usage data.")
	}
	label := displayString(data["level"], 80)
	if plan != nil {
		label = plan.Name
	}
	if label != "" {
		note := "Plan: " + label
		if plan != nil && plan.RenewsAt != "" {
			note += " · renews " + plan.RenewsAt
		}
		report.Notes = []string{note}
	}
	return report, nil
}

func zaiWindowMinutes(value any, multiplier, fallback float64) float64 {
	number, ok := nonnegativeNumber(value)
	if !ok || number <= 0 {
		return fallback
	}
	return math.Round(number * multiplier)
}

var zaiDatePrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

func normalizeZaiSubscription(payload map[string]any) *zaiPlan {
	if zaiPayloadError(payload) != "" {
		return nil
	}
	rows, _ := payload["data"].([]any)
	type candidate struct {
		plan    zaiPlan
		status  string
		current *bool
	}
	candidates := []candidate{}
	hasState := false
	for _, raw := range rows {
		entry := object(raw)
		name := displayString(entry["productName"], 80)
		if name == "" {
			continue
		}
		item := candidate{plan: zaiPlan{Name: name}, status: strings.ToUpper(displayString(entry["status"], 80))}
		switch value := entry["inCurrentPeriod"].(type) {
		case bool:
			item.current = new(value)
		case float64:
			if value == 0 || value == 1 {
				item.current = new(value == 1)
			}
		}
		if date, ok := entry["nextRenewTime"].(string); ok && zaiDatePrefix.MatchString(date) {
			item.plan.RenewsAt = date[:10]
		} else if millis, ok := nonnegativeNumber(entry["nextRenewTime"]); ok && millis > 0 {
			if millis > 8_640_000_000_000_000 {
				return nil
			}
			instant := time.UnixMilli(int64(math.Trunc(millis))).UTC()
			formatted := instant.Format("2006-01-02")
			if instant.Year() > 9999 {
				formatted = fmt.Sprintf("+%06d-%02d-%02d", instant.Year(), instant.Month(), instant.Day())
			}
			item.plan.RenewsAt = formatted[:10]
		}
		hasState = hasState || item.status != "" || item.current != nil
		candidates = append(candidates, item)
	}
	if !hasState {
		if len(candidates) == 0 {
			return nil
		}
		return &candidates[0].plan
	}
	for priority := range 3 {
		for _, item := range candidates {
			if priority == 0 && item.current != nil && *item.current && item.status == "VALID" || priority == 1 && item.current != nil && *item.current && item.status == "" || priority == 2 && item.status == "VALID" && item.current == nil {
				return &item.plan
			}
		}
	}
	return nil
}
