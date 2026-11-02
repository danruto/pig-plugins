package store

import (
	"reflect"
	"testing"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/config"
)

func guardEqual(t testing.TB, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func guardNoError(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPlanZeroCapacityIsEnforcedIndependentlyOfLimit(t *testing.T) {
	tests := []struct {
		name       string
		entries    []string
		operations []Mutation
		enforced   bool
		wantError  bool
		want       []string
	}{
		{name: "zero rejects add", enforced: true, wantError: true, operations: []Mutation{{Action: "add", Content: "new"}}},
		{name: "zero rejects replace", enforced: true, wantError: true, entries: []string{"old"}, operations: []Mutation{{Action: "replace", OldText: "old", Content: "new"}}},
		{name: "zero rejects nonempty remainder", enforced: true, wantError: true, entries: []string{"old", "retained"}, operations: []Mutation{{Action: "remove", OldText: "old"}}},
		{name: "zero permits removing final entry", enforced: true, entries: []string{"old"}, operations: []Mutation{{Action: "remove", OldText: "old"}}},
		{name: "policy remains uncapped", operations: []Mutation{{Action: "add", Content: "new"}}, want: []string{"new <!-- created=2026-01-02, last=2026-01-02 -->"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := planRequest{scope: Scope{Target: "memory"}, today: "2026-01-02", capEnforced: tt.enforced, plan: MutationPlan{Operations: tt.operations}}

			planned, err := planEntries(tt.entries, request)

			guardEqual(t, err != nil, tt.wantError)
			guardEqual(t, planned, tt.want)
		})
	}
}

func TestFailureCapacityDoesNotWrapAtLargestConfiguredLimit(t *testing.T) {
	memory := Memory{settings: config.Settings{MemoryMode: "legacy-inject", MemoryCharLimit: int(^uint(0) >> 1)}}
	request := planRequest{scope: Scope{Target: "failure"}, today: "2026-01-02", cap: memory.capacity(Scope{Target: "failure"}), capEnforced: true, plan: MutationPlan{Operations: []Mutation{{Action: "add", Content: "lesson"}}}}

	planned, err := planEntries(nil, request)

	guardNoError(t, err)
	guardEqual(t, planned, []string{"lesson <!-- created=2026-01-02, last=2026-01-02 -->"})
}

func TestMutationDetailsRejectInvalidUTF8BeforeEncoding(t *testing.T) {
	tests := []struct {
		name   string
		target string
		op     Mutation
	}{
		{name: "failure reason", target: "failure", op: Mutation{Content: "safe", FailureReason: "invalid\xffdetail"}},
		{name: "failure state", target: "failure", op: Mutation{Content: "safe", ToolState: "invalid\xffdetail"}},
		{name: "failure correction", target: "failure", op: Mutation{Content: "safe", CorrectedTo: "invalid\xffdetail"}},
		{name: "memory reason", target: "memory", op: Mutation{Content: "safe", FailureReason: "invalid\xffdetail"}},
		{name: "memory state", target: "memory", op: Mutation{Content: "safe", ToolState: "invalid\xffdetail"}},
		{name: "memory correction", target: "memory", op: Mutation{Content: "safe", CorrectedTo: "invalid\xffdetail"}},
		{name: "user reason", target: "user", op: Mutation{Content: "safe", FailureReason: "invalid\xffdetail"}},
		{name: "user state", target: "user", op: Mutation{Content: "safe", ToolState: "invalid\xffdetail"}},
		{name: "user correction", target: "user", op: Mutation{Content: "safe", CorrectedTo: "invalid\xffdetail"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, err := mutationText(tt.op, tt.target)

			guardEqual(t, err != nil, true)
			guardEqual(t, text, "")
		})
	}
	t.Run("valid details retain unicode", func(t *testing.T) {
		text, err := mutationText(Mutation{Content: "safe", Category: "failure", FailureReason: "日本語", ToolState: "🦊", CorrectedTo: "cafe\u0301"}, "failure")

		guardNoError(t, err)
		guardEqual(t, text, "[failure] safe — Failed: 日本語 — Tool state: 🦊 — Corrected to: cafe\u0301")
	})
}
