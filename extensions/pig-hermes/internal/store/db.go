// Package store implements the pinned Hermes schema and guarded Markdown memory mutations.
// Markdown publication and scoped search reconciliation are separate outcomes. Mutations use
// the shared SQLite mutation lease and retain recovery/conflict artifacts and bounded retries.
// They do not qualify stale-holder publication safety or implement database recovery; cutover
// remains blocked pending mixed-writer qualification. Callers must inspect MutationResult even
// when an error is returned: cancellation and cleanup failures can follow document publication.
// Memory borrows its database; callers close it only after synchronous operations return.
// Coordination factories select shared mutation, consolidation and recovery identities. Acquire
// owns a coordinator handle until Release; consolidation leases also own a renewal worker that
// Release cancels and joins. Lease methods may run concurrently; callers must always release.
// Renew and CurrentOwner fence only lease rows, never subsequent document or database publication.
// Consolidation and recovery operations remain unimplemented; their ownership APIs are staged.
// RecoveryLockOptions controls lease timing only, not the deferred recovery circuit or backups.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/paths"
)

//go:embed schema.sql
var schema string

func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("database path must be absolute")
	}
	canonical, err := paths.Canonical(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(canonical), 0700); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: canonical}
	query := url.Values{"_txlock": {"immediate"}, "_pragma": {
		"busy_timeout(5000)", "wal_autocheckpoint(1000)", "journal_size_limit(5242880)", "foreign_keys(ON)",
	}}
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := initialize(ctx, db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func admitSchema(ctx context.Context, conn *sql.Conn) (*sql.Tx, error) {
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
		return nil, err
	}
	budget := time.NewTimer(5 * time.Second)
	defer budget.Stop()
	retry := time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()
	for {
		tx, err := beginSchema(ctx, conn)
		if err == nil {
			return tx, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		failure, ok := errors.AsType[*sqlite.Error](err)
		if !ok || failure.Code()&0xff != sqlite3.SQLITE_BUSY {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-budget.C:
			return nil, err
		case <-retry.C:
		}
	}
}

func beginSchema(ctx context.Context, conn *sql.Conn) (*sql.Tx, error) {
	if _, err := conn.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return nil, err
	}
	return conn.BeginTx(ctx, nil)
}

func initialize(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := admitSchema(ctx, conn)
	if err != nil {
		return fmt.Errorf("begin schema initialization: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	if err := ensureColumns(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create Hermes schema: %w", err)
	}
	replaced, err := migrateTarget(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if err := migrateTokenizer(ctx, tx, replaced); err != nil {
		return err
	}
	return tx.Commit()
}

func columns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[name] = true
	}
	return names, rows.Err()
}

func ensureColumns(ctx context.Context, tx *sql.Tx) error {
	additions := []struct{ table, name, definition string }{
		{"memories", "project", "TEXT"}, {"memories", "category", "TEXT"},
		{"memories", "failure_reason", "TEXT"}, {"memories", "tool_state", "TEXT"},
		{"memories", "corrected_to", "TEXT"}, {"memories", "hit_count", "INTEGER NOT NULL DEFAULT 0"},
		{"memories", "last_hit_at", "DATE"}, {"sessions", "project", "TEXT"},
	}
	for _, column := range additions {
		names, err := columns(ctx, tx, column.table)
		if err != nil {
			return err
		}
		if len(names) == 0 || names[column.name] {
			continue
		}
		statement := "ALTER TABLE " + column.table + " ADD COLUMN " + column.name + " " + column.definition
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("add legacy column: %w", err)
		}
	}
	return backfillProjects(ctx, tx)
}

func backfillProjects(ctx context.Context, tx *sql.Tx) error {
	names, err := columns(ctx, tx, "sessions")
	if err != nil || len(names) == 0 {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id, cwd FROM sessions WHERE project IS NULL OR trim(project) = ''")
	if err != nil {
		return err
	}
	type session struct{ id, cwd string }
	var sessions []session
	for rows.Next() {
		var row session
		if err := rows.Scan(&row.id, &row.cwd); err != nil {
			return errors.Join(err, rows.Close())
		}
		sessions = append(sessions, row)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, row := range sessions {
		project := "unknown"
		if strings.TrimSpace(row.cwd) != "" && filepath.Clean(row.cwd) != "/" {
			project = filepath.Base(row.cwd)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET project = ? WHERE id = ?", project, row.id); err != nil {
			return err
		}
	}
	return nil
}

var legacyTarget = regexp.MustCompile(`(?i)target\s+TEXT\s+NOT\s+NULL\s+CHECK\s*\(\s*target\s+IN\s*\(\s*'memory'\s*,\s*'user'\s*\)\s*\)`)

func migrateTarget(ctx context.Context, tx *sql.Tx) (rebuildState, error) {
	var tableSQL string
	if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='memories'").Scan(&tableSQL); err != nil {
		return false, err
	}
	if !legacyTarget.MatchString(tableSQL) {
		return false, nil
	}
	start := strings.Index(schema, "CREATE TABLE IF NOT EXISTS memories (")
	end := strings.Index(schema[start:], ");") + start + 2
	create := strings.Replace(schema[start:end], "IF NOT EXISTS memories", "memories_new", 1)
	fields := "id, project, target, category, content, failure_reason, tool_state, corrected_to, created, last_referenced, hit_count, last_hit_at"
	statements := []string{
		create,
		"INSERT INTO memories_new (" + fields + ") SELECT " + fields + " FROM memories",
		"DROP TABLE memories", "ALTER TABLE memories_new RENAME TO memories",
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return false, fmt.Errorf("migrate memory targets: %w", err)
		}
	}
	return true, nil
}

var trigram = regexp.MustCompile(`(?i)\btokenize\s*=\s*['"]trigram['"]`)

type rebuildState bool

func migrateTokenizer(ctx context.Context, tx *sql.Tx, replaced rebuildState) error {
	var version string
	err := tx.QueryRowContext(ctx, "SELECT value FROM extension_metadata WHERE key = 'fts5_tokenizer_version'").Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rebuild := bool(replaced) || version != "trigram-v1"
	for _, table := range []string{"message_fts", "memory_fts"} {
		var tableSQL string
		if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&tableSQL); err != nil {
			return err
		}
		rebuild = rebuild || !trigram.MatchString(tableSQL)
	}
	if !rebuild {
		return nil
	}
	statements := []string{
		"DROP TABLE message_fts", "DROP TABLE memory_fts", schema,
		"INSERT INTO message_fts(message_fts) VALUES ('rebuild')",
		"INSERT INTO memory_fts(memory_fts) VALUES ('rebuild')",
		"INSERT INTO extension_metadata(key,value) VALUES ('fts5_tokenizer_version','trigram-v1') ON CONFLICT(key) DO UPDATE SET value=excluded.value",
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate trigram indexes: %w", err)
		}
	}
	return nil
}
