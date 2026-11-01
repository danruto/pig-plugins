package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type subscriptionFixture struct {
	Name     string         `json:"name"`
	Provider string         `json:"providerID"`
	Payload  map[string]any `json:"payload"`
	Expected map[string]any `json:"expected"`
	Error    string         `json:"error"`
}

func subscriptionFixtures(t testing.TB, success bool) []subscriptionFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Fixtures []subscriptionFixture `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	var found []subscriptionFixture
	for _, f := range reference.Fixtures {
		if (f.Provider == "openai" || f.Provider == "openai-codex" || f.Provider == "github-copilot") && (f.Error == "") == success {
			found = append(found, f)
		}
	}
	if len(found) == 0 {
		t.Fatal("no subscription reference fixtures")
	}
	return found
}

func subscriptionNamed(t testing.TB, name string) subscriptionFixture {
	t.Helper()
	for _, f := range append(subscriptionFixtures(t, true), subscriptionFixtures(t, false)...) {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("missing pinned fixture %s", name)
	return subscriptionFixture{}
}

func subscriptionGood(t testing.TB, provider string) subscriptionFixture {
	t.Helper()
	names := map[string]string{"openai": "openai:companion", "openai-codex": "openai-codex:primary-additional-credits", "github-copilot": "github-copilot:premium"}
	return subscriptionNamed(t, names[provider])
}

func subscriptionClone(t testing.TB, value map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func subscriptionSnapshot(provider string) map[string]any {
	access := "synthetic." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"synthetic-account"}}`)) + ".signature"
	switch provider {
	case "openai":
		return map[string]any{"Ready": true, "Source": "OAuth", "ModelBaseURL": "https://api.openai.com/v1", "Effective": map[string]any{"APIKey": "synthetic-native"}, "Provider": map[string]any{"APIKey": "synthetic-native"}, "Candidates": []any{map[string]any{"type": "oauth", "access": "synthetic-native", "refresh": "synthetic-refresh", "expires": float64(1), "clientId": "native-registration", "scopes": []any{"chatgpt.tokens.use.direct"}}}}
	case "openai-codex":
		return map[string]any{"Ready": true, "Source": "OAuth", "ModelBaseURL": "https://chatgpt.com", "Effective": map[string]any{"APIKey": access}, "Provider": map[string]any{"APIKey": access}, "Candidates": []any{map[string]any{"type": "oauth", "access": access, "refresh": "synthetic-codex-refresh", "expires": float64(1), "accountId": "synthetic-account"}}}
	default:
		return map[string]any{"Ready": true, "ModelBaseURL": "https://api.individual.githubcopilot.com", "Effective": map[string]any{"APIKey": "synthetic-access"}, "Candidates": []any{map[string]any{"type": "oauth", "access": "synthetic-access", "refresh": "synthetic-github-token", "expires": float64(1)}}}
	}
}

func subscriptionSnapshotFor(f subscriptionFixture) map[string]any {
	s := subscriptionSnapshot(f.Provider)
	if f.Provider == "openai" && (f.Payload["apps"] != nil || f.Payload["useCodexUsage"] == true) {
		s["CompanionReady"] = true
	}
	if f.Provider == "openai" && f.Payload["apps"] != nil {
		s["Companion"] = subscriptionSnapshot("openai-codex")
		s["Candidates"].([]any)[0].(map[string]any)["clientId"] = f.Payload["clientId"]
	}
	return s
}

func subscriptionAuth(t testing.TB, s map[string]any, optIn bool, resolve func(context.Context) (map[string]any, error)) ResolvedAuth {
	t.Helper()
	auth := ResolvedAuth{ModelBaseURL: s["ModelBaseURL"].(string), Headers: http.Header{"Authorization": {"Bearer synthetic-unused-outer-token"}}, Revalidate: func(context.Context) error { return nil }}
	encoded, err := json.Marshal(map[string]any{"UseCodexUsage": optIn})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &auth); err != nil {
		t.Fatal(err)
	}
	field := reflect.ValueOf(&auth).Elem().FieldByName("ResolveOAuth")
	if field.IsValid() {
		field.Set(reflect.MakeFunc(field.Type(), func(args []reflect.Value) []reflect.Value {
			snapshot, snapshotErr := resolve(args[0].Interface().(context.Context))
			value := reflect.New(field.Type().Out(0))
			data, marshalErr := json.Marshal(snapshot)
			if marshalErr != nil {
				t.Error(marshalErr)
			}
			if decodeErr := json.Unmarshal(data, value.Interface()); decodeErr != nil {
				t.Error(decodeErr)
			}
			errValue := reflect.Zero(field.Type().Out(1))
			if snapshotErr != nil {
				errValue = reflect.ValueOf(&snapshotErr).Elem()
			}
			return []reflect.Value{value.Elem(), errValue}
		}))
	}
	return auth
}

