package store_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"
)

func canonicalCoordinationFixture(t testing.TB, operation string) coordinationRequest {
	t.Helper()
	request := identityCoordinationFixture(t, coordinationIdentityCase{operation: operation})
	if operation == "consolidation" {
		request.Identity = filepath.Join(request.AgentRoot, "knowledge", "MEMORY.md")
		request.Key = fmt.Sprintf("memory:global:%x", sha256.Sum256([]byte(request.Identity)))
	}
	return request
}

func TestCoordinationOwnerTokenTracksTypeScriptTakeover(t *testing.T) {
	for _, operation := range []string{"mutation", "consolidation", "recovery"} {
		t.Run(operation, func(t *testing.T) { checkCoordinationOwnerToken(t, operation) })
	}
}

func checkCoordinationOwnerToken(t testing.TB, operation string) {
	t.Helper()
	t.Setenv("PI_HERMES_CONSOLIDATION_LOCK_DIR", "")
	request := canonicalCoordinationFixture(t, operation)
	db := coordinationDB(t, request.Database)
	coordination, err := nativeCoordination(request)
	must(t, err)
	lease, err := coordination.Acquire(t.Context())
	must(t, err)
	t.Cleanup(func() { must(t, lease.Release()) })
	initial := leaseRow(t, db, request.Key)

	current, err := lease.CurrentOwner(t.Context())

	must(t, err)
	same(t, current, true)
	same(t, lease.Token(), initial.Token)
	execSQL(t, db, `UPDATE locks SET acquired_at=0`)
	ts := startCoordinationPeer(t, "typescript")
	ownedCoordination(t, ts, request)
	successor := leaseRow(t, db, request.Key)
	current, err = lease.CurrentOwner(t.Context())
	must(t, err)
	same(t, current, false)
	must(t, lease.Release())
	same(t, leaseRow(t, db, request.Key), successor)
	releaseCoordination(t, ts)
}

func TestCoordinationRecoveryNonpositiveStaleDisablesAgeTakeover(t *testing.T) {
	for _, stale := range []float64{0, -1} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) { checkRecoveryNonpositiveStale(t, stale) })
	}
}

func checkRecoveryNonpositiveStale(t testing.TB, stale float64) {
	t.Helper()
	request := canonicalCoordinationFixture(t, "recovery")
	db := coordinationDB(t, request.Database)
	ts := startCoordinationPeer(t, "typescript")
	ownedCoordination(t, ts, request)
	execSQL(t, db, `UPDATE locks SET acquired_at=0`)
	initial := leaseRow(t, db, request.Key)
	zero, poll := float64(0), float64(5)
	coordination, err := store.RecoveryCoordination(request.Identity, store.RecoveryLockOptions{WaitMS: &zero, PollMS: &poll, StaleMS: &stale})
	must(t, err)
	start := time.Now()

	_, err = coordination.Acquire(t.Context())

	same(t, errors.Is(err, store.ErrLockContention), true)
	within(t, time.Since(start).Milliseconds(), 0, 300)
	same(t, leaseRow(t, db, request.Key), initial)
	releaseCoordination(t, ts)
	lease, err := coordination.Acquire(t.Context())
	must(t, err)
	current, err := lease.CurrentOwner(t.Context())
	must(t, err)
	same(t, current, true)
	must(t, lease.Release())
}

func TestCoordinationConsolidationCanonicalDocumentRenewsAndReleases(t *testing.T) {
	t.Setenv("PI_HERMES_CONSOLIDATION_LOCK_DIR", "")
	request := canonicalCoordinationFixture(t, "consolidation")
	db := coordinationDB(t, request.Database)
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

func TestCoordinationPendingReleaseRetriesSameKey(t *testing.T) {
	request := canonicalCoordinationFixture(t, "recovery")
	db := coordinationDB(t, request.Database)
	peer := startCoordinationPeer(t, "native")
	ownedCoordination(t, peer, request)
	initial := leaseRow(t, db, request.Key)
	blocker := raw(t, request.Database)
	execSQL(t, blocker, `BEGIN IMMEDIATE`)
	askCoordination(t, peer, coordinationRequest{Action: "release"})
	same(t, leaseRow(t, db, request.Key).Token, initial.Token)
	execSQL(t, blocker, `ROLLBACK`)

	ownedCoordination(t, peer, request)

	same(t, coordinationTokenCount(t, db, initial.Token), 0)
	same(t, len(leaseKeys(t, db)), 1)
	releaseCoordination(t, peer)
	same(t, number(t, db, `SELECT count(*) FROM locks`), 0)
}

func TestCoordinationCleanupPreservesEligibleLiveHolder(t *testing.T) {
	for _, operation := range []string{"mutation", "consolidation", "recovery"} {
		t.Run(operation, func(t *testing.T) { checkEligibleLiveHolder(t, operation) })
	}
}

func checkEligibleLiveHolder(t testing.TB, operation string) {
	t.Helper()
	request := identityCoordinationFixture(t, coordinationIdentityCase{operation: operation})
	db := coordinationDB(t, request.Database)
	deadPID := coordinationDeadPID(t)
	protected := coordinationRow{Key: "protected", Token: "protected", PID: os.Getpid(), AcquiredAt: time.Now().Add(-120 * time.Second).UnixMilli()}
	seedCoordination(t, db, protected)
	seedCoordination(t, db, coordinationRow{Key: "dead", Token: "dead", PID: deadPID, AcquiredAt: time.Now().Add(-61 * time.Second).UnixMilli()})
	peer := startCoordinationPeer(t, "native", requestEnvironment("0")...)

	ownedCoordination(t, peer, request)

	same(t, number(t, db, `SELECT count(*) FROM locks WHERE token='dead'`), 0)
	same(t, leaseRow(t, db, "protected"), protected)
	same(t, number(t, db, `SELECT count(*) FROM locks`), 2)
	releaseCoordination(t, peer)
}

func TestCoordinationWaitEnvironmentCapturedAtConstruction(t *testing.T) {
	t.Setenv("PI_HERMES_CONSOLIDATION_LOCK_DIR", "")
	request := canonicalCoordinationFixture(t, "consolidation")
	ts := startCoordinationPeer(t, "typescript")
	ownedCoordination(t, ts, request)
	t.Setenv("PI_HERMES_CONSOLIDATION_LOCK_WAIT_MS", "80")
	coordination, err := nativeCoordination(request)
	must(t, err)
	t.Setenv("PI_HERMES_CONSOLIDATION_LOCK_WAIT_MS", "0")
	start := time.Now()

	_, err = coordination.Acquire(t.Context())

	same(t, errors.Is(err, store.ErrLockContention), true)
	within(t, time.Since(start).Milliseconds(), 70, 400)
	releaseCoordination(t, ts)
	lease, err := coordination.Acquire(t.Context())
	must(t, err)
	current, err := lease.CurrentOwner(t.Context())
	must(t, err)
	same(t, current, true)
	must(t, lease.Release())
}
