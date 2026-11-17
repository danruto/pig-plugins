package nativeusage

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestEmitGateHoldsOneDispatchPerReadiness(t *testing.T) {
	held := errors.New("readiness still held")
	release := make(chan struct{})
	started := make(chan emitKey, 3)
	var runs atomic.Int32
	dispatch := func(key emitKey) func() error {
		return func() error {
			runs.Add(1)
			started <- key
			<-release
			return held
		}
	}
	readiness := emitKey{channel: "oauth:credential-readiness:v1", provider: "deepseek", session: "01a1e221-69f7-75ca-9cf0-06f11fba16ea"}
	sibling := emitKey{channel: readiness.channel, provider: "openrouter", session: readiness.session}
	gate := newEmitGate()

	first := gate.join(readiness, dispatch(readiness))
	if got := <-started; got != readiness {
		t.Fatalf("the first dispatch ran for %+v, want %+v", got, readiness)
	}
	again := gate.join(readiness, dispatch(readiness))
	other := gate.join(sibling, dispatch(sibling))
	if got := <-started; got != sibling {
		t.Fatalf("the second dispatch ran for %+v, want %+v", got, sibling)
	}
	close(release)
	<-first.done
	<-other.done

	if again != first {
		t.Fatal("a readiness collection for an in-flight dispatch started another dispatch")
	}
	if other == first {
		t.Fatal("a readiness collection for another provider joined the first provider's dispatch")
	}
	if !errors.Is(first.err, held) || !errors.Is(other.err, held) {
		t.Fatalf("the joined dispatches settled with %v and %v, want %v on both", first.err, other.err, held)
	}
	fresh := gate.join(readiness, dispatch(readiness))
	if fresh == first {
		t.Fatal("a settled dispatch stayed in the gate")
	}
	<-started
	<-fresh.done
	if runs.Load() != 3 {
		t.Fatalf("the gate ran %d dispatches, want 3", runs.Load())
	}
}

func TestAuthRevisionTracksSessionModelAndAuth(t *testing.T) {
	baseline := authIdentity{
		Session:       "01a1e221-69f7-75ca-9cf0-06f11fba16ea",
		Selected:      map[string]any{"id": "fixture", "provider": "openrouter", "maxTokens": float64(128)},
		Model:         map[string]any{"id": "fixture", "provider": "openrouter"},
		ProviderAuth:  map[string]any{"auth": map[string]any{"apiKey": "synthetic-lifecycle"}},
		EffectiveAuth: map[string]any{"auth": map[string]any{"apiKey": "synthetic-lifecycle"}},
	}
	want, err := authRevision(baseline)
	if err != nil {
		t.Fatalf("baseline revision: %v", err)
	}
	cases := []struct {
		name   string
		change func(*authIdentity)
		equal  bool
	}{
		{name: "equal inputs", change: func(*authIdentity) {}, equal: true},
		{name: "rotated session", change: func(i *authIdentity) { i.Session = "01a1e221-69f7-75ca-9cf0-06f11fba16eb" }},
		{name: "changed selected model", change: func(i *authIdentity) {
			i.Selected = map[string]any{"id": "fixture", "provider": "openrouter", "maxTokens": float64(256)}
		}},
		{name: "changed requested model", change: func(i *authIdentity) {
			i.Model = map[string]any{"id": "fixture", "provider": "openrouter", "maxTokens": float64(256)}
		}},
		{name: "rotated provider auth", change: func(i *authIdentity) {
			i.ProviderAuth = map[string]any{"auth": map[string]any{"apiKey": "synthetic-rotated"}}
		}},
		{name: "model authorization override", change: func(i *authIdentity) {
			i.EffectiveAuth = map[string]any{"auth": map[string]any{"apiKey": "synthetic-lifecycle", "headers": map[string]any{"Authorization": "Bearer synthetic-selected"}}}
		}},
		{name: "absent effective auth", change: func(i *authIdentity) { i.EffectiveAuth = nil }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			identity := baseline
			test.change(&identity)
			revision, err := authRevision(identity)
			if err != nil {
				t.Fatalf("revision: %v", err)
			}
			if (revision == want) != test.equal {
				t.Fatalf("revision=%q baseline=%q equal=%t", revision, want, test.equal)
			}
		})
	}
}

func TestObservableFailsClosedForOAuthCollection(t *testing.T) {
	apiKey := map[string]any{"auth": map[string]any{"apiKey": "synthetic-lifecycle"}, "source": "synthetic"}
	oauth := map[string]any{"auth": map[string]any{"apiKey": "synthetic-access"}, "source": "OAuth"}
	cases := []struct {
		name         string
		kind         string
		usingOAuth   bool
		providerAuth map[string]any
		refused      bool
	}{
		{name: "API-key readiness", kind: "readiness", providerAuth: apiKey},
		{name: "unauthenticated readiness", kind: "readiness"},
		{name: "stored OAuth readiness", kind: "readiness", usingOAuth: true, providerAuth: apiKey, refused: true},
		{name: "resolved OAuth readiness", kind: "readiness", providerAuth: oauth, refused: true},
		{name: "API-key offers", kind: "offers", providerAuth: apiKey, refused: true},
		{name: "OAuth offers", kind: "offers", usingOAuth: true, providerAuth: oauth, refused: true},
		{name: "unknown kind", kind: "inventory", providerAuth: apiKey, refused: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := observable(test.kind, test.usingOAuth, test.providerAuth)
			if errors.Is(err, errUnobservableCollection) != test.refused {
				t.Fatalf("err=%v refused=%t", err, test.refused)
			}
		})
	}
}