func subscriptionFixedAuth(t testing.TB, f subscriptionFixture) ResolvedAuth {
	t.Helper()
	s := subscriptionSnapshotFor(f)
	return subscriptionAuth(t, s, f.Payload["apps"] != nil || f.Payload["useCodexUsage"] == true, func(context.Context) (map[string]any, error) { return s, nil })
}

type subscriptionWire struct{ Method, Host, Path, Query, Authorization, Account, Version, Agent string }
type subscriptionServer struct {
	client Client
	mu     sync.Mutex
	wire   []subscriptionWire
}

func subscriptionHTTPS(t testing.TB, f subscriptionFixture) *subscriptionServer {
	t.Helper()
	s := new(subscriptionServer)
	s.client = fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.wire = append(s.wire, subscriptionWire{r.Method, r.Host, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-Id"), r.Header.Get("X-GitHub-Api-Version"), r.UserAgent()})
		s.mu.Unlock()
		payload := any(f.Payload)
		if f.Provider == "openai" {
			payload = f.Payload["plan"]
			if r.URL.Path == "/backend-api/wham/usage/chatpass/apps" {
				payload = f.Payload["apps"]
			}
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Error(err)
			return
		}
		writeQueryFixture(t, w, encoded)
	})
	return s
}

func (s *subscriptionServer) observations() []subscriptionWire {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]subscriptionWire(nil), s.wire...)
}

func subscriptionExpectedWire(f subscriptionFixture, plan bool) []subscriptionWire {
	snapshot := subscriptionSnapshot("openai-codex")
	codex := subscriptionWire{Method: "GET", Host: "chatgpt.com", Path: "/backend-api/wham/usage", Authorization: "Bearer " + snapshot["Effective"].(map[string]any)["APIKey"].(string), Account: "synthetic-account", Agent: "pi-usage"}
	switch f.Provider {
	case "openai":
		if f.Payload["apps"] == nil {
			return nil
		}
		apps := codex
		apps.Path = "/backend-api/wham/usage/chatpass/apps"
		if !plan {
			return []subscriptionWire{apps}
		}
		return []subscriptionWire{apps, codex}
	case "openai-codex":
		return []subscriptionWire{{Method: "GET", Host: "chatgpt.com", Path: "/backend-api/wham/usage", Authorization: "Bearer synthetic-unused-outer-token", Agent: "pi-usage"}}
	default:
		return []subscriptionWire{{Method: "GET", Host: "api.github.com", Path: "/copilot_internal/user", Authorization: "Bearer synthetic-github-token", Version: "2025-05-01", Agent: "pi-usage"}}
	}
}

func requireSubscriptionWire(t testing.TB, got, want []subscriptionWire) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subscription wire contract differs: got %d requests, want %d; method/origin/path/query/credential/account/version/agent compared", len(got), len(want))
	}
}

func subscriptionAssertReport(t testing.TB, report Report, err error, f subscriptionFixture) {
	t.Helper()
	requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
}

func subscriptionValidQuery(t testing.TB, f subscriptionFixture) {
	t.Helper()
	server := subscriptionHTTPS(t, f)
	report, err := server.client.Query(t.Context(), f.Provider, subscriptionFixedAuth(t, f), time.Second)
	subscriptionAssertReport(t, report, err, f)
	requireSubscriptionWire(t, server.observations(), subscriptionExpectedWire(f, true))
}

func TestSubscriptionQueryMatchesPinnedSource(t *testing.T) {
	for _, tt := range subscriptionFixtures(t, true) {
		t.Run(tt.Name, func(t *testing.T) { subscriptionValidQuery(t, tt) })
	}
}

