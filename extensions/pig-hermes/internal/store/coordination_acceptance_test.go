// Package store_test checks the shared coordination protocol through exported storage APIs and real peers.
package store_test

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func canonicalDocumentHash(identity string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
}

type coordinationIdentityCase struct{ name, operation, layout string }

func identityCoordinationFixture(t testing.TB, tt coordinationIdentityCase) coordinationRequest {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	canonical := filepath.Join(root, "shared")
	must(t, os.MkdirAll(canonical, 0o700))
	display := canonical
	if tt.layout == "symlink missing tail" {
		display = filepath.Join(root, "alias")
		must(t, os.Symlink(canonical, display))
	}
	request := coordinationRequest{Operation: tt.operation, AgentRoot: root, ToolTarget: "memory", Target: "global"}
	switch tt.operation {
	case "mutation":
		canonicalRoot := canonical
		request.Identity = filepath.Join(display, "knowledge", "MEMORY.md")
		canonical = filepath.Join(canonical, "knowledge", "MEMORY.md")
		if tt.layout == "project" {
			request.Identity = filepath.Join(display, "projects", "alpha", "MEMORY.md")
			canonical = filepath.Join(canonicalRoot, "projects", "alpha", "MEMORY.md")
		}
		if tt.layout == "symlink missing tail" {
			request.Identity = filepath.Join(display, "knowledge", "missing", "MEMORY.md")
			canonical = filepath.Join(filepath.Dir(canonical), "missing", "MEMORY.md")
		}
		request.Database = filepath.Join(filepath.Dir(filepath.Dir(canonical)), ".pi-hermes-locks.sqlite")
		request.Key = "mutation:" + canonical
	case "recovery":
		request.Identity = filepath.Join(display, "knowledge", "sessions.db")
		request.Database = filepath.Join(canonical, "knowledge", ".pi-hermes-locks.sqlite")
		request.Key = "recovery:" + filepath.Join(canonical, "knowledge", "sessions.db")
	case "consolidation":
		request.Database = defaultConsolidationDB(root)
		request.Identity = filepath.Join(display, "knowledge", "missing", "MEMORY.md")
		request.Key = "memory:global:" + canonicalDocumentHash(filepath.Join(canonical, "knowledge", "missing", "MEMORY.md"))
		if tt.layout == "configured root" || tt.layout == "symlink missing tail" {
			t.Setenv("PI_HERMES_CONSOLIDATION_LOCK_DIR", "  "+display+"  ")
			request.Database = filepath.Join(canonical, "locks.sqlite")
		}
	}
	must(t, os.MkdirAll(filepath.Dir(request.Database), 0o700))
	return request
}

func TestCoordinationACIdentitiesShareCanonicalRowsWithTypeScript(t *testing.T) {
	tests := []coordinationIdentityCase{
		{"mutation global", "mutation", "global"},
		{"mutation project", "mutation", "project"},
		{"mutation symlink missing tail", "mutation", "symlink missing tail"},
		{"recovery global", "recovery", "global"},
		{"recovery symlink missing tail", "recovery", "symlink missing tail"},
		{"consolidation default", "consolidation", "global"},
		{"consolidation configured", "consolidation", "configured root"},
		{"consolidation symlink missing tail", "consolidation", "symlink missing tail"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { checkCoordinationIdentity(t, tt) })
	}
}

func checkCoordinationIdentity(t testing.TB, tt coordinationIdentityCase) {
	t.Helper()
	t.Setenv("PI_HERMES_CONSOLIDATION_LOCK_DIR", "")
	t.Setenv("PI_HERMES_CONSOLIDATION_LOCK_WAIT_MS", "0")
	request := identityCoordinationFixture(t, tt)
	native := startCoordinationPeer(t, "native")
	ts := startCoordinationPeer(t, "typescript")
	ownedCoordination(t, native, request)
	db := coordinationDB(t, request.Database)
	assertLeaseOwner(t, db, request, native)
	same(t, acquireCoordination(t, ts, request).Acquired, false)
	releaseCoordination(t, native)
	ownedCoordination(t, ts, request)
	incumbent := leaseRow(t, db, request.Key)
	request.TimeoutMS = 100

	blocked := acquireCoordination(t, native, request)

	same(t, blocked.Acquired, false)
	same(t, leaseRow(t, db, request.Key).Token, incumbent.Token)
	releaseCoordination(t, ts)
	request.TimeoutMS = 0
	ownedCoordination(t, native, request)
	assertLeaseOwner(t, db, request, native)
	same(t, acquireCoordination(t, ts, request).Acquired, false)
	releaseCoordination(t, native)
	same(t, number(t, db, `SELECT count(*) FROM locks`), 0)
	ownedCoordination(t, ts, request)
	releaseCoordination(t, ts)
}

