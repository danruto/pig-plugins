package compatibility

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func fixtureCommand(t *testing.T, root, action string) *exec.Cmd {
	t.Helper()
	if os.Getenv("HERMES_PACKAGE") == "" {
		t.Fatal("HERMES_PACKAGE must point at the pinned Hermes source package")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate probe source")
	}
	project := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	cmd := exec.CommandContext(t.Context(), "node", "--import", "tsx", filepath.Join(filepath.Dir(file), "fixture.mjs"), action)
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+root)
	return cmd
}

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.Exec("PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;"); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestGoOpensHermesFTSAndTypeScriptReadsGoWrites(t *testing.T) {
	root := t.TempDir()
	seed, err := fixtureCommand(t, root, "seed").CombinedOutput()
	if err != nil {
		t.Fatalf("seed: %v\n%s", err, seed)
	}
	t.Logf("TypeScript SQLite: %s", seed)
	db := open(t, filepath.Join(root, "store", "sessions.db"))
	var version string
	if err := db.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("Go SQLite: %s", version)
	var content string
	if err := db.QueryRow("SELECT content FROM memory_fts WHERE memory_fts MATCH ?", `"日本語"`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "東京日本語 compatibility fixture" {
		t.Fatalf("trigram search: %q", content)
	}
	if _, err := db.Exec("INSERT INTO memories(target,content,created,last_referenced) VALUES ('memory','written by Go','2026-01-01','2026-01-01')"); err != nil {
		t.Fatal(err)
	}
	read, err := fixtureCommand(t, root, "read").CombinedOutput()
	if err != nil {
		t.Fatalf("TypeScript read: %v\n%s", err, read)
	}
	t.Logf("cross-language read: %s", read)
}

func TestTypeScriptHonorsGoLeaseAndReleasesItsOwnLease(t *testing.T) {
	root := t.TempDir()
	cmd := fixtureCommand(t, root, "hold")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		if err := input.Close(); err != nil {
			t.Error(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Errorf("TypeScript holder: %v\n%s", err, &stderr)
		}
	}
	t.Cleanup(stop)
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() || scanner.Text() != `{"acquired":true}` {
		t.Fatalf("holder failed: %q, %v", scanner.Text(), scanner.Err())
	}
	db := open(t, filepath.Join(root, ".pi-hermes-locks.sqlite"))
	var token string
	if err := db.QueryRow("SELECT token FROM locks WHERE lock_key='mutation:fixture'").Scan(&token); err != nil || token == "" {
		t.Fatalf("Go cannot observe TypeScript lease: %q %v", token, err)
	}
	stop()
	var released int
	if err := db.QueryRow("SELECT COUNT(*) FROM locks").Scan(&released); err != nil || released != 0 {
		t.Fatalf("TypeScript did not release its own lease: %d %v", released, err)
	}
	if _, err := db.Exec("INSERT INTO locks(lock_key,token,pid,incarnation,acquired_at) VALUES ('mutation:fixture','go-owner',?,NULL,CAST(strftime('%s','now') AS INTEGER)*1000)", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	output, err := fixtureCommand(t, root, "try").CombinedOutput()
	if err != nil {
		t.Fatalf("contender: %v\n%s", err, output)
	}
	var result struct{ Acquired bool }
	if err := json.Unmarshal(output, &result); err != nil || result.Acquired {
		t.Fatalf("TypeScript stole live Go lease: %s %v", output, err)
	}
	if _, err := db.Exec("DELETE FROM locks WHERE token='go-owner'"); err != nil {
		t.Fatal(err)
	}
	output, err = fixtureCommand(t, root, "try").CombinedOutput()
	if err != nil {
		t.Fatalf("unlocked contender: %v\n%s", err, output)
	}
	if err := json.Unmarshal(output, &result); err != nil || !result.Acquired {
		t.Fatalf("TypeScript cannot acquire released lease: %s %v", output, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM locks").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unreleased fixture lease: %d %v", count, err)
	}
}