func TestSubscriptionQueryRefusesPinnedFailures(t *testing.T) {
	for _, tt := range subscriptionFixtures(t, false) {
		t.Run(tt.Name, func(t *testing.T) {
			subscriptionValidQuery(t, subscriptionGood(t, tt.Provider))
			server := subscriptionHTTPS(t, tt)

			report, err := server.client.Query(t.Context(), tt.Provider, subscriptionFixedAuth(t, tt), time.Second)

			requireQueryRefused(t, report, err)
			requireSafeDiagnostic(t, err)
			requireSubscriptionWire(t, server.observations(), subscriptionExpectedWire(tt, tt.Name == "openai:no-plan-windows"))
		})
	}
}

func subscriptionCandidate(s map[string]any) map[string]any {
	return s["Candidates"].([]any)[0].(map[string]any)
}
func subscriptionEffective(s map[string]any) map[string]any { return s["Effective"].(map[string]any) }
func subscriptionCompanion(s map[string]any) map[string]any { return s["Companion"].(map[string]any) }

func TestSubscriptionQueryRejectsInvalidFreshOAuth(t *testing.T) {
	for _, tt := range []struct {
		name, provider string
		change         func(map[string]any)
	}{
		{"native API key unsupported", "openai", func(s map[string]any) { s["Source"] = "environment" }},
		{"native readiness", "openai", func(s map[string]any) { s["Ready"] = false }},
		{"native direct scope", "openai", func(s map[string]any) { subscriptionCandidate(s)["scopes"] = []any{"openid"} }},
		{"native selected origin", "openai", func(s map[string]any) { s["ModelBaseURL"] = "https://proxy.invalid" }},
		{"native provider origin", "openai", func(s map[string]any) { s["Provider"].(map[string]any)["BaseURL"] = "https://proxy.invalid" }},
		{"native effective override", "openai", func(s map[string]any) {
			subscriptionEffective(s)["Headers"] = map[string]any{"Authorization": []any{"Bearer different"}}
		}},
		{"native explicit removal", "openai", func(s map[string]any) { subscriptionEffective(s)["Headers"] = map[string]any{"Authorization": nil} }},
		{"native duplicate override", "openai", func(s map[string]any) {
			subscriptionEffective(s)["Headers"] = map[string]any{"Authorization": []any{"Bearer synthetic-native"}, "authorization": []any{"Bearer synthetic-native"}}
		}},
		{"native registration", "openai", func(s map[string]any) { subscriptionCandidate(s)["clientId"] = " " }},
		{"companion readiness", "openai", func(s map[string]any) { s["CompanionReady"] = false }},
		{"companion candidate account", "openai", func(s map[string]any) { subscriptionCandidate(subscriptionCompanion(s))["accountId"] = "other" }},
		{"companion public origin", "openai", func(s map[string]any) { subscriptionCompanion(s)["ModelBaseURL"] = "https://proxy.invalid" }},
		{"companion ambiguous grant", "openai", func(s map[string]any) {
			c := subscriptionCompanion(s)
			other := map[string]any{"type": "oauth", "access": subscriptionEffective(c)["APIKey"], "refresh": "different", "expires": float64(1), "accountId": "synthetic-account"}
			c["Candidates"] = append(c["Candidates"].([]any), other)
		}},
		{"companion selected override", "openai", func(s map[string]any) {
			subscriptionEffective(subscriptionCompanion(s))["Headers"] = map[string]any{"Authorization": []any{"Basic synthetic"}}
		}},
		{"companion missing JWT", "openai", func(s map[string]any) {
			c := subscriptionCompanion(s)
			subscriptionEffective(c)["APIKey"] = "opaque"
			c["Provider"].(map[string]any)["APIKey"] = "opaque"
			subscriptionCandidate(c)["access"] = "opaque"
		}},
		{"companion missing refresh", "openai", func(s map[string]any) { delete(subscriptionCandidate(subscriptionCompanion(s)), "refresh") }},
		{"copilot public only", "github-copilot", func(s map[string]any) { subscriptionCandidate(s)["enterpriseUrl"] = "https://enterprise.invalid" }},
		{"copilot missing refresh", "github-copilot", func(s map[string]any) { delete(subscriptionCandidate(s), "refresh") }},
		{"copilot wrong effective account", "github-copilot", func(s map[string]any) { subscriptionEffective(s)["APIKey"] = "other" }},
		{"copilot not ready", "github-copilot", func(s map[string]any) { s["Ready"] = false }},
		{"copilot ambiguous matching refresh", "github-copilot", func(s map[string]any) {
			s["Candidates"] = append(s["Candidates"].([]any), map[string]any{"type": "oauth", "access": "synthetic-access", "refresh": "other", "expires": float64(1)})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goodFixture := subscriptionGood(t, tt.provider)
			subscriptionValidQuery(t, goodFixture)
			server := subscriptionHTTPS(t, goodFixture)
			s := subscriptionSnapshotFor(goodFixture)
			tt.change(s)
			auth := subscriptionAuth(t, s, true, func(context.Context) (map[string]any, error) { return s, nil })

			report, err := server.client.Query(t.Context(), tt.provider, auth, time.Second)

			requireQueryRefused(t, report, err)
			requireSafeDiagnostic(t, err)
			requireSubscriptionWire(t, server.observations(), nil)
		})
	}
}

