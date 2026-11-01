package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestRegionalNormalizationMatchesPinnedSource(t *testing.T) {
	data, err := os.ReadFile("testdata/regional-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Fixtures []struct {
			Name               string         `json:"name"`
			Kind               string         `json:"kind"`
			ProviderID         string         `json:"providerID"`
			CapturedAt         int64          `json:"capturedAt"`
			Payload            map[string]any `json:"payload"`
			Plan               *zaiPlan       `json:"plan"`
			UsageKind          string         `json:"usageKind"`
			Key                string         `json:"key"`
			Status             int            `json:"status"`
			Body               string         `json:"body"`
			SelectedProviderID string         `json:"selectedProviderID"`
			Source             string         `json:"source"`
			SourceLabel        *string        `json:"sourceLabel"`
			Expected           any            `json:"expected"`
			NativeError        string         `json:"nativeError"`
			Error              string         `json:"error"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Fixtures) == 0 {
		t.Fatal("regional reference has no cases")
	}
	for _, f := range reference.Fixtures {
		t.Run(f.Name, func(t *testing.T) {
			var result any
			var err error
			switch f.Kind {
			case "usage-kind":
				result = miniMaxUsageKind(f.Key)
			case "plan":
				result = normalizeZaiSubscription(f.Payload)
			case "payload-error":
				result = zaiPayloadError(f.Payload)
			case "response-error":
				result = zaiResponseError(f.Status, []byte(f.Body))
			case "auth":
				result = moonshotAuthAllowed(f.ProviderID, ResolvedAuth{SelectedProviderID: f.SelectedProviderID, Source: f.Source, SourceLabel: f.SourceLabel})
			default:
				switch f.ProviderID {
				case "minimax", "minimax-cn":
					result, err = normalizeMiniMax(f.ProviderID, f.UsageKind, f.Payload, f.CapturedAt)
				case "zai", "zai-coding-cn":
					result, err = normalizeZai(f.ProviderID, f.Payload, f.CapturedAt, f.Plan)
				default:
					result, err = normalizeReport(f.ProviderID, f.Payload, f.CapturedAt)
				}
			}
			expectedError := f.Error
			if f.NativeError != "" {
				expectedError = f.NativeError
			}
			if expectedError != "" {
				if err == nil || err.Error() != expectedError {
					t.Fatalf("error = %v, want %q", err, expectedError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var got any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, f.Expected) {
				t.Fatalf("result = %s, want pinned %v", encoded, f.Expected)
			}
		})
	}
}

func ExampleResolvedAuth() {
	label := "MOONSHOT_CN_API_KEY"
	auth := ResolvedAuth{SelectedProviderID: "moonshotai", Source: "environment", SourceLabel: &label}
	fmt.Println(moonshotAuthAllowed("moonshotai-cn", auth))
	label = "MOONSHOT_API_KEY"
	fmt.Println(moonshotAuthAllowed("moonshotai-cn", auth))
	// Output:
	// true
	// false
}
