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

var ErrUnsupportedOpenAIAuth = errors.New("openai API-key usage reporting is not supported; chatgpt plan status requires native /login openai oauth")

var copilotHost = regexp.MustCompile(`^api\.[a-z0-9-]+\.githubcopilot\.com$`)
var bearerAuthorization = regexp.MustCompile(`(?i)^Bearer[\t\n\v\f\r ]+(.+)$`)

func officialProviderOrigin(id providerID, base string) bool {
	p, ok := lookupProvider(id)
	return ok && p.allowsOrigin(base)
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

func validateSnapshotOrigins(id providerID, snapshot OAuthSnapshot) error {
	if !snapshot.Ready {
		return errors.New("oauth credential readiness failed closed")
	}
	if !officialProviderOrigin(id, snapshot.ModelBaseURL) {
		return errors.New("oauth model requires the official provider origin")
	}
	for _, base := range []string{snapshot.Effective.BaseURL, snapshot.Provider.BaseURL} {
		if base != "" && !officialProviderOrigin(id, base) {
			return errors.New("oauth resolved authentication requires the official provider origin")
		}
	}
	return nil
}

func resolveNativeOpenAI(snapshot OAuthSnapshot) (subscriptionIdentity, error) {
	if err := validateSnapshotOrigins(providerOpenAI, snapshot); err != nil {
		return subscriptionIdentity{}, err
	}
	if snapshot.Source != "OAuth" {
		return subscriptionIdentity{}, ErrUnsupportedOpenAIAuth
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
		_, validExpiry := finiteNumber(credential["expires"])
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
	if err := validateSnapshotOrigins(providerOpenAICodex, snapshot); err != nil {
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
		_, validExpiry := finiteNumber(credential["expires"])
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
	if err := validateSnapshotOrigins(providerGitHubCopilot, snapshot); err != nil {
		return subscriptionIdentity{}, err
	}
	access, err := authAccessToken(snapshot.Effective)
	if err != nil {
		return subscriptionIdentity{}, err
	}
	if access == "" {
		return subscriptionIdentity{}, errors.New("github copilot oauth credentials were incomplete")
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
				return subscriptionIdentity{}, errors.New("github copilot usage does not support github enterprise accounts")
			}
		}
		refresh, _ := credential["refresh"].(string)
		_, validExpiry := finiteNumber(credential["expires"])
		if refresh == "" || !validExpiry {
			return subscriptionIdentity{}, errors.New("github copilot oauth credentials were incomplete")
		}
		matches[refresh] = struct{}{}
	}
	if len(matches) != 1 {
		return subscriptionIdentity{}, errors.New("github copilot oauth must uniquely match the fresh runtime credential")
	}
	identity := subscriptionIdentity{access: access}
	for refresh := range matches {
		identity.refresh = refresh
	}
	identity.headers = http.Header{"Authorization": {"Bearer " + identity.refresh}, "X-GitHub-Api-Version": {"2025-05-01"}}
	return identity, nil
}

func (a ResolvedAuth) subscriptionIdentity(ctx context.Context, id providerID) (subscriptionIdentity, error) {
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
		return subscriptionIdentity{}, errors.New("oauth credential discovery failed closed")
	}
	p, ok := lookupProvider(id)
	if !ok || p.identity == nil {
		return subscriptionIdentity{}, ErrUsageUnsupported
	}
	return p.identity(snapshot, a)
}

func openAIIdentity(snapshot OAuthSnapshot, auth ResolvedAuth) (subscriptionIdentity, error) {
	if !auth.UseCodexUsage {
		return resolveNativeOpenAI(snapshot)
	}
	return resolveOpenAICompanion(snapshot)
}

func resolveOpenAICompanion(snapshot OAuthSnapshot) (subscriptionIdentity, error) {
	identity, err := resolveNativeOpenAI(snapshot)
	if err != nil {
		return subscriptionIdentity{}, err
	}
	if !snapshot.CompanionReady {
		return subscriptionIdentity{}, errors.New("companion oauth credential readiness failed closed")
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

func (c Client) queryOAuth(ctx context.Context, p provider, auth ResolvedAuth, _ http.Header) (Report, error) {
	identity, err := auth.subscriptionIdentity(ctx, p.id)
	if err != nil {
		return Report{}, err
	}
	session := oauthSession{client: c, auth: auth, provider: p.id, identity: identity}
	if p.id == providerOpenAI && identity.companionAccess == "" {
		return normalizeReport(providerOpenAI, map[string]any{"useCodexUsage": auth.UseCodexUsage}, time.Now().UnixMilli())
	}
	if err := session.check(ctx); err != nil {
		return Report{}, err
	}
	if p.id == providerGitHubCopilot {
		payload, err := session.read(ctx, p.endpoint, p.description)
		if err != nil {
			return Report{}, err
		}
		return normalizeCopilot(payload, time.Now().UnixMilli())
	}
	return session.companionReport(ctx)
}

type oauthSession struct {
	client   Client
	auth     ResolvedAuth
	provider providerID
	identity subscriptionIdentity
}

func (s oauthSession) check(ctx context.Context) error {
	fresh, err := s.auth.subscriptionIdentity(ctx, s.provider)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(s.identity, fresh) {
		return errors.New("usage OAuth account changed during query")
	}
	return nil
}

func (s oauthSession) read(ctx context.Context, endpoint, description string) (map[string]any, error) {
	headers, err := validatedHeaders(s.provider, s.identity.headers)
	if err != nil {
		return nil, err
	}
	payload, err := s.client.fetch(ctx, endpoint, description, headers)
	if err != nil {
		return nil, err
	}
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s oauthSession) companionReport(ctx context.Context) (Report, error) {
	codex, ok := lookupProvider(providerOpenAICodex)
	if !ok {
		return Report{}, ErrUsageUnsupported
	}
	apps, err := s.read(ctx, codex.endpoint+"/chatpass/apps", "ChatGPT companion app usage endpoint")
	if err != nil {
		return Report{}, err
	}
	if _, _, err := matchingOpenAIAppBuckets(apps, s.identity.clientID); err != nil {
		return Report{}, err
	}
	plan, err := s.read(ctx, codex.endpoint, "ChatGPT companion plan usage endpoint")
	if err != nil {
		return Report{}, err
	}
	return normalizeReport(providerOpenAI, map[string]any{"apps": apps, "plan": plan, "clientId": s.identity.clientID}, time.Now().UnixMilli())
}