func TestCoordinationACSanitizeHashesStorageIdentityBytes(t *testing.T) {
	tests := []struct{ name, tool, target, wantTool, wantTarget, identity string }{
		{"invalid runs", "mem 東京 / tool", "project / 東京", "mem_tool", "project_", "synthetic-storage"},
		{"empty", "", "", "unknown", "unknown", "synthetic-storage"},
		{"allowed ASCII", "A-z_9.-", "project_2.-", "A-z_9.-", "project_2.-", "synthetic-storage"},
		{"truncate at eighty", strings.Repeat("a", 79) + "東京!suffix", strings.Repeat("b", 81), strings.Repeat("a", 79) + "_", strings.Repeat("b", 80), "synthetic-storage"},
		{"UTF8 identity bytes", "memory", "global", "memory", "global", "café/東京 😀"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			must(t, err)
			request := consolidationRequest(root)
			request.ToolTarget = tt.tool
			request.Target = tt.target
			request.Identity = filepath.Join(root, tt.identity, "MEMORY.md")
			request.Database = defaultConsolidationDB(root)
			request.Key = tt.wantTool + ":" + tt.wantTarget + ":" + canonicalDocumentHash(request.Identity)
			db := coordinationDB(t, request.Database)
			peer := startCoordinationPeer(t, "native", requestEnvironment("0")...)
			ts := startCoordinationPeer(t, "typescript")

			ownedCoordination(t, peer, request)

			same(t, leaseKeys(t, db), []string{request.Key})
			same(t, acquireCoordination(t, ts, request).Acquired, false)
			distinct := request
			distinct.Identity = filepath.Join(root, "synthetic-storage-distinct", "MEMORY.md")
			distinct.Key = tt.wantTool + ":" + tt.wantTarget + ":" + canonicalDocumentHash(distinct.Identity)
			second := startCoordinationPeer(t, "native", requestEnvironment("0")...)
			ownedCoordination(t, second, distinct)
			same(t, len(leaseKeys(t, db)), 2)
			releaseCoordination(t, second)
			releaseCoordination(t, peer)
		})
	}
}

func TestCoordinationACWaitUsesNumericEnvironmentAndFiveSecondDefault(t *testing.T) {
	tests := []struct {
		name, operation, wait string
		min, max              int64
	}{
		{"consolidation default", "consolidation", "unset", 4900, 7000},
		{"consolidation zero", "consolidation", "0", 0, 300},
		{"consolidation whitespace zero", "consolidation", "  ", 0, 300},
		{"consolidation numeric hexadecimal", "consolidation", "0x20", 25, 350},
		{"consolidation finite positive", "consolidation", " 80 ", 70, 400},
		{"consolidation negative defaults", "consolidation", "-1", 4900, 7000},
		{"consolidation nonfinite defaults", "consolidation", "Infinity", 4900, 7000},
		{"consolidation NaN defaults", "consolidation", "not-a-number", 4900, 7000},
		{"mutation default", "mutation", "0", 4900, 7000},
		{"recovery default", "recovery", "0", 4900, 7000},
		{"recovery overrides", "recovery-overrides", "0", 70, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { checkCoordinationWait(t, tt.operation, tt.wait, [2]int64{tt.min, tt.max}) })
	}
}

