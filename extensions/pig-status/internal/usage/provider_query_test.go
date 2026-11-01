package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type providerQueryFixture struct {
	provider      string
	base          string
	authorization string
	body          json.RawMessage
	expected      map[string]any
}

func providerQueryFixtures(t testing.TB) []providerQueryFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Fixtures []struct {
			Name     string          `json:"name"`
			Payload  json.RawMessage `json:"payload"`
			Expected map[string]any  `json:"expected"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	fixtures := []providerQueryFixture{
		{provider: "vercel-ai-gateway", base: "https://ai-gateway.vercel.sh/v1", authorization: "Basic fixture-secret"},
		{provider: "moonshotai", base: "https://api.moonshot.ai/v1", authorization: "Custom fixture-secret"},
		{provider: "moonshotai-cn", base: "https://api.moonshot.cn/v1", authorization: "Custom fixture-secret"},
	}
	for i := range fixtures {
		for _, captured := range reference.Fixtures {
			if captured.Name == fixtures[i].provider+":normal" {
				fixtures[i].body = captured.Payload
				fixtures[i].expected = captured.Expected
			}
		}
		if len(fixtures[i].body) == 0 || len(fixtures[i].expected) == 0 {
			t.Fatalf("missing pinned normal fixture for %s", fixtures[i].provider)
		}
	}
	return fixtures
}

func (f providerQueryFixture) auth() ResolvedAuth {
	auth := deepSeekAuth()
	auth.ModelBaseURL = f.base
	auth.Headers.Set("Authorization", f.authorization)
	return auth
}

func requirePinnedQueryReport(t testing.TB, report Report, err error, fixture providerQueryFixture) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s query failed: %v", fixture.provider, err)
	}
	if report.CapturedAt <= 0 {
		t.Fatal("query returned no capture timestamp")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	got["capturedAt"] = fixture.expected["capturedAt"]
	if !reflect.DeepEqual(got, fixture.expected) {
		t.Fatalf("%s report = %s, want pinned report %v (capture time normalized)", fixture.provider, encoded, fixture.expected)
	}
}

func queryAuthorizationCase(t testing.TB, fixture providerQueryFixture, headers http.Header) {
	t.Helper()
	var requests atomic.Int32
	authorization := make(chan string, 2)
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		authorization <- r.Header.Get("Authorization")
		writeQueryFixture(t, w, fixture.body)
	})
	auth := fixture.auth()
	auth.Headers = headers

	goodReport, goodErr := client.Query(t.Context(), fixture.provider, fixture.auth(), time.Second)
	report, err := client.Query(t.Context(), fixture.provider, auth, time.Second)

	requirePinnedQueryReport(t, goodReport, goodErr, fixture)
	requireString(t, <-authorization, fixture.authorization)
	requireQueryRefused(t, report, err)
	requireRequestCount(t, requests.Load(), 1)
	requireSafeDiagnostic(t, err)
}

func TestQueryRejectsMissingOrAmbiguousProviderAuthorization(t *testing.T) {
	for _, fixture := range providerQueryFixtures(t) {
		t.Run(fixture.provider, func(t *testing.T) {
			for _, tt := range []struct {
				name    string
				headers http.Header
			}{
				{"missing", nil},
				{"removed", http.Header{"Authorization": nil}},
				{"empty", http.Header{"Authorization": {""}}},
				{"whitespace", http.Header{"Authorization": {" \t "}}},
				{"duplicate values", http.Header{"Authorization": {fixture.authorization, "Custom account-canary-89342"}}},
				{"duplicate case", http.Header{"Authorization": {fixture.authorization}, "authorization": {"Custom account-canary-89342"}}},
			} {
				t.Run(tt.name, func(t *testing.T) {
					queryAuthorizationCase(t, fixture, tt.headers)
				})
			}
		})
	}
}

func TestQueryRejectsControlProviderAuthorization(t *testing.T) {
	for _, fixture := range providerQueryFixtures(t) {
		t.Run(fixture.provider, func(t *testing.T) {
			for _, tt := range []struct {
				name  string
				value string
			}{
				{"header injection", "Custom fixture-secret\r\nX-Injected: account-canary-89342"},
				{"terminal control", "Custom fixture-secret\x1b[31m"},
			} {
				t.Run(tt.name, func(t *testing.T) {
					queryAuthorizationCase(t, fixture, http.Header{"Authorization": {tt.value}})
				})
			}
		})
	}
}

type providerQueryResult struct {
	report Report
	err    error
}

func startProviderQuery(t testing.TB, client Client, provider string, auth ResolvedAuth) <-chan providerQueryResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	result := make(chan providerQueryResult, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		report, err := client.Query(ctx, provider, auth, 5*time.Second)
		result <- providerQueryResult{report: report, err: err}
	})
	t.Cleanup(func() { cancel(); workers.Wait() })
	return result
}

func awaitQueryEvent(t testing.TB, event <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case <-event:
	case <-ctx.Done():
		t.Fatal("expected HTTPS admission did not occur")
	}
}

func awaitProviderQuery(t testing.TB, result <-chan providerQueryResult) providerQueryResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		t.Fatal("owned query did not settle")
		return providerQueryResult{}
	}
}

func writeQueryFixture(t testing.TB, w http.ResponseWriter, body []byte) {
	t.Helper()
	if _, err := io.WriteString(w, string(body)); err != nil {
		t.Errorf("write local response: %v", err)
	}
}

func heldQueryFixture(t testing.TB, w http.ResponseWriter, r *http.Request, release <-chan struct{}, body []byte) {
	t.Helper()
	select {
	case <-release:
		writeQueryFixture(t, w, body)
	case <-r.Context().Done():
	}
}

func TestQueryIsolatesProviderFailure(t *testing.T) {
	fixtures := providerQueryFixtures(t)
	vercel, moonshot := fixtures[0], fixtures[2]
	failureStarted, successStarted := make(chan struct{}, 1), make(chan struct{}, 1)
	releaseFailure, releaseSuccess := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Host == "api.deepseek.com" {
			failureStarted <- struct{}{}
			select {
			case <-releaseFailure:
				w.WriteHeader(http.StatusServiceUnavailable)
				writeQueryFixture(t, w, []byte("raw-provider-canary fixture-secret account-canary-89342\x1b[31m"))
			case <-r.Context().Done():
			}
			return
		}
		successStarted <- struct{}{}
		heldQueryFixture(t, w, r, releaseSuccess, vercel.body)
	})

	failedQuery := startProviderQuery(t, client, "deepseek", deepSeekAuth())
	awaitQueryEvent(t, failureStarted)
	successQuery := startProviderQuery(t, client, vercel.provider, vercel.auth())
	awaitQueryEvent(t, successStarted)
	close(releaseFailure)
	failed := awaitProviderQuery(t, failedQuery)
	close(releaseSuccess)
	successful := awaitProviderQuery(t, successQuery)

	requireQueryRefused(t, failed.report, failed.err)
	requireSafeDiagnostic(t, failed.err)
	requirePinnedQueryReport(t, successful.report, successful.err, vercel)

	recoveredStarted := make(chan struct{}, 2)
	releaseRecovery := make(chan struct{})
	recoveryClient := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		recoveredStarted <- struct{}{}
		if r.Host == "ai-gateway.vercel.sh" {
			heldQueryFixture(t, w, r, releaseRecovery, vercel.body)
			return
		}
		heldQueryFixture(t, w, r, releaseRecovery, moonshot.body)
	})
	recoveredVercelQuery := startProviderQuery(t, recoveryClient, vercel.provider, vercel.auth())
	recoveredMoonshotQuery := startProviderQuery(t, recoveryClient, moonshot.provider, moonshot.auth())
	awaitQueryEvent(t, recoveredStarted)
	awaitQueryEvent(t, recoveredStarted)
	close(releaseRecovery)
	recoveredVercel := awaitProviderQuery(t, recoveredVercelQuery)
	recoveredMoonshot := awaitProviderQuery(t, recoveredMoonshotQuery)

	requirePinnedQueryReport(t, recoveredVercel.report, recoveredVercel.err, vercel)
	requirePinnedQueryReport(t, recoveredMoonshot.report, recoveredMoonshot.err, moonshot)
	requireRequestCount(t, requests.Load(), 4)
}
