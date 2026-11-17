package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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

func subscriptionCopy(snapshot OAuthSnapshot) OAuthSnapshot {
	clone := snapshot
	clone.Candidates = make([]any, len(snapshot.Candidates))
	for index, candidate := range snapshot.Candidates {
		values := map[string]any{}
		maps.Copy(values, candidate.(map[string]any))
		clone.Candidates[index] = values
	}
	if snapshot.Companion != nil {
		companion := subscriptionCopy(*snapshot.Companion)
		clone.Companion = &companion
	}
	return clone
}

func subscriptionSnapshot(provider string) OAuthSnapshot {
	switch provider {
	case "openai":
		return nativeSnapshot()
	case "openai-codex":
		return companionSnapshot()
	default:
		return copilotSnapshot()
	}
}

func subscriptionSnapshotFor(f subscriptionFixture) OAuthSnapshot {
	snapshot := subscriptionSnapshot(f.Provider)
	if f.Provider != "openai" {
		return snapshot
	}
	if f.Payload["apps"] != nil {
		companion := subscriptionSnapshot("openai-codex")
		snapshot.Companion, snapshot.CompanionReady = &companion, true
		snapshot.Candidates[0].(map[string]any)["clientId"] = f.Payload["clientId"]
		return snapshot
	}
	if f.Payload["useCodexUsage"] == true {
		snapshot.CompanionReady = true
	}
	return snapshot
}

func subscriptionAuth(snapshot OAuthSnapshot, optIn bool, resolve func(context.Context) (OAuthSnapshot, error)) ResolvedAuth {
	return ResolvedAuth{ModelBaseURL: snapshot.ModelBaseURL, Headers: http.Header{"Authorization": {"Bearer synthetic-unused-outer-token"}}, Revalidate: func(context.Context) error { return nil }, UseCodexUsage: optIn, ResolveOAuth: resolve}
}

