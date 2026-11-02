package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"
)

func TestOpenBuildsTrigramTriggersForMemoriesAndMessages(t *testing.T) {
	db := native(t, filepath.Join(t.TempDir(), "sessions.db"))
	execSQL(t, db, `INSERT INTO memories(target,content,created,last_referenced,category,hit_count,last_hit_at) VALUES ('memory','東京日本語 substring','2026-01-01','2026-01-02','insight',7,'2026-01-03'); INSERT INTO sessions(id,project,cwd,started_at) VALUES ('s','project','/synthetic/project','2026-01-01'); INSERT INTO messages(id,session_id,role,content,timestamp) VALUES ('m','s','user','東京日本語 message','2026-01-01');`)

	same(t, text(t, db, `SELECT content FROM memory_fts WHERE memory_fts MATCH '"日本語"'`), "東京日本語 substring")
	same(t, text(t, db, `SELECT content FROM message_fts WHERE message_fts MATCH '"日本語"'`), "東京日本語 message")
	execSQL(t, db, `UPDATE memories SET content='updated native phrase'; UPDATE messages SET content='updated message phrase';`)
	same(t, number(t, db, `SELECT count(*) FROM memory_fts WHERE memory_fts MATCH '"日本語"'`), 0)
	same(t, number(t, db, `SELECT count(*) FROM message_fts WHERE message_fts MATCH '"日本語"'`), 0)
	same(t, text(t, db, `SELECT content FROM memory_fts WHERE memory_fts MATCH '"native"'`), "updated native phrase")
	same(t, text(t, db, `SELECT content FROM message_fts WHERE message_fts MATCH '"message"'`), "updated message phrase")
	execSQL(t, db, `DELETE FROM memories; DELETE FROM messages;`)
	same(t, number(t, db, `SELECT count(*) FROM memory_fts WHERE memory_fts MATCH '"native"'`), 0)
	same(t, number(t, db, `SELECT count(*) FROM message_fts WHERE message_fts MATCH '"message"'`), 0)
	same(t, text(t, db, `PRAGMA integrity_check`), "ok")
}

func TestOpenEnforcesSharedMemoryAndMessageConstraints(t *testing.T) {
	db := native(t, filepath.Join(t.TempDir(), "sessions.db"))
	execSQL(t, db, `INSERT INTO memories(target,category,content,created,last_referenced) VALUES ('user','preference','valid preference','2026-01-01','2026-01-01'); INSERT INTO sessions(id,project,cwd,started_at) VALUES ('s','project','/synthetic/project','2026-01-01'); INSERT INTO messages(id,session_id,role,content,timestamp) VALUES ('m','s','assistant','valid response','2026-01-01');`)
	same(t, text(t, db, `SELECT target||'|'||category FROM memories`), "user|preference")
	same(t, text(t, db, `SELECT role FROM messages`), "assistant")
	tests := []struct{ name, query string }{
		{"unsupported target", `INSERT INTO memories(target,content,created,last_referenced) VALUES ('sqlite-only','invalid','2026-01-01','2026-01-01')`},
		{"unsupported category", `INSERT INTO memories(target,category,content,created,last_referenced) VALUES ('memory','unsupported','invalid','2026-01-01','2026-01-01')`},
		{"unsupported role", `INSERT INTO messages(id,session_id,role,content,timestamp) VALUES ('invalid','s','tool','invalid','2026-01-01')`},
		{"orphan message", `INSERT INTO messages(id,session_id,role,content,timestamp) VALUES ('orphan','missing','user','invalid','2026-01-01')`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.ExecContext(t.Context(), tt.query)
			reject(t, err)
		})
	}
}

func TestOpenPreservesSharedMetadataAndSessionFilesOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db := native(t, path)
	execSQL(t, db, `INSERT INTO extension_metadata(key,value) VALUES ('mdsync:v1:["memory",null]','{"synthetic":true}'); INSERT INTO sessions(id,project,cwd,started_at,message_count) VALUES ('s','project','/synthetic/project','2026-01-01',3); INSERT INTO session_files(path,session_id,size,mtime_ms,indexed_at) VALUES ('/synthetic/pi/session.jsonl','s',123,456,'2026-01-02');`)
	closeDB(t, db)

	reopened := native(t, path)
	same(t, text(t, reopened, `SELECT value FROM extension_metadata WHERE key='mdsync:v1:["memory",null]'`), `{"synthetic":true}`)
	same(t, text(t, reopened, `SELECT path||'|'||size||'|'||mtime_ms||'|'||indexed_at FROM session_files`), "/synthetic/pi/session.jsonl|123|456|2026-01-02")
	same(t, number(t, reopened, `SELECT message_count FROM sessions WHERE id='s'`), 3)
	same(t, number(t, reopened, `PRAGMA foreign_keys`), 1)
	same(t, text(t, reopened, `PRAGMA journal_mode`), "wal")
	same(t, number(t, reopened, `PRAGMA busy_timeout`), 5000)
	execSQL(t, reopened, `DELETE FROM sessions WHERE id='s'`)
	same(t, number(t, reopened, `SELECT count(*) FROM session_files`), 0)
}

