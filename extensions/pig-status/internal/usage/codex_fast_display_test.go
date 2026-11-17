package usage

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestFastAvailabilityReportsDirectoryFailureText(t *testing.T) {
	tests := []struct {
		name    string
		handler http.RoundTripper
		want    string
	}{
		{
			name: "transport failure",
			handler: queryRoundTripper(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("synthetic transport failure")
			}),
			want: "Codex Fast capability is unknown: Codex model directory request failed.",
		},
		{
			name:    "rate limited",
			handler: acceptFastStatus(http.StatusTooManyRequests, `{"error":"synthetic"}`),
			want:    "Codex Fast capability is unknown: Codex model directory returned 429.",
		},
		{
			name:    "server error",
			handler: acceptFastStatus(http.StatusInternalServerError, `{"error":"synthetic"}`),
			want:    "Codex Fast capability is unknown: Codex model directory returned 500.",
		},
		{
			name:    "unauthorized",
			handler: acceptFastStatus(http.StatusUnauthorized, `{"error":"synthetic"}`),
			want:    "Codex Fast capability is unknown: Codex model directory returned 401.",
		},
		{
			name:    "redirected response",
			handler: acceptFastStatus(http.StatusFound, `{"models":[]}`),
			want:    "Codex Fast capability is unknown: Codex model directory refused a redirected response.",
		},
		{
			name:    "oversized response",
			handler: acceptFastStatus(http.StatusOK, `{"models":[]}`+strings.Repeat(" ", 8<<20)),
			want:    "Codex Fast capability is unknown: Codex model directory response exceeded 8388608 bytes.",
		},
		{
			name:    "invalid json",
			handler: acceptFastStatus(http.StatusOK, `{"models":`),
			want:    "Codex Fast capability is unknown: Codex model directory returned invalid JSON.",
		},
		{
			name:    "non object payload",
			handler: acceptFastStatus(http.StatusOK, `["synthetic"]`),
			want:    "Codex Fast capability is unknown: Codex model directory response was not an object.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := acceptFastNew(t, "", tt.handler, nil)

			_, ok := fixture.fast.RefreshCapabilities(t.Context(), true)
			available := fixture.fast.Availability(acceptFastModel(acceptFastModelID))

			acceptEqual(t, true, ok, "capability refresh")
			acceptEqual(t, FastUnknown, available.Kind, "availability kind")
			acceptEqual(t, tt.want, available.Reason, "availability reason")
		})
	}
}
