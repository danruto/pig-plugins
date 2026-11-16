package usage

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCodexResetCredits(t *testing.T) {
	t.Parallel()
	sooner := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC).Unix()
	later := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC).Unix()
	truncatedTitle := strings.Repeat("é", 160)
	credit := func(id, status, resetType string) map[string]any {
		return map[string]any{"id": id, "status": status, "reset_type": resetType}
	}
	credits := make([]any, 0, 40)
	cappedIDs := make([]string, 0, 32)
	for index := range 40 {
		credits = append(credits, credit("credit-"+strconv.Itoa(index), "available", "codex_rate_limits"))
		if index < 32 {
			cappedIDs = append(cappedIDs, "credit-"+strconv.Itoa(index))
		}
	}
	for _, test := range []struct {
		name        string
		payload     map[string]any
		wantCount   int
		wantIDs     []string
		wantOptions []CodexResetOption
		wantErr     error
	}{
		{
			name: "keeps available codex credits sorted with no-expiry last",
			payload: map[string]any{
				"available_count": 3.0,
				"credits": []any{
					map[string]any{"id": "c-later", "status": "available", "reset_type": "codex_rate_limits", "expires_at": "2026-03-04T05:06:07Z", "title": "Later", "description": "Second"},
					credit("c-none", "available", "codex_rate_limits"),
					map[string]any{"id": "c-sooner", "status": "available", "reset_type": "codex_rate_limits", "expires_at": "2026-02-03T04:05:06Z"},
					credit("c-spent", "consumed", "codex_rate_limits"),
					credit("c-other", "available", "boost"),
					"not-an-object",
				},
			},
			wantCount: 3,
			wantIDs:   []string{"c-sooner", "c-later", "c-none"},
			wantOptions: []CodexResetOption{
				{CreditID: "c-sooner", Title: "Full reset", Description: "Reset your current usage limits.", ExpiresAt: &sooner},
				{CreditID: "c-later", Title: "Later", Description: "Second", ExpiresAt: &later},
				{CreditID: "c-none", Title: "Full reset", Description: "Reset your current usage limits."},
			},
		},
		{
			name:      "accepts a numeric string count",
			payload:   map[string]any{"available_count": "2", "credits": []any{credit("c1", "available", "codex_rate_limits"), credit("c2", "available", "codex_rate_limits")}},
			wantCount: 2,
			wantIDs:   []string{"c1", "c2"},
		},
		{
			name:      "caps options at the spec maximum",
			payload:   map[string]any{"available_count": 40, "credits": credits},
			wantCount: 40,
			wantIDs:   cappedIDs,
		},
		{
			name:      "credits may be absent",
			payload:   map[string]any{"available_count": 0},
			wantCount: 0,
		},
		{
			name:      "count above zero without eligible credits offers the generic reset",
			payload:   map[string]any{"available_count": 2, "credits": []any{credit("c-spent", "consumed", "codex_rate_limits")}},
			wantCount: 2,
			wantIDs:   []string{""},
			wantOptions: []CodexResetOption{
				{Title: "Full reset", Description: "Reset your current usage limits."},
			},
		},
		{
			name:    "rejects a missing available_count",
			payload: map[string]any{"credits": []any{}},
			wantErr: errCodexResetCredits,
		},
		{
			name:    "rejects a negative available_count",
			payload: map[string]any{"available_count": -1},
			wantErr: errCodexResetCredits,
		},
		{
			name:    "rejects a fractional available_count",
			payload: map[string]any{"available_count": 1.5},
			wantErr: errCodexResetCredits,
		},
		{
			name:    "rejects a blank string count",
			payload: map[string]any{"available_count": " "},
			wantErr: errCodexResetCredits,
		},
		{
			name:    "rejects credits that are not an array",
			payload: map[string]any{"available_count": 1, "credits": "none"},
			wantErr: errCodexResetCredits,
		},
		{
			name:    "rejects a credit without an id",
			payload: map[string]any{"available_count": 1, "credits": []any{credit("", "available", "codex_rate_limits")}},
			wantErr: errCodexResetCredits,
		},
		{
			name:    "rejects a numeric expires_at",
			payload: map[string]any{"available_count": 1, "credits": []any{map[string]any{"id": "c1", "status": "available", "reset_type": "codex_rate_limits", "expires_at": 1767225600}}},
			wantErr: errCodexResetCredits,
		},
		{
			name:    "rejects an unparsable expires_at",
			payload: map[string]any{"available_count": 1, "credits": []any{map[string]any{"id": "c1", "status": "available", "reset_type": "codex_rate_limits", "expires_at": "not-a-date"}}},
			wantErr: errCodexResetCredits,
		},
		{
			name:      "truncates a long title to the spec limit",
			payload:   map[string]any{"available_count": 1, "credits": []any{map[string]any{"id": "c1", "status": "available", "reset_type": "codex_rate_limits", "title": strings.Repeat("é", 200)}}},
			wantCount: 1,
			wantIDs:   []string{"c1"},
			wantOptions: []CodexResetOption{
				{CreditID: "c1", Title: truncatedTitle, Description: "Reset your current usage limits."},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeCodexResetCredits(test.payload)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize reset credits: %v", err)
			}
			if got.AvailableCount != test.wantCount {
				t.Fatalf("available count = %d, want %d", got.AvailableCount, test.wantCount)
			}
			if ids := normalizedResetIDs(got.Options); !slices.Equal(ids, test.wantIDs) {
				t.Fatalf("option ids = %v, want %v", ids, test.wantIDs)
			}
			if test.wantOptions != nil && !reflect.DeepEqual(got.Options, test.wantOptions) {
				t.Fatalf("options = %+v, want %+v", got.Options, test.wantOptions)
			}
		})
	}
}

func normalizedResetIDs(options []CodexResetOption) []string {
	ids := make([]string, 0, len(options))
	for _, option := range options {
		ids = append(ids, option.CreditID)
	}
	return ids
}
