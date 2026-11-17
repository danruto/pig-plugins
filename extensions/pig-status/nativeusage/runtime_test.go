package nativeusage

import (
	"testing"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

func TestNextWakeHonorsResetExpiryAndRefresh(t *testing.T) {
	now := time.Unix(2000000000, 0)
	cases := []struct {
		name string
		view usage.Cached
		want time.Duration
	}{
		{name: "reset", view: usage.Cached{ExpiresAt: now.Add(50 * time.Second), RefreshAt: now.Add(40 * time.Second), Windows: []usage.Window{{ResetsAt: now.Add(20 * time.Second)}}}, want: 20 * time.Second},
		{name: "expiry", view: usage.Cached{ExpiresAt: now.Add(10 * time.Second), RefreshAt: now.Add(40 * time.Second)}, want: 10 * time.Second},
		{name: "refresh", view: usage.Cached{RefreshAt: now.Add(5 * time.Second)}, want: 5 * time.Second},
		{name: "overdue", view: usage.Cached{RefreshAt: now.Add(-time.Second)}, want: time.Millisecond},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := nextWake(test.view, now); got != test.want {
				t.Fatalf("next wake=%v want %v", got, test.want)
			}
		})
	}
}

func TestConfigurePublishesOnlyWhenTheCollectionSettingsChange(t *testing.T) {
	initial := usage.CollectionSettings{Targets: map[string]string{"fireworks": "first"}}
	cases := []struct {
		name      string
		next      usage.CollectionSettings
		published int
	}{
		{name: "identical settings", next: usage.CollectionSettings{Targets: map[string]string{"fireworks": "first"}}},
		{name: "different target", next: usage.CollectionSettings{Targets: map[string]string{"fireworks": "second"}}, published: 1},
		{name: "different codex companion toggle", next: usage.CollectionSettings{Targets: map[string]string{"fireworks": "first"}, UseCodexUsage: true}, published: 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var published int
			runtime, err := Register(sdk.New("lifecycle"), Options{Settings: initial, Publish: func(usage.Cached) { published++ }})
			if err != nil {
				t.Fatalf("register: %v", err)
			}
			t.Cleanup(func() { _ = runtime.Close() })

			if err := runtime.Configure(test.next); err != nil {
				t.Fatalf("configure: %v", err)
			}

			if published != test.published {
				t.Fatalf("configure published %d views, want %d", published, test.published)
			}
		})
	}
}
