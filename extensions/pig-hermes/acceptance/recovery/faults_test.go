package recovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

type recoveryTrace struct {
	BackupMoved bool
	Killed      bool
	Events      []struct {
		Kind, Source, Target string
		Result               int
		Injected             bool
	}
}

func recoveryTraceStart(t testing.TB, f recoveryFixture, mode string) (*recoveryProcess, string) {
	t.Helper()
	if runtime.GOOS != "linux" || (runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64") {
		t.Skip("Linux arm64/amd64 syscall barrier required")
	}
	python, err := exec.LookPath("python3")
	recoveryMust(t, err)
	report := filepath.Join(recoveryScratch, "trace-"+mode+".json")
	p := recoveryLaunch(t, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, python, "testdata/syscall_barrier.py", recoveryBinary, mode, f.path, report, filepath.Join(recoveryScratch, "pids"))
	})
	return p, report
}

func recoveryReadTrace(t testing.TB, path string) recoveryTrace {
	t.Helper()
	var trace recoveryTrace
	recoveryMust(t, json.Unmarshal(recoveryBytes(t, path), &trace))
	return trace
}

func recoveryContainsBackup(t testing.TB, f recoveryFixture, want []byte) bool {
	t.Helper()
	for _, path := range recoveryBackups(t, f) {
		if bytes.Equal(recoveryBytes(t, path), want) {
			return true
		}
	}
	return false
}

func recoveryTraceFaults(t testing.TB, trace recoveryTrace, database string) {
	t.Helper()
	var rename, link bool
	for _, event := range trace.Events {
		if event.Injected && event.Target == database {
			rename = rename || event.Kind == "rename"
			link = link || event.Kind == "link"
		}
	}
	recoveryEqual(t, rename, true)
	recoveryEqual(t, link, true)
}

func TestRecovery_AC19Restore_FailedRestorationRetainsSourceForRetry(t *testing.T) {
	f := recoveryNew(t)
	recoveryDamageIndex(t, f)
	before := recoveryBytes(t, f.path)
	p, report := recoveryTraceStart(t, f, "restore-fail")
	p.success(t, recoveryRequest{Action: "manager", Root: f.root})

	response := p.call(t, recoveryRequest{Action: "recover"})
	p.stop(t)
	trace := recoveryReadTrace(t, report)

	recoveryEqual(t, trace.BackupMoved, true)
	recoveryTraceFaults(t, trace, f.path)
	recoveryEqual(t, response.Failed, true)
	recoveryEqual(t, recoveryContainsBackup(t, f, before), true)
	recoveryEqual(t, recoveryRecover(t, f).Failed, false)
	after := recoveryInspect(t, f)
	recoveryEqual(t, after.Rows, f.before.Rows)
}

func TestRecovery_AC19CrashSet_RestartBetweenBackupRenamesPreservesRows(t *testing.T) {
	tests := []string{"main", "wal", "shm"}
	for _, tt := range tests {
		t.Run(tt, func(t *testing.T) { recoveryCrashBackup(t, tt) })
	}
}

func recoveryCrashBackup(t testing.TB, member string) {
	t.Helper()
	f := recoveryNew(t)
	peer := recoveryStart(t)
	peer.success(t, recoveryRequest{Action: "raw", Root: f.root})
	peer.success(t, recoveryRequest{Action: "exec", SQL: `PRAGMA wal_autocheckpoint=0; INSERT INTO extension_metadata VALUES('wal-crash-control','accepted before crash')`})
	recoveryRange(t, len(recoveryBytes(t, f.path+"-wal")), 1, 1<<30)
	recoveryRange(t, len(recoveryBytes(t, f.path+"-shm")), 1, 1<<30)
	f.before = recoveryInspect(t, f)
	recoveryDamageIndex(t, f)
	p, report := recoveryTraceStart(t, f, "crash-"+member)
	p.success(t, recoveryRequest{Action: "manager", Root: f.root})

	recoveryMust(t, p.encode.Encode(recoveryRequest{Action: "recover"}))
	p.stop(t)
	peer.stop(t)
	trace := recoveryReadTrace(t, report)
	recoveryEqual(t, recoveryRecover(t, f).Failed, false)
	after := recoveryInspect(t, f)

	recoveryEqual(t, trace.BackupMoved, true)
	recoveryEqual(t, trace.Killed, true)
	recoveryEqual(t, after.Rows, f.before.Rows)
	recoveryEqual(t, after.Integrity, "ok")
}

func TestRecovery_AC19Retry_RepeatedCorruptionStopsAfterOneCallbackRetry(t *testing.T) {
	f := recoveryNew(t)
	p := recoveryStart(t)
	p.success(t, recoveryRequest{Action: "manager", Root: f.root})

	response := p.call(t, recoveryRequest{Action: "corruption-loop"})
	p.stop(t)

	recoveryEqual(t, response.Failed, true)
	recoveryEqual(t, response.Calls, 2)
	recoveryEqual(t, recoveryRecover(t, f).Failed, false)
}

func TestRecovery_AC19Close_IntegrityObservationAndPostCloseRefusal(t *testing.T) {
	f := recoveryNew(t)
	p := recoveryStart(t)
	p.success(t, recoveryRequest{Action: "manager", Root: f.root})

	response := p.call(t, recoveryRequest{Action: "drain"})
	p.stop(t)

	recoveryEqual(t, response.Failed, false)
	recoveryEqual(t, response.IntegrityComplete, true)
	recoveryEqual(t, response.Drained, true)
	recoveryEqual(t, response.PostCloseCalls, 0)
	recoveryEqual(t, response.PostCloseRejected, true)
}
