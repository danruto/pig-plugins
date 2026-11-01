package usage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf16"
)

func resolveXai(snapshot OAuthSnapshot) (subscriptionIdentity, error) {
	if err := validateSnapshotOrigins("xai", snapshot); err != nil {
		return subscriptionIdentity{}, err
	}
	access, err := authAccessToken(snapshot.Effective)
	if err != nil {
		return subscriptionIdentity{}, err
	}
	if access == "" {
		access = snapshot.Effective.APIKey
	}
	if access == "" {
		return subscriptionIdentity{}, errors.New("xAI runtime authentication was incomplete.")
	}
	sawOAuth := false
	matches := []subscriptionIdentity{}
	for _, raw := range snapshot.Candidates {
		row := object(raw)
		if row["type"] != "oauth" {
			continue
		}
		sawOAuth = true
		if row["access"] != access {
			continue
		}
		refresh, _ := row["refresh"].(string)
		_, valid := finiteNumber(row["expires"], false)
		if refresh == "" || !valid {
			return subscriptionIdentity{}, errors.New("The matching xAI OAuth credential was incomplete.")
		}
		matches = append(matches, subscriptionIdentity{access: access, refresh: refresh})
	}
	if len(matches) > 1 {
		return subscriptionIdentity{}, errors.New("Multiple OAuth credentials match the active xAI runtime account.")
	}
	if len(matches) == 0 {
		if !sawOAuth {
			return subscriptionIdentity{}, errors.New("xAI consumer usage requires the OAuth subscription account configured through Pi /login; XAI_API_KEY users can review API spend at console.x.ai.")
		}
		return subscriptionIdentity{}, errors.New("The active xAI runtime account does not match Pi's stored OAuth account.")
	}
	identity := matches[0]
	identity.headers = http.Header{"Authorization": {"Bearer " + access}, "X-XAI-Token-Auth": {"xai-grok-cli"}, "x-grok-client-version": {"1.0.10"}, "x-grok-client-mode": {"interactive"}, "User-Agent": {""}}
	if _, err := validatedHeaders("xai", identity.headers); err != nil {
		return subscriptionIdentity{}, err
	}
	return identity, nil
}

