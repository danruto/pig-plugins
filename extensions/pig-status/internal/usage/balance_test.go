package usage

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestBalancesMatchPinnedSource(t *testing.T) {
	data, err := os.ReadFile("testdata/reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Fixtures []struct {
			Name       string         `json:"name"`
			ProviderID string         `json:"providerID"`
			CapturedAt int64          `json:"capturedAt"`
			Payload    map[string]any `json:"payload"`
			Expected   map[string]any `json:"expected"`
			Error      string         `json:"error"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Fixtures) == 0 {
		t.Fatal("reference has no fixtures")
	}
	for _, fixture := range reference.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			report, err := normalizeReport(fixture.ProviderID, fixture.Payload, fixture.CapturedAt)
			if fixture.Error != "" {
				if err == nil || err.Error() != fixture.Error {
					t.Fatalf("error = %v, want pinned error %q", err, fixture.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, fixture.Expected) {
				t.Fatalf("report = %s, want pinned report %v", encoded, fixture.Expected)
			}
		})
	}
}
