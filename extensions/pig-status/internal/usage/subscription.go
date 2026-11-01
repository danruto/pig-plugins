package usage

import (
	"errors"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

var jsDecimalNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func finiteNumber(value any, stringsAllowed bool) (float64, bool) {
	number, ok := value.(float64)
	if !ok && stringsAllowed {
		text, isString := value.(string)
		text = jsTrim(text)
		if isString && text != "" {
			var err error
			switch {
			case strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X"), strings.HasPrefix(text, "0b") || strings.HasPrefix(text, "0B"), strings.HasPrefix(text, "0o") || strings.HasPrefix(text, "0O"):
				integer, valid := new(big.Int).SetString(text[2:], map[byte]int{'x': 16, 'X': 16, 'b': 2, 'B': 2, 'o': 8, 'O': 8}[text[1]])
				if !valid || strings.ContainsAny(text[2:], "+-_") {
					return 0, false
				}
				number, _ = integer.Float64()
			default:
				if !jsDecimalNumber.MatchString(text) {
					return 0, false
				}
				number, err = strconv.ParseFloat(text, 64)
				if errors.Is(err, strconv.ErrRange) && number == 0 {
					err = nil
				}
			}
			ok = err == nil
		}
	}
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func nonnegativeNumber(value any) (float64, bool) {
	number, ok := finiteNumber(value, false)
	return number, ok && number >= 0
}

func displayString(value any, maxChars int) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	runes := []rune(text)
	var result strings.Builder
	for i := 0; i < len(runes); {
		r := runes[i]
		i++
		if r == 0x1b || r == 0x9b || r == 0x9d {
			osc, csi := r == 0x9d, r == 0x9b
			if r == 0x1b && i < len(runes) {
				osc, csi = runes[i] == ']', runes[i] == '['
				if osc || csi {
					i++
				}
			}
			if osc {
				for i < len(runes) {
					current := runes[i]
					i++
					if current == 7 {
						break
					}
					if current == 0x1b && i < len(runes) && runes[i] == '\\' {
						i++
						break
					}
				}
			} else if csi {
				for i < len(runes) {
					current := runes[i]
					i++
					if current >= 0x40 && current <= 0x7e {
						break
					}
				}
			} else if r == 0x1b && i < len(runes) {
				i++
			}
			continue
		}
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) {
			if r == '\t' || r == '\n' || r == '\r' {
				result.WriteByte(' ')
			}
			continue
		}
		result.WriteRune(r)
	}
	clean := strings.Join(strings.FieldsFunc(result.String(), func(r rune) bool { return unicode.IsSpace(r) && r != 0x85 || r == 0xfeff }), " ")
	units := utf16.Encode([]rune(clean))
	if len(units) > maxChars {
		return string(utf16.Decode(units[:maxChars-1])) + "…"
	}
	return clean
}

func normalizeCodex(payload map[string]any, capturedAt int64) (Report, error) {
	report := Report{ProviderID: "openai-codex", ProviderName: "OpenAI Codex", CapturedAt: capturedAt, Source: "codex-pi-auth", Semantics: Semantics{Kind: "consumer-subscription", Label: "ChatGPT subscription limits"}, Buckets: []Bucket{}, Metrics: []Metric{}}
	if err := addCodexGroup(&report.Buckets, "codex", "Codex", payload["rate_limit"], false); err != nil {
		return Report{}, err
	}
	additional, _ := payload["additional_rate_limits"].([]any)
	for _, raw := range additional {
		item := object(raw)
		id := displayString(item["metered_feature"], 160)
		if id == "" {
			id = displayString(item["limit_name"], 160)
		}
		if id == "" {
			continue
		}
		label := displayString(item["limit_name"], 160)
		if label == "" {
			label = id
		}
		if err := addCodexGroup(&report.Buckets, id, label, item["rate_limit"], true); err != nil {
			continue
		}
	}
	credits := object(payload["credits"])
	if credits["has_credits"] == true {
		value := any("available")
		unit := ""
		if credits["unlimited"] == true {
			value = "unlimited"
		} else if balance, ok := finiteNumber(credits["balance"], true); ok {
			value, unit = balance, "count"
		}
		report.Metrics = append(report.Metrics, Metric{ID: "credits", Label: "Credits", Value: value, Unit: unit})
	} else if credits["has_credits"] == false {
		report.Metrics = append(report.Metrics, Metric{ID: "credits", Label: "Credits", Value: "none"})
	}
	resetCredits := object(payload["rate_limit_reset_credits"])
	if count, ok := finiteNumber(resetCredits["available_count"], true); ok && safeInteger(count) {
		report.Metrics = append(report.Metrics, Metric{ID: "reset-credits", Label: "Usage limit resets", Value: math.Max(0, count), Unit: "count"})
	}
	if len(report.Buckets) == 0 && len(report.Metrics) == 0 {
		return Report{}, errors.New("Codex usage endpoint returned no displayable usage data.")
	}
	if plan := displayString(payload["plan_type"], 160); plan != "" {
		report.Notes = []string{"Plan: " + plan}
	}
	return report, nil
}

