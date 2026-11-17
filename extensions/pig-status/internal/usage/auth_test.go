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
