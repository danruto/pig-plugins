package usage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

type Target struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var fireworksAccountID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)
var fireworksCurrency = regexp.MustCompile(`^[A-Z]{3}$`)
var fireworksInteger = regexp.MustCompile(`^-?[0-9]+$`)

func (c Client) ListTargets(ctx context.Context, providerID string, auth ResolvedAuth, timeout time.Duration) ([]Target, error) {
	if providerID != "fireworks" {
		return nil, errors.New("usage provider does not support billing targets")
	}
	if !officialProviderOrigin(providerID, auth.ModelBaseURL) {
		return nil, errors.New("usage requires the official provider origin")
	}
	headers, err := validatedHeaders(providerID, auth.Headers)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, min(timeout, maxQueryDuration))
	defer cancel()
	select {
	case querySlots <- struct{}{}:
		defer func() { <-querySlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	targets := []Target{}
	seen := make(map[string]bool)
	pageToken := ""
	for range 5 {
		if err := auth.revalidate(ctx); err != nil {
			return nil, err
		}
		query := url.Values{"pageSize": {"200"}}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		payload, err := c.fetch(ctx, "https://api.fireworks.ai/v1/accounts?"+query.Encode(), "Fireworks accounts endpoint", headers)
		if err != nil {
			return nil, err
		}
		if err := auth.revalidate(ctx); err != nil {
			return nil, err
		}
		accounts, err := normalizeFireworksAccounts(payload)
		if err != nil {
			return nil, err
		}
		for _, id := range accounts {
			if seen[id] {
				return nil, errors.New("Fireworks accounts listing repeated an account.")
			}
			seen[id] = true
			targets = append(targets, Target{ID: id, Label: id})
		}
		pageToken, err = fireworksNextPageToken(payload["nextPageToken"])
		if err != nil {
			return nil, err
		}
		if pageToken == "" {
			return targets, ctx.Err()
		}
	}
	return nil, errors.New("Fireworks account listing exceeded 5 pages.")
}

func normalizeFireworksAccounts(payload map[string]any) ([]string, error) {
	rows, ok := payload["accounts"].([]any)
	if !ok {
		return nil, errors.New("Fireworks accounts response did not contain an accounts array.")
	}
	accounts := []string{}
	seen := make(map[string]bool)
	for _, raw := range rows {
		row := object(raw)
		if row == nil {
			return nil, errors.New("Fireworks accounts response row was not an object.")
		}
		name, ok := row["name"].(string)
		if !ok {
			return nil, errors.New("Fireworks accounts response omitted the account resource name.")
		}
		id, found := strings.CutPrefix(name, "accounts/")
		if !found || !fireworksAccountID.MatchString(id) {
			return nil, errors.New("Fireworks accounts response returned an unsafe account resource name.")
		}
		if seen[id] {
			return nil, errors.New("Fireworks accounts response repeated an account.")
		}
		seen[id] = true
		accounts = append(accounts, id)
	}
	return accounts, nil
}

func fireworksNextPageToken(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	token, ok := value.(string)
	if !ok || token == "" || len(utf16.Encode([]rune(token))) > 512 {
		return "", errors.New("Fireworks accounts listing returned an invalid page token.")
	}
	return token, nil
}

func fireworksBillingURL(accountID string, at time.Time) string {
	at = at.UTC()
	query := url.Values{}
	query.Set("startTime", at.Add(-29*24*time.Hour).Format("2006-01-02T00:00:00Z"))
	query.Set("endTime", at.Add(24*time.Hour).Format("2006-01-02T00:00:00Z"))
	return "https://api.fireworks.ai/v1/accounts/" + accountID + "/billing/summary?" + query.Encode()
}

func normalizeFireworks(payload map[string]any, accountID string, capturedAt int64) (Report, error) {
	if !fireworksAccountID.MatchString(accountID) {
		return Report{}, errors.New("Fireworks billing summary received an unsafe account identifier.")
	}
	rows, ok := payload["lineItems"].([]any)
	if _, present := payload["lineItems"]; present && !ok {
		return Report{}, errors.New("Fireworks billing summary lineItems was not an array.")
	}
	currencies := []string{}
	totals := make(map[string]map[string]*big.Int)
	for _, raw := range rows {
		row := object(raw)
		if row == nil {
			return Report{}, errors.New("Fireworks billing line item was not an object.")
		}
		currency, amount, err := fireworksMoney(row["totalCost"])
		if err != nil {
			return Report{}, err
		}
		series := "other"
		if value := row["series"]; value != nil {
			text, ok := value.(string)
			if !ok {
				return Report{}, errors.New("Fireworks billing line item series was invalid.")
			}
			switch text {
			case "SERVERLESS":
				series = "serverless"
			case "DEDICATED_DEPLOYMENT":
				series = "dedicated"
			case "TRAINING":
				series = "training"
			}
		}
		if totals[currency] == nil {
			currencies = append(currencies, currency)
			totals[currency] = make(map[string]*big.Int)
		}
		if totals[currency][series] == nil {
			totals[currency][series] = new(big.Int)
		}
		totals[currency][series].Add(totals[currency][series], amount)
	}
	report := Report{
		ProviderID: "fireworks", ProviderName: "Fireworks", CapturedAt: capturedAt,
		Source: "fireworks-billing-summary", Semantics: Semantics{Kind: "api-key", Label: "Fireworks API spend"},
		AccountLabel: displayString(accountID, 80), Buckets: []Bucket{}, Metrics: []Metric{},
		Notes: []string{"Rated line items may differ from the final invoice once credits or adjustments are applied."},
	}
	for _, currency := range currencies {
		amounts := totals[currency]
		total := new(big.Int)
		for _, amount := range amounts {
			total.Add(total, amount)
		}
		report.Metrics = append(report.Metrics, fireworksMetric(currency, "total", "Total spend", total))
		for _, series := range [][2]string{{"serverless", "Serverless"}, {"dedicated", "Dedicated deployments"}, {"training", "Training"}, {"other", "Other"}} {
			if amount := amounts[series[0]]; amount != nil {
				report.Metrics = append(report.Metrics, fireworksMetric(currency, series[0], series[1], amount))
			}
		}
	}
	if len(report.Metrics) == 0 {
		report.Notes = append(report.Notes, "Fireworks returned no rated line items for the last 30 days.")
	}
	return report, nil
}

func fireworksMoney(value any) (string, *big.Int, error) {
	money := object(value)
	if money == nil {
		return "", nil, errors.New("Fireworks billing line item total cost was not a money object.")
	}
	currency, ok := money["currencyCode"].(string)
	if !ok || !fireworksCurrency.MatchString(currency) {
		return "", nil, errors.New("Fireworks billing line item total cost currency was not an ISO 4217 code.")
	}
	units, err := fireworksMoneyInteger(money, "units", "whole units", 20)
	if err != nil {
		return "", nil, err
	}
	if !units.IsInt64() {
		return "", nil, errors.New("Fireworks billing line item total cost whole units exceeded the int64 range.")
	}
	nanos, err := fireworksMoneyInteger(money, "nanos", "nano units", 11)
	if err != nil {
		return "", nil, err
	}
	if nanos.Cmp(big.NewInt(-1_000_000_000)) <= 0 || nanos.Cmp(big.NewInt(1_000_000_000)) >= 0 {
		return "", nil, errors.New("Fireworks billing line item total cost nano units exceeded the Money range.")
	}
	if units.Sign()*nanos.Sign() < 0 {
		return "", nil, errors.New("Fireworks billing line item total cost mixed unit and nano signs.")
	}
	return currency, new(big.Int).Add(new(big.Int).Mul(units, big.NewInt(1_000_000_000)), nanos), nil
}

func fireworksMoneyInteger(money map[string]any, key, component string, maxChars int) (*big.Int, error) {
	value, present := money[key]
	if !present {
		return new(big.Int), nil
	}
	text, ok := value.(string)
	if number, valid := finiteNumber(value, false); valid && math.Trunc(number) == number && math.Abs(number) <= 9_007_199_254_740_991 {
		text, ok = jsNumber(number), true
	}
	if !ok || len(text) > maxChars || !fireworksInteger.MatchString(text) {
		return nil, fmt.Errorf("Fireworks billing line item total cost %s was not a bounded integer.", component)
	}
	result, valid := new(big.Int).SetString(text, 10)
	if !valid {
		return nil, fmt.Errorf("Fireworks billing line item total cost %s was not a bounded integer.", component)
	}
	return result, nil
}

func fireworksMetric(currency, series, label string, amount *big.Int) Metric {
	magnitude := new(big.Int).Abs(amount)
	units, nanos := new(big.Int), new(big.Int)
	units.QuoRem(magnitude, big.NewInt(1_000_000_000), nanos)
	value := units.String()
	if nanos.Sign() != 0 {
		value += "." + strings.TrimRight(fmt.Sprintf("%09d", nanos.Int64()), "0")
	}
	if amount.Sign() < 0 {
		value = "-" + value
	}
	return Metric{ID: strings.ToLower(currency) + "-" + series, Label: label, Value: value, Unit: "currency", Currency: currency}
}
