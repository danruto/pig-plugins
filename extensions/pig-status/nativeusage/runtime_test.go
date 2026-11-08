package nativeusage

import (
	"testing"
	"time"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

func TestNextWakeHonorsResetExpiryAndRefresh(t *testing.T) {
	now := time.Unix(2000000000, 0)
	cases := []struct {
		name string
		view usage.CachedUsage
		want time.Duration
	}{
		{name: "reset", view: usage.CachedUsage{ExpiresAt: now.Add(50 * time.Second), RefreshAt: now.Add(40 * time.Second), Windows: []usage.Window{{ResetsAt: now.Add(20 * time.Second)}}}, want: 20 * time.Second},
		{name: "expiry", view: usage.CachedUsage{ExpiresAt: now.Add(10 * time.Second), RefreshAt: now.Add(40 * time.Second)}, want: 10 * time.Second},
		{name: "refresh", view: usage.CachedUsage{RefreshAt: now.Add(5 * time.Second)}, want: 5 * time.Second},
		{name: "overdue", view: usage.CachedUsage{RefreshAt: now.Add(-time.Second)}, want: time.Millisecond},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := nextWake(test.view, now); got != test.want {
				t.Fatalf("next wake=%v want %v", got, test.want)
			}
		})
	}
}
