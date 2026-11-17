package usage

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type authOriginHost struct {
	*lifecycleHost
	authBase string
}

func newAuthOriginHost(provider, base string) *authOriginHost {
	return &authOriginHost{lifecycleHost: newLifecycleHost(provider, base), authBase: base}
}

func (h *authOriginHost) setAuthBase(base string) {
	h.authBase = base
}

func (h *authOriginHost) SessionAuth(ctx context.Context, options map[string]any) (map[string]any, error) {
	result, err := h.lifecycleHost.SessionAuth(ctx, options)
	if err != nil {
		return nil, err
	}
	providerAuth, ok := result["providerAuth"].(map[string]any)
	if !ok {
		return nil, ErrUsageHost
	}
	auth, ok := providerAuth["auth"].(map[string]any)
	if !ok {
		return nil, ErrUsageHost
	}
	auth["baseUrl"] = h.authBase
	return result, nil
}

func TestCollectorRefresh_TargetDiscoveryDeadlineReportsTimedOutAndKeepsWindows(t *testing.T) {
	host := newLifecycleHost("fireworks", "https://api.fireworks.ai/inference/v1")
	var discoveryHeld atomic.Bool
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/accounts" {
			discoveryHeld.Store(true)
			<-r.Context().Done()
			return
		}
		writeQueryFixture(t, w, []byte(`{}`))
	})
	collector := NewCollector(client)
	collector.Configure(CollectionSettings{Targets: map[string]string{"fireworks": "first"}})
	publishedAt := time.Now()
	ticket := collector.begin()
	identity, err := acquireCollection(t.Context(), Resolver{Host: host}, ticket)
	if err != nil {
		t.Fatalf("collection identity: %v", err)
	}
	outcome := Outcome{Status: "ok", ProviderID: identity.provider, TargetID: "first", Report: lifecycleReport()}
	if err := collector.commit(ticket, identity.key, outcome, publishedAt); err != nil {
		t.Fatalf("publishing the report under test: %v", err)
	}
	published := collector.Snapshot(publishedAt)
	deadline, cancel := context.WithDeadline(t.Context(), publishedAt.Add(time.Second))
	defer cancel()

	deadlineErr := collector.Refresh(deadline, Resolver{Host: host})
	afterDeadline := collector.Snapshot(time.Now())

	if len(published.Windows) == 0 {
		t.Fatal("the published report carried no windows to retain")
	}
	if !discoveryHeld.Load() {
		t.Fatal("the deadline passed before Fireworks target discovery was in flight, so the timed-out path was never reached")
	}
	if deadlineErr == nil {
		t.Fatal("refresh after the deadline returned a report")
	}
	if afterDeadline.Reason != "timed-out" {
		t.Fatalf("reason after the deadline = %q, want %q", afterDeadline.Reason, "timed-out")
	}
	if !reflect.DeepEqual(afterDeadline.Windows, published.Windows) {
		t.Fatalf("windows after the deadline = %+v, want the published %+v", afterDeadline.Windows, published.Windows)
	}
}

func TestResolver_RevalidateFencesChangedCodexCompanion(t *testing.T) {
	host := newLifecycleHost("openai", "https://api.openai.com/v1")
	host.setCredential("openai-codex", syntheticCodexAccess, "https://chatgpt.com/backend-api")
	resolved, err := Resolver{Host: host}.Resolve(t.Context(), "openai", ResolveOptions{UseCodexUsage: true})
	if err != nil {
		t.Fatalf("resolving the companion query: %v", err)
	}
	if !resolved.UseCodexUsage {
		t.Fatal("resolution dropped the requested Codex companion")
	}
	host.setRevision("openai-codex", "revision-2")

	revalidateErr := resolved.Revalidate(t.Context())

	if !errors.Is(revalidateErr, ErrUsageChanged) {
		t.Fatalf("revalidation after the companion account switch = %v, want %v", revalidateErr, ErrUsageChanged)
	}
}

func TestResolver_RevalidateRejectsSwappedProviderAuthOrigin(t *testing.T) {
	host := newAuthOriginHost("deepseek", "https://api.deepseek.com/v1")
	resolved, err := Resolver{Host: host}.Resolve(t.Context(), "deepseek", ResolveOptions{})
	if err != nil {
		t.Fatalf("resolving the official origin: %v", err)
	}
	host.setAuthBase("https://api.deepseek.com.attacker.invalid/v1")

	revalidateErr := resolved.Revalidate(t.Context())

	if !errors.Is(revalidateErr, ErrUsageOrigin) {
		t.Fatalf("revalidation after the authentication origin swap = %v, want %v", revalidateErr, ErrUsageOrigin)
	}
}
