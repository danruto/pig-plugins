package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type RequestAuth struct {
	APIKey  string
	Headers http.Header
	BaseURL string
}

type OAuthSnapshot struct {
	Ready          bool
	Source         string
	ModelBaseURL   string
	Effective      RequestAuth
	Provider       RequestAuth
	Candidates     []any
	Companion      *OAuthSnapshot
	CompanionReady bool
}

type subscriptionIdentity struct {
	headers          http.Header
	access           string
	refresh          string
	clientID         string
	companionAccess  string
	companionRefresh string
	accountID        string
}

var ErrUnsupportedOpenAIUsageAuth = errors.New("OpenAI API-key usage reporting is not supported; ChatGPT plan status requires native /login openai OAuth.")

var copilotHost = regexp.MustCompile(`^api\.[a-z0-9-]+\.githubcopilot\.com$`)
var bearerAuthorization = regexp.MustCompile(`(?i)^Bearer[\t\n\v\f\r ]+(.+)$`)

func officialProviderOrigin(providerID, base string) bool {
	if providerID == "baseten" && sameHTTPSOrigin(base, "https://inference.baseten.co") {
		return true
	}
	if providerID == "github-copilot" {
		u, err := url.Parse(base)
		return err == nil && u.Scheme == "https" && u.User == nil && (u.Port() == "" || u.Port() == "443") && copilotHost.MatchString(strings.ToLower(u.Hostname()))
	}
	endpoint, _ := providerEndpoint(providerID)
	return endpoint != "" && sameHTTPSOrigin(base, endpoint)
}

func jsTrim(value string) string {
	return strings.TrimFunc(value, func(r rune) bool { return unicode.IsSpace(r) && r != 0x85 || r == 0xfeff })
}

func authAccessToken(auth RequestAuth) (string, error) {
	found := false
	var authorization string
	for name, values := range auth.Headers {
		if !strings.EqualFold(name, "Authorization") {
			continue
		}
		if found || len(values) > 1 {
			return "", errors.New("runtime authorization headers are ambiguous")
		}
		found = true
		if len(values) == 1 {
			authorization = values[0]
		}
	}
	if !found {
		return auth.APIKey, nil
	}
	match := bearerAuthorization.FindStringSubmatch(authorization)
	if len(match) != 2 {
		return "", nil
	}
	return match[1], nil
}

func validateSnapshotOrigins(providerID string, snapshot OAuthSnapshot) error {
	if !snapshot.Ready {
		return errors.New("OAuth credential readiness failed closed")
	}
	if !officialProviderOrigin(providerID, snapshot.ModelBaseURL) {
		return errors.New("OAuth model requires the official provider origin")
	}
	for _, base := range []string{snapshot.Effective.BaseURL, snapshot.Provider.BaseURL} {
		if base != "" && !officialProviderOrigin(providerID, base) {
			return errors.New("OAuth resolved authentication requires the official provider origin")
		}
	}
	return nil
}

func resolveNativeOpenAI(snapshot OAuthSnapshot) (subscriptionIdentity, error) {
	if err := validateSnapshotOrigins("openai", snapshot); err != nil {
		return subscriptionIdentity{}, err
	}
	if snapshot.Source != "OAuth" {
		return subscriptionIdentity{}, ErrUnsupportedOpenAIUsageAuth
	}
	access, err := authAccessToken(snapshot.Effective)
	if err != nil {
		return subscriptionIdentity{}, err
	}
	providerAccess, err := authAccessToken(snapshot.Provider)
	if err != nil {
		return subscriptionIdentity{}, err
	}
	if access == "" || access != providerAccess {
		return subscriptionIdentity{}, errors.New("active OpenAI authorization does not match its resolved OAuth credential")
	}
	matches := make(map[[2]string]struct{})
	for _, raw := range snapshot.Candidates {
		credential := object(raw)
		if credential["type"] != "oauth" || credential["access"] != access {
			continue
		}
		refresh, _ := credential["refresh"].(string)
		clientID, _ := credential["clientId"].(string)
		_, validExpiry := finiteNumber(credential["expires"], false)
		scopes, validScopes := credential["scopes"].([]any)
		direct := false
		for _, raw := range scopes {
			scope, ok := raw.(string)
			validScopes = validScopes && ok
			direct = direct || scope == "chatgpt.tokens.use.direct"
		}
		if refresh == "" || jsTrim(clientID) == "" || !validExpiry || !validScopes || !direct {
			return subscriptionIdentity{}, errors.New("matching OpenAI credential is not a complete native ChatGPT grant")
		}
		matches[[2]string{clientID, refresh}] = struct{}{}
	}
	if len(matches) != 1 {
		return subscriptionIdentity{}, errors.New("native ChatGPT OAuth must uniquely match the fresh runtime credential")
	}
	identity := subscriptionIdentity{access: access}
	for match := range matches {
		identity.clientID, identity.refresh = match[0], match[1]
	}
	return identity, nil
}

