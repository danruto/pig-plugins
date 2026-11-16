package usage

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const syntheticCodexAccess = "header.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoic3ludGhldGljLWFjY291bnQifX0.signature"

type lifecycleCredential struct {
	apiKey string
	base   string
}

type lifecycleHost struct {
	mu          sync.Mutex
	session     string
	current     string
	revision    map[string]string
	models      map[string]map[string]any
	credentials map[string]lifecycleCredential
	collected   map[string][]any
}

func newLifecycleHost(provider, base string) *lifecycleHost {
	host := &lifecycleHost{
		session:     "synthetic-session",
		current:     provider,
		revision:    map[string]string{},
		models:      map[string]map[string]any{},
		credentials: map[string]lifecycleCredential{},
		collected:   map[string][]any{},
	}
	host.setCredential(provider, "fixture-secret", base)
	return host
}

func (h *lifecycleHost) setCredential(provider, apiKey, base string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.credentials[provider] = lifecycleCredential{apiKey: apiKey, base: base}
	h.models[provider] = map[string]any{"id": "fixture-model", "provider": provider, "baseUrl": base}
	h.revision[provider] = "revision-1"
}

func (h *lifecycleHost) removeCredential(provider string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.credentials, provider)
	delete(h.models, provider)
	delete(h.revision, provider)
}

func (h *lifecycleHost) setRevision(provider, revision string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.revision[provider] = revision
}

func (h *lifecycleHost) setModelOption(provider, key string, value any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	changed := maps.Clone(h.models[provider])
	changed[key] = value
	h.models[provider] = changed
}

func (h *lifecycleHost) Metadata(context.Context) (HostMetadata, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	modelID, ok := h.models[h.current]["id"].(string)
	if !ok {
		return HostMetadata{}, errors.New("synthetic host has no model id for " + h.current)
	}
	return HostMetadata{SessionID: h.session, ProviderID: h.current, ModelID: modelID}, nil
}

func (h *lifecycleHost) SessionAuth(_ context.Context, options map[string]any) (map[string]any, error) {
	provider, _ := options["provider"].(string)
	h.mu.Lock()
	defer h.mu.Unlock()
	credential, ok := h.credentials[provider]
	if !ok {
		return nil, errors.New("synthetic host has no credential for " + provider)
	}
	auth := map[string]any{"apiKey": credential.apiKey, "baseUrl": credential.base}
	return map[string]any{
		"provider":      provider,
		"sessionId":     h.session,
		"revision":      h.revision[provider],
		"selectedModel": h.models[provider],
		"model":         h.models[provider],
		"providerAuth":  map[string]any{"auth": auth, "source": "oauth"},
		"effectiveAuth": map[string]any{"auth": auth, "source": "oauth"},
	}, nil
}

func (h *lifecycleHost) Inventory(context.Context, []string) (HostInventory, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	inventory := HostInventory{Providers: map[string]HostProvider{}}
	for _, provider := range slices.Sorted(maps.Keys(h.models)) {
		inventory.Models = append(inventory.Models, h.models[provider])
		inventory.Available = append(inventory.Available, h.models[provider])
		inventory.Providers[provider] = HostProvider{Configured: true}
	}
	return inventory, nil
}

func (h *lifecycleHost) ModelState(_ context.Context, providerID, _ string) (HostModelState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	credential, ok := h.credentials[providerID]
	if !ok {
		return HostModelState{}, errors.New("synthetic host has no model state for " + providerID)
	}
	return HostModelState{Model: h.models[providerID], ProviderBaseURL: credential.base}, nil
}

func (h *lifecycleHost) Collect(_ context.Context, request CollectionRequest) (CollectionResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return CollectionResult{SessionID: h.session, Values: h.collected[request.ProviderID]}, nil
}

func lifecycleReport() *Report {
	return &Report{Buckets: []Bucket{{Label: "daily", Unit: "percent", Used: new(20.0)}}}
}

