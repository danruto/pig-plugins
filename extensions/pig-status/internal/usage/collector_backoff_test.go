package usage

import (
	"testing"
	"time"
)

func TestCollectorPublishBackoff_SupersededTicketKeepsPublishedState(t *testing.T) {
	now := time.Unix(2000000000, 0)
	collector := NewCollector(Client{})
	collector.Configure(CollectionSettings{})
	superseded := collector.begin()
	supersededKey := [32]byte{1}
	if err := collector.commit(superseded, supersededKey, Outcome{Status: "failed", Reason: "selection-required"}, now); err == nil {
		t.Fatal("failed outcome reported success")
	}
	successor := collector.begin()
	successorKey := [32]byte{2}
	if err := collector.commit(successor, successorKey, Outcome{Status: "ok", Report: lifecycleReport()}, now); err != nil {
		t.Fatalf("successor report: %v", err)
	}
	successorView := collector.Snapshot(now)

	collector.publishBackoff(superseded, supersededKey)
	published := collector.Snapshot(now)

	if len(successorView.Windows) != 1 {
		t.Fatalf("successor windows = %+v", successorView.Windows)
	}
	if len(published.Windows) != 1 {
		t.Fatalf("windows after superseded backoff publish = %+v", published.Windows)
	}
	if published.Reason != successorView.Reason {
		t.Fatalf("reason after superseded backoff publish = %q, want %q", published.Reason, successorView.Reason)
	}
	if !published.RefreshAt.Equal(successorView.RefreshAt) {
		t.Fatalf("refresh deadline after superseded backoff publish = %v, want %v", published.RefreshAt, successorView.RefreshAt)
	}
}