func addCodexGroup(buckets *[]Bucket, id, label string, raw any, optional bool) error {
	if raw == nil {
		return nil
	}
	details := object(raw)
	if details == nil {
		if optional {
			return nil
		}
		return errors.New("Codex rate limit was not an object.")
	}
	for _, position := range []string{"primary", "secondary"} {
		raw := details[position+"_window"]
		if raw == nil {
			continue
		}
		window := object(raw)
		if window == nil {
			return errors.New("Codex rate-limit window was not an object.")
		}
		used, ok := finiteNumber(window["used_percent"], true)
		if !ok {
			continue
		}
		windowLabel := "Primary limit"
		if position == "secondary" {
			windowLabel = "Secondary limit"
		}
		bucket := Bucket{ID: id + ":" + position, Label: windowLabel, GroupID: id, GroupLabel: label, ModelKeys: []string{id, label}, Used: new(used), Remaining: new(100 - math.Min(100, math.Max(0, used))), Limit: new(100.0), Unit: "percent"}
		if seconds, ok := finiteNumber(window["limit_window_seconds"], true); ok && seconds > 0 {
			bucket.WindowMinutes = new(math.Ceil(seconds / 60))
		}
		if reset, ok := finiteNumber(window["reset_at"], true); ok {
			bucket.ResetsAt = new(reset)
		}
		*buckets = append(*buckets, bucket)
	}
	return nil
}

func normalizeCopilot(payload map[string]any, capturedAt int64) (Report, error) {
	report := Report{ProviderID: "github-copilot", ProviderName: "GitHub Copilot", CapturedAt: capturedAt, Source: "github-copilot-user", Semantics: Semantics{Kind: "consumer-subscription"}, AccountLabel: displayString(payload["login"], 80), Buckets: []Bucket{}, Metrics: []Metric{}}
	premium := object(object(payload["quota_snapshots"])["premium_interactions"])
	bucket := Bucket{Unit: "count"}
	if premium != nil {
		bucket.ID, bucket.Label = "premium-requests", "Premium requests"
		report.Semantics.Label = "GitHub Copilot premium request quota"
		if premium["token_based_billing"] == true {
			bucket.ID, bucket.Label = "ai-credits", "AI credits"
			report.Semantics.Label = "GitHub Copilot AI Credits allowance"
		}
		if premium["unlimited"] != true {
			entitlement, valid := nonnegativeNumber(premium["entitlement"])
			remaining, ok := finiteNumber(premium["remaining"], false)
			if !ok {
				remaining, ok = finiteNumber(premium["quota_remaining"], false)
			}
			if !valid || !ok {
				return Report{}, errors.New("GitHub Copilot " + strings.ToLower(bucket.Label) + " quota was incomplete.")
			}
			overage, _ := nonnegativeNumber(premium["overage_count"])
			overage = math.Max(overage, math.Max(0, -remaining))
			if overage > 0 {
				report.Metrics = append(report.Metrics, Metric{ID: "overage-used", Label: "Additional usage", Value: overage, Unit: "count"})
			}
			used, ok := nonnegativeNumber(premium["credits_used"])
			if !ok {
				used = math.Max(0, entitlement-remaining)
			}
			bucket.Used, bucket.Remaining, bucket.Limit = new(used), new(math.Max(0, remaining)), new(entitlement)
			bucket.Period, bucket.ResetsAt = "monthly", copilotReset(payload)
		}
	} else {
		remaining, ok := nonnegativeNumber(object(payload["limited_user_quotas"])["chat"])
		entitlement, valid := nonnegativeNumber(object(payload["monthly_quotas"])["chat"])
		if !ok || !valid {
			return Report{}, errors.New("GitHub Copilot usage response contained no supported quota.")
		}
		report.Semantics.Label = "GitHub Copilot Free chat quota"
		bucket.ID, bucket.Label = "chat-requests", "Chat requests"
		bucket.Used, bucket.Remaining, bucket.Limit = new(math.Max(0, entitlement-remaining)), new(remaining), new(entitlement)
		bucket.Period, bucket.ResetsAt = "monthly", copilotReset(payload)
	}
	report.Buckets = append(report.Buckets, bucket)
	plan := displayString(payload["copilot_plan"], 80)
	if plan == "" {
		plan = displayString(payload["access_type_sku"], 80)
	}
	if plan != "" {
		report.Notes = []string{"Plan: " + plan}
	}
	return report, nil
}

func copilotReset(payload map[string]any) *float64 {
	var text string
	for _, key := range []string{"quota_reset_date_utc", "quota_reset_date", "limited_user_reset_date"} {
		text = displayString(payload[key], 80)
		if text != "" {
			break
		}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04Z07:00", "2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02", "2006-01", "2006", time.RFC1123, time.RFC1123Z, "Mon, 02 Jan 2006 15:04:05 MST", "01/02/2006", "January 2, 2006 15:04:05 GMT"} {
		location := time.UTC
		if layout == "2006-01-02T15:04:05.999999999" || layout == "2006-01-02T15:04" || layout == "01/02/2006" {
			location = time.Local
		}
		if strings.HasSuffix(layout, " MST") {
			zone := text[strings.LastIndexByte(text, ' ')+1:]
			offset, known := map[string]int{"UTC": 0, "GMT": 0, "EST": -5 * 3600, "EDT": -4 * 3600, "CST": -6 * 3600, "CDT": -5 * 3600, "MST": -7 * 3600, "MDT": -6 * 3600, "PST": -8 * 3600, "PDT": -7 * 3600}[zone]
			if !known && (strings.HasPrefix(zone, "GMT+") || strings.HasPrefix(zone, "GMT-")) {
				numeric := zone[3:]
				if len(numeric) == 2 || len(numeric) == 4 {
					numeric = numeric[:1] + "0" + numeric[1:]
				}
				for _, zoneLayout := range []string{"-07", "-0700"} {
					if parsed, err := time.Parse(zoneLayout, numeric); err == nil {
						_, offset = parsed.Zone()
						known = true
						break
					}
				}
			}
			if !known {
				continue
			}
			location = time.FixedZone(zone, offset)
		}
		if parsed, err := time.ParseInLocation(layout, text, location); err == nil {
			return new(math.Floor(float64(parsed.UnixMilli()) / 1000))
		}
	}
	return nil
}

func safeInteger(number float64) bool {
	return math.Abs(number) <= 9_007_199_254_740_991 && math.Trunc(number) == number
}
