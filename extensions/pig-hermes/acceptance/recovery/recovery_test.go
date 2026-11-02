package recovery_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecovery_AC19Readable_RebuildPreservesRowsSchemaProvenance(t *testing.T) {
	f := recoveryNew(t)
	before := f.before
	recoveryDamageIndex(t, f)

	recoveryEqual(t, recoveryRecover(t, f).Failed, false)
	after := recoveryInspect(t, f)

	recoveryEqual(t, after.Rows, before.Rows)
	recoveryEqual(t, after.Schema, before.Schema)
	recoveryEqual(t, after.Integrity, "ok")
	recoveryEqual(t, after.Search, []string{"native checkpoint", "東京日本語 checkpoint"})
}

func TestRecovery_AC19Reconcile_CorruptIndexDoesNotHideDurableDocument(t *testing.T) {
	f := recoveryNew(t)
	p := recoveryStart(t)
	p.success(t, recoveryRequest{Action: "manager", Root: f.root})
	p.success(t, recoveryRequest{Action: "open"})
	p.success(t, recoveryRequest{Action: "memory"})
	recoveryWrite(t, filepath.Join(f.knowledge, "MEMORY.md"), "external durable 日本語 <!-- created=2026-01-01, last=2026-01-02 -->")
	p.success(t, recoveryRequest{Action: "exec", SQL: `PRAGMA wal_checkpoint(TRUNCATE)`})
	recoveryDamageIndex(t, f)
	p.success(t, recoveryRequest{Action: "exec", SQL: `PRAGMA shrink_memory`})

	response := p.call(t, recoveryRequest{Action: "sync"})
	p.stop(t)

	recoveryEqual(t, response.Failed, false)
	after := recoveryInspect(t, f)
	recoveryEqual(t, after.Contents, []string{"external durable 日本語"})
	recoveryEqual(t, after.Search, []string{"external durable 日本語"})
}

func TestRecovery_AC19Circuit_HonorsFailureThresholdAndWindow(t *testing.T) {
	tests := []struct {
		name    string
		count   int
		age     time.Duration
		limit   *int
		window  *int64
		refused bool
		header  string
	}{
		{name: "two_recent_failures_allow_recovery", count: 2, age: time.Second, header: "SQLite format 3\x00"},
		{name: "three_recent_failures_open_default_circuit", count: 3, age: time.Second, refused: true, header: "synthetic-corrup"},
		{name: "three_failures_older_than_300000ms_expire", count: 3, age: 301 * time.Second, header: "SQLite format 3\x00"},
		{name: "configured_one_failure_opens_circuit", count: 1, age: time.Second, limit: new(1), refused: true, header: "synthetic-corrup"},
		{name: "configured_window_expires_failure", count: 1, age: 2 * time.Second, limit: new(1), window: new(int64(1000)), header: "SQLite format 3\x00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := recoveryNew(t)
			recoveryGarbage(t, f)
			recoveryFailureState(t, f, tt.count, tt.age)
			p := recoveryStart(t)
			p.success(t, recoveryRequest{Action: "manager", Root: f.root, Limit: tt.limit, WindowMS: tt.window})

			response := p.call(t, recoveryRequest{Action: "recover"})
			p.stop(t)

			recoveryEqual(t, response.Failed, tt.refused)
			recoveryEqual(t, recoveryHeader(t, f.path), tt.header)
		})
	}
}

func recoveryFailureState(t testing.TB, f recoveryFixture, count int, age time.Duration) {
	t.Helper()
	failures := []int64{}
	for range count {
		failures = append(failures, time.Now().Add(-age).UnixMilli())
	}
	bytes, err := json.Marshal(map[string]any{"failures": failures})
	recoveryMust(t, err)
	recoveryMust(t, os.WriteFile(f.path+".recovery-state.json", bytes, 0o600))
}

func TestRecovery_AC19Backups_DefaultAndConfiguredRetention(t *testing.T) {
	tests := []struct {
		name       string
		cap        int
		configured *int
	}{
		{name: "default_three", cap: 3},
		{name: "configured_one", cap: 1, configured: new(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := recoveryNew(t)
			recoverySeedBackups(t, f)
			recoveryGarbage(t, f)
			p := recoveryStart(t)
			p.success(t, recoveryRequest{Action: "manager", Root: f.root, Backups: tt.configured})

			response := p.call(t, recoveryRequest{Action: "recover"})
			p.stop(t)
			backups := recoveryBackups(t, f)
			after := recoveryInspect(t, f)

			recoveryEqual(t, response.Failed, false)
			recoveryRange(t, len(backups), 1, tt.cap)
			recoveryEqual(t, after.Integrity, "ok")
		})
	}
}

func recoverySeedBackups(t testing.TB, f recoveryFixture) {
	t.Helper()
	for index := range 5 {
		recoveryWrite(t, fmt.Sprintf("%s.corrupt-20260701-%d", f.path, index), "synthetic prior quarantined database")
	}
}

func TestRecovery_AC19Success_SuccessesDoNotTripFailureCircuit(t *testing.T) {
	f := recoveryNew(t)

	completed := recoveryRepeatedSuccesses(t, f)

	recoveryEqual(t, completed, 4)
}

