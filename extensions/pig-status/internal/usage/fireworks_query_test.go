package usage

import (
	"errors"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fireworksResolvedAuth() ResolvedAuth {
	auth := deepSeekAuth()
	auth.ModelBaseURL = "https://api.fireworks.ai/inference/v1"
	return auth
}

func TestQueryFireworksRefusesForeignOriginBeforeDispatch(t *testing.T) {
	var requests atomic.Int32
	client := Client{Transport: queryRoundTripper(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected provider request")
	})}
	auth := fireworksResolvedAuth()
	auth.ModelBaseURL = "https://example.test/v1"

	report, err := client.QueryFireworks(t.Context(), auth, "team-alpha", time.Second)

	requireQueryRefused(t, report, err)
	requireSentinelError(t, err, ErrUsageOrigin)
	requireRequestCount(t, requests.Load(), 0)
}

func TestListFireworksTargetsRefusesForeignOriginBeforeDispatch(t *testing.T) {
	var requests atomic.Int32
	client := Client{Transport: queryRoundTripper(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected provider request")
	})}
	auth := fireworksResolvedAuth()
	auth.ModelBaseURL = "https://example.test/v1"

	targets, err := client.ListFireworksTargets(t.Context(), auth, time.Second)

	requireSentinelError(t, err, ErrUsageOrigin)
	if len(targets) != 0 {
		t.Fatalf("refused discovery published partial targets: %#v", targets)
	}
	requireRequestCount(t, requests.Load(), 0)
}

func TestFireworksListingThenBillingReportsProviderAndTarget(t *testing.T) {
	fixture := billingNamed(t, "fireworks:currencies-series-exact")
	var mu sync.Mutex
	var paths []string
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/v1/accounts" {
			writeQueryFixture(t, w, []byte(`{"accounts":[{"name":"accounts/team-alpha"}]}`))
			return
		}
		writeQueryFixture(t, w, fixture.Payload)
	})

	targets, listErr := client.ListFireworksTargets(t.Context(), fireworksResolvedAuth(), time.Second)
	report, queryErr := client.QueryFireworks(t.Context(), fireworksResolvedAuth(), "team-alpha", time.Second)

	requireFireworksTargets(t, targets, listErr, []Target{{ID: "team-alpha", Label: "team-alpha"}})
	requireFireworksBillingReport(t, report, queryErr, "team-alpha")
	mu.Lock()
	defer mu.Unlock()
	requireFireworksRequestPaths(t, paths, []string{"/v1/accounts", "/v1/accounts/team-alpha/billing/summary"})
}

func requireFireworksTargets(tb testing.TB, targets []Target, err error, want []Target) {
	tb.Helper()
	if err != nil {
		tb.Fatalf("Fireworks target discovery failed: %v", err)
	}
	if !reflect.DeepEqual(targets, want) {
		tb.Fatalf("targets = %#v, want %#v", targets, want)
	}
}

func requireFireworksBillingReport(tb testing.TB, report Report, err error, target string) {
	tb.Helper()
	if err != nil {
		tb.Fatalf("Fireworks billing summary failed: %v", err)
	}
	if report.ProviderID != "fireworks" || report.AccountLabel != target || len(report.Metrics) == 0 {
		tb.Fatalf("report provider/target/metrics = %q/%q/%d, want fireworks/%q with metrics", report.ProviderID, report.AccountLabel, len(report.Metrics), target)
	}
}

func requireFireworksRequestPaths(tb testing.TB, got, want []string) {
	tb.Helper()
	if !reflect.DeepEqual(got, want) {
		tb.Fatalf("Fireworks request paths = %v, want %v", got, want)
	}
}
