package usage

import (
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBillingNormalizationMatchesPinnedSource(t *testing.T) {
	data, err := os.ReadFile("testdata/billing-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Fixtures []struct {
			Name        string         `json:"name"`
			ProviderID  string         `json:"providerID"`
			Kind        string         `json:"kind"`
			AccountID   string         `json:"accountID"`
			CapturedAt  int64          `json:"capturedAt"`
			Payload     map[string]any `json:"payload"`
			Expected    any            `json:"expected"`
			Error       string         `json:"error"`
			NativeError string         `json:"nativeError"`
		} `json:"fixtures"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Fixtures) == 0 {
		t.Fatal("billing reference has no fixtures")
	}
	for _, fixture := range reference.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			var result any
			var err error
			switch {
			case fixture.Kind == "accounts":
				result, err = normalizeFireworksAccounts(fixture.Payload)
			case fixture.ProviderID == "fireworks":
				result, err = normalizeFireworks(fixture.Payload, fixture.AccountID, fixture.CapturedAt)
			default:
				result, err = normalizeReport(fixture.ProviderID, fixture.Payload, fixture.CapturedAt)
			}
			expectedError := fixture.Error
			if fixture.NativeError != "" {
				expectedError = fixture.NativeError
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
			if !reflect.DeepEqual(got, fixture.Expected) {
				t.Fatalf("result = %s, want pinned %v", encoded, fixture.Expected)
			}
		})
	}
}

func TestBillingWindowsKeepProviderTimeSemantics(t *testing.T) {
	for _, tt := range []struct {
		at                           string
		fireworksStart, fireworksEnd string
		basetenStart, basetenEnd     string
	}{
		{"2024-03-01T00:05:06.789+02:00", "2024-01-31T00:00:00Z", "2024-03-01T00:00:00Z", "2024-01-30T22:05:06.789Z", "2024-02-29T22:05:06.789Z"},
		{"2025-01-01T23:59:59.001-05:00", "2024-12-04T00:00:00Z", "2025-01-03T00:00:00Z", "2024-12-03T04:59:59.001Z", "2025-01-02T04:59:59.001Z"},
	} {
		t.Run(tt.at, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339Nano, tt.at)
			if err != nil {
				t.Fatal(err)
			}
			fireworks, err := url.Parse(fireworksBillingURL("selected-account", at))
			if err != nil {
				t.Fatal(err)
			}
			if fireworks.Scheme != "https" || fireworks.Host != "api.fireworks.ai" || fireworks.Path != "/v1/accounts/selected-account/billing/summary" || !reflect.DeepEqual(fireworks.Query(), url.Values{"startTime": {tt.fireworksStart}, "endTime": {tt.fireworksEnd}}) {
				t.Fatalf("incorrect Fireworks billing window: %s", fireworks)
			}
			baseten, err := url.Parse(basetenBillingURL(at))
			if err != nil {
				t.Fatal(err)
			}
			if baseten.Host != "api.baseten.co" || !reflect.DeepEqual(baseten.Query(), url.Values{"start_date": {tt.basetenStart}, "end_date": {tt.basetenEnd}}) {
				t.Fatalf("incorrect Baseten billing window: %s", baseten)
			}
		})
	}
}

func TestFireworksPageTokenIsOpaqueAndUTF16Bounded(t *testing.T) {
	for _, token := range []string{"a+b&next=/account?x=1#fragment", strings.Repeat("a", 512), strings.Repeat("😀", 256)} {
		got, err := fireworksNextPageToken(token)
		if err != nil || got != token {
			t.Fatalf("opaque page token was changed or rejected: %q, %v", got, err)
		}
	}
	for _, token := range []any{"", true, float64(1), strings.Repeat("a", 513), strings.Repeat("😀", 257)} {
		if _, err := fireworksNextPageToken(token); err == nil {
			t.Fatalf("invalid page token accepted: %v", token)
		}
	}
	if got, err := fireworksNextPageToken(nil); err != nil || got != "" {
		t.Fatalf("absent token = %q, %v", got, err)
	}
}