func checkCoordinationWait(t testing.TB, operation, wait string, bounds [2]int64) {
	t.Helper()
	fixtureOperation := operation
	if operation == "recovery-overrides" {
		fixtureOperation = "recovery"
	}
	request := identityCoordinationFixture(t, coordinationIdentityCase{operation: fixtureOperation})
	request.Operation = operation
	request.StaleMS = 300000
	peer := startCoordinationPeer(t, "native", requestEnvironment(wait)...)
	ts := startCoordinationPeer(t, "typescript")
	ownedCoordination(t, ts, request)

	reply := acquireCoordination(t, peer, request)

	same(t, reply.Acquired, false)
	within(t, reply.ElapsedMS, bounds[0], bounds[1])
	releaseCoordination(t, ts)
	ownedCoordination(t, peer, request)
	releaseCoordination(t, peer)
}

func TestCoordinationACStaleHonorsPIDIncarnationAndAge(t *testing.T) {
	tests := []struct {
		name, operation, holder string
		age                     int64
		acquired                bool
	}{
		{"current incarnation", "consolidation", "current", 0, false},
		{"unknown incarnation live", "consolidation", "unknown", 0, false},
		{"reused PID", "consolidation", "mismatch", 0, true},
		{"dead PID unknown incarnation", "consolidation", "dead", 0, true},
		{"live consolidation not stale", "consolidation", "current", 43000, false},
		{"live consolidation stale", "consolidation", "current", 47000, true},
		{"live mutation not stale", "mutation", "current", 298000, false},
		{"live mutation stale", "mutation", "current", 302000, true},
		{"live recovery not stale", "recovery", "current", 298000, false},
		{"live recovery stale", "recovery", "current", 302000, true},
		{"recovery stale override", "recovery-overrides", "current", 1000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkCoordinationLiveness(t, coordinationLivenessCase{tt.operation, tt.holder, tt.age, tt.acquired})
		})
	}
}

type coordinationLivenessCase struct {
	operation, holder string
	age               int64
	acquired          bool
}

func checkCoordinationLiveness(t testing.TB, tt coordinationLivenessCase) {
	t.Helper()
	operation := tt.operation
	if operation == "recovery-overrides" {
		operation = "recovery"
	}
	request := identityCoordinationFixture(t, coordinationIdentityCase{operation: operation})
	request.Operation = tt.operation
	request.StaleMS = 100
	request.TimeoutMS = 150
	db := coordinationDB(t, request.Database)
	ts := startCoordinationPeer(t, "typescript")
	ownedCoordination(t, ts, request)
	incumbent := leaseRow(t, db, request.Key)
	incumbent.AcquiredAt -= tt.age
	switch tt.holder {
	case "unknown":
		incumbent.Incarnation = sql.NullString{}
	case "mismatch":
		incumbent.Incarnation = sql.NullString{String: "synthetic-impossible-incarnation", Valid: true}
	case "dead":
		incumbent.Incarnation = sql.NullString{}
		killCoordinationPeer(t, ts)
	}
	seedCoordination(t, db, incumbent)
	peer := startCoordinationPeer(t, "native", requestEnvironment("0")...)

	reply := acquireCoordination(t, peer, request)

	same(t, reply.Acquired, tt.acquired)
	same(t, leaseRow(t, db, request.Key).Token != incumbent.Token, tt.acquired)
	releaseCoordination(t, peer)
	if tt.holder != "dead" {
		releaseCoordination(t, ts)
	}
	request.TimeoutMS = 0
	ownedCoordination(t, peer, request)
	releaseCoordination(t, peer)
}

func TestCoordinationACFenceOldTokenCannotRenewOrReleaseTypeScriptSuccessor(t *testing.T) {
	request := identityCoordinationFixture(t, coordinationIdentityCase{operation: "recovery"})
	db := coordinationDB(t, request.Database)
	old := startCoordinationPeer(t, "native")
	ownedCoordination(t, old, request)
	same(t, askCoordination(t, old, coordinationRequest{Action: "renew"}).Renewed, true)
	execSQL(t, db, `UPDATE locks SET acquired_at=0`)
	ts := startCoordinationPeer(t, "typescript")
	ownedCoordination(t, ts, request)
	successor := leaseRow(t, db, request.Key)

	renewed := askCoordination(t, old, coordinationRequest{Action: "renew"})

	same(t, renewed.Renewed, false)
	same(t, leaseRow(t, db, request.Key), successor)
	askCoordination(t, old, coordinationRequest{Action: "release"})
	same(t, leaseRow(t, db, request.Key), successor)
	same(t, askCoordination(t, ts, coordinationRequest{Action: "renew"}).Renewed, true)
	releaseCoordination(t, ts)
	ownedCoordination(t, old, request)
	releaseCoordination(t, old)
}