func TestRecovery_AC19Maintenance_SweepsDormantStoresIdempotently(t *testing.T) {
	f := recoveryNew(t)
	expected := recoverySeedSnapshots(t, f)
	before := recoveryDocument(t, f)
	beforeSnapshots := recoveryMaintenanceFiles(t, f.root)
	documents := recoveryMaintenanceDocuments(t, f.root)
	p := recoveryStart(t)

	response := p.call(t, recoveryRequest{Action: "maintain", Root: f.root})

	recoveryEqual(t, response.Failed, false)
	first := recoveryAssertMaintenance(t, f.root, beforeSnapshots, expected)
	recoveryEqual(t, recoveryDocument(t, f), before)
	recoveryEqual(t, recoveryMaintenanceDocuments(t, f.root), documents)
	p.success(t, recoveryRequest{Action: "maintain"})
	recoveryEqual(t, recoveryAssertMaintenance(t, f.root, beforeSnapshots, expected), first)
	recoveryEqual(t, recoveryDocument(t, f), before)
	recoveryEqual(t, recoveryMaintenanceDocuments(t, f.root), documents)
}

func TestRecovery_AC19MaintenanceBytes_BytePressureRetainsNewestSnapshots(t *testing.T) {
	f := recoveryNew(t)
	recoveryLargeSnapshots(t, f)
	beforeSnapshots := recoveryMaintenanceFiles(t, f.knowledge)
	documents := recoveryMaintenanceDocuments(t, f.root)
	p := recoveryStart(t)

	response := p.call(t, recoveryRequest{Action: "maintain", Root: f.root})

	recoveryEqual(t, response.Failed, false)
	recoveryAssertMaintenance(t, f.knowledge, beforeSnapshots, []string{".MEMORY.md.recovery-1790000000002-00000000-0000-4000-8000-000000000002", ".MEMORY.md.recovery-1790000000003-00000000-0000-4000-8000-000000000003"})
	recoveryEqual(t, recoveryMaintenanceDocuments(t, f.root), documents)
}

func recoveryLargeSnapshots(t testing.TB, f recoveryFixture) {
	t.Helper()
	for index := 1; index <= 3; index++ {
		path := filepath.Join(f.knowledge, fmt.Sprintf(".MEMORY.md.recovery-%013d-00000000-0000-4000-8000-%012d", 1790000000000+index, index))
		file, err := os.Create(path)
		recoveryMust(t, err)
		recoveryMust(t, file.Truncate(24*1024*1024))
		recoveryMust(t, file.Close())
		stamp := time.Now().Add(time.Duration(index-4) * time.Minute)
		recoveryMust(t, os.Chtimes(path, stamp, stamp))
	}
}

func TestRecovery_AC19Symlink_MaintenanceDoesNotFollowProjectAlias(t *testing.T) {
	f := recoveryNew(t)
	expected := recoverySeedSnapshots(t, f)
	beforeSnapshots := recoveryMaintenanceFiles(t, f.root)
	documents := recoveryMaintenanceDocuments(t, f.root)
	outside, err := os.MkdirTemp(recoveryScratch, "outside-")
	recoveryMust(t, err)
	canary := filepath.Join(outside, ".MEMORY.md.recovery-1790000000000-00000000-0000-4000-8000-000000000009")
	recoveryWrite(t, canary, "outside synthetic canary")
	old := time.Now().Add(-8 * 24 * time.Hour)
	recoveryMust(t, os.Chtimes(canary, old, old))
	recoveryWrite(t, filepath.Join(outside, "unrelated.txt"), "unrelated synthetic data")
	recoveryMust(t, os.Symlink(outside, filepath.Join(f.root, "projects", "alias")))
	before := recoveryArtifacts(t, outside)
	canaryBytes := recoveryBytes(t, canary)
	unrelated := recoveryBytes(t, filepath.Join(outside, "unrelated.txt"))
	p := recoveryStart(t)

	response := p.call(t, recoveryRequest{Action: "maintain", Root: f.root})

	recoveryEqual(t, response.Failed, false)
	recoveryEqual(t, recoveryArtifacts(t, outside), before)
	recoveryEqual(t, recoveryBytes(t, canary), canaryBytes)
	recoveryEqual(t, recoveryBytes(t, filepath.Join(outside, "unrelated.txt")), unrelated)
	recoveryAssertMaintenance(t, f.root, beforeSnapshots, expected)
	recoveryEqual(t, recoveryMaintenanceDocuments(t, f.root), documents)
}