func seedLifecycleReport(t *testing.T, collector *Collector, host *lifecycleHost, outcome Outcome) {
	t.Helper()
	ticket := collector.begin()
	identity, err := acquireCollection(t.Context(), Resolver{Host: host}, ticket)
	if err != nil {
		t.Fatalf("collection identity: %v", err)
	}
	if outcome.ProviderID == "" {
		outcome.ProviderID = identity.provider
	}
	if err := collector.commit(ticket, identity.key, outcome, time.Now()); err != nil {
		t.Fatalf("seed cached report: %v", err)
	}
}

func TestCollectorRefresh_FireworksTargetFailureBacksOffInsteadOfRelisting(t *testing.T) {
	host := newLifecycleHost("fireworks", "https://api.fireworks.ai/inference/v1")
	var listings atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts" {
			listings.Add(1)
			writeQueryFixture(t, w, []byte(`{"accounts":[{"name":"accounts/first"},{"name":"accounts/second"}]}`))
			return
		}
		writeQueryFixture(t, w, []byte(`{"lineItems":[]}`))
	})
	collector := NewCollector(client)
	collector.Configure(CollectionSettings{})

	firstErr := collector.Refresh(t.Context(), Resolver{Host: host})
	first := collector.Snapshot(time.Now())
	secondErr := collector.Refresh(t.Context(), Resolver{Host: host})
	second := collector.Snapshot(time.Now())

	if !errors.Is(firstErr, errCollectionTarget) {
		t.Fatalf("unselected target error = %v, want %v", firstErr, errCollectionTarget)
	}
	if first.Reason != "selection-required" {
		t.Fatalf("unselected target reason = %q", first.Reason)
	}
	if got := listings.Load(); got != 1 {
		t.Fatalf("discovery requests after first refresh = %d, want 1", got)
	}
	if secondErr == nil {
		t.Fatal("refresh inside backoff reported success")
	}
	if second.Reason != "selection-required" {
		t.Fatalf("backoff reason = %q, want selection-required", second.Reason)
	}
	if got := listings.Load(); got != 1 {
		t.Fatalf("discovery requests after repeated refresh = %d, want 1", got)
	}
}

func TestCollectorRefresh_IdentityChangeBacksOffInsteadOfQueryingProvider(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	var requests atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		host.setRevision("deepseek", "revision-2")
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	collector := NewCollector(client)

	firstErr := collector.Refresh(t.Context(), Resolver{Host: host})
	first := requests.Load()
	host.setRevision("deepseek", "revision-1")
	secondErr := collector.Refresh(t.Context(), Resolver{Host: host})
	second := requests.Load()

	if !errors.Is(firstErr, ErrUsageChanged) {
		t.Fatalf("rotated identity error = %v, want %v", firstErr, ErrUsageChanged)
	}
	if first != 1 {
		t.Fatalf("provider requests before identity change = %d, want 1", first)
	}
	if secondErr == nil {
		t.Fatal("refresh inside backoff reported success")
	}
	if second != 1 {
		t.Fatalf("provider requests after identity change = %d, want 1", second)
	}
}

func TestCollectorRefresh_CanceledQueryBacksOffInsteadOfRequerying(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	var requests atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			cancel()
			return
		}
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	collector := NewCollector(client)

	canceledErr := collector.Refresh(ctx, Resolver{Host: host})
	afterCancel := requests.Load()
	secondErr := collector.Refresh(t.Context(), Resolver{Host: host})
	afterBackoff := requests.Load()

	if !errors.Is(canceledErr, context.Canceled) {
		t.Fatalf("canceled query error = %v, want %v", canceledErr, context.Canceled)
	}
	if afterCancel != 1 {
		t.Fatalf("provider requests before cancellation = %d, want 1", afterCancel)
	}
	if afterBackoff != 1 {
		t.Fatalf("provider requests after cancellation = %d, want 1", afterBackoff)
	}
	if secondErr == nil {
		t.Fatal("refresh inside backoff reported success")
	}
}

