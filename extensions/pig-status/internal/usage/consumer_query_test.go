package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type consumerFixture struct {
	Name     string                     `json:"name"`
	Provider string                     `json:"provider"`
	Base     string                     `json:"base"`
	Routes   map[string]json.RawMessage `json:"routes"`
	Failures map[string]string          `json:"failures"`
	Expected map[string]any             `json:"expected"`
	Refused  bool                       `json:"refused"`
	Wires    []consumerRoute            `json:"wires"`
}
type consumerRoute struct {
	Host  string            `json:"host"`
	Path  string            `json:"path"`
	Query map[string]string `json:"query"`
}
type consumerWire struct {
	Route                                                                   consumerRoute
	Method, Authorization, Private, UserID, TokenAuth, Version, Mode, Agent string
}
type consumerServer struct {
	client Client
	mu     sync.Mutex
	wires  []consumerWire
}

func consumerFixtures(t testing.TB) []consumerFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/consumer-query-acceptance.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Fixtures []consumerFixture `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Fixtures) != 36 {
		t.Fatalf("independent fixture inventory = %d, want 36", len(doc.Fixtures))
	}
	return doc.Fixtures
}
func consumerNamed(t testing.TB, name string) consumerFixture {
	t.Helper()
	for _, f := range consumerFixtures(t) {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("missing independent consumer fixture %s", name)
	return consumerFixture{}
}
func consumerProviders(t testing.TB) []consumerFixture {
	t.Helper()
	return []consumerFixture{consumerNamed(t, "command-code:normal"), consumerNamed(t, "opencode-go:normal"), consumerNamed(t, "kimi-coding:normal"), consumerNamed(t, "xai:normal")}
}
func consumerOAuth() OAuthSnapshot {
	return OAuthSnapshot{Ready: true, Source: "OAuth", ModelBaseURL: "https://api.x.ai/v1", Effective: RequestAuth{APIKey: "fixture-secret", Headers: http.Header{"Authorization": {"Bearer fixture-secret"}, "X-Userid": {"borrowed-user"}, "X-Grok-Client-Version": {"untrusted-version"}, "User-Agent": {"untrusted-agent"}, "X-Private-Key": {"fixture-custom-secret"}}}, Candidates: []any{map[string]any{"type": "oauth", "access": "fixture-secret", "refresh": "fixture-refresh", "expires": float64(1)}}}
}
func consumerAuth(f consumerFixture) ResolvedAuth {
	auth := deepSeekAuth()
	auth.ModelBaseURL = f.Base
	if f.Provider == "xai" {
		auth.ResolveOAuth = func(context.Context) (OAuthSnapshot, error) { return consumerOAuth(), nil }
	}
	return auth
}
func consumerHTTPS(t testing.TB, f consumerFixture) *consumerServer {
	t.Helper()
	s := new(consumerServer)
	s.client = fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		query := map[string]string{}
		for key := range r.URL.Query() {
			query[key] = r.URL.Query().Get(key)
		}
		wire := consumerWire{Route: consumerRoute{r.Host, r.URL.Path, query}, Method: r.Method, Authorization: r.Header.Get("Authorization"), Private: r.Header.Get("X-Private-Key"), UserID: r.Header.Get("X-Userid"), TokenAuth: r.Header.Get("X-XAI-Token-Auth"), Version: r.Header.Get("X-Grok-Client-Version"), Mode: r.Header.Get("X-Grok-Client-Mode"), Agent: r.UserAgent()}
		s.mu.Lock()
		s.wires = append(s.wires, wire)
		s.mu.Unlock()
		body, ok := f.Routes[r.URL.Path]
		if !ok {
			t.Errorf("unplanned route %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		mode := f.Failures[r.URL.Path]
		switch mode {
		case "http":
			w.WriteHeader(503)
			body = []byte("fixture-secret raw-provider-canary")
		case "json":
			body = []byte("fixture-secret not-json")
		case "shape":
			body = []byte("[]")
		case "overflow":
			body = []byte(strings.Repeat(" ", 65537))
		}
		writeQueryFixture(t, w, body)
	})
	transport := s.client.Transport
	s.client.Transport = queryRoundTripper(func(r *http.Request) (*http.Response, error) {
		if f.Failures[r.URL.Path] == "transport" {
			s.mu.Lock()
			s.wires = append(s.wires, consumerWire{Route: consumerRoute{Host: r.URL.Host, Path: r.URL.Path, Query: consumerQueryValues(r)}, Method: r.Method, Authorization: r.Header.Get("Authorization"), Private: r.Header.Get("X-Private-Key"), Agent: r.UserAgent()})
			s.mu.Unlock()
			return nil, errors.New("fixture-secret raw-provider-canary\x1b[31m")
		}
		return transport.RoundTrip(r)
	})
	return s
}
func consumerQueryValues(r *http.Request) map[string]string {
	query := map[string]string{}
	for key := range r.URL.Query() {
		query[key] = r.URL.Query().Get(key)
	}
	return query
}
func (s *consumerServer) observed() []consumerWire {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]consumerWire(nil), s.wires...)
}
func (s *consumerServer) count() int32 { return int32(len(s.observed())) }
func consumerWant(f consumerFixture) []consumerWire {
	var want []consumerWire
	for _, route := range f.Wires {
		wire := consumerWire{Route: route, Method: "GET", Authorization: "Bearer fixture-secret", Private: "fixture-custom-secret", Agent: "pi-usage"}
		if f.Provider == "xai" {
			wire.Private = ""
			wire.TokenAuth = "xai-grok-cli"
			wire.Version = "1.0.10"
			wire.Mode = "interactive"
			wire.Agent = ""
			if route.Path == "/v1/billing" {
				wire.UserID = "canonical.user-15"
			}
		}
		want = append(want, wire)
	}
	return want
}
func requireConsumerWires(t testing.TB, got, want []consumerWire) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("consumer wire = %#v, want %#v", got, want)
	}
}
func consumerGood(t testing.TB, provider string) {
	t.Helper()
	f := consumerNamed(t, provider+":normal")
	s := consumerHTTPS(t, f)
	report, err := s.client.Query(t.Context(), provider, consumerAuth(f), time.Second)
	requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: provider, expected: f.Expected})
}
func consumerAssertFixture(t testing.TB, f consumerFixture) {
	t.Helper()
	s := consumerHTTPS(t, f)
	before := time.Now()
	report, err := s.client.Query(t.Context(), f.Provider, consumerAuth(f), time.Second)
	if f.Refused {
		consumerGood(t, f.Provider)
		requireQueryRefused(t, report, err)
		requireSafeDiagnostic(t, err)
	} else {
		requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
		requireRegionalCaptured(t, report, before, time.Now())
	}
	requireConsumerWires(t, s.observed(), consumerWant(f))
}
func TestConsumerQuery_ACNormalizationMatchesPinnedConsumers(t *testing.T) {
	for _, f := range consumerFixtures(t) {
		t.Run(f.Name, func(t *testing.T) { consumerAssertFixture(t, f) })
	}
}

func TestConsumerQuery_ACOfficialOriginAndAuthorization(t *testing.T) {
	for _, f := range consumerProviders(t) {
		for _, tt := range []struct {
			name   string
			change func(*ResolvedAuth)
		}{
			{"http", func(a *ResolvedAuth) { a.ModelBaseURL = strings.Replace(a.ModelBaseURL, "https:", "http:", 1) }},
			{"lookalike", func(a *ResolvedAuth) { a.ModelBaseURL = "https://api.commandcode.ai.attacker.invalid/v1" }},
			{"userinfo", func(a *ResolvedAuth) {
				a.ModelBaseURL = strings.Replace(a.ModelBaseURL, "https://", "https://fixture-secret@", 1)
			}},
			{"port", func(a *ResolvedAuth) {
				u, err := url.Parse(a.ModelBaseURL)
				consumerNoError(t, err)
				u.Host = net.JoinHostPort(u.Hostname(), "444")
				a.ModelBaseURL = u.String()
			}},
			{"proxy-not-model", func(a *ResolvedAuth) { a.ModelBaseURL = "https://cli-chat-proxy.grok.com/v1" }},
			{"missing", func(a *ResolvedAuth) { a.Headers = nil }},
			{"duplicate", func(a *ResolvedAuth) { a.Headers["authorization"] = []string{"Bearer fixture-secret"} }},
			{"control", func(a *ResolvedAuth) { a.Headers.Set("Authorization", "Bearer fixture-secret\r\nX-Bad: injected") }},
		} {
			t.Run(f.Provider+"/"+tt.name, func(t *testing.T) {
				consumerGood(t, f.Provider)
				s := consumerHTTPS(t, f)
				auth := consumerAuth(f)
				tt.change(&auth)
				consumerMirrorOAuth(&auth)

				report, err := s.client.Query(t.Context(), f.Provider, auth, time.Second)

				requireQueryRefused(t, report, err)
				requireSafeDiagnostic(t, err)
				requireRequestCount(t, s.count(), 0)
			})
		}
	}
}
func consumerMirrorOAuth(auth *ResolvedAuth) {
	if auth.ResolveOAuth == nil {
		return
	}
	snapshot := consumerOAuth()
	snapshot.ModelBaseURL = auth.ModelBaseURL
	snapshot.Effective.Headers = auth.Headers
	snapshot.Effective.APIKey = ""
	auth.ResolveOAuth = func(context.Context) (OAuthSnapshot, error) { return snapshot, nil }
}

func TestConsumerQuery_ACRequiredTransportBoundaries(t *testing.T) {
	for _, f := range consumerProviders(t) {
		for _, tt := range []struct {
			name   string
			status int
			body   string
		}{
			{"redirect", 302, "fixture-secret raw-provider-canary\x1b[31m"}, {"http", 503, "fixture-secret raw-provider-canary\x1b[31m"},
			{"json", 200, "fixture-secret raw-provider-canary\x1b[31m"}, {"overflow", 200, strings.Repeat(" ", 65537)},
		} {
			t.Run(f.Provider+"/"+tt.name, func(t *testing.T) {
				consumerGood(t, f.Provider)
				var requests atomic.Int32
				client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Location", "https://canary.invalid/fixture-secret")
					w.WriteHeader(tt.status)
					writeQueryFixture(t, w, []byte(tt.body))
				})

				report, err := client.Query(t.Context(), f.Provider, consumerAuth(f), time.Second)

				requireQueryRefused(t, report, err)
				requireSafeDiagnostic(t, err)
				requireRequestCount(t, requests.Load(), 1)
			})
		}
	}
}
func TestConsumerQuery_ACExactBodyLimitAccepted(t *testing.T) {
	for _, f := range consumerProviders(t) {
		t.Run(f.Provider, func(t *testing.T) {
			s := consumerHTTPS(t, consumerPadded(f))

			report, err := s.client.Query(t.Context(), f.Provider, consumerAuth(f), time.Second)

			requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
		})
	}
}
func consumerPadded(f consumerFixture) consumerFixture {
	for path, body := range f.Routes {
		f.Routes[path] = append(body, []byte(strings.Repeat(" ", 65536-len(body)))...)
	}
	return f
}
func TestConsumerQuery_ACDependencyErrorsAreSecretSafe(t *testing.T) {
	for _, f := range consumerProviders(t) {
		for _, mode := range []string{"transport", "body"} {
			t.Run(f.Provider+"/"+mode, func(t *testing.T) {
				consumerGood(t, f.Provider)
				var requests atomic.Int32
				body := &queryErrorBody{err: errors.New("fixture-secret fixture-custom-secret account-canary-89342 raw-provider-canary\x1b[31m")}
				client := Client{Transport: queryRoundTripper(func(*http.Request) (*http.Response, error) {
					requests.Add(1)
					if mode == "transport" {
						return nil, body.err
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
				})}

				report, err := client.Query(t.Context(), f.Provider, consumerAuth(f), time.Second)

				requireQueryRefused(t, report, err)
				requireSafeDiagnostic(t, err)
				requireRequestCount(t, requests.Load(), 1)
			})
		}
	}
}
func consumerFence(t testing.TB, f consumerFixture, phase string) {
	t.Helper()
	consumerGood(t, f.Provider)
	s := consumerHTTPS(t, f)
	auth := consumerAuth(f)
	want := int32(0)
	if phase == "publication" {
		want = int32(len(f.Wires))
	}
	if phase == "next-request" {
		want = 1
	}
	if phase == "missing" {
		auth.Revalidate = nil
	} else {
		auth.Revalidate = func(context.Context) error {
			if s.count() == want {
				return errors.New("fixture-secret raw-provider-canary\x1b[31m")
			}
			return nil
		}
	}
	report, err := s.client.Query(t.Context(), f.Provider, auth, time.Second)
	requireQueryRefused(t, report, err)
	requireSafeDiagnostic(t, err)
	requireRequestCount(t, s.count(), want)
}
func TestConsumerQuery_ACIdentityRevalidationFencesReports(t *testing.T) {
	for _, f := range consumerProviders(t) {
		for _, phase := range []string{"before", "missing", "publication"} {
			t.Run(f.Provider+"/"+phase, func(t *testing.T) { consumerFence(t, f, phase) })
		}
	}
}
func TestConsumerQuery_ACIdentityRevalidationBeforeOptionalWork(t *testing.T) {
	for _, provider := range []string{"command-code", "xai"} {
		t.Run(provider, func(t *testing.T) { consumerFence(t, consumerNamed(t, provider+":normal"), "next-request") })
	}
}
func TestConsumerQuery_ACHeadersSnapshottedBeforeRevalidation(t *testing.T) {
	for _, f := range consumerProviders(t) {
		t.Run(f.Provider, func(t *testing.T) {
			s := consumerHTTPS(t, f)
			auth := consumerAuth(f)
			auth.Revalidate = func(context.Context) error {
				auth.Headers.Set("Authorization", "Bearer account-canary-89342")
				auth.Headers.Set("X-Private-Key", "changed")
				return nil
			}

			report, err := s.client.Query(t.Context(), f.Provider, auth, time.Second)

			requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
			requireConsumerWires(t, s.observed(), consumerWant(f))
		})
	}
}
func consumerDeadline(t testing.TB, f consumerFixture, phase string, canceled bool) {
	t.Helper()
	consumerGood(t, f.Provider)
	var requests atomic.Int32
	started := make(chan struct{}, 1)
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if phase == "later" && n == 1 {
			writeQueryFixture(t, w, f.Routes[r.URL.Path])
			return
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
	})
	auth := consumerAuth(f)
	var mu sync.Mutex
	var deadlines []time.Time
	observe := func(ctx context.Context) {
		deadline, _ := ctx.Deadline()
		mu.Lock()
		deadlines = append(deadlines, deadline)
		mu.Unlock()
	}
	auth.Revalidate = func(ctx context.Context) error {
		observe(ctx)
		if phase == "authentication" {
			<-ctx.Done()
		}
		return ctx.Err()
	}
	if phase == "oauth" {
		auth.ResolveOAuth = func(ctx context.Context) (OAuthSnapshot, error) {
			observe(ctx)
			<-ctx.Done()
			return OAuthSnapshot{}, ctx.Err()
		}
	}
	transport := client.Transport
	client.Transport = queryRoundTripper(func(r *http.Request) (*http.Response, error) { observe(r.Context()); return transport.RoundTrip(r) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var workers sync.WaitGroup
	result := make(chan providerQueryResult, 1)
	workers.Go(func() {
		report, err := client.Query(ctx, f.Provider, auth, 150*time.Millisecond)
		result <- providerQueryResult{report, err}
	})
	t.Cleanup(func() { cancel(); workers.Wait() })
	if canceled {
		awaitQueryEvent(t, started)
		cancel()
	}
	got := awaitProviderQuery(t, result)
	requireQueryRefused(t, got.report, got.err)
	if canceled {
		requireBillingCanceled(t, got.err)
	} else {
		requireDeadline(t, got.err)
	}
	mu.Lock()
	defer mu.Unlock()
	requireRegionalBudget(t, deadlines)
	if len(deadlines) > 1 {
		requireBillingSameDeadline(t, deadlines)
	}
	want := int32(1)
	if phase == "authentication" || phase == "oauth" {
		want = 0
	}
	if phase == "later" {
		want = 2
	}
	requireRequestCount(t, requests.Load(), want)
}
func TestConsumerQuery_ACDeadlineIncludesAuthenticationAndBodies(t *testing.T) {
	for _, f := range consumerProviders(t) {
		for _, phase := range []string{"authentication", "body"} {
			t.Run(f.Provider+"/"+phase, func(t *testing.T) { consumerDeadline(t, f, phase, false) })
		}
	}
}
func TestConsumerQuery_ACDeadlineIncludesFreshOAuth(t *testing.T) {
	consumerDeadline(t, consumerNamed(t, "xai:normal"), "oauth", false)
}

func TestConsumerQuery_ACOptionalAndBillingShareTotalDeadline(t *testing.T) {
	for _, provider := range []string{"command-code", "xai"} {
		t.Run(provider, func(t *testing.T) { consumerDeadline(t, consumerNamed(t, provider+":normal"), "later", false) })
	}
}
func TestConsumerQuery_ACCancellationSettlesLocalHTTPS(t *testing.T) {
	for _, f := range consumerProviders(t) {
		t.Run(f.Provider, func(t *testing.T) { consumerDeadline(t, f, "body", true) })
	}
}
func TestConsumerQuery_ACOptionalAndBillingCancellation(t *testing.T) {
	for _, provider := range []string{"command-code", "xai"} {
		t.Run(provider, func(t *testing.T) { consumerDeadline(t, consumerNamed(t, provider+":normal"), "later", true) })
	}
}
func TestConsumerQuery_ACSharedAdmissionRecovers(t *testing.T) {
	for _, f := range consumerProviders(t) {
		t.Run(f.Provider, func(t *testing.T) {
			stop := occupyQuerySlots(t)
			s := consumerHTTPS(t, f)

			_, err := s.client.Query(t.Context(), f.Provider, consumerAuth(f), 40*time.Millisecond)
			observed := s.count()
			stop()
			report, goodErr := s.client.Query(t.Context(), f.Provider, consumerAuth(f), time.Second)

			requireDeadline(t, err)
			requireRequestCount(t, observed, 0)
			requirePinnedQueryReport(t, report, goodErr, providerQueryFixture{provider: f.Provider, expected: f.Expected})
		})
	}
}
func TestConsumerQuery_ACXaiRejectsUnmatchedFreshOAuth(t *testing.T) {
	for _, mode := range []string{"not-ready", "no-oauth", "mismatch", "incomplete", "incomplete-plus-match", "duplicate", "bad-expiry", "no-resolver", "resolver-error", "changed-before-billing", "changed-at-publication", "changed-account-before-billing", "changed-account-at-publication"} {
		t.Run(mode, func(t *testing.T) { consumerXaiRefusal(t, mode) })
	}
}
func consumerXaiRefusal(t testing.TB, mode string) {
	t.Helper()
	consumerGood(t, "xai")
	f := consumerNamed(t, "xai:normal")
	s := consumerHTTPS(t, f)
	auth := consumerAuth(f)
	auth.ResolveOAuth = func(context.Context) (OAuthSnapshot, error) {
		snapshot := consumerOAuth()
		candidate := snapshot.Candidates[0].(map[string]any)
		switch mode {
		case "not-ready":
			snapshot.Ready = false
		case "no-oauth":
			snapshot.Candidates = []any{map[string]any{"type": "api_key", "key": "fixture-secret"}}
		case "mismatch":
			candidate["access"] = "other-access"
		case "incomplete":
			delete(candidate, "refresh")
		case "incomplete-plus-match":
			snapshot.Candidates = append(snapshot.Candidates, map[string]any{"type": "oauth", "access": "fixture-secret", "expires": float64(1)})
		case "duplicate":
			snapshot.Candidates = append(snapshot.Candidates, candidate)
		case "bad-expiry":
			candidate["expires"] = "1"
		case "resolver-error":
			return OAuthSnapshot{}, errors.New("fixture-secret raw-provider-canary")
		case "changed-before-billing":
			if s.count() >= 1 {
				candidate["access"] = "other-access"
			}
		case "changed-at-publication":
			if s.count() >= 2 {
				candidate["access"] = "other-access"
			}
		case "changed-account-before-billing":
			if s.count() >= 1 {
				candidate["access"] = "other-access"
				snapshot.Effective = RequestAuth{APIKey: "other-access"}
			}
		case "changed-account-at-publication":
			if s.count() >= 2 {
				candidate["access"] = "other-access"
				snapshot.Effective = RequestAuth{APIKey: "other-access"}
			}
		}
		return snapshot, nil
	}
	if mode == "no-resolver" {
		auth.ResolveOAuth = nil
	}
	report, err := s.client.Query(t.Context(), "xai", auth, time.Second)
	want := int32(0)
	if mode == "changed-before-billing" || mode == "changed-account-before-billing" {
		want = 1
	}
	if mode == "changed-at-publication" || mode == "changed-account-at-publication" {
		want = 2
	}
	requireQueryRefused(t, report, err)
	requireSafeDiagnostic(t, err)
	requireRequestCount(t, s.count(), want)
}
func TestConsumerQuery_ACXaiRequiresSafeCanonicalDiscovery(t *testing.T) {
	for _, id := range []string{"", "../unsafe", "bad\r\nid", strings.Repeat("a", 129)} {
		t.Run(fmt.Sprintf("id-%q", id), func(t *testing.T) {
			consumerGood(t, "xai")
			f := consumerNamed(t, "xai:normal")
			body, err := json.Marshal(map[string]any{"userId": id})
			consumerNoError(t, err)
			f.Routes["/v1/user"] = body
			s := consumerHTTPS(t, f)

			report, queryErr := s.client.Query(t.Context(), "xai", consumerAuth(f), time.Second)

			requireQueryRefused(t, report, queryErr)
			requireSafeDiagnostic(t, queryErr)
			requireRequestCount(t, s.count(), 1)
		})
	}
}
func consumerNoError(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestConsumerQuery_ACNonfiniteCreditPoolCannotPublish(t *testing.T) {
	consumerGood(t, "command-code")
	f := consumerNamed(t, "command-code:normal")
	f.Routes["/alpha/billing/credits"] = json.RawMessage(`{"credits":{"monthlyCredits":1e308,"purchasedCredits":1e308}}`)
	s := consumerHTTPS(t, f)

	report, err := s.client.Query(t.Context(), f.Provider, consumerAuth(f), time.Second)

	requireQueryRefused(t, report, err)
	requireSafeDiagnostic(t, err)
}

func TestConsumerQuery_ACMaximumBudgetAppliesToConsumerDispatch(t *testing.T) {
	for _, f := range consumerProviders(t) {
		t.Run(f.Provider, func(t *testing.T) {
			s := consumerHTTPS(t, f)
			auth := consumerAuth(f)
			var deadlines []time.Time
			auth.Revalidate = func(ctx context.Context) error {
				deadline, _ := ctx.Deadline()
				deadlines = append(deadlines, deadline)
				return nil
			}

			report, err := s.client.Query(t.Context(), f.Provider, auth, time.Hour)

			requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
			requireRegionalBudget(t, deadlines)
			requireBillingSameDeadline(t, deadlines)
		})
	}
}

func TestConsumerQuery_ACMenuOnlyCapabilityMatchesSourceAdmission(t *testing.T) {
	got := consumerCapability(t)
	consumerGood(t, "xai")

	requireString(t, got, "baseten,command-code,deepseek,fireworks,github-copilot,kimi-coding,minimax,minimax-cn,moonshotai,moonshotai-cn,openai,openai-codex,opencode-go,openrouter,vercel-ai-gateway,zai,zai-coding-cn\nxai=false unknown=false\n")
}
func consumerCapability(t testing.TB) string {
	t.Helper()
	doc := exec.Command("go", "doc", "-short", ".")
	output, err := doc.CombinedOutput()
	consumerNoError(t, err)
	if !strings.Contains(string(output), "func Schedulable(providerID string) bool") {
		t.Fatal("schedulability capability unavailable at existing package boundary")
	}
	root, err := filepath.Abs("../..")
	consumerNoError(t, err)
	dir := t.TempDir()
	module := "module github.com/danruto/pig-plugins/extensions/pig-status/capabilitycheck\n\ngo 1.26\nrequire github.com/danruto/pig-plugins/extensions/pig-status v0.0.0\nreplace github.com/danruto/pig-plugins/extensions/pig-status => " + root + "\n"
	consumerNoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0600))
	source := `package main
import("fmt";"strings";usage "github.com/danruto/pig-plugins/extensions/pig-status/internal/usage")
func main(){ ids:=[]string{"baseten","command-code","deepseek","fireworks","github-copilot","kimi-coding","minimax","minimax-cn","moonshotai","moonshotai-cn","openai","openai-codex","opencode-go","openrouter","vercel-ai-gateway","zai","zai-coding-cn"};var admitted []string;for _,id:=range ids{if usage.Schedulable(id){admitted=append(admitted,id)}};fmt.Println(strings.Join(admitted,","));fmt.Printf("xai=%v unknown=%v\n",usage.Schedulable("xai"),usage.Schedulable("unknown"))}
`
	consumerNoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0600))
	cmd := exec.Command("go", "run", "-mod=mod", ".")
	cmd.Dir = dir
	result, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runtime capability invocation failed: %v: %s", err, result)
	}
	return string(result)
}
