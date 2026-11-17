package status

import "testing"

func TestDecodeSummaryRejectsWrongFieldTypes(t *testing.T) {
	for _, tt := range []struct {
		name string
		data any
	}{
		{name: "payload not an object", data: []any{}},
		{name: "non-integral input", data: map[string]any{"input": 1.5}},
		{name: "non-numeric output", data: map[string]any{"output": "2000"}},
		{name: "non-numeric cacheRead", data: map[string]any{"cacheRead": "100"}},
		{name: "non-numeric cacheWrite", data: map[string]any{"cacheWrite": true}},
		{name: "non-numeric cost", data: map[string]any{"cost": "0.216"}},
		{name: "non-boolean subscription", data: map[string]any{"subscription": "yes"}},
		{name: "windows not an array", data: map[string]any{"windows": map[string]any{}}},
		{name: "window not an object", data: map[string]any{"windows": []any{"5h"}}},
		{name: "non-string window label", data: map[string]any{"windows": []any{map[string]any{"label": 5.0}}}},
		{name: "non-numeric usedPercent", data: map[string]any{"windows": []any{map[string]any{"label": "5h", "usedPercent": "39"}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if summary, err := decodeSummary(tt.data); err == nil {
				t.Fatalf("malformed payload decoded to %+v", summary)
			}
		})
	}
}
