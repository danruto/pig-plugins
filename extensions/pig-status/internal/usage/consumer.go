package usage

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

var consumerIdentifier = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,128}$`)
var unsignedDigits = regexp.MustCompile(`^[0-9]+$`)

func normalizeCommandCode(bundle map[string]any, capturedAt int64) (Report, error) {
	account := object(bundle["account"])
	user := object(account["user"])
	label := displayString(user["userName"], 80)
	if label == "" {
		label = displayString(user["name"], 80)
	}
	if label == "" {
		return Report{}, errors.New("Command Code account response omitted a safe account name.")
	}
	creditsPayload := object(bundle["credits"])
	credits := object(creditsPayload["credits"])
	windows := object(creditsPayload["windowLimits"])
	subscription := object(object(bundle["subscription"])["data"])
	usage := object(bundle["usage"])
	report := Report{ProviderID: "command-code", ProviderName: "Command Code", CapturedAt: capturedAt, Source: "command-code-alpha-usage", Semantics: Semantics{Kind: "consumer-subscription", Label: "Command Code plan credits and rolling limits"}, AccountLabel: label, Buckets: []Bucket{}, Metrics: []Metric{}}
	for _, window := range []struct {
		key, id, label string
		minutes        float64
	}{{"fiveHour", "five-hour", "Five-hour window", 300}, {"weekly", "weekly", "Weekly window", 10080}} {
		row := object(windows[window.key])
		used, valid := nonnegativeNumber(row["used"])
		cap, ok := nonnegativeNumber(row["cap"])
		if !valid || !ok || cap <= 0 {
			continue
		}
		bucket := Bucket{ID: window.id, Label: window.label, Used: new(used), Remaining: new(math.Max(0, cap-used)), Limit: new(cap), Unit: "usd", WindowMinutes: new(window.minutes)}
		reset, valid := finiteNumber(row["resetAt"], false)
		if text, ok := row["resetAt"].(string); ok && unsignedDigits.MatchString(jsTrim(text)) {
			reset, valid = finiteNumber(jsTrim(text), true)
		}
		if valid && reset >= 0 {
			if reset >= 1e12 {
				reset /= 1000
			}
			bucket.ResetsAt = new(math.Floor(reset))
		}
		report.Buckets = append(report.Buckets, bucket)
	}
	totalCost, hasCost := nonnegativeNumber(usage["totalCost"])
	remaining, hasRemaining := 0.0, false
	for _, key := range []string{"monthlyCredits", "purchasedCredits", "freeCredits"} {
		if value, ok := nonnegativeNumber(credits[key]); ok {
			remaining += value
			hasRemaining = true
		}
	}
	monthly := hasCost && hasRemaining && totalCost+remaining > 0
	if monthly {
		report.Buckets = append(report.Buckets, Bucket{ID: "monthly", Label: "Monthly credits", Used: new(totalCost), Remaining: new(remaining), Limit: new(totalCost + remaining), Unit: "usd", Period: "billing-period", ResetsAt: consumerEpoch(subscription["currentPeriodEnd"])})
	}
	plan := commandCodePlan(subscription["planId"])
	status := displayString(subscription["status"], 40)
	if status != "" {
		if plan != "" {
			plan += " "
		}
		plan += "(" + status + ")"
	}
	if plan != "" {
		report.Metrics = append(report.Metrics, Metric{ID: "plan", Label: "Plan", Value: plan})
	}
	if !monthly {
		if hasRemaining {
			report.Metrics = append(report.Metrics, Metric{ID: "monthly-credits", Label: "Monthly credits remaining", Value: remaining, Unit: "usd"})
		} else if hasCost {
			report.Metrics = append(report.Metrics, Metric{ID: "period-cost", Label: "Cost this billing period", Value: totalCost, Unit: "usd"})
		}
	}
	for _, field := range [][3]string{{"purchasedCredits", "purchased-credits", "Purchased credits remaining"}, {"freeCredits", "free-credits", "Free credits remaining"}} {
		if value, ok := nonnegativeNumber(credits[field[0]]); ok && value != 0 {
			report.Metrics = append(report.Metrics, Metric{ID: field[1], Label: field[2], Value: value, Unit: "usd"})
		}
	}
	for _, field := range [][3]string{{"totalCount", "requests", "Requests this period"}, {"totalTokens", "tokens", "Tokens this period"}} {
		if value, ok := nonnegativeNumber(usage[field[0]]); ok {
			report.Metrics = append(report.Metrics, Metric{ID: field[1], Label: field[2], Value: value, Unit: "count"})
		}
	}
	if _, ok := nonnegativeNumber(usage["totalTokens"]); !ok {
		for _, field := range [][3]string{{"totalTokensIn", "tokens-in", "Input tokens"}, {"totalTokensOut", "tokens-out", "Output tokens"}} {
			if value, ok := nonnegativeNumber(usage[field[0]]); ok {
				report.Metrics = append(report.Metrics, Metric{ID: field[1], Label: field[2], Value: value, Unit: "count"})
			}
		}
	}
	if creditsPayload == nil {
		report.Notes = append(report.Notes, "Command Code credits were unavailable.")
	} else if windows["limited"] == false {
		report.Notes = append(report.Notes, "Rolling 5-hour and weekly limits are not enabled for this plan.")
	}
	if subscription == nil {
		report.Notes = append(report.Notes, "Command Code plan details were unavailable.")
	}
	if usage == nil {
		report.Notes = append(report.Notes, "Command Code billing-period usage was unavailable.")
	}
	if len(report.Buckets) == 0 && len(report.Metrics) == 0 {
		return Report{}, errors.New("Command Code usage response returned no displayable usage data.")
	}
	if math.IsInf(remaining, 0) || monthly && math.IsInf(totalCost+remaining, 0) {
		return Report{}, errors.New("Command Code credit total was not finite.")
	}
	return report, nil
}

func commandCodePlan(value any) string {
	raw := displayString(value, 40)
	if known := map[string]string{"individual-go": "Go", "individual-goat": "GOAT", "individual-pro": "Pro", "individual-provider": "Provider", "individual-max": "Max", "individual-ultra": "Ultra"}[raw]; known != "" {
		return known
	}
	words := raw
	if strings.HasPrefix(words, "individual-") || strings.HasPrefix(words, "individual_") {
		words = words[len("individual-"):]
	}
	words = jsTrim(strings.Join(strings.FieldsFunc(words, func(r rune) bool { return r == '-' || r == '_' }), " "))
	if words == "" {
		return raw
	}
	first, size := utf8.DecodeRuneInString(words)
	if first > 0xffff {
		return words
	}
	return jsUppercase(words[:size]) + words[size:]
}

func normalizeOpenCode(payload map[string]any, capturedAt int64) (Report, error) {
	usage := object(payload["usage"])
	if usage == nil {
		return Report{}, errors.New("OpenCode Zen usage response was not an object.")
	}
	report := Report{ProviderID: "opencode-go", ProviderName: "OpenCode Go", CapturedAt: capturedAt, Source: "opencode-zen-usage", Semantics: Semantics{Kind: "consumer-subscription", Label: "OpenCode Zen plan usage"}, Buckets: []Bucket{}, Metrics: []Metric{}}
	for _, window := range [][2]string{{"rolling", "Rolling"}, {"weekly", "Weekly"}, {"monthly", "Monthly"}} {
		row := object(usage[window[0]])
		if row == nil {
			continue
		}
		status := displayString(row["status"], 80)
		if status != "ok" && status != "rate-limited" {
			if status == "" {
				status = "unknown status"
			}
			report.Notes = append(report.Notes, window[1]+" window unavailable ("+status+").")
			continue
		}
		used, ok := nonnegativeNumber(row["percent"])
		if !ok {
			continue
		}
		report.Buckets = append(report.Buckets, Bucket{ID: window[0], Label: window[1] + " window", Used: new(used), Remaining: new(100 - math.Min(100, used)), Limit: new(100.0), Unit: "percent", ResetsAt: consumerEpoch(row["resetsAt"])})
	}
	if len(report.Buckets) == 0 {
		return Report{}, errors.New("OpenCode Zen usage endpoint returned no displayable usage data.")
	}
	return report, nil
}

func consumerEpoch(value any) *float64 {
	text, ok := value.(string)
	if !ok || jsTrim(text) == "" {
		return nil
	}
	return consumerDateSeconds(text)
}
