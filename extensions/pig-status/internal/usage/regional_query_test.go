package usage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type regionalQueryFixture struct {
	Name          string          `json:"name"`
	Provider      string          `json:"provider"`
	Base          string          `json:"base"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"payload"`
	Plan          json.RawMessage `json:"plan"`
	Expected      map[string]any  `json:"expected"`
	Error         string          `json:"error"`
	Status        int             `json:"status"`
	Raw           string          `json:"raw"`
	RequestPaths  []string        `json:"requestPaths"`
	NativeRefusal bool            `json:"nativeRefusal"`
}

func regionalFixtures(t testing.TB, selectFixture func(regionalQueryFixture) bool) []regionalQueryFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/regional-query-acceptance.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Fixtures []regionalQueryFixture `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	var selected []regionalQueryFixture
	for _, f := range document.Fixtures {
		if selectFixture(f) {
			selected = append(selected, f)
		}
	}
	if len(selected) == 0 {
		t.Fatal("independent source fixture selection was empty")
	}
	return selected
}

func regionalNamed(t testing.TB, name string) regionalQueryFixture {
	t.Helper()
	return regionalFixtures(t, func(f regionalQueryFixture) bool { return f.Name == name })[0]
}

func regionalSurfaces(t testing.TB) []regionalQueryFixture {
	t.Helper()
	return regionalFixtures(t, func(f regionalQueryFixture) bool {
		return strings.HasSuffix(f.Name, ":balance") || strings.HasSuffix(f.Name, ":count-directions") || strings.HasSuffix(f.Name, ":quota")
	})
}

func regionalOptionalField(auth *ResolvedAuth, name string, value any) {
	field := reflect.ValueOf(auth).Elem().FieldByName(name)
	if field.IsValid() {
		field.Set(reflect.ValueOf(value))
	}
}

func regionalAuth(f regionalQueryFixture) ResolvedAuth {
	auth := deepSeekAuth()
	auth.ModelBaseURL = f.Base
	key := "token-plan-fixture"
	if f.Kind == "account-balance" {
		key = "sk-api-fixture"
	}
	if f.Kind == "quota" || f.Kind == "error" {
		key = "fixture-secret"
	}
	auth.Headers.Set("Authorization", "Bearer "+key)
	regionalOptionalField(&auth, "APIKey", "opposite-kind-snapshot")
	return auth
}

type regionalWire struct{ Method, Host, Path, Query, Auth, Agent string }
type regionalServer struct {
	client   Client
	mu       sync.Mutex
	observed []regionalWire
}

func regionalHTTPS(t testing.TB, f regionalQueryFixture, status int, raw string) *regionalServer {
	t.Helper()
	s := new(regionalServer)
	s.client = fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.observed = append(s.observed, regionalWire{r.Method, r.Host, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.UserAgent()})
		s.mu.Unlock()
		if r.URL.Path == "/api/biz/subscription/list" {
			body := f.Plan
			if len(body) == 0 {
				body = []byte(`{"data":[]}`)
			}
			writeQueryFixture(t, w, body)
			return
		}
		if status != 0 {
			w.Header().Set("Location", "https://canary.invalid/leak?fixture-secret")
			w.WriteHeader(status)
			writeQueryFixture(t, w, []byte(raw))
			return
		}
		writeQueryFixture(t, w, f.Payload)
	})
	return s
}

func (s *regionalServer) wires() []regionalWire {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]regionalWire(nil), s.observed...)
}

func (s *regionalServer) count() int32 { return int32(len(s.wires())) }

func regionalWantWires(f regionalQueryFixture) []regionalWire {
	host := strings.TrimPrefix(strings.TrimSuffix(f.Base, "/v1"), "https://")
	path, authorization := "/v1/token_plan/remains", "Bearer token-plan-fixture"
	if f.Kind == "account-balance" {
		path, authorization = "/account/query_balance", "Bearer sk-api-fixture"
	}
	if strings.HasPrefix(f.Provider, "zai") {
		return []regionalWire{{"GET", host, "/api/monitor/usage/quota/limit", "", "fixture-secret", "pi-usage"}, {"GET", host, "/api/biz/subscription/list", "", "fixture-secret", "pi-usage"}}
	}
	return []regionalWire{{"GET", host, path, "", authorization, "pi-usage"}}
}

func requireRegionalWires(t testing.TB, got, want []regionalWire) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("regional requests = %#v, want %#v", got, want)
	}
}

func requireRegionalCaptured(t testing.TB, report Report, before, after time.Time) {
	t.Helper()
	if report.CapturedAt < before.UnixMilli() || report.CapturedAt > after.UnixMilli() {
		t.Fatal("capture timestamp outside query interval")
	}
}

func regionalGood(t testing.TB, provider string) {
	t.Helper()
	name := provider + ":balance"
	if strings.HasPrefix(provider, "zai") {
		name = provider + ":quota"
	}
	f := regionalNamed(t, name)
	s := regionalHTTPS(t, f, 0, "")
	report, err := s.client.Query(t.Context(), provider, regionalAuth(f), time.Second)
	requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
}

func TestRegionalQuery_ACParityMatchesActualSource(t *testing.T) {
	for _, f := range regionalFixtures(t, func(f regionalQueryFixture) bool {
		return f.Expected != nil && !strings.HasPrefix(f.Provider, "moonshot")
	}) {
		t.Run(f.Name, func(t *testing.T) {
			server := regionalHTTPS(t, f, 0, "")
			before := time.Now()

			report, err := server.client.Query(t.Context(), f.Provider, regionalAuth(f), time.Second)

			requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
			requireRegionalWires(t, server.wires(), regionalWantWires(f))
			requireRegionalCaptured(t, report, before, time.Now())
		})
	}
}

func TestRegionalQuery_ACMalformedSourceInputsRefuse(t *testing.T) {
	for _, f := range regionalFixtures(t, func(f regionalQueryFixture) bool { return f.Error != "" && f.Kind != "error" }) {
		t.Run(f.Name, func(t *testing.T) {
			regionalGood(t, f.Provider)
			server := regionalHTTPS(t, f, 0, "")

			report, err := server.client.Query(t.Context(), f.Provider, regionalAuth(f), time.Second)

			requireQueryRefused(t, report, err)
			requireSafeDiagnostic(t, err)
			requireRegionalWires(t, server.wires(), regionalRefusalWires(t, f))
		})
	}
}

func TestRegionalQuery_SafeZaiFiniteDurationOverflowRefuses(t *testing.T) {
	for _, f := range regionalFixtures(t, func(f regionalQueryFixture) bool { return f.NativeRefusal }) {
		t.Run(f.Name, func(t *testing.T) {
			regionalGood(t, f.Provider)
			server := regionalHTTPS(t, f, 0, "")

			report, err := server.client.Query(t.Context(), f.Provider, regionalAuth(f), time.Second)

			requireQueryRefused(t, report, err)
			requireSafeDiagnostic(t, err)
			requireRegionalWires(t, server.wires(), regionalRefusalWires(t, f))
		})
	}
}

func regionalRefusalWires(t testing.TB, f regionalQueryFixture) []regionalWire {
	t.Helper()
	want := regionalWantWires(f)
	if !strings.HasPrefix(f.Provider, "zai") {
		return want
	}
	if len(f.RequestPaths) < 1 || len(f.RequestPaths) > len(want) {
		t.Fatal("source query request sequence outside required/optional boundary")
	}
	for i, path := range f.RequestPaths {
		if path != want[i].Path {
			t.Fatal("source query request sequence differs from official quota/plan order")
		}
	}
	return want[:len(f.RequestPaths)]
}

func requireRegionalError(t testing.TB, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("fixed Zai error = %v, want %q", err, want)
	}
}

func TestRegionalQuery_ACFixedZaiErrorsMatchActualSource(t *testing.T) {
	for _, f := range regionalFixtures(t, func(f regionalQueryFixture) bool { return f.Kind == "error" }) {
		t.Run(f.Name, func(t *testing.T) {
			regionalGood(t, f.Provider)
			server := regionalHTTPS(t, f, f.Status, f.Raw)

			report, err := server.client.Query(t.Context(), f.Provider, regionalAuth(f), time.Second)

			requireQueryRefused(t, report, err)
			requireSafeDiagnostic(t, err)
			requireRegionalError(t, err, f.Error)
			requireRequestCount(t, server.count(), 1)
		})
	}
}

func TestRegionalQuery_ACOptionalZaiFailuresPreserveQuota(t *testing.T) {
	for _, provider := range []string{"zai", "zai-coding-cn"} {
		for _, tt := range []struct {
			name      string
			status    int
			body      string
			transport bool
		}{
			{"http", 503, "raw-provider-canary fixture-secret", false},
			{"json", 200, "raw-provider-canary fixture-secret", false},
			{"payload", 200, `{"error":{"code":1308,"message":"fixture-secret"}}`, false},
			{"transport", 0, "", true},
			{"overflow", 200, strings.Repeat(" ", 65537), false},
		} {
			t.Run(provider+"/"+tt.name, func(t *testing.T) {
				f := regionalNamed(t, provider+":quota")
				var requests atomic.Int32
				client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/biz/subscription/list" {
						w.WriteHeader(tt.status)
						writeQueryFixture(t, w, []byte(tt.body))
						return
					}
					writeQueryFixture(t, w, f.Payload)
				})
				transport := client.Transport
				client.Transport = queryRoundTripper(func(r *http.Request) (*http.Response, error) {
					requests.Add(1)
					if tt.transport && r.URL.Path == "/api/biz/subscription/list" {
						return nil, errors.New("fixture-secret raw-provider-canary\x1b[31m")
					}
					return transport.RoundTrip(r)
				})

				report, err := client.Query(t.Context(), provider, regionalAuth(f), time.Second)

				requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
				requireRequestCount(t, requests.Load(), 2)
			})
		}
	}
}

func TestRegionalQuery_ACOriginAndAuthorizationIsolation(t *testing.T) {
	for _, f := range regionalSurfaces(t) {
		for _, tt := range []struct {
			name   string
			change func(*ResolvedAuth)
		}{
			{"sibling", func(a *ResolvedAuth) {
				a.ModelBaseURL = map[string]string{"minimax": "https://api.minimaxi.com/v1", "minimax-cn": "https://api.minimax.io/v1", "zai": "https://open.bigmodel.cn/v1", "zai-coding-cn": "https://api.z.ai/v1"}[f.Provider]
			}},
			{"http", func(a *ResolvedAuth) { a.ModelBaseURL = strings.Replace(a.ModelBaseURL, "https:", "http:", 1) }},
			{"lookalike", func(a *ResolvedAuth) { a.ModelBaseURL = "https://api.minimax.io.attacker.invalid/v1" }},
			{"userinfo", func(a *ResolvedAuth) {
				a.ModelBaseURL = strings.Replace(a.ModelBaseURL, "https://", "https://fixture-secret@", 1)
			}},
			{"foreign-port", func(a *ResolvedAuth) { a.ModelBaseURL = strings.Replace(a.ModelBaseURL, "/v1", ":444/v1", 1) }},
			{"missing-auth", func(a *ResolvedAuth) { a.Headers = nil }},
			{"duplicate-auth", func(a *ResolvedAuth) { a.Headers["authorization"] = []string{"Bearer fixture-secret"} }},
			{"empty-auth", func(a *ResolvedAuth) { a.Headers.Set("Authorization", "") }},
			{"control-auth", func(a *ResolvedAuth) { a.Headers.Set("Authorization", "Bearer fixture-secret\x1b[31m") }},
		} {
			t.Run(f.Name+"/"+tt.name, func(t *testing.T) {
				regionalGood(t, f.Provider)
				server := regionalHTTPS(t, f, 0, "")
				auth := regionalAuth(f)
				tt.change(&auth)

				report, err := server.client.Query(t.Context(), f.Provider, auth, time.Second)

				requireQueryRefused(t, report, err)
				requireSafeDiagnostic(t, err)
				requireRequestCount(t, server.count(), 0)
			})
		}
	}
}

func requireRegionalProvenance(t testing.TB, report Report, err error, requests int32, allowed bool, f regionalQueryFixture) {
	t.Helper()
	if allowed {
		requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
		requireRequestCount(t, requests, 1)
		return
	}
	requireQueryRefused(t, report, err)
	requireSafeDiagnostic(t, err)
	requireRequestCount(t, requests, 0)
}

func TestRegionalQuery_ACMoonshotSiblingProvenance(t *testing.T) {
	for _, f := range regionalFixtures(t, func(f regionalQueryFixture) bool { return strings.HasPrefix(f.Provider, "moonshot") }) {
		for _, tt := range []struct {
			name, selected, source string
			label                  *string
			allowed                bool
		}{
			{"pre-resolved", "", "", nil, true},
			{"current", f.Provider, "environment", nil, true},
			{"unknown-sibling-source", "other", "", nil, false},
			{"stored-sibling", "other", "auth-storage", nil, true},
			{"environment-missing-label", "other", "environment", nil, false},
			{"environment-empty-label", "other", "environment", new(""), true},
			{"shared-exact", "other", "environment", new("MOONSHOT_API_KEY"), false},
			{"shared-in-list", "other", "environment", new("REGIONAL_KEY, MOONSHOT_API_KEY ,OTHER"), false},
			{"regional-only", "other", "environment", new("MOONSHOT_CN_API_KEY"), true},
			{"substring-not-exact", "other", "environment", new("MY_MOONSHOT_API_KEY"), true},
			{"case-sensitive", "other", "environment", new("moonshot_api_key"), true},
		} {
			t.Run(f.Provider+"/"+tt.name, func(t *testing.T) {
				control := regionalHTTPS(t, f, 0, "")
				server := regionalHTTPS(t, f, 0, "")
				auth := regionalAuth(f)
				regionalOptionalField(&auth, "SelectedProviderID", tt.selected)
				regionalOptionalField(&auth, "Source", tt.source)
				regionalOptionalField(&auth, "SourceLabel", tt.label)

				good, goodErr := control.client.Query(t.Context(), f.Provider, regionalAuth(f), time.Second)
				report, err := server.client.Query(t.Context(), f.Provider, auth, time.Second)

				requirePinnedQueryReport(t, good, goodErr, providerQueryFixture{provider: f.Provider, expected: f.Expected})
				requireRegionalProvenance(t, report, err, server.count(), tt.allowed, f)
			})
		}
	}
}

func TestRegionalQuery_SafeTransportRefusesUnsafeResponses(t *testing.T) {
	for _, f := range regionalSurfaces(t) {
		for _, tt := range []struct {
			name   string
			status int
			body   string
		}{
			{"redirect", 302, "fixture-secret raw-provider-canary\x1b[31m"},
			{"http", 503, "fixture-secret raw-provider-canary\x1b[31m"},
			{"malformed", 200, "fixture-secret raw-provider-canary\x1b[31m"},
			{"overflow", 200, string(f.Payload) + strings.Repeat(" ", 65537-len(f.Payload))},
		} {
			t.Run(f.Name+"/"+tt.name, func(t *testing.T) {
				regionalGood(t, f.Provider)
				server := regionalHTTPS(t, f, tt.status, tt.body)

				report, err := server.client.Query(t.Context(), f.Provider, regionalAuth(f), time.Second)

				requireQueryRefused(t, report, err)
				requireSafeDiagnostic(t, err)
				requireRequestCount(t, server.count(), 1)
			})
		}
	}
}

func TestRegionalQuery_SafeTransportAcceptsExactBodyLimit(t *testing.T) {
	for _, f := range regionalSurfaces(t) {
		t.Run(f.Name, func(t *testing.T) {
			server := regionalHTTPS(t, f, 200, string(f.Payload)+strings.Repeat(" ", 65536-len(f.Payload)))

			good, goodErr := server.client.Query(t.Context(), f.Provider, regionalAuth(f), time.Second)

			requirePinnedQueryReport(t, good, goodErr, providerQueryFixture{provider: f.Provider, expected: f.Expected})
		})
	}
}

func regionalFence(t testing.TB, f regionalQueryFixture, phase string) {
	t.Helper()
	regionalGood(t, f.Provider)
	server := regionalHTTPS(t, f, 0, "")
	auth := regionalAuth(f)
	var checks int32
	wantRequests := int32(0)
	if phase == "missing" {
		auth.Revalidate = nil
	} else {
		if phase == "publication" {
			wantRequests = 1
		}
		if phase == "publication" && strings.HasPrefix(f.Provider, "zai") {
			wantRequests = 2
		}
		auth.Revalidate = func(context.Context) error {
			checks++
			if (phase == "before" && checks == 1) || (phase == "publication" && server.count() == wantRequests) {
				return errors.New("fixture-secret raw-provider-canary\x1b[31m")
			}
			return nil
		}
	}
	report, err := server.client.Query(t.Context(), f.Provider, auth, time.Second)
	requireQueryRefused(t, report, err)
	requireSafeDiagnostic(t, err)
	requireRequestCount(t, server.count(), wantRequests)
}

func TestRegionalQuery_SafeRevalidationFencesIdentity(t *testing.T) {
	for _, f := range regionalSurfaces(t) {
		for _, phase := range []string{"missing", "before", "publication"} {
			t.Run(f.Name+"/"+phase, func(t *testing.T) { regionalFence(t, f, phase) })
		}
	}
}

func TestRegionalQuery_SafeAuthorizationSnapshot(t *testing.T) {
	for _, f := range regionalSurfaces(t) {
		t.Run(f.Name, func(t *testing.T) {
			server := regionalHTTPS(t, f, 0, "")
			auth := regionalAuth(f)
			auth.Revalidate = func(context.Context) error {
				auth.Headers.Set("Authorization", "Bearer account-canary-89342")
				return nil
			}

			report, err := server.client.Query(t.Context(), f.Provider, auth, time.Second)

			requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
			requireRegionalWires(t, server.wires(), regionalWantWires(f))
		})
	}
}

func regionalDeadline(t testing.TB, f regionalQueryFixture, phase string) {
	t.Helper()
	regionalGood(t, f.Provider)
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if phase == "optional" && r.URL.Path != "/api/biz/subscription/list" {
			writeQueryFixture(t, w, f.Payload)
			return
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	auth := regionalAuth(f)
	var deadlines []time.Time
	wantRequests := int32(1)
	if phase == "authentication" {
		wantRequests = 0
	}
	if phase == "optional" {
		wantRequests = 2
	}
	auth.Revalidate = func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		deadlines = append(deadlines, deadline)
		if phase == "authentication" {
			<-ctx.Done()
		}
		return ctx.Err()
	}
	transport := client.Transport
	client.Transport = queryRoundTripper(func(r *http.Request) (*http.Response, error) {
		deadline, _ := r.Context().Deadline()
		deadlines = append(deadlines, deadline)
		return transport.RoundTrip(r)
	})
	report, err := client.Query(t.Context(), f.Provider, auth, 70*time.Millisecond)
	requireQueryRefused(t, report, err)
	requireDeadline(t, err)
	if phase != "authentication" {
		requireBillingSameDeadline(t, deadlines)
	}
	requireRegionalBudget(t, deadlines)
	requireRequestCount(t, requests.Load(), wantRequests)
}

func TestRegionalQuery_SafeDeadlineCoversAuthenticationAndResponses(t *testing.T) {
	for _, f := range regionalSurfaces(t) {
		for _, phase := range []string{"authentication", "response"} {
			t.Run(f.Name+"/"+phase, func(t *testing.T) { regionalDeadline(t, f, phase) })
		}
	}
}

func TestRegionalQuery_SafeDeadlineCoversOptionalZaiWork(t *testing.T) {
	for _, provider := range []string{"zai", "zai-coding-cn"} {
		t.Run(provider, func(t *testing.T) { regionalDeadline(t, regionalNamed(t, provider+":quota"), "optional") })
	}
}

func TestRegionalQuery_SafeSharedAdmissionRecovers(t *testing.T) {
	for _, f := range regionalSurfaces(t) {
		t.Run(f.Name, func(t *testing.T) {
			stop := occupyQuerySlots(t)
			server := regionalHTTPS(t, f, 0, "")

			_, err := server.client.Query(t.Context(), f.Provider, regionalAuth(f), 40*time.Millisecond)
			observed := server.count()
			stop()
			good, goodErr := server.client.Query(t.Context(), f.Provider, regionalAuth(f), time.Second)

			requireDeadline(t, err)
			requireRequestCount(t, observed, 0)
			requirePinnedQueryReport(t, good, goodErr, providerQueryFixture{provider: f.Provider, expected: f.Expected})
		})
	}
}

func requireRegionalBudget(t testing.TB, deadlines []time.Time) {
	t.Helper()
	if len(deadlines) == 0 || deadlines[0].IsZero() || time.Until(deadlines[0]) > 15*time.Second {
		t.Fatal("query did not retain a bounded deadline")
	}
}

func TestRegionalQuery_ACMiniMaxRuntimeKeyRouting(t *testing.T) {
	for _, provider := range []string{"minimax", "minimax-cn"} {
		for _, tt := range []struct{ name, fixture, authorization, key string }{
			{"balance-fallback", "balance", "Custom fixture-secret", "sk-api-fixture"},
			{"plan-fallback", "count-directions", "Custom fixture-secret", "token-plan-fixture"},
			{"balance-override", "balance", "Bearer sk-api-fixture", "token-plan-fixture"},
			{"plan-override", "count-directions", "Bearer token-plan-fixture", "sk-api-fixture"},
		} {
			t.Run(provider+"/"+tt.name, func(t *testing.T) {
				f := regionalNamed(t, provider+":"+tt.fixture)
				server := regionalHTTPS(t, f, 0, "")
				auth := regionalAuth(f)
				auth.Headers.Set("Authorization", tt.authorization)
				regionalOptionalField(&auth, "APIKey", tt.key)
				want := regionalWantWires(f)
				want[0].Auth = tt.authorization

				report, err := server.client.Query(t.Context(), provider, auth, time.Second)

				requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
				requireRegionalWires(t, server.wires(), want)
			})
		}
	}
}

func TestRegionalQuery_ACZaiPreservesNonBearerAuthorization(t *testing.T) {
	for _, provider := range []string{"zai", "zai-coding-cn"} {
		t.Run(provider, func(t *testing.T) {
			f := regionalNamed(t, provider+":quota")
			server := regionalHTTPS(t, f, 0, "")
			auth := regionalAuth(f)
			auth.Headers.Set("Authorization", "Custom fixture-secret")
			want := regionalWantWires(f)
			want[0].Auth, want[1].Auth = "Custom fixture-secret", "Custom fixture-secret"

			report, err := server.client.Query(t.Context(), provider, auth, time.Second)

			requirePinnedQueryReport(t, report, err, providerQueryFixture{provider: f.Provider, expected: f.Expected})
			requireRegionalWires(t, server.wires(), want)
		})
	}
}

func TestRegionalQuery_SafeDependencyFailuresRedactDetails(t *testing.T) {
	for _, f := range regionalSurfaces(t) {
		for _, mode := range []string{"transport", "body"} {
			t.Run(f.Name+"/"+mode, func(t *testing.T) {
				regionalGood(t, f.Provider)
				var requests atomic.Int32
				body := &queryErrorBody{err: errors.New("fixture-secret fixture-custom-secret account-canary-89342 raw-provider-canary\x1b[31m")}
				client := Client{Transport: queryRoundTripper(func(*http.Request) (*http.Response, error) {
					requests.Add(1)
					if mode == "transport" {
						return nil, body.err
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
				})}

				report, err := client.Query(t.Context(), f.Provider, regionalAuth(f), time.Second)

				requireQueryRefused(t, report, err)
				requireSafeDiagnostic(t, err)
				requireRequestCount(t, requests.Load(), 1)
			})
		}
	}
}

func regionalCancel(t testing.TB, f regionalQueryFixture, optional bool) {
	t.Helper()
	server := regionalHTTPS(t, f, 0, "")
	good, goodErr := server.client.Query(t.Context(), f.Provider, regionalAuth(f), time.Second)
	requirePinnedQueryReport(t, good, goodErr, providerQueryFixture{provider: f.Provider, expected: f.Expected})
	started := make(chan struct{}, 1)
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if optional && r.URL.Path != "/api/biz/subscription/list" {
			writeQueryFixture(t, w, f.Payload)
			return
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	result := make(chan providerQueryResult, 1)
	workers.Go(func() {
		report, err := client.Query(ctx, f.Provider, regionalAuth(f), time.Second)
		result <- providerQueryResult{report, err}
	})
	t.Cleanup(func() { cancel(); workers.Wait() })
	awaitQueryEvent(t, started)
	cancel()
	got := awaitProviderQuery(t, result)
	wantRequests := int32(1)
	if optional {
		wantRequests = 2
	}
	requireQueryRefused(t, got.report, got.err)
	requireBillingCanceled(t, got.err)
	requireRequestCount(t, requests.Load(), wantRequests)
}

func TestRegionalQuery_SafeCancellationStopsRegionalHTTPS(t *testing.T) {
	for _, f := range regionalSurfaces(t) {
		t.Run(f.Name, func(t *testing.T) { regionalCancel(t, f, false) })
	}
}

func TestRegionalQuery_SafeCancellationStopsOptionalZaiHTTPS(t *testing.T) {
	for _, provider := range []string{"zai", "zai-coding-cn"} {
		t.Run(provider, func(t *testing.T) { regionalCancel(t, regionalNamed(t, provider+":quota"), true) })
	}
}

func TestRegionalQuery_SafeRevalidationBeforeOptionalZaiRequest(t *testing.T) {
	for _, provider := range []string{"zai", "zai-coding-cn"} {
		t.Run(provider, func(t *testing.T) {
			f := regionalNamed(t, provider+":quota")
			regionalGood(t, f.Provider)
			server := regionalHTTPS(t, f, 0, "")
			auth := regionalAuth(f)
			auth.Revalidate = func(context.Context) error {
				if server.count() == 1 {
					return errors.New("fixture-secret raw-provider-canary")
				}
				return nil
			}

			report, err := server.client.Query(t.Context(), provider, auth, time.Second)

			requireQueryRefused(t, report, err)
			requireSafeDiagnostic(t, err)
			requireRequestCount(t, server.count(), 1)
		})
	}
}

func TestRegionalQuery_SafeZaiErrorBodyBound(t *testing.T) {
	for _, provider := range []string{"zai", "zai-coding-cn"} {
		t.Run(provider, func(t *testing.T) {
			f := regionalNamed(t, provider+":quota")
			regionalGood(t, f.Provider)
			server := regionalHTTPS(t, f, 401, strings.Repeat(" ", 4096)+`{"error":{"code":1317,"message":"fixture-secret"}}`)

			report, err := server.client.Query(t.Context(), provider, regionalAuth(f), time.Second)

			requireQueryRefused(t, report, err)
			requireRegionalError(t, err, "Z.AI HTTP 401: Authentication failed. Check your API key.")
			requireRequestCount(t, server.count(), 1)
		})
	}
}
