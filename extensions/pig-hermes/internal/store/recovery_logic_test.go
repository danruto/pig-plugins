package store

import (
	"reflect"
	"testing"
	"time"
)

func TestRecentFailuresRetainsBoundaryAndFutureClockValues(t *testing.T) {
	failures := []int64{800, 899, 900, 901, 1000, 1100}
	got := recentFailures(failures, 1000, 100*time.Millisecond)
	want := []int64{900, 901, 1000, 1100}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("window: got %v, want %v", got, want)
	}
	advanced := recentFailures(got, 1200, 100*time.Millisecond)
	if !reflect.DeepEqual(advanced, []int64{1100}) {
		t.Fatalf("advanced window: %v", advanced)
	}
	if !reflect.DeepEqual(failures, []int64{800, 899, 900, 901, 1000, 1100}) {
		t.Fatal("failure history mutated")
	}
}

func TestBackupRetentionPrioritizesOriginalBeforeFailedCandidateAndHistory(t *testing.T) {
	current := []string{"original", "original-wal", "original-shm", "failed", "failed-wal"}
	original := databaseBackup{paths: []string{"original-shm", "original"}}
	failed := databaseBackup{paths: []string{"failed-wal", "failed"}}
	historical := databaseBackup{paths: []string{"historical"}}
	if backupPriority(original, current) >= backupPriority(failed, current) {
		t.Fatal("failed candidate could evict the original rollback source")
	}
	if backupPriority(failed, current) >= backupPriority(historical, current) {
		t.Fatal("historical backup could evict the current failed candidate")
	}
}

func TestRebuiltMetadataDropsMirrorClaimsButKeepsProvenance(t *testing.T) {
	for _, key := range []string{`mdsync:v1:["memory",null]`, `mdsync:v1:["failure","project"]`, "fts5_tokenizer_version"} {
		if !rebuiltMetadataExcluded(key) {
			t.Errorf("stale derived metadata retained: %q", key)
		}
	}
	for _, key := range []string{"source:session:synthetic", "import:claude-code", "index:last-success", "mdsync:v2:future", "unrelated"} {
		if rebuiltMetadataExcluded(key) {
			t.Errorf("unrelated metadata dropped: %q", key)
		}
	}
}