func TestSubscriptionQueryRejectsInvalidResolvedAuth(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*ResolvedAuth)
	}{
		{"legacy missing authorization", func(a *ResolvedAuth) { a.Headers = nil }},
		{"legacy ambiguous authorization", func(a *ResolvedAuth) {
			a.Headers = http.Header{"Authorization": {"Custom synthetic"}, "authorization": {"Custom other"}}
		}},
		{"legacy terminal control", func(a *ResolvedAuth) { a.Headers.Set("Authorization", "Custom fixture-secret\x1b[31m") }},
		{"legacy selected origin", func(a *ResolvedAuth) { a.ModelBaseURL = "https://proxy.invalid" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := subscriptionGood(t, "openai-codex")
			subscriptionValidQuery(t, f)
			server := subscriptionHTTPS(t, f)
			auth := subscriptionFixedAuth(t, f)
			tt.change(&auth)

			report, err := server.client.Query(t.Context(), "openai-codex", auth, time.Second)

			requireQueryRefused(t, report, err)
			requireSafeDiagnostic(t, err)
			requireSubscriptionWire(t, server.observations(), nil)
		})
	}
}

func TestSubscriptionQueryCopiesResolvedAuthorization(t *testing.T) {
	f := subscriptionGood(t, "openai-codex")
	s := subscriptionSnapshotFor(f)
	server := subscriptionHTTPS(t, f)
	auth := subscriptionAuth(t, s, true, func(context.Context) (map[string]any, error) {
		return nil, errors.New("fixture-secret companion resolver must not select legacy authorization")
	})
	auth.Headers.Set("Authorization", "Custom synthetic-selected-model")
	auth.Revalidate = func(ctx context.Context) error {
		auth.Headers.Set("Authorization", "Bearer account-canary-89342")
		return ctx.Err()
	}
	want := subscriptionExpectedWire(f, true)
	want[0].Authorization = "Custom synthetic-selected-model"

	report, err := server.client.Query(t.Context(), "openai-codex", auth, time.Second)

	subscriptionAssertReport(t, report, err, f)
	requireSubscriptionWire(t, server.observations(), want)
}

func TestOpenAIQueryRequiresExplicitCompanionOptIn(t *testing.T) {
	f := subscriptionNamed(t, "openai:auth-only")
	s := subscriptionSnapshot("openai")
	s["CompanionReady"] = true
	s["Companion"] = subscriptionSnapshot("openai-codex")
	server := subscriptionHTTPS(t, subscriptionGood(t, "openai"))
	auth := subscriptionAuth(t, s, false, func(context.Context) (map[string]any, error) { return s, nil })

	report, err := server.client.Query(t.Context(), "openai", auth, time.Second)

	subscriptionAssertReport(t, report, err, f)
	requireSubscriptionWire(t, server.observations(), nil)
}

func subscriptionFenceAuth(t testing.TB, provider string, s map[string]any, optIn bool, changeAfter string, change func(map[string]any), changed *atomic.Bool) ResolvedAuth {
	t.Helper()
	if provider == "openai-codex" {
		auth := subscriptionAuth(t, s, optIn, func(context.Context) (map[string]any, error) {
			return nil, errors.New("fixture-secret legacy must use resolved authentication")
		})
		var validations atomic.Int32
		auth.Revalidate = func(ctx context.Context) error {
			if validations.Add(1) > 1 || changeAfter == "resolve" {
				return errors.New("fixture-secret account-canary-89342 revalidation changed")
			}
			return ctx.Err()
		}
		return auth
	}
	var resolves atomic.Int32
	return subscriptionAuth(t, s, optIn, func(context.Context) (map[string]any, error) {
		copy := subscriptionClone(t, s)
		if (resolves.Add(1) > 1 && changeAfter == "resolve") || changed.Load() {
			change(copy)
		}
		return copy, nil
	})
}

