package store

import (
	"database/sql"
	"strings"
	"testing"
)

func TestSanitizeLockPart(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", "unknown"}, {"aZ09._-", "aZ09._-"}, {"a :/日本語🔥b", "a_b"},
		{"é🚀", "_"}, {"a/ b::c", "a_b_c"},
		{strings.Repeat("x", 79) + "🔥" + "z", strings.Repeat("x", 79) + "_"},
		{strings.Repeat("é", 100), "_"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := sanitizeLockPart(tc.input); got != tc.want {
				t.Fatalf("sanitize = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConsolidationWaitNumber(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  float64
	}{
		{"", 0}, {" \t\ufeff\u00a0", 0}, {"0", 0}, {"-0", 0}, {"+12.5", 12.5},
		{".25e2", 25}, {"12.", 12}, {"1e30", 1e30}, {"1e-4000", 0},
		{"0x20", 32}, {"0B101", 5}, {"0o17", 15}, {"010", 10},
		{"0x20000000000003", 9007199254740996},
		{"-1", 5000}, {"Infinity", 5000}, {"NaN", 5000}, {"1e4000", 5000},
		{"inf", 5000}, {"1_000", 5000}, {"0x1p4", 5000}, {"+0x20", 5000},
		{"0b2", 5000}, {"\u008510\u0085", 5000}, {"1 2", 5000}, {"wait", 5000},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := consolidationWaitMS(tc.input); got != tc.want {
				t.Fatalf("wait = %g, want %g", got, tc.want)
			}
		})
	}
}

func TestLeaseReclaimDecision(t *testing.T) {
	known := sql.NullString{String: "start-1", Valid: true}
	for _, tc := range []struct {
		name       string
		stored     sql.NullString
		observed   processObservation
		age, stale float64
		want       bool
	}{
		{"matching live", known, processObservation{"start-1", true}, 44999, 45000, false},
		{"stale live at boundary", known, processObservation{"start-1", true}, 45000, 45000, true},
		{"dead before stale", known, processObservation{"", false}, 0, 45000, true},
		{"reused pid", known, processObservation{"start-2", true}, 0, 45000, true},
		{"unknown stored", sql.NullString{}, processObservation{"start-2", true}, 0, 45000, false},
		{"unknown observed", known, processObservation{"", true}, 0, 45000, false},
		{"both unknown", sql.NullString{}, processObservation{"", true}, 0, 45000, false},
		{"stale unknown", sql.NullString{}, processObservation{"", true}, 300000, 300000, true},
		{"disabled stale", known, processObservation{"start-1", true}, 1e12, 0, false},
		{"negative stale", known, processObservation{"start-1", true}, 1e12, -1, false},
		{"disabled still reclaims reused", known, processObservation{"start-2", true}, 0, 0, true},
		{"clock moved backwards", known, processObservation{"start-1", true}, -1000, 45000, false},
		{"empty known incarnation is not null", sql.NullString{Valid: true}, processObservation{"start-1", true}, 0, 45000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := leaseReclaimable(tc.stored, tc.observed, tc.age, tc.stale); got != tc.want {
				t.Fatalf("reclaimable = %t, want %t", got, tc.want)
			}
		})
	}
}
