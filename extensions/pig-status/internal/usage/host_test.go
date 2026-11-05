package usage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func TestSelectionFenceRequiresCompleteAdmittedOwner(t *testing.T) {
	metadata := HostMetadata{SessionID: "session", ProviderID: "deepseek", ModelID: "selected"}
	fence := selectionFence{metadata: metadata}
	initial := hostSnapshot{SessionID: "session", SelectedModel: map[string]any{"provider": "deepseek", "id": "selected", "headers": map[string]any{"Authorization": "Bearer first"}}}
	if !errors.Is(fence.verify(initial), ErrUsageChanged) {
		t.Fatal("metadata alone became an authority fence")
	}
	foreign := hostSnapshot{SessionID: "successor", SelectedModel: initial.SelectedModel}
	if !errors.Is(fence.admit(foreign), ErrUsageChanged) {
		t.Fatal("successor was admitted")
	}
	if err := fence.admit(initial); err != nil {
		t.Fatal(err)
	}
	if err := fence.verify(initial); err != nil {
		t.Fatal(err)
	}
	changed := hostSnapshot{SessionID: "session", SelectedModel: map[string]any{"provider": "deepseek", "id": "selected", "headers": map[string]any{"Authorization": "Bearer second"}}}
	if !errors.Is(fence.verify(changed), ErrUsageChanged) || !errors.Is(fence.admit(changed), ErrUsageChanged) {
		t.Fatal("same-ID header change replaced the admitted owner")
	}
	if err := fence.verify(initial); err != nil {
		t.Fatal("failed admission changed the owner")
	}
}

func TestUsageAuthorizationPreservesOverrideSuppression(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers http.Header
		want    http.Header
		fail    bool
	}{
		{name: "key", want: http.Header{"Authorization": {"Bearer key"}}},
		{name: "explicit", headers: http.Header{"authorization": {"Bearer selected"}, "X-Private": {"never-forward"}}, want: http.Header{"authorization": {"Bearer selected"}}},
		{name: "empty", headers: http.Header{"Authorization": {""}}, fail: true},
		{name: "null", headers: http.Header{"Authorization": nil}, fail: true},
		{name: "duplicate", headers: http.Header{"Authorization": {"one"}, "authorization": {"two"}}, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := usageAuthorization(RequestAuth{APIKey: "key", Headers: test.headers})
			if (err != nil) != test.fail || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("authorization = %#v, %v", got, err)
			}
		})
	}
}

func TestRequestFromHostPreservesNullableAndEmptyHeaders(t *testing.T) {
	var auth hostAuthResult
	auth.Auth.Headers = map[string]*string{"Authorization": nil, "X-Empty": new("")}
	got := requestFromHost(&auth)
	if !reflect.DeepEqual(got.Headers, http.Header{"Authorization": nil, "X-Empty": {""}}) {
		t.Fatalf("headers = %#v", got.Headers)
	}
	auth.Auth.Headers = nil
	if requestFromHost(&auth).Headers != nil {
		t.Fatal("absent headers became an object")
	}
	auth.Auth.Headers = map[string]*string{}
	if got := requestFromHost(&auth).Headers; got == nil || len(got) != 0 {
		t.Fatal("empty header object lost")
	}
}

func TestEffectiveHostAuthDoesNotBorrowAnotherModelsOverride(t *testing.T) {
	provider, selected := &hostAuthResult{}, &hostAuthResult{}
	provider.Auth.APIKey = "provider"
	selected.Auth.APIKey = "selected"
	snapshot := hostSnapshot{Provider: "deepseek", SelectedModel: map[string]any{"provider": "deepseek"}, ProviderAuth: provider, EffectiveAuth: selected}
	if got := requestFromHost(effectiveHostAuth(snapshot)).APIKey; got != "selected" {
		t.Fatalf("current auth = %q", got)
	}
	snapshot.SelectedModel["provider"] = "openai"
	if got := requestFromHost(effectiveHostAuth(snapshot)).APIKey; got != "provider" {
		t.Fatalf("other provider auth = %q", got)
	}
}

func TestProviderSelectionRequiresExplicitScope(t *testing.T) {
	inventory := HostInventory{Providers: map[string]HostProvider{"deepseek": {Configured: true}, "fireworks": {Configured: true}, "unrelated": {Configured: true}}}
	for _, test := range []struct {
		selection Selection
		want      []string
		fail      bool
	}{
		{selection: Selection{Mode: "current"}, want: []string{"openai"}},
		{selection: Selection{Mode: "provider", ProviderID: "fireworks"}, want: []string{"fireworks"}},
		{selection: Selection{Mode: "all"}, want: []string{"openai", "deepseek", "fireworks"}},
		{selection: Selection{}, fail: true},
		{selection: Selection{Mode: "current", ProviderID: "deepseek"}, fail: true},
		{selection: Selection{Mode: "provider"}, fail: true},
		{selection: Selection{Mode: "all", ProviderID: "deepseek"}, fail: true},
	} {
		got, err := selectProviders(test.selection, "openai", inventory)
		if (err != nil) != test.fail || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("selection %#v = %v, %v", test.selection, got, err)
		}
	}
}

func TestBillingTargetIdentityNeverSubstitutes(t *testing.T) {
	targets := []Target{{ID: "first", Label: "same"}, {ID: "second", Label: "same"}}
	for _, id := range []string{"", "gone", "../first", "same"} {
		if selectedTarget(targets, id) {
			t.Fatalf("substituted %q", id)
		}
	}
	if !selectedTarget(targets, "second") || !selectedTarget(targets, "first") {
		t.Fatal("exact choices unavailable")
	}
}

func ExampleDispatcher_Query() {
	_, err := (Dispatcher{}).Query(context.Background(), Selection{Mode: "current"})
	fmt.Println(err)
	// Output: usage requires a positive query budget
}