func TestOpenAIQueryFencesDefaultAuthOnlyResult(t *testing.T) {
	f := subscriptionNamed(t, "openai:auth-only")
	subscriptionValidQuery(t, f)
	server := subscriptionHTTPS(t, f)
	s := subscriptionSnapshotFor(f)
	auth := subscriptionFenceAuth(t, "openai", s, false, "resolve", func(s map[string]any) { s["Ready"] = false }, new(atomic.Bool))

	report, err := server.client.Query(t.Context(), "openai", auth, time.Second)

	requireQueryRefused(t, report, err)
	requireSafeDiagnostic(t, err)
	requireSubscriptionWire(t, server.observations(), nil)
}

func TestSubscriptionQueryFencesChangingIdentity(t *testing.T) {
	for _, tt := range []struct {
		name, provider, changeAfter string
		wantRequests                int32
		change                      func(map[string]any)
	}{
		{"companion readiness before HTTP", "openai", "resolve", 0, func(s map[string]any) { s["Ready"] = false }},
		{"companion before HTTP", "openai", "resolve", 0, func(s map[string]any) { subscriptionCandidate(s)["clientId"] = "changed" }},
		{"legacy revalidation before HTTP", "openai-codex", "resolve", 0, nil},
		{"copilot before HTTP", "github-copilot", "resolve", 0, func(s map[string]any) { subscriptionCandidate(s)["refresh"] = "changed" }},
		{"companion before plan", "openai", "/backend-api/wham/usage/chatpass/apps", 1, func(s map[string]any) { subscriptionCandidate(subscriptionCompanion(s))["accountId"] = "changed" }},
		{"companion before result", "openai", "/backend-api/wham/usage", 2, func(s map[string]any) { subscriptionCandidate(s)["clientId"] = "changed" }},
		{"legacy revalidation before result", "openai-codex", "/backend-api/wham/usage", 1, nil},
		{"copilot before result", "github-copilot", "/copilot_internal/user", 1, func(s map[string]any) { subscriptionCandidate(s)["refresh"] = "changed" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := subscriptionGood(t, tt.provider)
			subscriptionValidQuery(t, f)
			s := subscriptionSnapshotFor(f)
			var received atomic.Int32
			var changed atomic.Bool
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				received.Add(1)
				payload := any(f.Payload)
				if tt.provider == "openai" {
					payload = f.Payload["plan"]
					if r.URL.Path == "/backend-api/wham/usage/chatpass/apps" {
						payload = f.Payload["apps"]
					}
				}
				if r.URL.Path == tt.changeAfter {
					changed.Store(true)
				}
				encoded, err := json.Marshal(payload)
				if err != nil {
					t.Error(err)
					return
				}
				writeQueryFixture(t, w, encoded)
			})
			auth := subscriptionFenceAuth(t, tt.provider, s, true, tt.changeAfter, tt.change, &changed)

			report, err := client.Query(t.Context(), tt.provider, auth, time.Second)

			requireQueryRefused(t, report, err)
			requireSafeDiagnostic(t, err)
			requireRequestCount(t, received.Load(), tt.wantRequests)
		})
	}
}

func TestSubscriptionQueryUsesGuardedTransport(t *testing.T) {
	for _, provider := range []string{"openai", "openai-codex", "github-copilot"} {
		t.Run(provider, func(t *testing.T) {
			for _, tt := range []struct {
				name   string
				status int
				body   string
			}{
				{"redirect", http.StatusFound, "fixture-secret raw-provider-canary account-canary-89342\x1b[31m"},
				{"HTTP failure", http.StatusServiceUnavailable, "fixture-secret raw-provider-canary account-canary-89342\x1b[31m"},
				{"oversized body", http.StatusOK, strings.Repeat(" ", 65537)},
			} {
				t.Run(tt.name, func(t *testing.T) {
					f := subscriptionGood(t, provider)
					subscriptionValidQuery(t, f)
					var requests atomic.Int32
					client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
						requests.Add(1)
						w.Header().Set("Location", "https://canary.invalid/leak?fixture-secret")
						w.WriteHeader(tt.status)
						writeQueryFixture(t, w, []byte(tt.body))
					})

					report, err := client.Query(t.Context(), provider, subscriptionFixedAuth(t, f), time.Second)

					requireQueryRefused(t, report, err)
					requireSafeDiagnostic(t, err)
					requireRequestCount(t, requests.Load(), 1)
				})
			}
		})
	}
}

