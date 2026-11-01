package usage

import (
	"context"
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

func (c Client) queryZai(ctx context.Context, providerID string, auth ResolvedAuth, headers http.Header) (Report, error) {
	endpoint, description := providerEndpoint(providerID)
	headers = headers.Clone()
	if match := bearerAuthorization.FindStringSubmatch(headers.Get("Authorization")); len(match) == 2 {
		headers.Set("Authorization", match[1])
	}
	payload, err := c.fetchJSON(ctx, endpoint, description, headers, true)
	if err != nil {
		return Report{}, err
	}
	if err := auth.revalidate(ctx); err != nil {
		return Report{}, err
	}
	planURL := strings.TrimSuffix(endpoint, "/api/monitor/usage/quota/limit") + "/api/biz/subscription/list"
	planPayload, planErr := c.fetchJSON(ctx, planURL, strings.Replace(description, "quota", "plan", 1), headers, true)
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
	report, err := normalizeZai(providerID, payload, time.Now().UnixMilli(), plan)
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return report, nil
}