func subscriptionFixedAuth(t testing.TB, f subscriptionFixture) ResolvedAuth {
	t.Helper()
	snapshot := subscriptionSnapshotFor(f)
	return subscriptionAuth(snapshot, f.Payload["apps"] != nil || f.Payload["useCodexUsage"] == true, func(context.Context) (OAuthSnapshot, error) { return snapshot, nil })
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
	codex := subscriptionWire{Method: "GET", Host: "chatgpt.com", Path: "/backend-api/wham/usage", Authorization: "Bearer " + snapshot.Effective.APIKey, Account: "synthetic-account", Agent: "pi-usage"}
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

func subscriptionCandidate(snapshot *OAuthSnapshot) map[string]any {
	return snapshot.Candidates[0].(map[string]any)
}

func TestSubscriptionQueryRejectsInvalidFreshOAuth(t *testing.T) {
	for _, tt := range []struct {
		name, provider string
		change         func(*OAuthSnapshot)
	}{
		{"native resolver", "openai", func(s *OAuthSnapshot) { s.Candidates[0].(map[string]any)["scopes"] = []any{"openid"} }},
		{"companion resolver", "openai", func(s *OAuthSnapshot) { s.CompanionReady = false }},
		{"copilot resolver", "github-copilot", func(s *OAuthSnapshot) { s.Effective.APIKey = "other" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			goodFixture := subscriptionGood(t, tt.provider)
			subscriptionValidQuery(t, goodFixture)
			server := subscriptionHTTPS(t, goodFixture)
			snapshot := subscriptionSnapshotFor(goodFixture)
			tt.change(&snapshot)
			auth := subscriptionAuth(snapshot, true, func(context.Context) (OAuthSnapshot, error) { return snapshot, nil })

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
	auth := subscriptionAuth(s, true, func(context.Context) (OAuthSnapshot, error) {
		return OAuthSnapshot{}, errors.New("fixture-secret companion resolver must not select legacy authorization")
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
	s := nativeSnapshot()
	companion := companionSnapshot()
	s.CompanionReady, s.Companion = true, &companion
	server := subscriptionHTTPS(t, subscriptionGood(t, "openai"))
	auth := subscriptionAuth(s, false, func(context.Context) (OAuthSnapshot, error) { return s, nil })

	report, err := server.client.Query(t.Context(), "openai", auth, time.Second)

	subscriptionAssertReport(t, report, err, f)
	requireSubscriptionWire(t, server.observations(), nil)
}

func subscriptionFenceAuth(t testing.TB, provider string, s OAuthSnapshot, optIn bool, changeAfter string, change func(*OAuthSnapshot), changed *atomic.Bool) ResolvedAuth {
	t.Helper()
	if provider == "openai-codex" {
		auth := subscriptionAuth(s, optIn, func(context.Context) (OAuthSnapshot, error) {
			return OAuthSnapshot{}, errors.New("fixture-secret legacy must use resolved authentication")
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
	return subscriptionAuth(s, optIn, func(context.Context) (OAuthSnapshot, error) {
		copy := subscriptionCopy(s)
		if (resolves.Add(1) > 1 && changeAfter == "resolve") || changed.Load() {
			change(&copy)
		}
		return copy, nil
	})
}

func TestOpenAIQueryResolvesIdentityOnceWithoutAFetch(t *testing.T) {
	f := subscriptionNamed(t, "openai:auth-only")
	subscriptionValidQuery(t, f)
	server := subscriptionHTTPS(t, f)
	s := subscriptionSnapshotFor(f)
	var resolves, revalidations atomic.Int32
	auth := subscriptionAuth(s, false, func(context.Context) (OAuthSnapshot, error) {
		resolves.Add(1)
		return s, nil
	})
	auth.Revalidate = func(context.Context) error {
		revalidations.Add(1)
		return nil
	}

	report, err := server.client.Query(t.Context(), "openai", auth, time.Second)

	subscriptionAssertReport(t, report, err, f)
	if got := resolves.Load(); got != 1 {
		t.Fatalf("OAuth identity reads = %d, want 1: no I/O separates the identity read from the result", got)
	}
	if got := revalidations.Load(); got != 1 {
		t.Fatalf("host revalidations = %d, want 1: no I/O separates the identity read from the result", got)
	}
	requireSubscriptionWire(t, server.observations(), nil)
}

func TestOpenAIQueryRefusesIdentityChangeReportedByRevalidation(t *testing.T) {
	f := subscriptionNamed(t, "openai:auth-only")
	subscriptionValidQuery(t, f)
	server := subscriptionHTTPS(t, f)
	s := subscriptionSnapshotFor(f)
	auth := subscriptionAuth(s, false, func(context.Context) (OAuthSnapshot, error) { return s, nil })
	auth.Revalidate = func(context.Context) error {
		return errors.New("fixture-secret account-canary-89342 identity changed")
	}

	report, err := server.client.Query(t.Context(), "openai", auth, time.Second)

	requireQueryRefused(t, report, err)
	requireSafeDiagnostic(t, err)
	requireSubscriptionWire(t, server.observations(), nil)
}

func TestSubscriptionQueryFencesChangingIdentity(t *testing.T) {
	for _, tt := range []struct {
		name, provider, changeAfter string
		wantRequests                int32
		change                      func(*OAuthSnapshot)
	}{
		{"companion readiness before HTTP", "openai", "resolve", 0, func(s *OAuthSnapshot) { s.Ready = false }},
		{"companion before HTTP", "openai", "resolve", 0, func(s *OAuthSnapshot) { subscriptionCandidate(s)["clientId"] = "changed" }},
		{"legacy revalidation before HTTP", "openai-codex", "resolve", 0, nil},
		{"copilot before HTTP", "github-copilot", "resolve", 0, func(s *OAuthSnapshot) { subscriptionCandidate(s)["refresh"] = "changed" }},
		{"companion before plan", "openai", "/backend-api/wham/usage/chatpass/apps", 1, func(s *OAuthSnapshot) { subscriptionCandidate(s.Companion)["accountId"] = "changed" }},
		{"companion before result", "openai", "/backend-api/wham/usage", 2, func(s *OAuthSnapshot) { subscriptionCandidate(s)["clientId"] = "changed" }},
		{"legacy revalidation before result", "openai-codex", "/backend-api/wham/usage", 1, nil},
		{"copilot before result", "github-copilot", "/copilot_internal/user", 1, func(s *OAuthSnapshot) { subscriptionCandidate(s)["refresh"] = "changed" }},
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
	auth := subscriptionAuth(s, true, func(ctx context.Context) (OAuthSnapshot, error) { <-ctx.Done(); return OAuthSnapshot{}, ctx.Err() })
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
		bucket := maps.Clone(buckets[2].(map[string]any))
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
