package usage

import (
	"net/http"
	"reflect"
	"testing"
)

func TestValidatedHeadersPreserveExplicitAuthorization(t *testing.T) {
	for _, tt := range []struct {
		name          string
		provider      string
		headers       http.Header
		authorization string
	}{
		{"canonical bearer", "deepseek", http.Header{"Authorization": {"Bearer synthetic-token"}}, "Bearer synthetic-token"},
		{"case insensitive override", "deepseek", http.Header{"authorization": {"bearer\t synthetic-token"}}, "Bearer synthetic-token"},
		{"opaque override", "moonshotai", http.Header{"AUTHORIZATION": {"Custom synthetic-token"}}, "Custom synthetic-token"},
		{"basic override", "vercel-ai-gateway", http.Header{"Authorization": {"Basic synthetic-token"}}, "Basic synthetic-token"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validatedHeaders(tt.provider, tt.headers)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, http.Header{"Authorization": {tt.authorization}}) {
				t.Fatalf("headers = %v, want one canonical explicit authorization", got)
			}
		})
	}
}

func TestValidatedHeadersRejectAmbiguousOrEmptyOverrides(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider string
		headers  http.Header
	}{
		{"missing", "deepseek", nil},
		{"removed", "moonshotai", http.Header{"Authorization": nil}},
		{"empty", "vercel-ai-gateway", http.Header{"Authorization": {""}}},
		{"whitespace", "moonshotai-cn", http.Header{"Authorization": {" \t "}}},
		{"duplicate values", "deepseek", http.Header{"Authorization": {"Bearer first", "Bearer second"}}},
		{"duplicate case", "deepseek", http.Header{"Authorization": {"Bearer first"}, "authorization": {"Bearer second"}}},
		{"header injection", "deepseek", http.Header{"Authorization": {"Bearer token\r\nX-Injected: value"}}},
		{"terminal control", "moonshotai", http.Header{"Authorization": {"Bearer \x1bsecret"}}},
		{"non bearer deepseek", "deepseek", http.Header{"Authorization": {"Basic synthetic-token"}}},
		{"empty bearer", "deepseek", http.Header{"Authorization": {"Bearer \t"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validatedHeaders(tt.provider, tt.headers)
			if err == nil || got != nil {
				t.Fatal("invalid authorization accepted")
			}
		})
	}
}