func TestCollectorRefresh_CodexCompanionRevisionMissesCachedReport(t *testing.T) {
	host := newLifecycleHost("openai", "https://api.openai.com/v1")
	host.setCredential("openai-codex", syntheticCodexAccess, "https://chatgpt.com/backend-api")
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeQueryFixture(t, w, []byte(`{}`))
	})
	collector := NewCollector(client)
	collector.Configure(CollectionSettings{UseCodexUsage: true})
	seedLifecycleReport(t, collector, host, Outcome{Status: "ok", Report: lifecycleReport()})

	reuseErr := collector.Refresh(t.Context(), Resolver{Host: host})
	sameCompanion := collector.Snapshot(time.Now())
	host.setRevision("openai-codex", "revision-2")
	switchErr := collector.Refresh(t.Context(), Resolver{Host: host})
	switchedCompanion := collector.Snapshot(time.Now())

	if reuseErr != nil {
		t.Fatalf("refresh with unchanged companion: %v", reuseErr)
	}
	if len(sameCompanion.Windows) != 1 {
		t.Fatalf("cached report windows with unchanged companion = %+v", sameCompanion.Windows)
	}
	if switchErr == nil {
		t.Fatal("refresh after companion identity change reported success from cache")
	}
	if len(switchedCompanion.Windows) != 0 {
		t.Fatalf("stale companion report windows = %+v", switchedCompanion.Windows)
	}
}

func TestCollectorRefresh_FireworksBackoffPublishesNoCachedWindows(t *testing.T) {
	host := newLifecycleHost("fireworks", "https://api.fireworks.ai/inference/v1")
	var listings atomic.Int32
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts" {
			listings.Add(1)
			writeQueryFixture(t, w, []byte(`{"accounts":[{"name":"accounts/first"}]}`))
			return
		}
		writeQueryFixture(t, w, []byte(`{"lineItems":[]}`))
	})
	collector := NewCollector(client)
	collector.Configure(CollectionSettings{Targets: map[string]string{"fireworks": "first"}})
	now := time.Now()
	ticket := collector.begin()
	identity, err := acquireCollection(t.Context(), Resolver{Host: host}, ticket)
	if err != nil {
		t.Fatalf("collection identity: %v", err)
	}
	if err := collector.commit(ticket, identity.key, Outcome{Status: "ok", ProviderID: identity.provider, TargetID: "first", Report: lifecycleReport()}, now.Add(-time.Minute)); err != nil {
		t.Fatalf("seed cached report: %v", err)
	}
	if err := collector.commit(ticket, identity.key, Outcome{Status: "failed", ProviderID: identity.provider, Reason: "query-failed"}, now); err == nil {
		t.Fatal("seeded failure reported success")
	}
	seeded := collector.Snapshot(now)

	backoffErr := collector.Refresh(t.Context(), Resolver{Host: host})
	backoff := collector.Snapshot(time.Now())

	if len(seeded.Windows) != 1 {
		t.Fatalf("cached report windows before backoff refresh = %+v", seeded.Windows)
	}
	if backoffErr == nil {
		t.Fatal("refresh inside backoff reported success")
	}
	if len(backoff.Windows) != 0 {
		t.Fatalf("cached report windows published during backoff = %+v", backoff.Windows)
	}
	if got := listings.Load(); got != 0 {
		t.Fatalf("discovery requests during backoff = %d, want 0", got)
	}
}

func TestHostResolution_RevalidateFencesChangedRevisionAndModel(t *testing.T) {
	tests := []struct {
		name   string
		change func(*lifecycleHost)
	}{
		{name: "host revision", change: func(host *lifecycleHost) { host.setRevision("deepseek", "revision-2") }},
		{name: "registered model", change: func(host *lifecycleHost) { host.setModelOption("deepseek", "maxTokens", 64) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
			resolution, err := Resolver{Host: host}.acquire(t.Context(), "deepseek", nil)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			tt.change(host)

			revalidateErr := resolution.revalidate(t.Context())

			if !errors.Is(revalidateErr, ErrUsageChanged) {
				t.Fatalf("%s revalidation = %v, want %v", tt.name, revalidateErr, ErrUsageChanged)
			}
		})
	}
}