func TestOpenMigratesLegacyColumnsTargetsAndSessionProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db := raw(t, path)
	execSQL(t, db, `CREATE TABLE memories(id INTEGER PRIMARY KEY AUTOINCREMENT,project TEXT,target TEXT NOT NULL CHECK(target IN ('memory','user')),content TEXT NOT NULL,created DATE NOT NULL,last_referenced DATE NOT NULL); INSERT INTO memories VALUES (41,'project','memory','legacy 日本語 phrase','2026-01-01','2026-01-02'); CREATE TABLE extension_metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO extension_metadata VALUES ('custom','preserved'); CREATE TABLE sessions(id TEXT PRIMARY KEY,project TEXT NOT NULL,cwd TEXT NOT NULL,started_at TEXT NOT NULL,ended_at TEXT,message_count INTEGER DEFAULT 0); INSERT INTO sessions VALUES ('s','','/synthetic/project','2026-01-01',NULL,2);`)
	closeDB(t, db)

	migrated := native(t, path)
	same(t, text(t, migrated, `SELECT content||'|'||created||'|'||last_referenced FROM memories WHERE id=41`), "legacy 日本語 phrase|2026-01-01|2026-01-02")
	same(t, text(t, migrated, `SELECT value FROM extension_metadata WHERE key='custom'`), "preserved")
	same(t, text(t, migrated, `SELECT project FROM sessions WHERE id='s'`), "project")
	same(t, number(t, migrated, `SELECT hit_count FROM memories WHERE id=41`), 0)
	same(t, text(t, migrated, `SELECT content FROM memory_fts WHERE memory_fts MATCH '"日本語"'`), "legacy 日本語 phrase")
	execSQL(t, migrated, `INSERT INTO memories(target,category,content,created,last_referenced,hit_count,last_hit_at,failure_reason,tool_state,corrected_to) VALUES ('failure','failure','new failure','2026-01-03','2026-01-03',2,'2026-01-03','synthetic','{}','fixed')`)
	same(t, text(t, migrated, `SELECT content FROM memory_fts WHERE memory_fts MATCH '"failure"'`), "new failure")
}

func TestOpenRebuildsStaleTokenizerFromOwningRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db := native(t, path)
	execSQL(t, db, `INSERT INTO memories(target,content,created,last_referenced) VALUES ('memory','東京日本語 rebuild','2026-01-01','2026-01-01'); DROP TABLE memory_fts; CREATE VIRTUAL TABLE memory_fts USING fts5(content,content='memories',content_rowid='id',tokenize='unicode61'); UPDATE extension_metadata SET value='legacy' WHERE key='fts5_tokenizer_version';`)
	closeDB(t, db)

	migrated := native(t, path)
	same(t, text(t, migrated, `SELECT content FROM memory_fts WHERE memory_fts MATCH '"日本語"'`), "東京日本語 rebuild")
	same(t, text(t, migrated, `SELECT value FROM extension_metadata WHERE key='fts5_tokenizer_version'`), "trigram-v1")
	execSQL(t, migrated, `UPDATE memories SET content='replacement 日本語'`)
	same(t, text(t, migrated, `SELECT content FROM memory_fts WHERE memory_fts MATCH '"日本語"'`), "replacement 日本語")
}