func subscriptionDeadlineAuth(t testing.TB, f subscriptionFixture) ResolvedAuth {
	t.Helper()
	s := subscriptionSnapshotFor(f)
	auth := subscriptionAuth(t, s, true, func(ctx context.Context) (map[string]any, error) { <-ctx.Done(); return nil, ctx.Err() })
	if f.Provider == "openai-codex" {
		auth.Revalidate = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	}
	return auth
}

func TestSubscriptionQueryDeadlineCoversAuthentication(t *testing.T) {
	for _, provider := range []string{"openai", "openai-codex", "github-copilot"} {
		t.Run(provider, func(t *testing.T) {
			f := subscriptionGood(t, provider)
			subscriptionValidQuery(t, f)
			server := subscriptionHTTPS(t, f)
			auth := subscriptionDeadlineAuth(t, f)

			report, err := server.client.Query(t.Context(), provider, auth, 20*time.Millisecond)

			requireQueryRefused(t, report, err)
			requireDeadline(t, err)
			requireSubscriptionWire(t, server.observations(), nil)
		})
	}
}

func TestSubscriptionQueryDeadlineCoversResponse(t *testing.T) {
	for _, provider := range []string{"openai", "openai-codex", "github-copilot"} {
		t.Run(provider, func(t *testing.T) {
			f := subscriptionGood(t, provider)
			subscriptionValidQuery(t, f)
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})

			report, err := client.Query(t.Context(), provider, subscriptionFixedAuth(t, f), 50*time.Millisecond)

			requireQueryRefused(t, report, err)
			requireDeadline(t, err)
		})
	}
}

func TestSubscriptionQuerySharesAdmission(t *testing.T) {
	for _, provider := range []string{"openai", "openai-codex", "github-copilot"} {
		t.Run(provider, func(t *testing.T) {
			f := subscriptionGood(t, provider)
			stop := occupyQuerySlots(t)
			server := subscriptionHTTPS(t, f)

			report, err := server.client.Query(t.Context(), provider, subscriptionFixedAuth(t, f), 40*time.Millisecond)
			stop()
			good, goodErr := server.client.Query(t.Context(), provider, subscriptionFixedAuth(t, f), time.Second)

			subscriptionAssertReport(t, good, goodErr, f)
			requireQueryRefused(t, report, err)
			requireDeadline(t, err)
			requireSubscriptionWire(t, server.observations(), subscriptionExpectedWire(f, true))
		})
	}
}

func subscriptionBoundedRegistration(t testing.TB, apps, windowCount int) subscriptionFixture {
	t.Helper()
	f := subscriptionNamed(t, "openai:companion")
	items := f.Payload["apps"].(map[string]any)["items"].([]any)
	for len(items) < apps {
		items = append(items, map[string]any{"id": fmt.Sprintf("unrelated-%d", len(items)), "windows": []any{}})
	}
	f.Payload["apps"].(map[string]any)["items"] = items
	matched := items[1].(map[string]any)
	windows := matched["windows"].([]any)
	buckets := f.Expected["buckets"].([]any)
	for len(windows) < windowCount {
		windows = append(windows, windows[0])
		bucket := subscriptionClone(t, buckets[2].(map[string]any))
		bucket["id"] = fmt.Sprintf("app:%d", len(windows)-1)
		buckets = append(buckets, bucket)
	}
	matched["windows"] = windows
	f.Expected["buckets"] = buckets
	return f
}

func TestOpenAIQueryAcceptsBoundedCompleteRegistration(t *testing.T) {
	for _, tt := range []struct {
		name          string
		apps, windows int
	}{{"128 registrations", 128, 1}, {"32 windows", 2, 32}} {
		t.Run(tt.name, func(t *testing.T) {
			f := subscriptionBoundedRegistration(t, tt.apps, tt.windows)

			subscriptionValidQuery(t, f)
		})
	}
}
