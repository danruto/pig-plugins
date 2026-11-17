package usage

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

type admissionHost struct {
	provider      string
	authProviders []string
}

func (h *admissionHost) Metadata(context.Context) (HostMetadata, error) {
	return HostMetadata{SessionID: "synthetic-session", ProviderID: h.provider, ModelID: "fixture"}, nil
}

func (h *admissionHost) SessionAuth(_ context.Context, request map[string]any) (map[string]any, error) {
	provider, _ := request["provider"].(string)
	h.authProviders = append(h.authProviders, provider)
	return nil, errors.New("synthetic authentication refused")
}

func (*admissionHost) Inventory(context.Context, []string) (HostInventory, error) {
	return HostInventory{}, errors.New("unexpected inventory acquisition")
}

func (*admissionHost) ModelState(context.Context, string, string) (HostModelState, error) {
	return HostModelState{}, errors.New("unexpected model acquisition")
}

func (*admissionHost) Collect(context.Context, CollectionRequest) (CollectionResult, error) {
	return CollectionResult{}, errors.New("unexpected credential collection")
}

func TestCollector_ACMenuOnlySkipsAuthenticationBeforeAutomaticCollection(t *testing.T) {
	host := &admissionHost{provider: "xai"}
	collector := NewCollector(Client{})
	xaiErr := collector.Refresh(t.Context(), Resolver{Host: host})
	xaiWindows := collector.Snapshot(time.Now()).Windows
	host.provider = "openrouter"
	supportedErr := collector.Refresh(t.Context(), Resolver{Host: host})

	if !reflect.DeepEqual(host.authProviders, []string{"openrouter"}) || len(xaiWindows) != 0 || supportedErr == nil {
		t.Fatalf("automatic admission: auth=%v xai-windows=%v xai-error=%v supported-error=%v", host.authProviders, xaiWindows, xaiErr, supportedErr)
	}
}

func TestCollectionKeyFencesCompleteOwnership(t *testing.T) {
	snapshot := hostSnapshot{SessionID: "session", Revision: "account", Provider: "deepseek", SelectedModel: map[string]any{"id": "same", "provider": "deepseek", "headers": map[string]any{"x-owner": "first"}, "reasoning": false}}
	ticket := collectionTicket{generation: 3, settings: CollectionSettings{Targets: map[string]string{"deepseek": "target"}}}
	state := HostModelState{Model: map[string]any{"id": "same"}, ProviderBaseURL: "https://api.deepseek.com"}
	baseline, err := collectionKey(snapshot, state, ticket, "")
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{"session", "account", "provider", "target", "settings", "companion", "nested-model", "model-option", "registered-model", "registered-origin"}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			s, st, owner := snapshot, state, ticket
			switch kind {
			case "session":
				s.SessionID = "successor"
			case "account":
				s.Revision = "rotated-account"
			case "provider":
				s.Provider = "openrouter"
			case "target":
				owner.settings = CollectionSettings{Targets: map[string]string{"deepseek": "other-target"}}
			case "settings":
				owner.generation++
			case "companion":
				owner.settings.UseCodexUsage = true
			case "nested-model":
				s.SelectedModel = map[string]any{"id": "same", "provider": "deepseek", "headers": map[string]any{"x-owner": "second"}, "reasoning": false}
			case "model-option":
				s.SelectedModel = map[string]any{"id": "same", "provider": "deepseek", "headers": map[string]any{"x-owner": "first"}, "reasoning": true}
			case "registered-model":
				st.Model = map[string]any{"id": "same", "maxTokens": 128}
			case "registered-origin":
				st.ProviderBaseURL = "https://api.deepseek.com/v1"
			}
			changed, err := collectionKey(s, st, owner, "")
			if err != nil {
				t.Fatal(err)
			}
			if changed == baseline {
				t.Fatal("foreign ownership reused cache identity")
			}
		})
	}
	snapshot.SelectedModel = map[string]any{"reasoning": false, "headers": map[string]any{"x-owner": "first"}, "provider": "deepseek", "id": "same"}
	reordered, err := collectionKey(snapshot, state, ticket, "")
	if err != nil || reordered != baseline {
		t.Fatalf("equivalent JSON changed identity: %v", err)
	}
}