func TestHostResolution_RejectsForeignAuthenticationOrigin(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	resolution, err := Resolver{Host: host}.acquire(t.Context(), "deepseek", nil)
	if err != nil {
		t.Fatalf("official origin acquire: %v", err)
	}
	if err := resolution.validateOrigins(); err != nil {
		t.Fatalf("official origin validation: %v", err)
	}
	foreign := resolution
	foreign.snapshot.ProviderAuth = &hostAuthResult{}
	foreign.snapshot.ProviderAuth.Auth.BaseURL = "https://api.deepseek.com.attacker.invalid/v1"

	foreignErr := foreign.validateOrigins()

	if !errors.Is(foreignErr, ErrUsageOrigin) {
		t.Fatalf("foreign origin validation = %v, want %v", foreignErr, ErrUsageOrigin)
	}
}

func TestResolver_AcquireRefusesForeignAuthenticationOrigin(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com.attacker.invalid/v1")

	_, err := Resolver{Host: host}.acquire(t.Context(), "deepseek", nil)

	if !errors.Is(err, ErrUsageOrigin) {
		t.Fatalf("foreign origin acquire = %v, want %v", err, ErrUsageOrigin)
	}
}

func TestResolver_ReadyCurrentReportsFreshIdentityOrRefuses(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	resolver := Resolver{Host: host}
	before, companion, err := resolver.readyCurrent(t.Context(), "deepseek")
	if err != nil {
		t.Fatalf("ready current: %v", err)
	}
	host.setRevision("deepseek", "revision-2")
	after, _, afterErr := resolver.readyCurrent(t.Context(), "deepseek")
	host.removeCredential("deepseek")
	_, _, missingErr := resolver.readyCurrent(t.Context(), "deepseek")

	if companion != "deepseek" {
		t.Fatalf("companion identity = %q", companion)
	}
	if before.Revision != "revision-1" {
		t.Fatalf("initial revision = %q", before.Revision)
	}
	if afterErr != nil || after.Revision != "revision-2" {
		t.Fatalf("refreshed revision = %q, %v", after.Revision, afterErr)
	}
	if !errors.Is(missingErr, ErrUsageHost) {
		t.Fatalf("missing credential = %v, want %v", missingErr, ErrUsageHost)
	}
}

func TestCollectorRefresh_SupersededFailurePublishesNothing(t *testing.T) {
	host := newLifecycleHost("deepseek", "https://api.deepseek.com/v1")
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
			<-release
			host.setRevision("deepseek", "revision-2")
			writeQueryFixture(t, w, []byte(deepSeekBalance))
			return
		}
		writeQueryFixture(t, w, []byte(deepSeekBalance))
	})
	collector := NewCollector(client)
	collector.Configure(CollectionSettings{})
	start := time.Now()
	superseded := make(chan error, 1)

	go func() {
		superseded <- collector.Refresh(t.Context(), Resolver{Host: host})
	}()
	<-started
	successorErr := collector.Refresh(t.Context(), Resolver{Host: host})
	successor := collector.Snapshot(time.Now())
	close(release)
	supersededErr := <-superseded
	afterSuccessor := collector.Snapshot(time.Now())
	reuseErr := collector.Refresh(t.Context(), Resolver{Host: host})
	queried := requests.Load()

	if successorErr != nil {
		t.Fatalf("successor refresh: %v", successorErr)
	}
	if !successor.RefreshAt.After(start.Add(time.Minute)) {
		t.Fatalf("successor refresh deadline = %v, want the success deadline after %v", successor.RefreshAt, start.Add(time.Minute))
	}
	if !errors.Is(supersededErr, ErrUsageChanged) {
		t.Fatalf("superseded refresh error = %v, want %v", supersededErr, ErrUsageChanged)
	}
	if afterSuccessor.Reason != successor.Reason {
		t.Fatalf("successor reason after superseded failure = %q, want %q", afterSuccessor.Reason, successor.Reason)
	}
	if !afterSuccessor.RefreshAt.Equal(successor.RefreshAt) {
		t.Fatalf("successor refresh deadline after superseded failure = %v, want %v", afterSuccessor.RefreshAt, successor.RefreshAt)
	}
	if reuseErr != nil {
		t.Fatalf("refresh after superseded failure = %v", reuseErr)
	}
	if queried != 3 {
		t.Fatalf("provider requests after superseded failure = %d, want 3", queried)
	}
}
