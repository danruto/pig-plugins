package usage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"testing"
)

func TestConsumerNormalizationMatchesPinnedSource(t *testing.T) {
	data, err := os.ReadFile("testdata/consumer-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Fixtures []struct {
			Name       string         `json:"name"`
			Kind       string         `json:"kind"`
			ProviderID string         `json:"providerID"`
			CapturedAt int64          `json:"capturedAt"`
			Payload    map[string]any `json:"payload"`
			Tier       any            `json:"tier"`
			Effective  struct {
				APIKey  string            `json:"apiKey"`
				Headers map[string]string `json:"headers"`
			} `json:"effective"`
			Candidates  []any  `json:"candidates"`
			Expected    any    `json:"expected"`
			Error       string `json:"error"`
			NativeError string `json:"nativeError"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Fixtures) == 0 {
		t.Fatal("consumer reference has no cases")
	}
	for _, f := range reference.Fixtures {
		t.Run(f.Name, func(t *testing.T) {
			var result any
			var err error
			switch f.Kind {
			case "auth":
				headers := http.Header{}
				for name, value := range f.Effective.Headers {
					headers.Set(name, value)
				}
				var identity subscriptionIdentity
				identity, err = resolveXai(OAuthSnapshot{Ready: true, ModelBaseURL: "https://api.x.ai/v1", Effective: RequestAuth{APIKey: f.Effective.APIKey, Headers: headers}, Candidates: f.Candidates})
				if err == nil {
					result = identity.headers.Get("Authorization")
				}
			case "scheduling":
				ids, ok := f.Expected.([]any)
				if !ok {
					t.Fatal("invalid scheduling reference")
				}
				for _, raw := range ids {
					id, ok := raw.(string)
					if !ok || !Schedulable(id) {
						t.Fatalf("schedulable provider %v rejected", raw)
					}
				}
				if Schedulable("xai") || Schedulable("unknown") {
					t.Fatal("menu-only or unsupported provider admitted to scheduling")
				}
				return
			default:
				if f.ProviderID == "xai" {
					result, err = normalizeXai(f.Payload, f.Tier, f.CapturedAt)
				} else {
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

func ExampleSchedulable() {
	fmt.Println(Schedulable("command-code"), Schedulable("opencode-go"), Schedulable("kimi-coding"))
	fmt.Println(Schedulable("xai"))
	// Output:
	// true true true
	// false
}