func TestReportWindowsUnknownZeroRatiosAndResetBoundaries(t *testing.T) {
	now := time.Unix(2000000000, 0)
	report := Report{Buckets: []Bucket{
		{Label: "unknown", Unit: "percent"},
		{Label: "zero", Unit: "percent", Used: new(0.0)},
		{Label: "remaining", Unit: "count", Limit: new(40.0), Remaining: new(30.0)},
		{Label: "zero-denominator", Unit: "count", Limit: new(0.0), Used: new(0.0)},
		{Label: "infinite", Unit: "percent", Used: new(math.Inf(1))},
		{Label: "overflow", Unit: "count", Limit: new(math.SmallestNonzeroFloat64), Used: new(math.MaxFloat64)},
		{Label: "expired", Unit: "percent", Used: new(12.0), ResetsAt: new(float64(now.Unix()))},
		{Label: "720h", Unit: "percent", Remaining: new(5.0), ResetsAt: new(float64(now.Unix() + 90))},
		{Label: "clamped", Unit: "percent", Used: new(-2.0)},
	}}
	got := reportWindows(report, now)
	want := []Window{{Label: "zero", UsedPercent: 0}, {Label: "remaining", UsedPercent: 25}, {Label: "Month", UsedPercent: 95, ResetsAt: now.Add(90 * time.Second), Countdown: 90 * time.Second}, {Label: "clamped", UsedPercent: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windows=%+v want %+v", got, want)
	}
	later := reportWindows(report, now.Add(30*time.Second))
	if later[2].Countdown != time.Minute {
		t.Fatalf("countdown=%v", later[2].Countdown)
	}
	afterReset := reportWindows(report, now.Add(90*time.Second))
	if len(afterReset) != 3 || afterReset[2].Label != "clamped" {
		t.Fatalf("reset window remained: %+v", afterReset)
	}
}

func TestAccountCacheExpiryBackoffEvictionAndStaleCommits(t *testing.T) {
	now := time.Unix(2000000000, 0)
	c := NewCollector(Client{})
	c.Configure(CollectionSettings{})
	ticket := c.begin()
	key := [32]byte{1}
	report := Report{Buckets: []Bucket{{Label: "daily", Unit: "percent", Used: new(20.0)}}}
	outcome := Outcome{Status: "ok", Report: &report}
	if err := c.commit(ticket, key, outcome, now); err != nil {
		t.Fatal(err)
	}
	view := c.Snapshot(now)
	if len(view.Windows) != 1 || view.Windows[0].UsedPercent != 20 {
		t.Fatalf("fresh report not visible: %+v", view)
	}
	if len(c.Snapshot(view.ExpiresAt).Windows) != 0 || c.reuse(ticket, key, view.ExpiresAt) {
		t.Fatal("expired report reused")
	}
	failureKey := [32]byte{2}
	if err := c.commit(ticket, failureKey, Outcome{Status: "failed", Reason: "query-failed"}, now); err == nil {
		t.Fatal("failed query reported success")
	}
	if !c.reuse(ticket, failureKey, now.Add(time.Second)) {
		t.Fatal("failure backoff was not applied")
	}
	if c.reuse(ticket, failureKey, now.Add(time.Minute)) {
		t.Fatal("failure backoff never expired")
	}
	for i := range 40 {
		if err := c.commit(ticket, [32]byte{byte(i + 3)}, outcome, now); err != nil {
			t.Fatal(err)
		}
	}
	if c.reuse(ticket, [32]byte{3}, now) || !c.reuse(ticket, [32]byte{42}, now) {
		t.Fatal("cache did not evict oldest account while preserving newest")
	}
	successor := c.begin()
	if err := c.commit(ticket, key, outcome, now); err != ErrUsageChanged {
		t.Fatalf("superseded commit=%v", err)
	}
	if err := c.commit(successor, key, outcome, now); err != nil {
		t.Fatal(err)
	}
	c.Configure(CollectionSettings{Targets: map[string]string{"fireworks": "changed"}})
	if err := c.commit(successor, key, outcome, now); err != ErrUsageChanged {
		t.Fatalf("old settings commit=%v", err)
	}
	if len(c.Snapshot(now).Windows) != 0 {
		t.Fatal("settings replacement retained foreign report")
	}
}

func TestCollectionFailureDeadlinesSurviveReuseAndAdmitDueRetry(t *testing.T) {
	now := time.Unix(2000000000, 0)
	for _, scenario := range []struct {
		name      string
		seed      bool
		failAfter time.Duration
		deadline  time.Duration
	}{
		{name: "failure-without-report", deadline: 30 * time.Second},
		{name: "retry-before-valid-expiry", seed: true, failAfter: time.Minute, deadline: 90 * time.Second},
		{name: "valid-expiry-before-retry", seed: true, failAfter: 290 * time.Second, deadline: 300 * time.Second},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			collector := NewCollector(Client{})
			ticket := collector.begin()
			key := [32]byte{1}
			if scenario.seed {
				report := &Report{Buckets: []Bucket{{Label: "daily", Unit: "percent", Used: new(20.0)}}}
				if err := collector.commit(ticket, key, Outcome{Status: "ok", Report: report}, now); err != nil {
					t.Fatal(err)
				}
				if view := collector.Snapshot(now); !view.RefreshAt.Equal(now.Add(5 * time.Minute)) {
					t.Fatalf("success deadline: %+v", view)
				}
			}
			failedAt := now.Add(scenario.failAfter)
			if err := collector.commit(ticket, key, Outcome{Status: "failed", Reason: "query-failed"}, failedAt); err == nil {
				t.Fatal("failure reported success")
			}
			deadline := now.Add(scenario.deadline)
			if view := collector.Snapshot(failedAt); !view.RefreshAt.Equal(deadline) {
				t.Fatalf("failure deadline: %+v, want %v", view, deadline)
			}
			if !collector.reuse(ticket, key, failedAt.Add(time.Second)) || !collector.Snapshot(failedAt).RefreshAt.Equal(deadline) {
				t.Fatal("backoff reuse postponed deadline")
			}
			if scenario.name == "valid-expiry-before-retry" {
				if !collector.reuse(ticket, key, deadline) {
					t.Fatal("expiry lost pending backoff")
				}
				view := collector.Snapshot(deadline)
				if len(view.Windows) != 0 || !view.RefreshAt.Equal(failedAt.Add(30*time.Second)) {
					t.Fatalf("expired quota or displaced retry: %+v", view)
				}
			}
			if collector.reuse(ticket, key, failedAt.Add(30*time.Second)) {
				t.Fatal("due retry suppressed by cached report")
			}
		})
	}
}
