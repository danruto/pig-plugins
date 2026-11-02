package store_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"
)

func TestMemoryCoordinationCleanupRetainsBoundAndDatabaseCadence(t *testing.T) {
	f := mutationStore(t, `{}`)
	db := raw(t, filepath.Join(f.resolved.AgentRoot, ".pi-hermes-locks.sqlite"))
	execSQL(t, db, `CREATE TABLE locks(lock_key TEXT PRIMARY KEY,token TEXT NOT NULL,pid INTEGER NOT NULL,incarnation TEXT,acquired_at INTEGER NOT NULL)`)
	child := exec.CommandContext(t.Context(), "true")
	must(t, child.Start())
	must(t, child.Wait())
	ledgerPath := os.Getenv("HERMES_ACCEPTANCE_PID_FILE")
	if ledgerPath == "" {
		ledgerPath = filepath.Join(t.TempDir(), "pids")
	}
	ledger, err := os.OpenFile(ledgerPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	must(t, err)
	_, err = fmt.Fprintf(ledger, "%d cleanup guard terminated fixture\n", child.Process.Pid)
	must(t, err)
	must(t, ledger.Close())
	_, err = db.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<8192) INSERT INTO locks SELECT 'dead:'||i,'dead',?,NULL,? FROM n`, child.Process.Pid, time.Now().Add(-61*time.Second).UnixMilli())
	must(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO locks VALUES('protected','protected',?,NULL,?)`, os.Getpid(), time.Now().UnixMilli())
	must(t, err)

	addMemory(t, f, store.Scope{Target: "memory"}, "first accepted mutation")

	afterFirst := number(t, db, `SELECT count(*) FROM locks WHERE token='dead'`)
	assertCleanupExecutionBound(t, 8192-int64(afterFirst))
	same(t, text(t, db, `SELECT token FROM locks WHERE lock_key='protected'`), "protected")
	addMemory(t, f, store.Scope{Target: "memory"}, "second accepted mutation")
	same(t, number(t, db, `SELECT count(*) FROM locks WHERE token='dead'`), afterFirst)
	same(t, number(t, f.db, `SELECT count(*) FROM memories`), 2)
}

func TestMemoryMutationsRespectTypeScriptOwnership(t *testing.T) {
	tests := []struct {
		action string
		final  []string
	}{
		{"replace", []string{"memory|<global>|<none>|changed"}},
		{"remove", []string{}},
		{"apply", []string{"memory|<global>|<none>|changed", "memory|<global>|<none>|must not leak an early plan step"}},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) { checkMemoryTypeScriptOwnership(t, tt.action, tt.final) })
	}
}

func checkMemoryTypeScriptOwnership(t testing.TB, action string, final []string) {
	t.Helper()
	f := mutationStore(t, `{}`)
	scope := store.Scope{Target: "memory"}
	addMemory(t, f, scope, "safe original")
	path := memoryDocument(f, scope)
	before := readDocument(t, path)
	request := coordinationRequest{Operation: "mutation", Identity: path, Database: filepath.Join(filepath.Dir(filepath.Dir(path)), ".pi-hermes-locks.sqlite"), Key: "mutation:" + path}
	db := coordinationDB(t, request.Database)
	ts := startCoordinationPeer(t, "typescript")
	ownedCoordination(t, ts, request)
	incumbent := leaseRow(t, db, request.Key)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	result, err := mutationAction(f, action)(ctx, scope, store.Mutation{OldText: "safe original", Content: "changed"})

	t.Logf("held TypeScript lease error=%v code=%s", err, result.Code)
	same(t, result.DocumentSaved, false)
	same(t, leaseRow(t, db, request.Key), incumbent)
	same(t, readDocument(t, path), before)
	same(t, inventory(t, f.db), []string{"memory|<global>|<none>|safe original"})
	releaseCoordination(t, ts)
	accepted, err := mutationAction(f, action)(t.Context(), scope, store.Mutation{OldText: "safe original", Content: "changed"})
	savedMutation(t, accepted, err)
	same(t, inventory(t, f.db), final)
	same(t, number(t, db, `SELECT count(*) FROM locks`), 0)
}

func assertCleanupExecutionBound(t testing.TB, removed int64) {
	t.Helper()
	if removed < 1 || removed > 128 {
		t.Fatalf("removed %d dead rows; want [1,128]", removed)
	}
}