func TestRecovery_AC19Crash_DeathAfterDocumentPublicationCanSync(t *testing.T) {
	f := recoveryNew(t)
	child := recoveryStart(t)
	child.success(t, recoveryRequest{Action: "manager", Root: f.root})
	child.success(t, recoveryRequest{Action: "open"})
	child.success(t, recoveryRequest{Action: "memory"})
	peer := recoveryStart(t)
	peer.success(t, recoveryRequest{Action: "raw", Root: f.root})
	peer.success(t, recoveryRequest{Action: "exec", SQL: `BEGIN IMMEDIATE`})
	recoveryMust(t, child.encode.Encode(recoveryRequest{Action: "add", Content: "crash durable marker"}))
	published := recoveryWaitDocument(t, f)
	child.kill(t)
	peer.success(t, recoveryRequest{Action: "exec", SQL: `ROLLBACK`})
	peer.stop(t)
	repair := recoveryStart(t)
	repair.success(t, recoveryRequest{Action: "manager", Root: f.root})
	repair.success(t, recoveryRequest{Action: "open"})
	repair.success(t, recoveryRequest{Action: "memory"})

	repair.success(t, recoveryRequest{Action: "sync"})
	repair.stop(t)
	after := recoveryInspect(t, f)

	recoveryEqual(t, after.Contents, []string{"crash durable marker"})
	recoveryEqual(t, recoveryDocument(t, f), published)
	recoveryEqual(t, after.Integrity, "ok")
}

func TestRecovery_AC19WAL_CommittedRowsSurviveWriterDeath(t *testing.T) {
	f := recoveryNew(t)
	writer := recoveryStart(t)
	writer.success(t, recoveryRequest{Action: "raw", Root: f.root})
	writer.success(t, recoveryRequest{Action: "exec", SQL: `PRAGMA wal_autocheckpoint=0; INSERT INTO memories(target,content,created,last_referenced) VALUES('memory','wal accepted','2026-01-01','2026-01-02')`})
	recoveryRange(t, len(recoveryBytes(t, f.path+"-wal")), 1, 1<<30)
	recoveryRange(t, len(recoveryBytes(t, f.path+"-shm")), 1, 1<<30)
	writer.kill(t)
	p := recoveryStart(t)
	p.success(t, recoveryRequest{Action: "manager", Root: f.root})

	p.success(t, recoveryRequest{Action: "open"})
	p.stop(t)
	after := recoveryInspect(t, f)

	recoveryEqual(t, after.Contents, []string{"native checkpoint", "wal accepted", "東京日本語 checkpoint"})
	recoveryEqual(t, after.Integrity, "ok")
}

func TestRecovery_AC19Peer_AcceptedOpenHandleCommitCannotDetach(t *testing.T) {
	f := recoveryNew(t)
	peer := recoveryStart(t)
	peer.success(t, recoveryRequest{Action: "raw", Root: f.root})
	peer.success(t, recoveryRequest{Action: "exec", SQL: `INSERT INTO extension_metadata VALUES('peer-primer','accepted before recovery'); PRAGMA wal_checkpoint(TRUNCATE)`})
	recoveryDamageIndex(t, f)

	recoveryEqual(t, recoveryRecover(t, f).Failed, false)
	peerReply := peer.call(t, recoveryRequest{Action: "exec", SQL: `INSERT INTO extension_metadata VALUES('peer-accepted','peer accepted')`})
	peer.stop(t)

	recoveryAcceptedCommitVisible(t, peerReply, recoveryInspect(t, f).Rows["metadata"], "peer accepted")
}

func TestRecovery_AC19Failure_UnwritableRecoveryPreservesOriginal(t *testing.T) {
	f := recoveryNew(t)
	recoveryDamageIndex(t, f)
	before := recoveryBytes(t, f.path)
	artifacts := recoveryArtifacts(t, f.root)
	p := recoveryStart(t)
	p.success(t, recoveryRequest{Action: "manager", Root: f.root})
	recoveryMust(t, os.Chmod(f.knowledge, 0o500))
	t.Cleanup(func() { recoveryMust(t, os.Chmod(f.knowledge, 0o700)) })

	response := p.call(t, recoveryRequest{Action: "recover"})
	p.stop(t)
	recoveryMust(t, os.Chmod(f.knowledge, 0o700))

	recoveryEqual(t, response.Failed, true)
	recoveryEqual(t, recoveryBytes(t, f.path), before)
	recoveryEqual(t, recoveryArtifacts(t, f.root), artifacts)
	recoveryEqual(t, recoveryRecover(t, f).Failed, false)
}

func TestRecovery_AC19Rollback_FrozenReaderNeedsNoSchemaDowngrade(t *testing.T) {
	f := recoveryNew(t)
	before := f.before
	recoveryDamageIndex(t, f)
	recoveryEqual(t, recoveryRecover(t, f).Failed, false)

	recoveryEqual(t, recoveryFrozenRead(t, f), true)
	after := recoveryInspect(t, f)

	recoveryEqual(t, after.Schema, before.Schema)
	recoveryEqual(t, after.Rows, before.Rows)
}

func TestRecovery_AC19Cancel_CanceledRecoveryRetainsBytesAndCanRetry(t *testing.T) {
	f := recoveryNew(t)
	recoveryDamageIndex(t, f)
	before := recoveryBytes(t, f.path)
	p := recoveryStart(t)
	p.success(t, recoveryRequest{Action: "manager", Root: f.root})

	response := p.call(t, recoveryRequest{Action: "recover", Cancel: true})
	p.stop(t)

	recoveryEqual(t, response.Failed, true)
	recoveryEqual(t, recoveryBytes(t, f.path), before)
	recoveryEqual(t, recoveryRecover(t, f).Failed, false)
}
