package usage

import (
	"testing"
	"time"
)

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
