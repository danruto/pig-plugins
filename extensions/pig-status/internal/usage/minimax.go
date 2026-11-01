package usage

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var regionalSlug = regexp.MustCompile(`[^a-z0-9]+`)

func miniMaxUsageKind(key string) string {
	if strings.HasPrefix(key, "sk-api-") {
		return "account-balance"
	}
	return "token-plan"
}

func normalizeMiniMax(providerID, kind string, payload map[string]any, capturedAt int64) (Report, error) {
	code, ok := object(payload["base_resp"])["status_code"].(float64)
	if !ok || code != 0 {
		return Report{}, errors.New("MiniMax usage response did not report success.")
	}
	name, currency := "MiniMax", "USD"
	if providerID == "minimax-cn" {
		name, currency = "MiniMax CN", "CNY"
	}
	report := Report{ProviderID: providerID, ProviderName: name, CapturedAt: capturedAt, Buckets: []Bucket{}, Metrics: []Metric{}}
	if kind == "account-balance" {
		report.Source = "minimax-account-balance"
		report.Semantics = Semantics{Kind: "api-key", Label: "MiniMax pay-as-you-go account balance"}
		for _, field := range [][3]string{{"available_amount", "available-balance", "Available balance"}, {"cash_balance", "cash-balance", "Cash balance"}, {"voucher_balance", "voucher-balance", "Voucher balance"}, {"credit_balance", "credit-balance", "Credit balance"}, {"owed_amount", "owed-amount", "Owed amount"}} {
			if !validDecimal(payload[field[0]], field[0] == "cash_balance") {
				return Report{}, fmt.Errorf("MiniMax %s was not a valid amount.", strings.ToLower(field[2]))
			}
			report.Metrics = append(report.Metrics, Metric{ID: field[1], Label: field[2], Value: payload[field[0]], Unit: "currency", Currency: currency})
		}
		return report, nil
	}
	rows, ok := payload["model_remains"].([]any)
	if !ok || len(rows) == 0 {
		return Report{}, errors.New("MiniMax Token Plan returned no quota rows.")
	}
	report.Source = "minimax-token-plan"
	report.Semantics = Semantics{Kind: "consumer-subscription", Label: "MiniMax Token Plan quota"}
	groups := make(map[string]bool)
	for i, raw := range rows {
		row := object(raw)
		if row == nil {
			return Report{}, errors.New("MiniMax Token Plan quota row was not an object.")
		}
		if _, ok := row["model_name"].(string); !ok {
			return Report{}, errors.New("MiniMax Token Plan model name was not a string.")
		}
		label := displayString(row["model_name"], 80)
		if label == "" {
			label = "Quota " + strconv.Itoa(i+1)
		}
		id := strings.Trim(regionalSlug.ReplaceAllString(strings.ToLower(strings.ReplaceAll(label, "İ", "I\u0307")), "-"), "-")
		if id == "" {
			id = "quota"
		}
		if groups[id] {
			id += "-" + strconv.Itoa(i+1)
		}
		groups[id] = true
		for _, weekly := range []bool{false, true} {
			bucket, err := miniMaxWindow(row, weekly)
			if err != nil {
				return Report{}, err
			}
			bucket.ID = id + ":interval"
			if weekly {
				bucket.ID = id + ":weekly"
			}
			bucket.GroupID, bucket.GroupLabel = id, label
			report.Buckets = append(report.Buckets, bucket)
		}
	}
	return report, nil
}

func miniMaxInteger(value any, field string, optional bool) (float64, error) {
	if optional && value == nil {
		return 0, nil
	}
	number, ok := nonnegativeNumber(value)
	if !ok || !safeInteger(number) {
		return 0, fmt.Errorf("MiniMax Token Plan %s was not a nonnegative safe integer.", field)
	}
	return number, nil
}

func miniMaxWindow(row map[string]any, weekly bool) (Bucket, error) {
	prefix, startField, endField, label := "current_interval_", "start_time", "end_time", "Rolling window"
	if weekly {
		prefix, startField, endField, label = "current_weekly_", "weekly_start_time", "weekly_end_time", "Weekly window"
	}
	status, err := miniMaxInteger(row[prefix+"status"], prefix+"status", true)
	if err != nil {
		return Bucket{}, err
	}
	if row[prefix+"status"] != nil && status != 1 && status != 2 && status != 3 {
		return Bucket{}, fmt.Errorf("MiniMax Token Plan %s status was unsupported.", label)
	}
	percent, hasPercent := nonnegativeNumber(row[prefix+"remaining_percent"])
	if row[prefix+"remaining_percent"] != nil && (!hasPercent || percent > 100) {
		return Bucket{}, fmt.Errorf("MiniMax Token Plan %s was not a percentage.", prefix+"remaining_percent")
	}
	if weekly {
		boost, err := miniMaxInteger(row["weekly_boost_permille"], "weekly_boost_permille", true)
		if err != nil {
			return Bucket{}, err
		}
		if boost > 10000 {
			return Bucket{}, errors.New("MiniMax Token Plan weekly boost was unreasonable.")
		}
	}
	times := [2]float64{}
	for i, field := range []string{startField, endField} {
		value, ok := nonnegativeNumber(row[field])
		if !ok || !safeInteger(value) || value <= 0 {
			return Bucket{}, fmt.Errorf("MiniMax Token Plan %s was not a valid timestamp.", field)
		}
		times[i] = value
	}
	if times[1] < times[0] {
		return Bucket{}, fmt.Errorf("MiniMax Token Plan %s timestamps were reversed.", label)
	}
	bucket := Bucket{Label: label, WindowMinutes: new(math.Max(1, math.Floor((times[1]-times[0])/60000+0.5)))}
	if status == 3 {
		bucket.Remaining, bucket.Unit, bucket.Period = new(100.0), "percent", "unlimited"
		return bucket, nil
	}
	total, err := miniMaxInteger(row[prefix+"total_count"], prefix+"total_count", false)
	if err != nil {
		return Bucket{}, err
	}
	count, err := miniMaxInteger(row[prefix+"usage_count"], prefix+"usage_count", false)
	if err != nil {
		return Bucket{}, err
	}
	bucket.ResetsAt = new(math.Floor(times[1] / 1000))
	if total == 0 {
		if count != 0 {
			return Bucket{}, fmt.Errorf("MiniMax Token Plan %s counts were inconsistent.", label)
		}
		if !hasPercent {
			return Bucket{}, fmt.Errorf("MiniMax Token Plan %s returned no quota and no percent.", label)
		}
		bucket.Remaining, bucket.Used, bucket.Limit, bucket.Unit = new(percent), new(100-percent), new(0.0), "percent"
		return bucket, nil
	}
	if count > total {
		return Bucket{}, fmt.Errorf("MiniMax Token Plan %s counts were inconsistent.", label)
	}
	remaining := count
	if hasPercent {
		remainingDistance, usedDistance := math.Abs(float64(count/total*100)-percent), math.Abs(float64((total-count)/total*100)-percent)
		if math.Min(remainingDistance, usedDistance) > 1 {
			return Bucket{}, fmt.Errorf("MiniMax Token Plan %s counts were inconsistent.", label)
		}
		if usedDistance < remainingDistance {
			remaining = total - count
		}
	}
	bucket.Used, bucket.Remaining, bucket.Limit, bucket.Unit = new(total-remaining), new(remaining), new(total), "count"
	return bucket, nil
}