func TestOpenFailedMigrationLeavesOriginalRowsAndMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db := native(t, path)
	execSQL(t, db, `INSERT INTO memories(target,content,created,last_referenced) VALUES ('memory','do not discard','2026-01-01','2026-01-01'); INSERT INTO extension_metadata VALUES ('custom','original'); DROP TABLE memory_fts; CREATE VIRTUAL TABLE memory_fts USING fts5(content,content='memories',content_rowid='id',tokenize='unicode61'); UPDATE extension_metadata SET value='legacy' WHERE key='fts5_tokenizer_version'; CREATE TRIGGER synthetic_migration_failure BEFORE UPDATE ON extension_metadata BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END;`)
	closeDB(t, db)

	_, err := store.Open(t.Context(), path)
	reject(t, err)
	preserved := raw(t, path)
	same(t, text(t, preserved, `SELECT content FROM memories WHERE id=1`), "do not discard")
	same(t, text(t, preserved, `SELECT value FROM extension_metadata WHERE key='custom'`), "original")
	same(t, text(t, preserved, `SELECT value FROM extension_metadata WHERE key='fts5_tokenizer_version'`), "legacy")
	same(t, number(t, preserved, `SELECT count(*) FROM sqlite_schema WHERE name='memory_fts' AND sql LIKE '%unicode61%'`), 1)
}

func TestOpenHonorsCancellationWithoutLosingExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db := native(t, path)
	execSQL(t, db, `INSERT INTO extension_metadata(key,value) VALUES ('custom','preserved')`)
	closeDB(t, db)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := store.Open(ctx, path)
	same(t, errors.Is(err, context.Canceled), true)
	preserved := raw(t, path)
	same(t, text(t, preserved, `SELECT value FROM extension_metadata WHERE key='custom'`), "preserved")

	for _, tt := range []openAdmissionCase{
		{"canceled after context use with held writer", cancelAfterContextUse, holdIndependentWriter, context.Canceled},
		{"deadline during held writer", liveDeadline, holdIndependentWriter, context.DeadlineExceeded},
		{"uncontended live deadline", liveDeadline, withoutWriter, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkOpenAdmission(t, tt)
		})
	}
}

type openAdmissionCase struct {
	name    string
	context func(testing.TB) (context.Context, func())
	writer  func(testing.TB, string) func()
	want    error
}

type observedContext struct {
	context.Context
	consulted chan struct{}
	once      sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.consulted) })
	return c.Context.Done()
}

func cancelAfterContextUse(t testing.TB) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	observed := &observedContext{Context: ctx, consulted: make(chan struct{})}
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-observed.consulted:
			cancel()
		case <-stop:
		}
	}()
	var once sync.Once
	finish := func() { once.Do(func() { close(stop); cancel(); <-joined }) }
	t.Cleanup(finish)
	return observed, finish
}

func liveDeadline(t testing.TB) (context.Context, func()) {
	t.Helper()
	return context.WithTimeout(t.Context(), 250*time.Millisecond)
}

func holdIndependentWriter(t testing.TB, path string) func() {
	t.Helper()
	db := raw(t, path)
	execSQL(t, db, `BEGIN IMMEDIATE; UPDATE extension_metadata SET value='uncommitted' WHERE key='custom'`)
	var once sync.Once
	release := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := db.ExecContext(ctx, `ROLLBACK`)
			must(t, err)
			closeDB(t, db)
		})
	}
	t.Cleanup(release)
	return release
}

func withoutWriter(testing.TB, string) func() { return func() {} }

func checkOpenAdmission(t testing.TB, tt openAdmissionCase) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.db")
	seed := native(t, path)
	execSQL(t, seed, `INSERT INTO memories(target,content,created,last_referenced) VALUES ('memory','preserved admission row','2026-01-01','2026-01-02'); INSERT INTO extension_metadata(key,value) VALUES ('custom','preserved');`)
	closeDB(t, seed)
	release := tt.writer(t, path)
	ctx, finish := tt.context(t)
	same(t, ctx.Err(), nil)

	started := time.Now()
	db, err := store.Open(ctx, path)
	elapsed := time.Since(started)
	finish()
	release()

	if !errors.Is(err, tt.want) {
		t.Errorf("Open error = %v; want errors.Is(_, %v)", err, tt.want)
	}
	if elapsed >= 2*time.Second {
		t.Errorf("Open took %s; want < 2s, below normal 5000ms busy wait", elapsed)
	}
	if tt.want != nil && db != nil {
		t.Error("failed Open returned a database handle")
	}
	if tt.want == nil && db == nil {
		t.Error("uncontended Open returned no database handle")
	}
	if db != nil {
		closeDB(t, db)
	}
	preserved := native(t, path)
	same(t, text(t, preserved, `SELECT content||'|'||created||'|'||last_referenced FROM memories`), "preserved admission row|2026-01-01|2026-01-02")
	same(t, text(t, preserved, `SELECT value FROM extension_metadata WHERE key='custom'`), "preserved")
	same(t, text(t, preserved, `SELECT value FROM extension_metadata WHERE key='fts5_tokenizer_version'`), "trigram-v1")
	same(t, number(t, preserved, `PRAGMA foreign_keys`), 1)
	same(t, text(t, preserved, `PRAGMA journal_mode`), "wal")
	same(t, number(t, preserved, `PRAGMA busy_timeout`), 5000)
	same(t, text(t, preserved, `PRAGMA integrity_check`), "ok")
	closeDB(t, preserved)
	independent := raw(t, path)
	execSQL(t, independent, `BEGIN EXCLUSIVE; INSERT INTO extension_metadata(key,value) VALUES ('after-open','writable'); ROLLBACK;`)
	var busy, logFrames, checkpointed int
	must(t, independent.QueryRowContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed))
	same(t, busy, 0)
	closeDB(t, independent)
	t.Logf("Open elapsed=%s; preserved rows/metadata, reopen settings, rollback and checkpoint passed", elapsed)
}

