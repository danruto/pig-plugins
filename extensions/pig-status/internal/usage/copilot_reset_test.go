package usage

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestCopilotResetsMatchPinnedSource(t *testing.T) {
	data, err := os.ReadFile("testdata/copilot-resets.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Fixtures []struct {
			Name     string         `json:"name"`
			Payload  map[string]any `json:"payload"`
			ResetsAt *float64       `json:"resetsAt"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Fixtures) == 0 {
		t.Fatal("reset reference has no fixtures")
	}
	for _, tt := range reference.Fixtures {
		t.Run(tt.Name, func(t *testing.T) {
			report, err := normalizeCopilot(tt.Payload, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Buckets) != 1 {
				t.Fatalf("got %d buckets, want one Copilot quota", len(report.Buckets))
			}
			if !reflect.DeepEqual(report.Buckets[0].ResetsAt, tt.ResetsAt) {
				encoded, err := json.Marshal(report.Buckets[0])
				if err != nil {
					t.Fatal(err)
				}
				t.Fatalf("bucket = %s, differs from pinned reset in fixture %q", encoded, tt.Name)
			}
		})
	}
}
