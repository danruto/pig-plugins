package nativeusage

import "testing"

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