func resolveCompanion(snapshot OAuthSnapshot) (subscriptionIdentity, error) {
	if err := validateSnapshotOrigins("openai-codex", snapshot); err != nil {
		return subscriptionIdentity{}, err
	}
	if snapshot.Source != "OAuth" {
		return subscriptionIdentity{}, errors.New("companion usage requires Codex OAuth")
	}
	access, err := authAccessToken(snapshot.Provider)
	if err != nil {
		return subscriptionIdentity{}, err
	}
	selected, err := authAccessToken(snapshot.Effective)
	if err != nil {
		return subscriptionIdentity{}, err
	}
	if access == "" || access != selected {
		return subscriptionIdentity{}, errors.New("companion runtime authorization does not match its OAuth credential")
	}
	account := codexAccountID(access)
	if account == "" {
		return subscriptionIdentity{}, errors.New("companion token did not contain a valid account ID")
	}
	matches := make(map[string]struct{})
	for _, raw := range snapshot.Candidates {
		credential := object(raw)
		if credential["type"] != "oauth" || credential["access"] != access {
			continue
		}
		refresh, _ := credential["refresh"].(string)
		_, validExpiry := finiteNumber(credential["expires"], false)
		if credential["accountId"] != account || refresh == "" || !validExpiry {
			return subscriptionIdentity{}, errors.New("matching companion credential was incomplete or had a different account ID")
		}
		matches[refresh] = struct{}{}
	}
	if len(matches) != 1 {
		return subscriptionIdentity{}, errors.New("companion OAuth must uniquely match the fresh runtime credential")
	}
	identity := subscriptionIdentity{companionAccess: access, accountID: account, headers: http.Header{"Authorization": {"Bearer " + access}, "ChatGPT-Account-Id": {account}}}
	for refresh := range matches {
		identity.companionRefresh = refresh
	}
	return identity, nil
}

func codexAccountID(access string) string {
	parts := strings.Split(access, ".")
	if len(parts) != 3 || parts[1] == "" {
		return ""
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return ""
	}
	account, _ := object(payload["https://api.openai.com/auth"])["chatgpt_account_id"].(string)
	if len(account) == 0 || len(account) > 512 {
		return ""
	}
	for _, r := range account {
		if r < 0x21 || r > 0x7e {
			return ""
		}
	}
	return account
}

func resolveCopilot(snapshot OAuthSnapshot) (subscriptionIdentity, error) {
	if err := validateSnapshotOrigins("github-copilot", snapshot); err != nil {
		return subscriptionIdentity{}, err
	}
	access, err := authAccessToken(snapshot.Effective)
	if err != nil {
		return subscriptionIdentity{}, err
	}
	if access == "" {
		return subscriptionIdentity{}, errors.New("GitHub Copilot OAuth credentials were incomplete")
	}
	matches := make(map[string]struct{})
	for _, raw := range snapshot.Candidates {
		credential := object(raw)
		if credential["type"] != "oauth" || credential["access"] != access {
			continue
		}
		enterprise, _ := credential["enterpriseUrl"].(string)
		if enterprise != "" {
			if !strings.Contains(enterprise, "://") {
				enterprise = "https://" + enterprise
			}
			u, err := url.Parse(enterprise)
			if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
				return subscriptionIdentity{}, errors.New("GitHub Copilot usage does not support GitHub Enterprise accounts")
			}
		}
		refresh, _ := credential["refresh"].(string)
		_, validExpiry := finiteNumber(credential["expires"], false)
		if refresh == "" || !validExpiry {
			return subscriptionIdentity{}, errors.New("GitHub Copilot OAuth credentials were incomplete")
		}
		matches[refresh] = struct{}{}
	}
	if len(matches) != 1 {
		return subscriptionIdentity{}, errors.New("GitHub Copilot OAuth must uniquely match the fresh runtime credential")
	}
	identity := subscriptionIdentity{access: access}
	for refresh := range matches {
		identity.refresh = refresh
	}
	identity.headers = http.Header{"Authorization": {"Bearer " + identity.refresh}, "X-GitHub-Api-Version": {"2025-05-01"}}
	return identity, nil
}

