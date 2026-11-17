package usage

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

func miniMaxEndpoint(providerID, kind string) string {
	root := "https://api.minimax.io"
	if providerID == "minimax-cn" {
		root = "https://api.minimaxi.com"
	}
	if kind == "account-balance" {
		return root + "/account/query_balance"
	}
	return root + "/v1/token_plan/remains"
}

func miniMaxKind(endpoint string) string {
	if strings.HasSuffix(endpoint, "/account/query_balance") {
		return "account-balance"
	}
	return "token-plan"
}

func miniMaxUsageURL(providerID string, auth ResolvedAuth, headers http.Header) (string, error) {
	key := auth.APIKey
	if match := bearerAuthorization.FindStringSubmatch(headers.Get("Authorization")); len(match) == 2 {
		key = match[1]
	}
	if key == "" {
		return "", errors.New("minimax runtime API key was unavailable")
	}
	return miniMaxEndpoint(providerID, miniMaxUsageKind(key)), nil
}

func moonshotAuthAllowed(providerID string, auth ResolvedAuth) bool {
	if providerID != "moonshotai" && providerID != "moonshotai-cn" || auth.SelectedProviderID == "" || auth.SelectedProviderID == providerID {
		return true
	}
	if auth.Source == "" {
		return false
	}
	if auth.Source != "environment" {
		return true
	}
	if auth.SourceLabel == nil {
		return false
	}
	for name := range strings.SplitSeq(*auth.SourceLabel, ",") {
		if jsTrim(name) == "MOONSHOT_API_KEY" {
			return false
		}
	}
	return true
}

func (c Client) queryZai(ctx context.Context, p provider, auth ResolvedAuth, headers http.Header) (Report, error) {
	headers = headers.Clone()
	if match := bearerAuthorization.FindStringSubmatch(headers.Get("Authorization")); len(match) == 2 {
		headers.Set("Authorization", match[1])
	}
	payload, _, err := c.fetchBounded(ctx, usageRequest{endpoint: p.endpoint, description: p.description, headers: headers, maxBytes: maxSuccessBodyBytes, failureError: zaiResponseError})
	if err != nil {
		return Report{}, err
	}
	if err := auth.revalidate(ctx); err != nil {
		return Report{}, err
	}
	planURL := strings.TrimSuffix(p.endpoint, "/api/monitor/usage/quota/limit") + "/api/biz/subscription/list"
	planRequest := usageRequest{endpoint: planURL, description: strings.Replace(p.description, "quota", "plan", 1), headers: headers, maxBytes: maxSuccessBodyBytes, failureError: zaiResponseError}
	planPayload, _, planErr := c.fetchBounded(ctx, planRequest)
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if err := auth.revalidate(ctx); err != nil {
		return Report{}, err
	}
	var plan *zaiPlan
	if planErr == nil {
		plan = normalizeZaiSubscription(planPayload)
	}
	report, err := normalizeZai(p.id, payload, time.Now().UnixMilli(), plan)
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return report, nil
}
