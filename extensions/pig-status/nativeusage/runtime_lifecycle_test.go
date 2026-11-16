package nativeusage

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

const lifecycleDeadline = 5 * time.Second

type viewRecorder struct {
	seen chan usage.CachedUsage
}

func newViewRecorder() *viewRecorder {
	return &viewRecorder{seen: make(chan usage.CachedUsage, 16)}
}

func (v *viewRecorder) publish(view usage.CachedUsage) {
	select {
	case v.seen <- view:
	default:
	}
}

func (v *viewRecorder) awaitReason(t *testing.T, reason string) usage.CachedUsage {
	t.Helper()
	timer := time.NewTimer(lifecycleDeadline)
	defer timer.Stop()
	for {
		select {
		case view := <-v.seen:
			if view.Reason == reason {
				return view
			}
		case <-timer.C:
			t.Fatalf("no %q view within the %v cap", reason, lifecycleDeadline)
			return usage.CachedUsage{}
		}
	}
}

func registerRuntime(t *testing.T) (*Runtime, *viewRecorder) {
	t.Helper()
	views := newViewRecorder()
	runtime, err := Register(sdk.New("lifecycle"), Options{Publish: views.publish})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, views
}

func ownedOwner(t *testing.T, runtime *Runtime) *owner {
	t.Helper()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.owner == nil {
		t.Fatal("runtime has no owner")
	}
	return runtime.owner
}

func awaitBridgeFailure(t *testing.T, views *viewRecorder, runtime *Runtime) {
	t.Helper()
	current := ownedOwner(t, runtime)
	timer := time.NewTimer(lifecycleDeadline)
	defer timer.Stop()
	for {
		select {
		case view := <-views.seen:
			if view.Reason == "host-bridge-failed" {
				return
			}
		case <-current.done:
			return
		case <-timer.C:
			t.Fatalf("the host bridge failure did not settle within the %v cap", lifecycleDeadline)
			return
		}
	}
}

type seedHost struct {
	mu       sync.Mutex
	session  string
	provider string
	base     string
	apiKey   string
}

func newSeedHost() *seedHost {
	return &seedHost{session: "synthetic-session", provider: "deepseek", base: "https://api.deepseek.com/v1", apiKey: "fixture-secret"}
}

func (h *seedHost) model() map[string]any {
	return map[string]any{"id": "fixture-model", "provider": h.provider, "baseUrl": h.base}
}

func (h *seedHost) Metadata(context.Context) (usage.HostMetadata, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return usage.HostMetadata{SessionID: h.session, ProviderID: h.provider, ModelID: "fixture-model"}, nil
}

func (h *seedHost) SessionAuth(_ context.Context, options map[string]any) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	provider, _ := options["provider"].(string)
	auth := map[string]any{"apiKey": h.apiKey, "baseUrl": h.base}
	return map[string]any{
		"provider":      provider,
		"sessionId":     h.session,
		"revision":      "revision-1",
		"selectedModel": h.model(),
		"model":         h.model(),
		"providerAuth":  map[string]any{"auth": auth, "source": "oauth"},
		"effectiveAuth": map[string]any{"auth": auth, "source": "oauth"},
	}, nil
}

func (h *seedHost) Inventory(context.Context, []string) (usage.HostInventory, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return usage.HostInventory{
		Models:    []map[string]any{h.model()},
		Available: []map[string]any{h.model()},
		Providers: map[string]usage.HostProvider{h.provider: {Configured: true}},
	}, nil
}

func (h *seedHost) ModelState(context.Context, string, string) (usage.HostModelState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return usage.HostModelState{Model: h.model(), ProviderBaseURL: h.base}, nil
}

func (h *seedHost) Collect(context.Context, usage.CollectionRequest) (usage.CollectionResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return usage.CollectionResult{SessionID: h.session}, nil
}

const deepseekBalanceBody = `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"12.50","granted_balance":"0","topped_up_balance":"12.50"}]}`

type seedTransport struct {
	mu      sync.Mutex
	failing bool
}

func (t *seedTransport) setFailing(failing bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failing = failing
}

func (t *seedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	failing := t.failing
	t.mu.Unlock()
	status, body := http.StatusOK, deepseekBalanceBody
	if failing {
		status, body = http.StatusServiceUnavailable, `{"error":"synthetic unavailable"}`
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}, nil
}

func registerSeededRuntime(t *testing.T) (*Runtime, *seedHost, *seedTransport) {
	t.Helper()
	host, transport := newSeedHost(), &seedTransport{}
	runtime, err := Register(sdk.New("lifecycle"), Options{Client: usage.Client{Transport: transport}, Publish: func(usage.CachedUsage) {}})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, host, transport
}

func seedCachedReport(t *testing.T, runtime *Runtime, host usage.HostPort) usage.CachedUsage {
	t.Helper()
	if err := runtime.collector.Refresh(t.Context(), usage.Resolver{Host: host}); err != nil {
		t.Fatalf("seed cached report: %v", err)
	}
	cached := runtime.collector.Snapshot(time.Now())
	if cached.ExpiresAt.IsZero() {
		t.Fatalf("seeded report has no expiry: %+v", cached)
	}
	return cached
}

func seedFailureBackoff(t *testing.T, runtime *Runtime, host usage.HostPort, transport *seedTransport) usage.CachedUsage {
	t.Helper()
	transport.setFailing(true)
	if err := runtime.collector.Refresh(t.Context(), usage.Resolver{Host: host}); err == nil {
		t.Fatal("seeded failure reported success")
	}
	backoff := runtime.collector.Snapshot(time.Now())
	if !backoff.RefreshAt.After(time.Now()) {
		t.Fatalf("seeded failure has no backoff: %+v", backoff)
	}
	return backoff
}