func (c Client) queryXai(ctx context.Context, auth ResolvedAuth) (Report, error) {
	identity, err := auth.subscriptionIdentity(ctx, "xai")
	if err != nil {
		return Report{}, err
	}
	check := func() error {
		fresh, err := auth.subscriptionIdentity(ctx, "xai")
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(identity, fresh) {
			return errors.New("usage OAuth account changed during query")
		}
		return nil
	}
	if err := check(); err != nil {
		return Report{}, err
	}
	user, err := c.fetch(ctx, "https://cli-chat-proxy.grok.com/v1/user?include=subscription", "xAI consumer identity endpoint", identity.headers)
	if err != nil {
		return Report{}, err
	}
	if err := check(); err != nil {
		return Report{}, err
	}
	id, ok := user["userId"].(string)
	if !ok || !consumerIdentifier.MatchString(id) {
		return Report{}, errors.New("xAI consumer identity returned an unsafe canonical user ID.")
	}
	headers := identity.headers.Clone()
	headers.Set("x-userid", id)
	if err := check(); err != nil {
		return Report{}, err
	}
	payload, err := c.fetch(ctx, "https://cli-chat-proxy.grok.com/v1/billing?format=credits", "xAI consumer billing endpoint", headers)
	if err != nil {
		return Report{}, err
	}
	if err := check(); err != nil {
		return Report{}, err
	}
	report, err := normalizeXai(payload, user["subscriptionTier"], time.Now().UnixMilli())
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func normalizeXai(payload map[string]any, tier any, capturedAt int64) (Report, error) {
	config := object(payload["config"])
	if payload["config"] != nil && config == nil {
		return Report{}, errors.New("xAI billing response config was not an object or null.")
	}
	report := Report{ProviderID: "xai", ProviderName: "xAI", CapturedAt: capturedAt, Source: "cli-chat-proxy.grok.com consumer billing", Semantics: Semantics{Kind: "consumer-subscription", Label: "xAI consumer subscription usage"}, Buckets: []Bucket{}, Metrics: []Metric{}}
	if config != nil {
		period, reset, err := xaiPeriod(config)
		if err != nil {
			return Report{}, err
		}
		bucket := Bucket{ID: "included-allowance", Label: "Included allowance", Unit: "percent", Period: period, ResetsAt: reset}
		if raw := config["creditUsagePercent"]; raw != nil {
			percent, ok := nonnegativeNumber(raw)
			if !ok || percent > 100 {
				return Report{}, errors.New("xAI billing creditUsagePercent was outside 0–100.")
			}
			bucket.Used, bucket.Remaining = new(percent), new(100-percent)
			report.Buckets = append(report.Buckets, bucket)
		} else {
			limit, err := xaiUSD(config["monthlyLimit"], "monthlyLimit")
			if err != nil {
				return Report{}, err
			}
			used, err := xaiUSD(config["used"], "used")
			if err != nil {
				return Report{}, err
			}
			if limit != nil || used != nil {
				bucket.Unit = "usd"
				bucket.Limit, bucket.Used = limit, used
				if limit != nil && used != nil {
					bucket.Remaining = new(*limit - *used)
				}
				report.Buckets = append(report.Buckets, bucket)
			} else if period != "" || reset != nil {
				report.Buckets = append(report.Buckets, bucket)
			}
		}
		cap, err := xaiUSD(config["onDemandCap"], "onDemandCap")
		if err != nil {
			return Report{}, err
		}
		used, err := xaiUSD(config["onDemandUsed"], "onDemandUsed")
		if err != nil {
			return Report{}, err
		}
		if cap != nil || used != nil {
			bucket := Bucket{ID: "on-demand", Label: "On-demand usage", Limit: cap, Used: used, Unit: "usd"}
			if cap != nil && used != nil {
				bucket.Remaining = new(*cap - *used)
			}
			report.Buckets = append(report.Buckets, bucket)
		}
		balance, err := xaiUSD(config["prepaidBalance"], "prepaidBalance")
		if err != nil {
			return Report{}, err
		}
		if balance != nil {
			report.Metrics = append(report.Metrics, Metric{ID: "prepaid-balance", Label: "Prepaid balance", Value: *balance, Unit: "usd"})
		}
	}
	if tier != nil {
		text, ok := tier.(string)
		if !ok || len(utf16.Encode([]rune(text))) > 160 {
			return Report{}, errors.New("xAI subscription tier was not a bounded string or null.")
		}
		if safe := displayString(text, 80); safe != "" {
			report.Metrics = append(report.Metrics, Metric{ID: "subscription-tier", Label: "Plan tier", Value: safe})
		}
	}
	if config == nil {
		report.Notes = []string{"No xAI consumer billing configuration is available for this account."}
	} else if len(report.Buckets) == 0 && len(report.Metrics) == 0 {
		report.Notes = []string{"The xAI consumer billing response contained no displayable usage fields."}
	}
	return report, nil
}

func xaiUSD(raw any, field string) (*float64, error) {
	if raw == nil {
		return nil, nil
	}
	row := object(raw)
	if row == nil {
		return nil, fmt.Errorf("xAI billing %s was not a cent wrapper.", field)
	}
	cents := 0.0
	if value, present := row["val"]; present {
		var ok bool
		cents, ok = finiteNumber(value, false)
		if !ok || !safeInteger(cents) {
			return nil, fmt.Errorf("xAI billing %s.val was not a safe signed integer.", field)
		}
	}
	return new(cents / 100), nil
}

func xaiPeriod(config map[string]any) (string, *float64, error) {
	current := object(config["currentPeriod"])
	if config["currentPeriod"] != nil && current == nil {
		return "", nil, errors.New("xAI billing currentPeriod was not an object or null.")
	}
	startField, endField := "billingPeriodStart", "billingPeriodEnd"
	row := config
	label := ""
	if current != nil {
		row = current
		startField, endField = "start", "end"
		if raw := row["type"]; raw != nil {
			text, ok := raw.(string)
			if !ok || len(utf16.Encode([]rune(text))) > 80 {
				return "", nil, errors.New("xAI billing currentPeriod.type was not a bounded string.")
			}
			label = text
		}
	}
	prefix := ""
	if current != nil {
		prefix = "currentPeriod."
	}
	start, err := xaiTimestamp(row[startField], prefix+startField)
	if err != nil {
		return "", nil, err
	}
	end, err := xaiTimestamp(row[endField], prefix+endField)
	if err != nil {
		return "", nil, err
	}
	if current == nil {
		if start != nil {
			label = "Monthly"
		}
		return label, end, nil
	}
	switch label {
	case "USAGE_PERIOD_TYPE_WEEKLY":
		label = "Weekly"
	case "USAGE_PERIOD_TYPE_MONTHLY":
		label = "Monthly"
	case "":
		if start != nil {
			label = "Current period"
		}
	default:
		label = displayString(strings.ReplaceAll(strings.TrimPrefix(label, "USAGE_PERIOD_TYPE_"), "_", " "), 40)
	}
	return label, end, nil
}

func xaiTimestamp(raw any, field string) (*float64, error) {
	if raw == nil {
		return nil, nil
	}
	text, ok := raw.(string)
	if !ok || len(utf16.Encode([]rune(text))) > 80 {
		return nil, fmt.Errorf("xAI billing %s was not a bounded timestamp.", field)
	}
	value := consumerEpoch(text)
	if value == nil || math.IsInf(*value, 0) {
		return nil, fmt.Errorf("xAI billing %s was invalid.", field)
	}
	return value, nil
}