func (a ResolvedAuth) subscriptionIdentity(ctx context.Context, providerID string) (subscriptionIdentity, error) {
	if err := a.revalidate(ctx); err != nil {
		return subscriptionIdentity{}, err
	}
	if a.ResolveOAuth == nil {
		return subscriptionIdentity{}, errors.New("usage requires fresh OAuth snapshots")
	}
	snapshot, err := a.ResolveOAuth(ctx)
	if ctx.Err() != nil {
		return subscriptionIdentity{}, ctx.Err()
	}
	if err != nil {
		return subscriptionIdentity{}, errors.New("OAuth credential discovery failed closed")
	}
	if providerID == "xai" {
		return resolveXai(snapshot)
	}
	if providerID == "github-copilot" {
		return resolveCopilot(snapshot)
	}
	return resolveOpenAI(snapshot, a.UseCodexUsage)
}

func resolveOpenAI(snapshot OAuthSnapshot, useCompanion bool) (subscriptionIdentity, error) {
	identity, err := resolveNativeOpenAI(snapshot)
	if err != nil {
		return subscriptionIdentity{}, err
	}
	if !useCompanion {
		return identity, nil
	}
	if !snapshot.CompanionReady {
		return subscriptionIdentity{}, errors.New("companion OAuth credential readiness failed closed")
	}
	if snapshot.Companion == nil {
		return identity, nil
	}
	companion, err := resolveCompanion(*snapshot.Companion)
	if err != nil {
		return subscriptionIdentity{}, err
	}
	identity.headers, identity.companionAccess, identity.companionRefresh, identity.accountID = companion.headers, companion.companionAccess, companion.companionRefresh, companion.accountID
	return identity, nil
}

func (c Client) queryOAuth(ctx context.Context, providerID string, auth ResolvedAuth) (Report, error) {
	identity, err := auth.subscriptionIdentity(ctx, providerID)
	if err != nil {
		return Report{}, err
	}
	check := func() error {
		fresh, err := auth.subscriptionIdentity(ctx, providerID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(identity, fresh) {
			return errors.New("usage OAuth account changed during query")
		}
		return nil
	}
	read := func(endpoint, description string) (map[string]any, error) {
		if err := check(); err != nil {
			return nil, err
		}
		headers, err := validatedHeaders(providerID, identity.headers)
		if err != nil {
			return nil, err
		}
		payload, err := c.fetch(ctx, endpoint, description, headers)
		if err != nil {
			return nil, err
		}
		if err := check(); err != nil {
			return nil, err
		}
		return payload, nil
	}
	if providerID == "openai" && identity.companionAccess == "" {
		if err := check(); err != nil {
			return Report{}, err
		}
		return normalizeReport("openai", map[string]any{"useCodexUsage": auth.UseCodexUsage}, time.Now().UnixMilli())
	}
	endpoint, description := providerEndpoint(providerID)
	if providerID == "github-copilot" {
		payload, err := read(endpoint, description)
		if err != nil {
			return Report{}, err
		}
		return normalizeCopilot(payload, time.Now().UnixMilli())
	}
	endpoint, _ = providerEndpoint("openai-codex")
	apps, err := read(endpoint+"/chatpass/apps", "ChatGPT companion app usage endpoint")
	if err != nil {
		return Report{}, err
	}
	_, _, err = matchingOpenAIAppBuckets(apps, identity.clientID)
	if err != nil {
		return Report{}, err
	}
	plan, err := read(endpoint, "ChatGPT companion plan usage endpoint")
	if err != nil {
		return Report{}, err
	}
	return normalizeReport("openai", map[string]any{"apps": apps, "plan": plan, "clientId": identity.clientID}, time.Now().UnixMilli())
}