func TestConfigureAfterBridgeFailureLaunchesSuccessorWithNewSettings(t *testing.T) {
	runtime, views := registerRuntime(t)
	if err := runtime.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	awaitBridgeFailure(t, views, runtime)
	settings := usage.CollectionSettings{Targets: map[string]string{"fireworks": "target"}}

	configureErr := runtime.Configure(settings)

	runtime.mu.Lock()
	successor := runtime.owner
	runtime.mu.Unlock()
	applied := runtime.settings
	if configureErr == nil {
		t.Fatal("previous owner bridge failure was not reported")
	}
	if !errors.Is(configureErr, errBridge) {
		t.Errorf("reported error = %v, want the host bridge failure", configureErr)
	}
	if successor == nil {
		t.Fatal("configure after a bridge failure launched no successor owner")
	}
	if !maps.Equal(applied.Targets, settings.Targets) {
		t.Errorf("successor settings = %v, want %v", applied.Targets, settings.Targets)
	}
}

func TestSessionStartAfterBridgeFailureLaunchesSuccessorOwner(t *testing.T) {
	runtime, views := registerRuntime(t)
	if err := runtime.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	awaitBridgeFailure(t, views, runtime)
	predecessor := ownedOwner(t, runtime)

	startErr := runtime.start()

	runtime.mu.Lock()
	successor := runtime.owner
	active := runtime.active
	runtime.mu.Unlock()
	if startErr == nil {
		t.Fatal("previous owner bridge failure was not reported")
	}
	if !errors.Is(startErr, errBridge) {
		t.Errorf("reported error = %v, want the host bridge failure", startErr)
	}
	if successor == nil || successor == predecessor {
		t.Fatal("session start after a bridge failure launched no successor owner")
	}
	if !active {
		t.Fatal("session start after a bridge failure left the runtime inactive")
	}
}

func TestEmitFailurePublishesBridgeFailureViewAndKeepsSchedulerAlive(t *testing.T) {
	runtime, views := registerRuntime(t)
	if err := runtime.start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	view := views.awaitReason(t, "host-bridge-failed")

	if len(view.Windows) != 0 {
		t.Errorf("bridge failure view windows = %+v, want a cleared view", view.Windows)
	}
	current := ownedOwner(t, runtime)
	runtime.wake()
	select {
	case <-current.done:
		t.Fatal("scheduler ended after the emit failure")
	default:
	}
}

func TestOwnerChangeRepublishesBridgeFailureView(t *testing.T) {
	runtime, views := registerRuntime(t)
	if err := runtime.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	views.awaitReason(t, "host-bridge-failed")

	_ = runtime.start()

	view := views.awaitReason(t, "host-bridge-failed")
	if len(view.Windows) != 0 {
		t.Errorf("successor bridge failure view windows = %+v, want a cleared view", view.Windows)
	}
}

func TestSessionStartPreservesCachedReport(t *testing.T) {
	runtime, host, _ := registerSeededRuntime(t)
	if err := runtime.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	cached := seedCachedReport(t, runtime, host)

	_ = runtime.start()

	after := runtime.collector.Snapshot(time.Now())
	if !after.ExpiresAt.Equal(cached.ExpiresAt) || !after.RefreshAt.Equal(cached.RefreshAt) {
		t.Errorf("owner change deadlines = %v/%v, want the cached %v/%v", after.RefreshAt, after.ExpiresAt, cached.RefreshAt, cached.ExpiresAt)
	}
	if len(after.Windows) != len(cached.Windows) {
		t.Errorf("owner change windows = %+v, want the cached %+v", after.Windows, cached.Windows)
	}
}

func TestSessionStartPreservesFailureBackoff(t *testing.T) {
	runtime, host, transport := registerSeededRuntime(t)
	if err := runtime.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	backoff := seedFailureBackoff(t, runtime, host, transport)

	_ = runtime.start()

	after := runtime.collector.Snapshot(time.Now())
	if !after.RefreshAt.Equal(backoff.RefreshAt) {
		t.Errorf("owner change refresh deadline = %v, want the failure backoff %v", after.RefreshAt, backoff.RefreshAt)
	}
}

func TestConfigureWithIdenticalSettingsPreservesFailureBackoff(t *testing.T) {
	runtime, host, transport := registerSeededRuntime(t)
	if err := runtime.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	backoff := seedFailureBackoff(t, runtime, host, transport)

	_ = runtime.Configure(usage.CollectionSettings{})

	after := runtime.collector.Snapshot(time.Now())
	if !after.RefreshAt.Equal(backoff.RefreshAt) {
		t.Errorf("unchanged settings refresh deadline = %v, want the failure backoff %v", after.RefreshAt, backoff.RefreshAt)
	}
}

func TestConfigureWithChangedSettingsResetsCachedReport(t *testing.T) {
	runtime, host, _ := registerSeededRuntime(t)
	if err := runtime.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	seedCachedReport(t, runtime, host)

	_ = runtime.Configure(usage.CollectionSettings{Targets: map[string]string{"fireworks": "target"}})

	after := runtime.collector.Snapshot(time.Now())
	if len(after.Windows) != 0 || !after.RefreshAt.IsZero() || !after.ExpiresAt.IsZero() {
		t.Errorf("changed settings view = %+v, want a cleared cache", after)
	}
}
