package usage

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func nativeSnapshot() OAuthSnapshot {
	return OAuthSnapshot{Ready: true, Source: "OAuth", ModelBaseURL: "https://api.openai.com/v1", Effective: RequestAuth{APIKey: "synthetic-native"}, Provider: RequestAuth{APIKey: "synthetic-native"}, Candidates: []any{map[string]any{"type": "oauth", "access": "synthetic-native", "refresh": "synthetic-refresh", "expires": float64(1), "clientId": "native-registration", "scopes": []any{"chatgpt.tokens.use.direct"}}}}
}

func companionSnapshot() OAuthSnapshot {
	access := "synthetic." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"synthetic-account"}}`)) + ".signature"
	return OAuthSnapshot{Ready: true, Source: "OAuth", ModelBaseURL: "https://chatgpt.com", Effective: RequestAuth{APIKey: access}, Provider: RequestAuth{APIKey: access}, Candidates: []any{map[string]any{"type": "oauth", "access": access, "refresh": "synthetic-codex-refresh", "expires": float64(1), "accountId": "synthetic-account"}}}
}

func copilotSnapshot() OAuthSnapshot {
	return OAuthSnapshot{Ready: true, ModelBaseURL: "https://api.individual.githubcopilot.com", Effective: RequestAuth{APIKey: "synthetic-access"}, Candidates: []any{map[string]any{"type": "oauth", "access": "synthetic-access", "refresh": "synthetic-github-token", "expires": float64(1)}}}
}

func requireIdentity(t testing.TB, got, want subscriptionIdentity, err error) {
	t.Helper()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("OAuth identity did not match source contract; err=%v", err)
	}
}

func requireIdentityRefused(t testing.TB, got subscriptionIdentity, err error) {
	t.Helper()
	if err == nil || !reflect.DeepEqual(got, subscriptionIdentity{}) {
		t.Fatal("invalid OAuth identity was accepted")
	}
}

func TestNativeOpenAIMatchesCompleteFreshGrant(t *testing.T) {
	good := nativeSnapshot()
	good.Candidates = append(good.Candidates, good.Candidates[0])
	good.Effective = RequestAuth{APIKey: "ignored-underlying-key", Headers: http.Header{"authorization": {"Bearer synthetic-native"}}}
	identity, err := resolveNativeOpenAI(good)
	requireIdentity(t, identity, subscriptionIdentity{access: "synthetic-native", refresh: "synthetic-refresh", clientID: "native-registration"}, err)
	for _, tt := range []struct {
		name   string
		change func(*OAuthSnapshot)
	}{
		{"readiness", func(s *OAuthSnapshot) { s.Ready = false }},
		{"API key source", func(s *OAuthSnapshot) { s.Source = "environment" }},
		{"model origin", func(s *OAuthSnapshot) { s.ModelBaseURL = "https://proxy.invalid" }},
		{"provider origin", func(s *OAuthSnapshot) { s.Provider.BaseURL = "https://proxy.invalid" }},
		{"effective origin", func(s *OAuthSnapshot) { s.Effective.BaseURL = "https://proxy.invalid" }},
		{"removed authorization", func(s *OAuthSnapshot) { s.Effective.Headers = http.Header{"Authorization": nil} }},
		{"non bearer override", func(s *OAuthSnapshot) { s.Effective.Headers = http.Header{"Authorization": {"Basic synthetic-native"}} }},
		{"duplicate case", func(s *OAuthSnapshot) {
			s.Effective.Headers = http.Header{"Authorization": {"Bearer synthetic-native"}, "authorization": {"Bearer synthetic-native"}}
		}},
		{"duplicate values", func(s *OAuthSnapshot) {
			s.Provider.Headers = http.Header{"Authorization": {"Bearer synthetic-native", "Bearer synthetic-native"}}
		}},
		{"different effective account", func(s *OAuthSnapshot) { s.Effective.APIKey = "different" }},
		{"missing native scope", func(s *OAuthSnapshot) { object(s.Candidates[0])["scopes"] = []any{"openid"} }},
		{"non string scope", func(s *OAuthSnapshot) { object(s.Candidates[0])["scopes"] = []any{"chatgpt.tokens.use.direct", true} }},
		{"missing refresh", func(s *OAuthSnapshot) { delete(object(s.Candidates[0]), "refresh") }},
		{"invalid expiry", func(s *OAuthSnapshot) { object(s.Candidates[0])["expires"] = "1" }},
		{"blank registration", func(s *OAuthSnapshot) { object(s.Candidates[0])["clientId"] = " " }},
		{"no matching grant", func(s *OAuthSnapshot) { object(s.Candidates[0])["access"] = "other" }},
		{"conflicting native grant", func(s *OAuthSnapshot) {
			s.Candidates = append(s.Candidates, map[string]any{"type": "oauth", "access": "synthetic-native", "refresh": "other-refresh", "expires": float64(1), "clientId": "native-registration", "scopes": []any{"chatgpt.tokens.use.direct"}})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := nativeSnapshot()
			tt.change(&snapshot)
			got, err := resolveNativeOpenAI(snapshot)
			requireIdentityRefused(t, got, err)
		})
	}
}

func TestCompanionMatchesFreshCodexAccount(t *testing.T) {
	good := companionSnapshot()
	good.Candidates = append(good.Candidates, good.Candidates[0])
	identity, err := resolveCompanion(good)
	requireIdentity(t, identity, subscriptionIdentity{companionAccess: good.Effective.APIKey, companionRefresh: "synthetic-codex-refresh", accountID: "synthetic-account", headers: http.Header{"Authorization": {"Bearer " + good.Effective.APIKey}, "ChatGPT-Account-Id": {"synthetic-account"}}}, err)
	for _, tt := range []struct {
		name   string
		change func(*OAuthSnapshot)
	}{
		{"not ready", func(s *OAuthSnapshot) { s.Ready = false }},
		{"API key", func(s *OAuthSnapshot) { s.Source = "environment" }},
		{"nonofficial selected origin", func(s *OAuthSnapshot) { s.ModelBaseURL = "https://api.openai.com" }},
		{"resolved origin", func(s *OAuthSnapshot) { s.Provider.BaseURL = "https://proxy.invalid" }},
		{"selected removal", func(s *OAuthSnapshot) { s.Effective.Headers = http.Header{"Authorization": nil} }},
		{"provider removal", func(s *OAuthSnapshot) { s.Provider.Headers = http.Header{"Authorization": nil} }},
		{"selected mismatch", func(s *OAuthSnapshot) { s.Effective.APIKey = "other" }},
		{"no JWT", func(s *OAuthSnapshot) { s.Effective.APIKey = "opaque"; s.Provider.APIKey = "opaque" }},
		{"candidate account mismatch", func(s *OAuthSnapshot) { object(s.Candidates[0])["accountId"] = "other" }},
		{"incomplete refresh", func(s *OAuthSnapshot) { object(s.Candidates[0])["refresh"] = "" }},
		{"incomplete expiry", func(s *OAuthSnapshot) { delete(object(s.Candidates[0]), "expires") }},
		{"conflicting match", func(s *OAuthSnapshot) {
			s.Candidates = append(s.Candidates, map[string]any{"type": "oauth", "access": s.Effective.APIKey, "refresh": "other", "expires": float64(1), "accountId": "synthetic-account"})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := companionSnapshot()
			tt.change(&snapshot)
			got, err := resolveCompanion(snapshot)
			requireIdentityRefused(t, got, err)
		})
	}
}

func TestCopilotUsesUniquelyMatchedPublicOAuthRefresh(t *testing.T) {
	good := copilotSnapshot()
	good.Candidates = append(good.Candidates, good.Candidates[0], map[string]any{"type": "oauth", "access": "other", "refresh": "other"})
	identity, err := resolveCopilot(good)
	requireIdentity(t, identity, subscriptionIdentity{access: "synthetic-access", refresh: "synthetic-github-token", headers: http.Header{"Authorization": {"Bearer synthetic-github-token"}, "X-GitHub-Api-Version": {"2025-05-01"}}}, err)
	for _, tt := range []struct {
		name   string
		change func(*OAuthSnapshot)
	}{
		{"not ready", func(s *OAuthSnapshot) { s.Ready = false }},
		{"enterprise host", func(s *OAuthSnapshot) { object(s.Candidates[0])["enterpriseUrl"] = "https://github.enterprise.invalid" }},
		{"unmatched account", func(s *OAuthSnapshot) { s.Effective.APIKey = "different" }},
		{"missing refresh", func(s *OAuthSnapshot) { delete(object(s.Candidates[0]), "refresh") }},
		{"missing expiry", func(s *OAuthSnapshot) { delete(object(s.Candidates[0]), "expires") }},
		{"non OAuth", func(s *OAuthSnapshot) { object(s.Candidates[0])["type"] = "api_key" }},
		{"conflicting match", func(s *OAuthSnapshot) {
			s.Candidates = append(s.Candidates, map[string]any{"type": "oauth", "access": "synthetic-access", "refresh": "other", "expires": float64(1)})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := copilotSnapshot()
			tt.change(&snapshot)
			got, err := resolveCopilot(snapshot)
			requireIdentityRefused(t, got, err)
		})
	}
}

func TestOpenAICompanionRequiresOptInAndReadiness(t *testing.T) {
	native := nativeSnapshot()
	native.Companion = new(companionSnapshot())
	nativeIdentity := subscriptionIdentity{access: "synthetic-native", refresh: "synthetic-refresh", clientID: "native-registration"}
	identity, err := resolveOpenAI(native, false)
	requireIdentity(t, identity, nativeIdentity, err)

	notReady, notReadyErr := resolveOpenAI(native, true)
	requireIdentityRefused(t, notReady, notReadyErr)

	native.CompanionReady = true
	companion, companionErr := resolveOpenAI(native, true)
	requireIdentity(t, companion, subscriptionIdentity{access: "synthetic-native", refresh: "synthetic-refresh", clientID: "native-registration", companionAccess: native.Companion.Effective.APIKey, companionRefresh: "synthetic-codex-refresh", accountID: "synthetic-account", headers: http.Header{"Authorization": {"Bearer " + native.Companion.Effective.APIKey}, "ChatGPT-Account-Id": {"synthetic-account"}}}, companionErr)

	native.Companion = nil
	unavailable, unavailableErr := resolveOpenAI(native, true)
	requireIdentity(t, unavailable, nativeIdentity, unavailableErr)
}

func ExampleOAuthSnapshot() {
	snapshot := OAuthSnapshot{
		Ready: true, Source: "OAuth", ModelBaseURL: "https://api.openai.com/v1",
		Effective: RequestAuth{APIKey: "synthetic-native"}, Provider: RequestAuth{APIKey: "synthetic-native"},
		Candidates: []any{map[string]any{"type": "oauth", "access": "synthetic-native", "refresh": "synthetic-refresh", "expires": float64(1), "clientId": "native-registration", "scopes": []any{"chatgpt.tokens.use.direct"}}},
	}
	auth := ResolvedAuth{ModelBaseURL: snapshot.ModelBaseURL, Revalidate: func(context.Context) error { return nil }, ResolveOAuth: func(context.Context) (OAuthSnapshot, error) { return snapshot, nil }}
	report, err := (Client{}).Query(context.Background(), "openai", auth, time.Second)
	fmt.Println(report.Source, err)
	snapshot.Source = "environment"
	_, err = (Client{}).Query(context.Background(), "openai", auth, time.Second)
	fmt.Println(errors.Is(err, ErrUnsupportedOpenAIUsageAuth))
	// Output:
	// openai-chatgpt-auth <nil>
	// true
}
