package usage

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"strconv"
	"time"
)

var kimiTimestamp = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|([+-])(\d{2}):(\d{2}))$`)

func kimiInteger(value any) (float64, bool) {
	if text, ok := value.(string); ok && !unsignedDigits.MatchString(text) {
		return 0, false
	}
	number, ok := finiteNumber(value, true)
	return number, ok && number >= 0 && safeInteger(number)
}

func normalizeKimi(payload map[string]any, capturedAt int64) (Report, error) {
	if payload == nil {
		return Report{}, errors.New("Kimi Coding usage response was not an object.")
	}
	report := Report{ProviderID: "kimi-coding", ProviderName: "Kimi For Coding", CapturedAt: capturedAt, Source: "kimi-managed-usage", Semantics: Semantics{Kind: "consumer-subscription", Label: "Kimi Coding Plan usage"}, Buckets: []Bucket{}, Metrics: []Metric{}}
	explicit := map[float64]bool{}
	candidates := []Bucket{}
	omitted := false
	if raw, present := payload["usage"]; present {
		explicit[10080] = true
		if bucket := kimiUsageRow(raw, 10080, "Weekly window"); bucket != nil {
			candidates = append(candidates, *bucket)
		} else {
			omitted = true
		}
	}
	if raw, present := payload["limits"]; present {
		if rows, ok := raw.([]any); ok {
			for _, raw := range rows {
				row := object(raw)
				window := object(row["window"])
				duration, valid := kimiInteger(window["duration"])
				unit, _ := window["timeUnit"].(string)
				multiplier := map[string]float64{"TIME_UNIT_MINUTE": 1, "TIME_UNIT_HOUR": 60, "TIME_UNIT_DAY": 1440, "TIME_UNIT_WEEK": 10080}[unit]
				minutes := duration * multiplier
				if !valid || duration <= 0 || multiplier == 0 || !safeInteger(minutes) {
					omitted = true
					continue
				}
				explicit[minutes] = true
				label := displayString(row["name"], 80)
				if label == "" {
					label = kimiWindowLabel(minutes)
				}
				if bucket := kimiUsageRow(row["detail"], minutes, label); bucket != nil {
					candidates = append(candidates, *bucket)
				} else {
					omitted = true
				}
			}
		} else {
			omitted = true
		}
	}
	counts := map[float64]int{}
	for _, bucket := range candidates {
		counts[*bucket.WindowMinutes]++
	}
	for _, bucket := range candidates {
		if counts[*bucket.WindowMinutes] == 1 {
			report.Buckets = append(report.Buckets, bucket)
		} else {
			omitted = true
		}
	}
	usages := object(payload["usages"])
	if _, present := payload["usages"]; present && usages == nil {
		omitted = true
	}
	for _, window := range []struct {
		key, id, label string
		minutes        float64
	}{{"limit_5h", "five-hour", "Five-hour window", 300}, {"limit_week", "weekly", "Weekly window", 10080}, {"limit_month_total", "monthly", "Monthly window", 0}} {
		raw, present := usages[window.key]
		if !present || window.minutes != 0 && explicit[window.minutes] {
			continue
		}
		row := object(raw)
		ratio, valid := nonnegativeNumber(row["used_ratio"])
		if !valid || ratio > 1 {
			omitted = true
			continue
		}
		bucket := Bucket{ID: window.id, Label: window.label, Used: new(ratio * 100), Remaining: new(100 - ratio*100), Unit: "percent", ResetsAt: kimiEpoch(row["reset_time"])}
		if window.minutes != 0 {
			bucket.WindowMinutes = new(window.minutes)
		}
		report.Buckets = append(report.Buckets, bucket)
	}
	slices.SortStableFunc(report.Buckets, func(a, b Bucket) int {
		left, right := math.Inf(1), math.Inf(1)
		if a.WindowMinutes != nil {
			left = *a.WindowMinutes
		}
		if b.WindowMinutes != nil {
			right = *b.WindowMinutes
		}
		if left < right {
			return -1
		}
		if left > right {
			return 1
		}
		return 0
	})
	wallet, present := payload["boosterWallet"]
	if !present {
		wallet = payload["booster_wallet"]
	}
	report.Metrics = kimiWallet(wallet)
	if len(report.Buckets) == 0 && len(report.Metrics) == 0 {
		return Report{}, errors.New("Kimi Coding usage endpoint returned no displayable usage data.")
	}
	if omitted {
		report.Notes = []string{"Unsupported, malformed, or duplicate plan windows were unavailable."}
	}
	return report, nil
}

func kimiUsageRow(raw any, minutes float64, label string) *Bucket {
	row := object(raw)
	limit, ok := kimiInteger(row["limit"])
	if !ok || limit == 0 {
		return nil
	}
	used, hasUsed := kimiInteger(row["used"])
	remaining, hasRemaining := kimiInteger(row["remaining"])
	if _, present := row["used"]; present && !hasUsed {
		return nil
	}
	if _, present := row["remaining"]; present && !hasRemaining {
		return nil
	}
	if !hasUsed && hasRemaining {
		used = math.Max(0, limit-remaining)
	}
	if !hasRemaining {
		remaining = math.Max(0, limit-used)
	}
	id := "window-" + jsNumber(minutes) + "-minutes"
	switch minutes {
	case 300:
		id = "five-hour"
	case 1440:
		id = "daily"
	case 10080:
		id = "weekly"
	}
	return &Bucket{ID: id, Label: label, Used: new(used), Remaining: new(remaining), Limit: new(limit), Unit: "count", WindowMinutes: new(minutes), ResetsAt: kimiEpoch(row["resetTime"])}
}

func kimiWindowLabel(minutes float64) string {
	if minutes == 10080 {
		return "Weekly window"
	}
	for _, unit := range []struct {
		minutes float64
		label   string
	}{{10080, "w"}, {1440, "d"}, {60, "h"}} {
		if math.Mod(minutes, unit.minutes) == 0 {
			return jsNumber(minutes/unit.minutes) + unit.label + " window"
		}
	}
	return jsNumber(minutes) + "m window"
}

func kimiWallet(raw any) []Metric {
	metrics := []Metric{}
	wallet := object(raw)
	balance := object(wallet["balance"])
	if balance["type"] != "BOOSTER" {
		return metrics
	}
	total, valid := kimiInteger(balance["amount"])
	if !valid || total <= 0 {
		return metrics
	}
	left, _ := kimiInteger(balance["amountLeft"])
	limit, limitCurrency, hasLimit := kimiMoney(wallet["monthlyChargeLimit"])
	used, usedCurrency, hasUsed := kimiMoney(wallet["monthlyUsed"])
	currency := limitCurrency
	if currency == "" {
		currency = usedCurrency
	}
	if currency == "" || hasLimit && hasUsed && limitCurrency != usedCurrency {
		return metrics
	}
	totalCents, leftCents := kimiFixedCents(total), kimiFixedCents(left)
	if !safeInteger(totalCents) || !safeInteger(leftCents) {
		return metrics
	}
	metrics = append(metrics, Metric{ID: "booster-balance", Label: "Balance", Value: leftCents / 100, Unit: "currency", Currency: currency}, Metric{ID: "booster-total", Label: "Total balance", Value: totalCents / 100, Unit: "currency", Currency: currency})
	if hasUsed {
		metrics = append(metrics, Metric{ID: "booster-monthly-used", Label: "Used this month", Value: used / 100, Unit: "currency", Currency: currency})
	}
	if wallet["monthlyChargeLimitEnabled"] == false {
		metrics = append(metrics, Metric{ID: "booster-monthly-limit", Label: "Monthly limit", Value: "unlimited", Unit: "currency", Currency: currency})
	} else if wallet["monthlyChargeLimitEnabled"] == true && hasLimit {
		metrics = append(metrics, Metric{ID: "booster-monthly-limit", Label: "Monthly limit", Value: limit / 100, Unit: "currency", Currency: currency})
	}
	return metrics
}

func kimiMoney(raw any) (float64, string, bool) {
	row := object(raw)
	cents, valid := kimiInteger(row["priceInCents"])
	currency := jsUppercase(displayString(row["currency"], 3))
	if !valid || len(currency) != 3 {
		return 0, "", false
	}
	for _, r := range currency {
		if r < 'A' || r > 'Z' {
			return 0, "", false
		}
	}
	return cents, currency, true
}

func kimiFixedCents(value float64) float64 {
	cents := value / 1000000
	if cents > 0 && cents < 1 {
		return 1
	}
	return math.Round(cents)
}

func kimiEpoch(raw any) *float64 {
	text, ok := raw.(string)
	if !ok {
		return nil
	}
	match := kimiTimestamp.FindStringSubmatch(text)
	if match == nil {
		return nil
	}
	parts := make([]int, 6)
	for i := range parts {
		number, err := strconv.Atoi(match[i+1])
		if err != nil {
			return nil
		}
		parts[i] = number
	}
	year, month, day := parts[0], parts[1], parts[2]
	validationYear := year
	if year < 100 {
		validationYear += 1900
	}
	if month < 1 || month > 12 || day < 1 || day > time.Date(validationYear, time.Month(month+1), 0, 0, 0, 0, 0, time.UTC).Day() || parts[3] > 23 || parts[4] > 59 || parts[5] > 59 {
		return nil
	}
	if match[8] != "" {
		hour, err := strconv.Atoi(match[8])
		if err != nil || hour > 23 {
			return nil
		}
		minute, err := strconv.Atoi(match[9])
		if err != nil || minute > 59 {
			return nil
		}
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || parsed.UnixMilli() < 0 {
		return nil
	}
	return new(math.Floor(float64(parsed.UnixMilli()) / 1000))
}