func TestCoordinationACRenewConsolidationAtTenSeconds(t *testing.T) {
	root := t.TempDir()
	request := consolidationRequest(root)
	db := coordinationDB(t, defaultConsolidationDB(root))
	peer := startCoordinationPeer(t, "native", requestEnvironment("0")...)
	ownedCoordination(t, peer, request)
	initial := leaseRow(t, db, request.Key)

	renewed := waitForCoordinationRenewal(t, db, initial)

	within(t, renewed.AcquiredAt-initial.AcquiredAt, 9000, 12500)
	same(t, renewed.Token, initial.Token)
	same(t, renewed.PID, initial.PID)
	same(t, renewed.Incarnation, initial.Incarnation)
	releaseCoordination(t, peer)
	same(t, number(t, db, `SELECT count(*) FROM locks`), 0)
}

func TestCoordinationACPendingReleaseRetriesAfterSQLiteContention(t *testing.T) {
	tests := []string{"released owner", "typescript successor"}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) { checkPendingCoordinationRelease(t, tt) })
	}
}

func checkPendingCoordinationRelease(t testing.TB, scenario string) {
	t.Helper()
	request := identityCoordinationFixture(t, coordinationIdentityCase{operation: "recovery"})
	db := coordinationDB(t, request.Database)
	peer := startCoordinationPeer(t, "native")
	ownedCoordination(t, peer, request)
	initial := leaseRow(t, db, request.Key)
	blocker := raw(t, request.Database)
	execSQL(t, blocker, `BEGIN IMMEDIATE`)

	failed := askCoordination(t, peer, coordinationRequest{Action: "release"})

	same(t, failed.Error != "", true)
	same(t, leaseRow(t, db, request.Key).Token, initial.Token)
	execSQL(t, blocker, `ROLLBACK`)
	ts := startCoordinationPeer(t, "typescript")
	if scenario == "typescript successor" {
		execSQL(t, db, `UPDATE locks SET acquired_at=0`)
		ownedCoordination(t, ts, request)
		successor := leaseRow(t, db, request.Key)
		request.TimeoutMS = 100
		blocked := acquireCoordination(t, peer, request)
		same(t, blocked.Acquired, false)
		same(t, blocked.Error != "", true)
		same(t, leaseRow(t, db, request.Key), successor)
		same(t, coordinationTokenCount(t, db, initial.Token), 0)
		releaseCoordination(t, ts)
	}
	request.TimeoutMS = 0
	ownedCoordination(t, peer, request)
	same(t, coordinationTokenCount(t, db, initial.Token), 0)
	same(t, number(t, db, `SELECT count(*) FROM locks`), 1)
	releaseCoordination(t, peer)
	releaseCoordination(t, ts)
}

func TestCoordinationACCleanupBoundsDeadRowsAndPreservesLiveHolder(t *testing.T) {
	for _, operation := range []string{"recovery", "consolidation"} {
		t.Run(operation, func(t *testing.T) { checkCleanupLiveHolder(t, operation) })
	}
}

func checkCleanupLiveHolder(t testing.TB, operation string) {
	t.Helper()
	request := identityCoordinationFixture(t, coordinationIdentityCase{operation: operation})
	db := coordinationDB(t, request.Database)
	deadPID := coordinationDeadPID(t)
	seedDeadCoordinationRows(t, db, deadPID)
	protected := coordinationRow{Key: "protected", Token: "protected", PID: os.Getpid(), AcquiredAt: time.Now().Add(-61 * time.Second).UnixMilli()}
	seedCoordination(t, db, protected)
	peer := startCoordinationPeer(t, "native", requestEnvironment("0")...)

	ownedCoordination(t, peer, request)

	within(t, 8192-int64(number(t, db, `SELECT count(*) FROM locks WHERE token='dead'`)), 1, 8191)
	same(t, leaseRow(t, db, "protected"), protected)
	releaseCoordination(t, peer)
}