func TestNativeAndFrozenTypeScriptExchangeSharedRows(t *testing.T) {
	tests := []struct {
		name string
		seed func(testing.TB, string)
	}{{"typescript creates schema", func(t testing.TB, root string) { frozen(t, root, "checkpoint-seed") }}, {"native creates schema", nativeSeed}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.seed(t, root)
			db := native(t, filepath.Join(root, "store", "sessions.db"))

			same(t, text(t, db, `SELECT content||'|'||category||'|'||hit_count||'|'||last_hit_at FROM memories`), "東京日本語 checkpoint|insight|7|2026-01-03")
			same(t, text(t, db, `SELECT content FROM memory_fts WHERE memory_fts MATCH '"日本語"'`), "東京日本語 checkpoint")
			same(t, text(t, db, `SELECT value FROM extension_metadata WHERE key='checkpoint'`), `{"synthetic":true}`)
			execSQL(t, db, `INSERT INTO memories(target,content,category,created,last_referenced,hit_count) VALUES ('memory','native checkpoint','preference','2026-01-04','2026-01-04',2)`)
			closeDB(t, db)
			frozen(t, root, "checkpoint-read")
		})
	}
}

func nativeSeed(t testing.TB, root string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Join(root, "store"), 0o700))
	db := native(t, filepath.Join(root, "store", "sessions.db"))
	execSQL(t, db, `INSERT INTO memories(target,content,created,last_referenced,category,hit_count,last_hit_at) VALUES ('memory','東京日本語 checkpoint','2026-01-01','2026-01-02','insight',7,'2026-01-03'); INSERT INTO extension_metadata(key,value) VALUES ('checkpoint','{"synthetic":true}');`)
	closeDB(t, db)
}

func frozen(t testing.TB, root, action string) {
	t.Helper()
	if os.Getenv("HERMES_PACKAGE") == "" {
		t.Skip("set HERMES_PACKAGE to reconstructed frozen public source")
	}
	_, file, _, ok := runtime.Caller(0)
	same(t, ok, true)
	repo := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	cmd := exec.CommandContext(t.Context(), "node", "--import", "tsx", filepath.Join(repo, "probes/hermes-sqlite/configuration-fixture.mjs"), action)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+root)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("frozen fixture %s: %v\n%s", action, err, output)
	}
	t.Logf("frozen %s: %s", action, output)
}
func native(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := store.Open(t.Context(), path)
	must(t, err)
	t.Cleanup(func() { must(t, db.Close()) })
	return db
}
func raw(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	must(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { must(t, db.Close()) })
	return db
}
func closeDB(t testing.TB, db *sql.DB) { t.Helper(); must(t, db.Close()) }
func execSQL(t testing.TB, db *sql.DB, q string) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), q)
	must(t, err)
}
func text(t testing.TB, db *sql.DB, q string) string {
	t.Helper()
	var got string
	must(t, db.QueryRowContext(t.Context(), q).Scan(&got))
	return got
}
func number(t testing.TB, db *sql.DB, q string) int {
	t.Helper()
	var got int
	must(t, db.QueryRowContext(t.Context(), q).Scan(&got))
	return got
}
func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func reject(t testing.TB, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected rejection")
	}
}
func same(t testing.TB, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v; want %#v", got, want)
	}
}
