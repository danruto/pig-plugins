package usage

import (
	"context"
	"errors"
	"testing"
	"time"
)

type sessionChangeHost struct {
	*lifecycleHost
	changed bool
}

func (h *sessionChangeHost) armSessionChange() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.changed = true
}

func (h *sessionChangeHost) Collect(ctx context.Context, request CollectionRequest) (CollectionResult, error) {
	h.mu.Lock()
	changed := h.changed
	h.mu.Unlock()
	if !changed {
		return h.lifecycleHost.Collect(ctx, request)
	}
	return CollectionResult{}, ErrUsageChanged
}

func TestResolver_CollectReportsSessionChangeAsIdentityChange(t *testing.T) {
	host := &sessionChangeHost{lifecycleHost: newLifecycleHost("deepseek", "https://api.deepseek.com/v1")}
	resolver := Resolver{Host: host}
	resolution, err := resolver.acquire(t.Context(), "deepseek", nil)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	host.armSessionChange()

	_, collectErr := resolver.collect(t.Context(), resolution.snapshot, "oauth:credential-source:v1", "usage")

	if !errors.Is(collectErr, ErrUsageChanged) {
		t.Errorf("session change during host collection = %v, want %v", collectErr, ErrUsageChanged)
	}
	collector := NewCollector(Client{})
	ticket := collector.begin()
	_ = collector.fail(ticket, collectErr)
	if reason := collector.Snapshot(time.Now()).Reason; reason != "identity-changed" {
		t.Errorf("identity change outcome reason = %q, want %q", reason, "identity-changed")
	}
}
