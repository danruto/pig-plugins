package nativeusage

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
)

func TestChoiceAcceptsOnlyStringDialogResults(t *testing.T) {
	cases := []struct {
		name   string
		result any
		value  string
		chosen bool
	}{
		{name: "selected option", result: "Refresh current provider", value: "Refresh current provider", chosen: true},
		{name: "empty selection", result: "", chosen: true},
		{name: "nil result", result: nil},
		{name: "number result", result: 7},
		{name: "object result", result: map[string]any{"value": "Close"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value, chosen := choice(test.result)
			if value != test.value || chosen != test.chosen {
				t.Fatalf("choice=%q,%t want %q,%t", value, chosen, test.value, test.chosen)
			}
		})
	}
}

func TestCallContextCarriesTheHandlerCall(t *testing.T) {
	call := sdk.Context{}
	ctx, done := callContext(call)
	defer done()

	if from, ok := callFrom(ctx); !ok || from != call {
		t.Fatalf("callFrom=%v,%t want the handler call", from, ok)
	}
	if _, ok := callFrom(context.Background()); ok {
		t.Fatal("a context without a handler call reported one")
	}
	if _, err := currentScope(context.Background()); !errors.Is(err, errNoHostCall) {
		t.Fatalf("scope without a handler call error = %v, want %v", err, errNoHostCall)
	}
	if _, err := identifyAccount(context.Background()); !errors.Is(err, errNoHostCall) {
		t.Fatalf("account without a handler call error = %v, want %v", err, errNoHostCall)
	}
}

func TestFastScopeReturnsTheContextErrorBeforeReadingTheHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := fastScope(ctx, sdk.Context{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("scope error = %v, want %v", err, context.Canceled)
	}
}
