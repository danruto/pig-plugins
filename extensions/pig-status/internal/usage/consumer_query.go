package usage

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

func Schedulable(providerID string) bool {
	endpoint, _ := providerEndpoint(providerID)
	return endpoint != "" && providerID != "xai"
}

func (c Client) queryCommandCode(ctx context.Context, auth ResolvedAuth, headers http.Header) (Report, error) {
	endpoint, description := providerEndpoint("command-code")
	account, err := c.fetch(ctx, endpoint, description, headers)
	if err != nil {
		return Report{}, err
	}
	if err := auth.revalidate(ctx); err != nil {
		return Report{}, err
	}
	params := url.Values{}
	if org, ok := object(account["org"])["id"].(string); ok && consumerIdentifier.MatchString(org) {
		params.Set("orgId", org)
	}
	bundle := map[string]any{"account": account}
	for _, section := range []struct{ key, path, description string }{{"credits", "billing/credits", "Command Code credits endpoint"}, {"subscription", "billing/subscriptions", "Command Code plan endpoint"}, {"usage", "usage/summary", "Command Code usage summary endpoint"}} {
		if err := auth.revalidate(ctx); err != nil {
			return Report{}, err
		}
		if section.key == "usage" {
			if since, ok := object(object(bundle["subscription"])["data"])["currentPeriodStart"].(string); ok && jsTrim(since) != "" {
				params.Set("since", jsTrim(since))
			}
		}
		endpoint := "https://api.commandcode.ai/alpha/" + section.path
		if query := params.Encode(); query != "" {
			endpoint += "?" + query
		}
		payload, err := c.fetch(ctx, endpoint, section.description, headers)
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if err := auth.revalidate(ctx); err != nil {
			return Report{}, err
		}
		if err == nil {
			bundle[section.key] = payload
		}
	}
	report, err := normalizeCommandCode(bundle, time.Now().UnixMilli())
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return report, nil
}
