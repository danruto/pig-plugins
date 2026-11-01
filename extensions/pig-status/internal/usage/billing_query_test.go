package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type billingFixture struct {
	Name     string          `json:"name"`
	Provider string          `json:"providerID"`
	Kind     string          `json:"kind"`
	Account  string          `json:"accountID"`
	Payload  json.RawMessage `json:"payload"`
	Expected map[string]any  `json:"expected"`
}

func billingNamed(t testing.TB, name string) billingFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/billing-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Fixtures []json.RawMessage `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	for _, raw := range reference.Fixtures {
		var header struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			t.Fatal(err)
		}
		if header.Name == name {
			var fixture billingFixture
			if err := json.Unmarshal(raw, &fixture); err != nil {
				t.Fatal(err)
			}
			return fixture
		}
	}
	t.Fatalf("missing source-produced fixture %s", name)
	return billingFixture{}
}

func billingAuth(provider string) ResolvedAuth {
	auth := deepSeekAuth()
	auth.ModelBaseURL = map[string]string{"baseten": "https://inference.baseten.co/v1", "openrouter": "https://openrouter.ai/api/v1", "fireworks": "https://api.fireworks.ai/inference/v1"}[provider]
	return auth
}

type billingTarget struct {
	ID    string
	Label string
}
type billingResult struct {
	report  Report
	targets []billingTarget
	err     error
}

func billingList(t testing.TB, client Client, ctx context.Context, provider string, auth ResolvedAuth, timeout time.Duration) billingResult {
	t.Helper()
	method := reflect.ValueOf(client).MethodByName("ListTargets")
	if !method.IsValid() {
		t.Fatal("Client.ListTargets missing: explicit billing-target discovery unavailable")
	}
	values := method.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(provider), reflect.ValueOf(auth), reflect.ValueOf(timeout)})
	var result billingResult
	if !values[1].IsNil() {
		result.err = values[1].Interface().(error)
	}
	encoded, err := json.Marshal(values[0].Interface())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &result.targets); err != nil {
		t.Fatal(err)
	}
	return result
}

func billingTargetQuery(t testing.TB, client Client, ctx context.Context, provider string, auth ResolvedAuth, target string, timeout time.Duration) billingResult {
	t.Helper()
	query, ok := any(client).(interface {
		QueryTarget(context.Context, string, ResolvedAuth, string, time.Duration) (Report, error)
	})
	if !ok {
		t.Fatal("Client.QueryTarget missing: explicit billing-target query unavailable")
	}
	report, err := query.QueryTarget(ctx, provider, auth, target, timeout)
	return billingResult{report: report, err: err}
}

func billingCall(t testing.TB, client Client, ctx context.Context, surface string, auth ResolvedAuth, timeout time.Duration) billingResult {
	t.Helper()
	switch surface {
	case "targets":
		return billingList(t, client, ctx, "fireworks", auth, timeout)
	case "fireworks":
		return billingTargetQuery(t, client, ctx, "fireworks", auth, "synthetic-account", timeout)
	default:
		report, err := client.Query(ctx, surface, auth, timeout)
		return billingResult{report: report, err: err}
	}
}

func billingProvider(surface string) string {
	if surface == "targets" {
		return "fireworks"
	}
	return surface
}

func billingGoodName(provider string) string {
	return map[string]string{"baseten": "baseten:exact-decimals", "openrouter": "openrouter:limit-and-spend", "fireworks": "fireworks:currencies-series-exact"}[provider]
}

func billingBody(t testing.TB, surface string) []byte {
	t.Helper()
	if surface == "targets" {
		return []byte(`{"accounts":[{"name":"accounts/synthetic-account"}]}`)
	}
	return billingNamed(t, billingGoodName(surface)).Payload
}

func requireBillingSuccess(t testing.TB, result billingResult) {
	t.Helper()
	if result.err != nil {
		t.Fatalf("valid billing call failed: %v", result.err)
	}
	if result.report.ProviderID == "" && len(result.targets) == 0 {
		t.Fatal("valid billing call produced neither report nor targets")
	}
}

func requireBillingRefusal(t testing.TB, result billingResult) {
	t.Helper()
	requireQueryRefused(t, result.report, result.err)
	if len(result.targets) != 0 {
		t.Fatal("refused discovery published partial targets")
	}
}

func requireBillingTargetRefusal(t testing.TB, targets []billingTarget, err error) {
	t.Helper()
	requireBillingRefusal(t, billingResult{targets: targets, err: err})
}

func requireBillingTargets(t testing.TB, targets []billingTarget, err error, want []billingTarget) {
	t.Helper()
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func requireBillingReport(t testing.TB, report Report, err error, f billingFixture, before, after time.Time) {
	t.Helper()
	requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
	if report.CapturedAt < before.UnixMilli() || report.CapturedAt > after.UnixMilli() {
		t.Fatal("report capture timestamp is not current Unix milliseconds")
	}
}

func billingGood(t testing.TB, provider string) {
	t.Helper()
	f := billingNamed(t, billingGoodName(provider))
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) { writeQueryFixture(t, w, f.Payload) })
	before := time.Now()
	result := billingCall(t, client, t.Context(), provider, billingAuth(provider), time.Second)
	requireBillingReport(t, result.report, result.err, f, before, time.Now())
}

func billingDiscoveryGood(t testing.TB) {
	t.Helper()
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) { writeQueryFixture(t, w, billingBody(t, "targets")) })
	result := billingCall(t, client, t.Context(), "targets", billingAuth("fireworks"), time.Second)
	requireBillingTargets(t, result.targets, result.err, []billingTarget{{"synthetic-account", "synthetic-account"}})
}

type billingWire struct {
	Method, Host, Path, Authorization string
	Query                             url.Values
}
type billingServer struct {
	client Client
	mu     sync.Mutex
	wire   []billingWire
}

func billingHTTPS(t testing.TB, body []byte) *billingServer {
	t.Helper()
	s := new(billingServer)
	s.client = fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.wire = append(s.wire, billingWire{r.Method, r.Host, r.URL.Path, r.Header.Get("Authorization"), r.URL.Query()})
		s.mu.Unlock()
		writeQueryFixture(t, w, body)
	})
	return s
}

func (s *billingServer) observations() []billingWire {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]billingWire(nil), s.wire...)
}

func requireBillingOrigin(t testing.TB, wire []billingWire, provider string) {
	t.Helper()
	if len(wire) != 1 {
		t.Fatalf("billing query made %d requests, want exactly one", len(wire))
	}
	host := map[string]string{"baseten": "api.baseten.co", "openrouter": "openrouter.ai", "fireworks": "api.fireworks.ai"}[provider]
	if wire[0].Host != host || wire[0].Method != "GET" || wire[0].Authorization != "Bearer fixture-secret" {
		t.Fatal("incorrect official billing origin, method or authorization")
	}
}

func requireBillingWires(t testing.TB, got, want []billingWire) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("billing wire differs: got %#v, want %#v", got, want)
	}
}

func requireBillingWindow(t testing.TB, wire billingWire, before, after time.Time) {
	t.Helper()
	query := wire.Query
	if wire.Host == "openrouter.ai" {
		return
	}
	if wire.Host == "api.fireworks.ai" {
		beforeDay := time.Date(before.UTC().Year(), before.UTC().Month(), before.UTC().Day(), 0, 0, 0, 0, time.UTC)
		afterDay := time.Date(after.UTC().Year(), after.UTC().Month(), after.UTC().Day(), 0, 0, 0, 0, time.UTC)
		wantBefore := url.Values{"startTime": {beforeDay.AddDate(0, 0, -29).Format(time.RFC3339)}, "endTime": {beforeDay.AddDate(0, 0, 1).Format(time.RFC3339)}}
		wantAfter := url.Values{"startTime": {afterDay.AddDate(0, 0, -29).Format(time.RFC3339)}, "endTime": {afterDay.AddDate(0, 0, 1).Format(time.RFC3339)}}
		if !reflect.DeepEqual(query, wantBefore) && !reflect.DeepEqual(query, wantAfter) {
			t.Fatal("Fireworks did not use UTC day start-29/end+1 billing window")
		}
		return
	}
	start, e1 := time.Parse("2006-01-02T15:04:05.000Z", query.Get("start_date"))
	end, e2 := time.Parse("2006-01-02T15:04:05.000Z", query.Get("end_date"))
	if e1 != nil || e2 != nil || len(query) != 2 || end.Sub(start) != 30*24*time.Hour || end.UnixMilli() < before.UnixMilli() || end.UnixMilli() > after.UnixMilli() {
		t.Fatal("Baseten did not use rolling 30x24h UTC millisecond billing window")
	}
}

func TestBillingClientMatchesRepresentativeSourceReports(t *testing.T) {
	for _, name := range []string{"baseten:exact-decimals", "baseten:numbers", "baseten:missing", "openrouter:limit-and-spend", "openrouter:uncapped", "openrouter:unknown-cap", "openrouter:unknown-remaining", "openrouter:invalid-optionals", "fireworks:currencies-series-exact", "fireworks:zero-sum-preserves-series", "fireworks:negative-fraction", "fireworks:empty", "fireworks:safe-number"} {
		t.Run(name, func(t *testing.T) {
			f := billingNamed(t, name)
			server := billingHTTPS(t, f.Payload)
			before := time.Now()

			result := billingCall(t, server.client, t.Context(), f.Provider, billingAuth(f.Provider), time.Second)

			requireBillingReport(t, result.report, result.err, f, before, time.Now())
			requireBillingOrigin(t, server.observations(), f.Provider)
			requireBillingWindow(t, server.observations()[0], before, time.Now())
		})
	}
}

func TestBillingClientRefusesMalformedSourcePayloads(t *testing.T) {
	for _, name := range []string{"baseten:bad-object", "baseten:negative", "baseten:missing-total", "openrouter:missing-data", "openrouter:no-displayable", "fireworks:bad-lines", "fireworks:null-units", "fireworks:null-nanos", "fireworks:int64-overflow", "fireworks:mixed-signs"} {
		t.Run(name, func(t *testing.T) {
			f := billingNamed(t, name)
			billingGood(t, f.Provider)
			var requests atomic.Int32
			client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); writeQueryFixture(t, w, f.Payload) })

			result := billingCall(t, client, t.Context(), f.Provider, billingAuth(f.Provider), time.Second)

			requireQueryRefused(t, result.report, result.err)
			requireSafeDiagnostic(t, result.err)
			requireRequestCount(t, requests.Load(), 1)
		})
	}
}

func billingPageScenario(t testing.TB, tokens []string) {
	t.Helper()
	var mu sync.Mutex
	var wire []billingWire
	var wantTargets []billingTarget
	var wantWire []billingWire
	for i := range len(tokens) + 1 {
		id := fmt.Sprintf("account-%d", i)
		wantTargets = append(wantTargets, billingTarget{id, id})
		q := url.Values{"pageSize": {"200"}}
		if i > 0 {
			q.Set("pageToken", tokens[i-1])
		}
		wantWire = append(wantWire, billingWire{"GET", "api.fireworks.ai", "/v1/accounts", "Bearer fixture-secret", q})
	}
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		i := len(wire)
		wire = append(wire, billingWire{r.Method, r.Host, r.URL.Path, r.Header.Get("Authorization"), r.URL.Query()})
		if i > len(tokens) {
			t.Error("unexpected extra discovery page")
			w.WriteHeader(500)
			return
		}
		response := map[string]any{"accounts": []any{map[string]any{"name": fmt.Sprintf("accounts/account-%d", i)}}}
		if i < len(tokens) {
			response["nextPageToken"] = tokens[i]
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Error(err)
			return
		}
		writeQueryFixture(t, w, encoded)
	})
	result := billingCall(t, client, t.Context(), "targets", billingAuth("fireworks"), time.Second)
	requireBillingTargets(t, result.targets, result.err, wantTargets)
	mu.Lock()
	defer mu.Unlock()
	requireBillingWires(t, wire, wantWire)
}

func TestBillingClientDiscoversDistinctPagedTargets(t *testing.T) {
	for _, tt := range []struct {
		name   string
		tokens []string
	}{
		{"single page", nil},
		{"opaque query and utf16 bound", []string{"a+b&next=/account?x=1#fragment", strings.Repeat("😀", 256)}},
		{"five pages", []string{"p1", "p2", "p3", "p4"}},
	} {
		t.Run(tt.name, func(t *testing.T) { billingPageScenario(t, tt.tokens) })
	}
}

func billingSelectedScenario(t testing.TB, selected string) {
	t.Helper()
	f := billingNamed(t, "fireworks:currencies-series-exact")
	f.Expected["accountLabel"] = selected
	server := billingHTTPS(t, f.Payload)
	before := time.Now()
	result := billingTargetQuery(t, server.client, t.Context(), "fireworks", billingAuth("fireworks"), selected, time.Second)
	requireBillingReport(t, result.report, result.err, f, before, time.Now())
	wire := server.observations()
	requireBillingOrigin(t, wire, "fireworks")
	requireBillingWindow(t, wire[0], before, time.Now())
	query := wire[0].Query
	requireBillingWires(t, wire, []billingWire{{"GET", "api.fireworks.ai", "/v1/accounts/" + selected + "/billing/summary", "Bearer fixture-secret", query}})
}

func TestBillingClientQueriesOnlyExplicitTarget(t *testing.T) {
	for _, selected := range []string{"synthetic-account", "different-org", "Two._~-2"} {
		t.Run(selected, func(t *testing.T) { billingSelectedScenario(t, selected) })
	}
}

func TestBillingClientRejectsMissingUnsafeOrForeignTargets(t *testing.T) {
	for _, tt := range []struct{ name, provider, target string }{
		{"empty", "fireworks", ""}, {"resource instead of slug", "fireworks", "accounts/synthetic-account"}, {"path traversal", "fireworks", "../other"}, {"query injection", "fireworks", "synthetic-account?x=1"}, {"encoded slash", "fireworks", "one%2Fother"}, {"terminal", "fireworks", "account-canary-89342\x1b[31m"}, {"other provider", "openrouter", "synthetic-account"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			billingGood(t, "fireworks")
			var requests atomic.Int32
			client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				writeQueryFixture(t, w, billingBody(t, "fireworks"))
			})

			result := billingTargetQuery(t, client, t.Context(), tt.provider, billingAuth("fireworks"), tt.target, time.Second)

			requireQueryRefused(t, result.report, result.err)
			requireSafeDiagnostic(t, result.err)
			requireRequestCount(t, requests.Load(), 0)
		})
	}
}

func TestBillingClientDoesNotImplicitlyDiscoverTargets(t *testing.T) {
	billingGood(t, "fireworks")
	billingDiscoveryGood(t)
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, billingBody(t, "targets"))
	})

	report, err := client.Query(t.Context(), "fireworks", billingAuth("fireworks"), time.Second)
	foreign := billingList(t, client, t.Context(), "openrouter", billingAuth("openrouter"), time.Second)

	requireQueryRefused(t, report, err)
	requireSafeDiagnostic(t, err)
	requireBillingTargetRefusal(t, foreign.targets, foreign.err)
	requireSafeDiagnostic(t, foreign.err)
	requireRequestCount(t, requests.Load(), 0)
}

func billingBadPages(t testing.TB, pages []string, wantRequests int32) {
	t.Helper()
	billingDiscoveryGood(t)
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		index := int(requests.Add(1)) - 1
		if index >= len(pages) {
			t.Error("exceeded discovery page bound")
			w.WriteHeader(500)
			return
		}
		writeQueryFixture(t, w, []byte(pages[index]))
	})
	result := billingCall(t, client, t.Context(), "targets", billingAuth("fireworks"), time.Second)
	requireBillingTargetRefusal(t, result.targets, result.err)
	requireSafeDiagnostic(t, result.err)
	requireRequestCount(t, requests.Load(), wantRequests)
}

func TestBillingClientRejectsUnsafeDiscoveryAtomically(t *testing.T) {
	for _, tt := range []struct {
		name     string
		pages    []string
		requests int32
	}{
		{"missing accounts", []string{`{}`}, 1},
		{"wrong accounts type", []string{`{"accounts":{}}`}, 1},
		{"unsafe resource", []string{`{"accounts":[{"name":"accounts/../account-canary-89342"}]}`}, 1},
		{"wrong prefix", []string{`{"accounts":[{"name":"other/account-canary-89342"}]}`}, 1},
		{"duplicate within page", []string{`{"accounts":[{"name":"accounts/account-canary-89342"},{"name":"accounts/account-canary-89342"}]}`}, 1},
		{"duplicate across pages", []string{`{"accounts":[{"name":"accounts/account-canary-89342"}],"nextPageToken":"p2"}`, `{"accounts":[{"name":"accounts/account-canary-89342"}]}`}, 2},
		{"invalid token type", []string{`{"accounts":[{"name":"accounts/one"}],"nextPageToken":true}`}, 1},
		{"token over utf16 bound", []string{`{"accounts":[{"name":"accounts/one"}],"nextPageToken":"` + strings.Repeat("😀", 257) + `"}`}, 1},
		{"sixth page refused", []string{`{"accounts":[],"nextPageToken":"p2"}`, `{"accounts":[],"nextPageToken":"p3"}`, `{"accounts":[],"nextPageToken":"p4"}`, `{"accounts":[],"nextPageToken":"p5"}`, `{"accounts":[],"nextPageToken":"p6"}`}, 5},
	} {
		t.Run(tt.name, func(t *testing.T) { billingBadPages(t, tt.pages, tt.requests) })
	}
}

func billingSurfaceGood(t testing.TB, surface string) {
	t.Helper()
	if surface == "targets" {
		billingDiscoveryGood(t)
		return
	}
	billingGood(t, surface)
}

func TestBillingClientGuardsOfficialOriginsAndAuthorization(t *testing.T) {
	for _, surface := range []string{"baseten", "openrouter", "fireworks", "targets"} {
		t.Run(surface, func(t *testing.T) {
			for _, tt := range []struct {
				name   string
				change func(*ResolvedAuth)
			}{
				{"http", func(a *ResolvedAuth) { a.ModelBaseURL = strings.Replace(a.ModelBaseURL, "https:", "http:", 1) }},
				{"lookalike origin", func(a *ResolvedAuth) { a.ModelBaseURL = "https://api.fireworks.ai.attacker.invalid/v1" }},
				{"userinfo", func(a *ResolvedAuth) {
					a.ModelBaseURL = strings.Replace(a.ModelBaseURL, "https://", "https://fixture-secret@", 1)
				}},
				{"missing authorization", func(a *ResolvedAuth) { a.Headers = nil }},
				{"duplicate authorization", func(a *ResolvedAuth) { a.Headers["authorization"] = []string{"Bearer account-canary-89342"} }},
				{"terminal authorization", func(a *ResolvedAuth) { a.Headers.Set("Authorization", "Bearer fixture-secret\x1b[31m") }},
			} {
				t.Run(tt.name, func(t *testing.T) {
					billingSurfaceGood(t, surface)
					var requests atomic.Int32
					client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
						requests.Add(1)
						writeQueryFixture(t, w, billingBody(t, surface))
					})
					auth := billingAuth(billingProvider(surface))
					tt.change(&auth)

					result := billingCall(t, client, t.Context(), surface, auth, time.Second)

					requireBillingRefusal(t, result)
					requireSafeDiagnostic(t, result.err)
					requireRequestCount(t, requests.Load(), 0)
				})
			}
		})
	}
}

func TestBillingClientUsesBoundedRedirectRefusingTransport(t *testing.T) {
	for _, surface := range []string{"baseten", "openrouter", "fireworks", "targets"} {
		t.Run(surface, func(t *testing.T) {
			for _, tt := range []struct {
				name   string
				status int
				body   string
			}{
				{"redirect", 302, "raw-provider-canary fixture-secret account-canary-89342\x1b[31m"},
				{"HTTP failure", 503, "raw-provider-canary fixture-secret account-canary-89342\x1b[31m"},
				{"body overflow", 200, string(billingBody(t, surface)) + strings.Repeat(" ", 65537-len(billingBody(t, surface)))},
				{"malformed body", 200, "raw-provider-canary fixture-secret account-canary-89342\x1b[31m"},
			} {
				t.Run(tt.name, func(t *testing.T) {
					billingSurfaceGood(t, surface)
					var requests atomic.Int32
					client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
						requests.Add(1)
						w.Header().Set("Location", "https://canary.invalid/leak?fixture-secret")
						w.WriteHeader(tt.status)
						writeQueryFixture(t, w, []byte(tt.body))
					})

					result := billingCall(t, client, t.Context(), surface, billingAuth(billingProvider(surface)), time.Second)

					requireBillingRefusal(t, result)
					requireSafeDiagnostic(t, result.err)
					requireRequestCount(t, requests.Load(), 1)
				})
			}
			t.Run("exact body bound", func(t *testing.T) {
				body := billingBody(t, surface)
				client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
					writeQueryFixture(t, w, append(body, []byte(strings.Repeat(" ", 65536-len(body)))...))
				})

				result := billingCall(t, client, t.Context(), surface, billingAuth(billingProvider(surface)), time.Second)

				requireBillingSuccess(t, result)
			})
		})
	}
}

func billingFenceScenario(t testing.TB, surface string, failAt int32, wantRequests int32) {
	t.Helper()
	billingSurfaceGood(t, surface)
	var requests, validations atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, billingBody(t, surface))
	})
	auth := billingAuth(billingProvider(surface))
	auth.Revalidate = func(context.Context) error {
		if validations.Add(1) == failAt {
			return errors.New("fixture-secret account-canary-89342 raw-provider-canary\x1b[31m")
		}
		return nil
	}
	if failAt == -1 {
		auth.Revalidate = nil
	}
	result := billingCall(t, client, t.Context(), surface, auth, time.Second)
	requireBillingRefusal(t, result)
	requireSafeDiagnostic(t, result.err)
	requireRequestCount(t, requests.Load(), wantRequests)
}

func TestBillingClientRevalidatesBeforeRequestsAndResults(t *testing.T) {
	for _, surface := range []string{"baseten", "openrouter", "fireworks", "targets"} {
		t.Run(surface, func(t *testing.T) {
			for _, tt := range []struct {
				name                 string
				failAt, wantRequests int32
			}{{"missing guard", -1, 0}, {"before request", 1, 0}, {"before result", 2, 1}} {
				t.Run(tt.name, func(t *testing.T) { billingFenceScenario(t, surface, tt.failAt, tt.wantRequests) })
			}
		})
	}
}

func TestBillingClientRevalidatesBetweenDiscoveryPages(t *testing.T) {
	billingDiscoveryGood(t)
	var requests atomic.Int32
	var changed atomic.Bool
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		changed.Store(true)
		writeQueryFixture(t, w, []byte(`{"accounts":[{"name":"accounts/one"}],"nextPageToken":"second"}`))
	})
	auth := billingAuth("fireworks")
	auth.Revalidate = func(context.Context) error {
		if changed.Load() {
			return errors.New("fixture-secret account-canary-89342")
		}
		return nil
	}

	result := billingCall(t, client, t.Context(), "targets", auth, time.Second)

	requireBillingRefusal(t, result)
	requireSafeDiagnostic(t, result.err)
	requireRequestCount(t, requests.Load(), 1)
}

func TestBillingClientCopiesAuthorizationSnapshot(t *testing.T) {
	for _, surface := range []string{"baseten", "openrouter", "fireworks", "targets"} {
		t.Run(surface, func(t *testing.T) {
			observed := make(chan string, 1)
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				observed <- r.Header.Get("Authorization")
				writeQueryFixture(t, w, billingBody(t, surface))
			})
			auth := billingAuth(billingProvider(surface))
			auth.Revalidate = func(context.Context) error {
				auth.Headers.Set("Authorization", "Bearer account-canary-89342")
				return nil
			}

			result := billingCall(t, client, t.Context(), surface, auth, time.Second)

			requireBillingSuccess(t, result)
			requireString(t, <-observed, "Bearer fixture-secret")
		})
	}
}

func TestBillingClientDeadlineCoversAuthAndResponse(t *testing.T) {
	for _, surface := range []string{"baseten", "openrouter", "fireworks", "targets"} {
		t.Run(surface, func(t *testing.T) {
			for _, phase := range []string{"authentication", "response"} {
				t.Run(phase, func(t *testing.T) { billingDeadlineScenario(t, surface, phase) })
			}
		})
	}
}

func billingDeadlineScenario(t testing.TB, surface, phase string) {
	t.Helper()
	billingSurfaceGood(t, surface)
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	auth := billingAuth(billingProvider(surface))
	wantRequests := int32(1)
	if phase == "authentication" {
		wantRequests = 0
		auth.Revalidate = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	}
	result := billingCall(t, client, t.Context(), surface, auth, 50*time.Millisecond)
	requireBillingRefusal(t, result)
	requireDeadline(t, result.err)
	requireRequestCount(t, requests.Load(), wantRequests)
}

func requireBillingSameDeadline(t testing.TB, deadlines []time.Time) {
	t.Helper()
	if len(deadlines) < 2 {
		t.Fatal("pagination did not propagate bounded contexts")
	}
	for _, deadline := range deadlines {
		if deadline.IsZero() || !deadline.Equal(deadlines[0]) {
			t.Fatal("pagination renewed or dropped the total query deadline")
		}
	}
}

func TestBillingClientPaginationSharesDeadline(t *testing.T) {
	billingDiscoveryGood(t)
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			writeQueryFixture(t, w, []byte(`{"accounts":[{"name":"accounts/one"}],"nextPageToken":"second"}`))
			return
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	auth := billingAuth("fireworks")
	var mu sync.Mutex
	var deadlines []time.Time
	auth.Revalidate = func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		mu.Lock()
		deadlines = append(deadlines, deadline)
		mu.Unlock()
		return ctx.Err()
	}

	result := billingCall(t, client, t.Context(), "targets", auth, 100*time.Millisecond)

	requireBillingRefusal(t, result)
	requireDeadline(t, result.err)
	requireRequestCount(t, requests.Load(), 2)
	mu.Lock()
	requireBillingSameDeadline(t, deadlines)
	mu.Unlock()
}

func requireBillingCanceled(t testing.TB, err error) {
	t.Helper()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
}

func billingCancellationScenario(t testing.TB, surface string) {
	t.Helper()
	billingSurfaceGood(t, surface)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
			writeQueryFixture(t, w, billingBody(t, surface))
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan billingResult, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		result <- billingCall(t, client, ctx, surface, billingAuth(billingProvider(surface)), time.Second)
	})
	t.Cleanup(func() { cancel(); workers.Wait() })
	awaitQueryEvent(t, started)
	cancel()
	got := <-result
	requireBillingRefusal(t, got)
	requireBillingCanceled(t, got.err)
}

func TestBillingClientCancellationStopsHTTPS(t *testing.T) {
	for _, surface := range []string{"baseten", "openrouter", "fireworks", "targets"} {
		t.Run(surface, func(t *testing.T) { billingCancellationScenario(t, surface) })
	}
}

func TestBillingClientSharesBoundedAdmission(t *testing.T) {
	for _, surface := range []string{"baseten", "openrouter", "fireworks", "targets"} {
		t.Run(surface, func(t *testing.T) {
			stop := occupyQuerySlots(t)
			var requests atomic.Int32
			client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				writeQueryFixture(t, w, billingBody(t, surface))
			})

			result := billingCall(t, client, t.Context(), surface, billingAuth(billingProvider(surface)), 40*time.Millisecond)
			observedRequests := requests.Load()
			stop()
			recovered := billingCall(t, client, t.Context(), surface, billingAuth(billingProvider(surface)), time.Second)

			requireBillingRefusal(t, result)
			requireDeadline(t, result.err)
			requireRequestCount(t, observedRequests, 0)
			requireBillingSuccess(t, recovered)
		})
	}
}
