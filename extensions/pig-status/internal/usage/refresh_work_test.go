package usage

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type hostWork struct {
	mu     sync.Mutex
	events []string
	paths  []string
}

func (w *hostWork) record(event string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
}

func (w *hostWork) recordRequest(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, "request")
	w.paths = append(w.paths, path)
}

func (w *hostWork) count(event string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	seen := 0
	for _, recorded := range w.events {
		if recorded == event {
			seen++
		}
	}
	return seen
}

func (w *hostWork) requests() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.paths)
}

type workHost struct {
	HostPort
	work *hostWork
}

func newWorkHost(host HostPort) *workHost {
	return &workHost{HostPort: host, work: new(hostWork)}
}

func (h workHost) SessionAuth(ctx context.Context, options map[string]any) (map[string]any, error) {
	h.work.record("session-auth")
	return h.HostPort.SessionAuth(ctx, options)
}

func (h workHost) Inventory(ctx context.Context, providerIDs []string) (HostInventory, error) {
	h.work.record("inventory")
	return h.HostPort.Inventory(ctx, providerIDs)
}

func workClient(tb testing.TB, host *workHost, body func(*http.Request) []byte) Client {
	tb.Helper()
	return fixtureClient(tb, func(w http.ResponseWriter, r *http.Request) {
		host.work.recordRequest(r.URL.Path)
		writeQueryFixture(tb, w, body(r))
	})
}

func TestClientQueryReportsChangedIdentityRevalidation(t *testing.T) {
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	auth := deepSeekAuth()
	auth.Revalidate = func(context.Context) error { return ErrUsageChanged }

	report, err := client.Query(t.Context(), "deepseek", auth, time.Second)

	requireQueryRefused(t, report, err)
	if !errors.Is(err, ErrUsageChanged) {
		t.Fatalf("changed identity revalidation = %v, want %v", err, ErrUsageChanged)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("provider requests after a changed identity = %d, want 0", got)
	}
}

func TestScheduledRefreshAcquiresNoInventory(t *testing.T) {
	host := newWorkHost(newLifecycleHost("deepseek", "https://api.deepseek.com/v1"))
	collector := NewCollector(workClient(t, host, func(*http.Request) []byte { return []byte(deepSeekBalance) }))

	err := collector.Refresh(t.Context(), Resolver{Host: host})

	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := host.work.count("inventory"); got != 0 {
		t.Fatalf("inventory acquisitions on a scheduled refresh = %d, want 0", got)
	}
	if got := host.work.count("request"); got != 1 {
		t.Fatalf("provider requests = %d, want 1", got)
	}
}

func TestCurrentSelectionAcquiresNoInventory(t *testing.T) {
	host := newWorkHost(newLifecycleHost("deepseek", "https://api.deepseek.com/v1"))
	dispatcher := Dispatcher{Resolver: Resolver{Host: host}, Client: workClient(t, host, func(*http.Request) []byte { return []byte(deepSeekBalance) })}

	outcomes, err := dispatcher.Query(t.Context(), Selection{Mode: "current", Timeout: 5 * time.Second})

	if err != nil {
		t.Fatalf("current selection: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "ok" {
		t.Fatalf("current outcomes = %+v, want one ok outcome", outcomes)
	}
	if got := host.work.count("inventory"); got != 0 {
		t.Fatalf("inventory acquisitions on a current selection = %d, want 0", got)
	}
	if got := host.work.count("request"); got != 1 {
		t.Fatalf("provider requests = %d, want 1", got)
	}
}

func TestAllSelectionAcquiresInventoryOnce(t *testing.T) {
	base := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	base.setCredential("openrouter", "fixture-secret", "https://openrouter.ai/api/v1")
	host := newWorkHost(acceptSelectedModelHost{lifecycleHost: base})
	openrouter := billingNamed(t, "openrouter:limit-and-spend").Payload
	dispatcher := Dispatcher{Resolver: Resolver{Host: host}, Client: workClient(t, host, func(r *http.Request) []byte {
		if r.Host == "openrouter.ai" {
			return openrouter
		}
		return []byte(deepSeekBalance)
	})}

	outcomes, err := dispatcher.Query(t.Context(), Selection{Mode: "all", Timeout: 5 * time.Second})

	if err != nil {
		t.Fatalf("all selection: %v", err)
	}
	if len(outcomes) != 2 || outcomes[0].Status != "ok" || outcomes[1].Status != "ok" {
		t.Fatalf("all outcomes = %+v, want two ok outcomes", outcomes)
	}
	if got := host.work.count("inventory"); got != 1 {
		t.Fatalf("inventory acquisitions on an all selection = %d, want 1", got)
	}
	if got := host.work.count("request"); got != 2 {
		t.Fatalf("provider requests = %d, want one per configured provider", got)
	}
}

func TestFireworksRefreshDiscoversSelectedTargetOnce(t *testing.T) {
	host := newWorkHost(newLifecycleHost("fireworks", "https://api.fireworks.ai/inference/v1"))
	billing := billingNamed(t, "fireworks:currencies-series-exact").Payload
	collector := NewCollector(workClient(t, host, func(r *http.Request) []byte {
		if r.URL.Path == "/v1/accounts" {
			return []byte(`{"accounts":[{"name":"accounts/first"}]}`)
		}
		return billing
	}))
	collector.Configure(CollectionSettings{Targets: map[string]string{"fireworks": "first"}})

	err := collector.Refresh(t.Context(), Resolver{Host: host})

	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got, want := host.work.requests(), []string{"/v1/accounts", "/v1/accounts/first/billing/summary"}; !slices.Equal(got, want) {
		t.Fatalf("Fireworks refresh requests = %q, want %q", got, want)
	}
}

func TestRefreshRevalidatesHostPerProviderFetch(t *testing.T) {
	for _, tt := range []struct {
		name         string
		provider     string
		base         string
		settings     CollectionSettings
		wantRequests int
	}{
		{name: "single provider", provider: "deepseek", base: "https://api.deepseek.com/v1", wantRequests: 1},
		{
			name:         "fireworks selected target",
			provider:     "fireworks",
			base:         "https://api.fireworks.ai/inference/v1",
			settings:     CollectionSettings{Targets: map[string]string{"fireworks": "first"}},
			wantRequests: 2,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			host := newWorkHost(newLifecycleHost(tt.provider, tt.base))
			billing := billingNamed(t, "fireworks:currencies-series-exact").Payload
			collector := NewCollector(workClient(t, host, func(r *http.Request) []byte {
				if r.URL.Path == "/v1/accounts" {
					return []byte(`{"accounts":[{"name":"accounts/first"}]}`)
				}
				if r.Host == "api.fireworks.ai" {
					return billing
				}
				return []byte(deepSeekBalance)
			}))
			collector.Configure(tt.settings)

			err := collector.Refresh(t.Context(), Resolver{Host: host})

			if err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if got := host.work.count("request"); got != tt.wantRequests {
				t.Fatalf("provider requests = %d, want %d", got, tt.wantRequests)
			}
			if got := host.work.count("session-auth"); got > tt.wantRequests+2 {
				t.Fatalf("host session auth calls = %d, want at most %d: one acquisition, one per provider fetch, one before publication", got, tt.wantRequests+2)
			}
		})
	}
}
