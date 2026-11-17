package usage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
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
			expected, expectedError := consumerGoCaseExpectation(t, consumerGoCaseExpectationInput{name: f.Name, payload: f.Payload, pinned: f.Expected, pinnedError: expectedError})
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
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("result = %s, want pinned %v", encoded, expected)
			}
		})
	}
}

const consumerGoCaseKimiCurrencyError = "Kimi Coding usage endpoint returned no displayable usage data."

const consumerGoCasePlanLabelBytes = 30

type consumerGoCaseMapping func(t testing.TB, payload map[string]any, pinned any) (any, string)

func consumerGoCaseMappings() map[string]consumerGoCaseMapping {
	return map[string]consumerGoCaseMapping{
		"command-code:unicode-plan-individual-ßpecial":                         consumerGoCasePlanLabel,
		"command-code:unicode-plan-individual-ﬃle":                             consumerGoCasePlanLabel,
		"command-code:unicode-plan-individual-ŉew":                             consumerGoCasePlanLabel,
		"command-code:unicode-plan-individual-ΐota":                            consumerGoCasePlanLabel,
		"command-code:unicode-plan-individual-ﬄaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": consumerGoCasePlanLabel,
		"kimi-coding:unicode-currency-ßd":                                      consumerGoCaseKimiCurrency,
		"kimi-coding:unicode-currency-ﬃ":                                       consumerGoCaseKimiCurrency,
		"kimi-coding:unicode-currency-ﬄ":                                       consumerGoCaseKimiCurrency,
		"kimi-coding:expanded-limit-currency":                                  consumerGoCaseKimiLimitCurrency,
	}
}

type consumerGoCaseExpectationInput struct {
	name        string
	payload     map[string]any
	pinned      any
	pinnedError string
}

func consumerGoCaseExpectation(tb testing.TB, in consumerGoCaseExpectationInput) (any, string) {
	tb.Helper()
	mapping, ok := consumerGoCaseMappings()[in.name]
	if !ok {
		return in.pinned, in.pinnedError
	}
	return mapping(tb, in.payload, in.pinned)
}

func consumerGoCasePlanLabel(tb testing.TB, payload map[string]any, pinned any) (any, string) {
	tb.Helper()
	planID := consumerGoCaseString(tb, payload, "subscription", "data", "planId")
	fields := consumerGoCaseObject(tb, pinned)
	metrics, ok := fields["metrics"].([]any)
	if !ok {
		tb.Fatalf("consumer fixture expectation has no metrics: %v", pinned)
	}
	for _, metric := range metrics {
		entry := consumerGoCaseObject(tb, metric)
		if entry["id"] == "plan" {
			entry["value"] = consumerGoCaseLabel(strings.TrimPrefix(planID, "individual-"))
		}
	}
	return fields, ""
}

func consumerGoCaseKimiCurrency(tb testing.TB, payload map[string]any, _ any) (any, string) {
	tb.Helper()
	used := consumerGoCaseString(tb, payload, "boosterWallet", "monthlyUsed", "currency")
	if consumerGoCaseCurrencyCode(used) {
		tb.Fatalf("fixture currency %q is a displayable code under Go case mapping", used)
	}
	return nil, consumerGoCaseKimiCurrencyError
}

func consumerGoCaseKimiLimitCurrency(tb testing.TB, payload map[string]any, pinned any) (any, string) {
	tb.Helper()
	used := consumerGoCaseString(tb, payload, "boosterWallet", "monthlyUsed", "currency")
	limit := consumerGoCaseString(tb, payload, "boosterWallet", "monthlyChargeLimit", "currency")
	if !consumerGoCaseCurrencyCode(used) || consumerGoCaseCurrencyCode(limit) {
		tb.Fatalf("fixture currencies used=%q limit=%q do not exercise an undisplayable limit currency", used, limit)
	}
	fields := consumerGoCaseObject(tb, pinned)
	metrics, ok := fields["metrics"].([]any)
	if !ok {
		tb.Fatalf("consumer fixture expectation has no metrics: %v", pinned)
	}
	var displayed []any
	for _, metric := range metrics {
		if consumerGoCaseObject(tb, metric)["id"] != "booster-monthly-limit" {
			displayed = append(displayed, metric)
		}
	}
	fields["metrics"] = displayed
	return fields, ""
}

func consumerGoCaseLabel(segment string) string {
	first, size := utf8.DecodeRuneInString(segment)
	label := strings.ToUpper(string(first)) + segment[size:]
	if len(label) <= consumerGoCasePlanLabelBytes {
		return label
	}
	cut := consumerGoCasePlanLabelBytes
	for cut > 0 && !utf8.RuneStart(label[cut]) {
		cut--
	}
	return label[:cut] + "…"
}

func consumerGoCaseCurrencyCode(currency string) bool {
	upper := strings.ToUpper(currency)
	if len(upper) != 3 {
		return false
	}
	for _, letter := range upper {
		if letter < 'A' || letter > 'Z' {
			return false
		}
	}
	return true
}

func consumerGoCaseObject(tb testing.TB, value any) map[string]any {
	tb.Helper()
	fields, ok := value.(map[string]any)
	if !ok {
		tb.Fatalf("consumer fixture value %v is not an object", value)
	}
	return fields
}

func consumerGoCaseString(tb testing.TB, payload map[string]any, path ...string) string {
	tb.Helper()
	current := payload
	for _, key := range path[:len(path)-1] {
		current = consumerGoCaseObject(tb, current[key])
	}
	value, ok := current[path[len(path)-1]].(string)
	if !ok {
		tb.Fatalf("consumer fixture has no string at %v", path)
	}
	return value
}

func ExampleSchedulable() {
	fmt.Println(Schedulable("command-code"), Schedulable("opencode-go"), Schedulable("kimi-coding"))
	fmt.Println(Schedulable("xai"))
	// Output:
	// true true true
	// false
}
