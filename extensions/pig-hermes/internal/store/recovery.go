package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func readOnlyDatabase(path string) (*sql.DB, error) { return rawDatabase(path, "ro") }

func rawDatabase(path, mode string) (*sql.DB, error) {
	uri := url.URL{Scheme: "file", Path: path}
	uri.RawQuery = url.Values{"mode": {mode}, "_pragma": {"busy_timeout(5000)"}, "_txlock": {"immediate"}}.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func (d *Database) recover(ctx context.Context) (result RecoveryResult, err error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	d.status.Degraded, d.status.Reason = true, "recovery-failed"
	defer func() {
		d.status.Recovery = result
		if err != nil {
			d.status.Degraded, d.status.Reason = true, "recovery-failed"
			err = &RecoveryError{Result: result, Cause: err}
		}
	}()
	if err := d.closeHandle(); err != nil {
		return result, err
	}
	coordination, err := RecoveryCoordination(d.path, d.options.Lock)
	if err != nil {
		return result, err
	}
	lease, err := coordination.Acquire(ctx)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Release()) }()
	failures, err := d.recoveryFailures()
	if err != nil {
		return result, err
	}
	healthy, err := d.healthyCurrent(ctx)
	if err != nil {
		return result, err
	}
	if healthy {
		return d.reuse(ctx)
	}
	if len(failures) >= d.options.CircuitLimit {
		return result, ErrRecoveryCircuit
	}
	if err := d.restoreInterrupted(ctx, lease); err != nil {
		return result, errors.Join(err, d.recordFailure(failures))
	}
	result, err = d.rebuild(ctx, lease)
	if err != nil {
		return result, errors.Join(err, d.recordFailure(failures), d.retainFailedBackups(lease, result))
	}
	d.status.Recovery = result
	d.status.Degraded, d.status.Reason = false, "recovered"
	if err := d.clearFailures(); err != nil {
		return result, err
	}
	if err := d.pruneDatabaseBackups(ctx, lease, result.BackupPaths, *d.options.BackupRetention); err != nil {
		return result, err
	}
	return result, nil
}

func (d *Database) retainFailedBackups(lease *Lease, result RecoveryResult) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.pruneDatabaseBackups(ctx, lease, result.BackupPaths, max(1, *d.options.BackupRetention))
}

func (d *Database) healthyCurrent(ctx context.Context) (bool, error) {
	info, err := os.Lstat(d.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("database must be a regular file")
	}
	db, err := rawDatabase(d.path, "rw")
	if err != nil {
		return false, err
	}
	err = checkDatabase(ctx, db)
	closeErr := db.Close()
	if closeErr != nil {
		return false, closeErr
	}
	if isDatabaseCorruption(err) {
		return false, nil
	}
	return err == nil, err
}

func (d *Database) reuse(ctx context.Context) (RecoveryResult, error) {
	var err error
	d.db, err = Open(ctx, d.path)
	result := RecoveryResult{Strategy: "reused"}
	if err != nil {
		return result, err
	}
	if err := d.clearFailures(); err != nil {
		return result, err
	}
	d.status.Recovery, d.status.Degraded, d.status.Reason = result, false, "reused"
	return result, nil
}

func (d *Database) rebuild(ctx context.Context, lease *Lease) (result RecoveryResult, err error) {
	token, err := randomToken()
	if err != nil {
		return result, err
	}
	temp := d.path + ".rebuild-" + token
	backup := d.path + ".corrupt-" + fmt.Sprintf("%013d-", recoveryNow()) + token
	defer func() { err = errors.Join(err, removeDatabaseFiles(temp)) }()
	result, err = rebuildReadable(ctx, d.path, temp)
	if err != nil {
		return result, err
	}
	if err := preserveDatabaseMode(d.path, temp); err != nil {
		return result, err
	}
	result.BackupPaths, err = moveDatabaseFiles(ctx, databaseMove{d.path, backup}, lease)
	if err != nil {
		return result, err
	}
	if err := requireRecoveryOwner(ctx, lease); err != nil {
		return result, errors.Join(err, restoreDatabaseFiles(d.path, backup))
	}
	if err := ctx.Err(); err != nil {
		return result, errors.Join(err, restoreDatabaseFiles(d.path, backup))
	}
	if err := os.Rename(temp, d.path); err != nil {
		return result, errors.Join(err, restoreDatabaseFiles(d.path, backup))
	}
	candidate, err := os.Lstat(d.path)
	if err != nil {
		return result, err
	}
	d.db, err = Open(ctx, d.path)
	if err == nil {
		err = checkDatabase(ctx, d.db)
	}
	if err != nil {
		closeErr := d.closeHandle()
		restoreErr := requireRecoveryOwner(ctx, lease)
		if restoreErr == nil {
			restoreErr = rollbackDatabase(d.path, backup+"-failed", candidate)
		}
		for _, suffix := range databaseSuffixes {
			if _, statErr := os.Lstat(backup + "-failed" + suffix); statErr == nil {
				result.BackupPaths = append(result.BackupPaths, backup+"-failed"+suffix)
			}
		}
		if restoreErr == nil {
			restoreErr = restoreDatabaseFiles(d.path, backup)
		}
		return result, errors.Join(err, closeErr, restoreErr)
	}
	return result, nil
}

func preserveDatabaseMode(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("recovery source must be a regular file")
	}
	return os.Chmod(target, info.Mode().Perm())
}

func rebuildReadable(ctx context.Context, sourcePath, targetPath string) (result RecoveryResult, err error) {
	target, err := Open(ctx, targetPath)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, target.Close()) }()
	source, err := readOnlyDatabase(sourcePath)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	result = RecoveryResult{Strategy: "rebuilt", RecoveredRows: map[string]int{}}
	for _, table := range recoveryTables {
		count, copyErr := copyReadableTable(ctx, source, target, table)
		result.RecoveredRows[table.name] = count
		if copyErr != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			if !isRecoverableReadFailure(copyErr) {
				return result, copyErr
			}
			result.UnreadableTables = append(result.UnreadableTables, table.name)
			result.Degraded = true
		}
	}
	if err := finalizeRebuild(ctx, target); err != nil {
		return result, err
	}
	total := 0
	for _, count := range result.RecoveredRows {
		total += count
	}
	if total == 0 && result.Degraded {
		result.Strategy = "recreated-empty"
	}
	return result, nil
}

type recoveryTable struct{ name, columns string }

var recoveryTables = []recoveryTable{
	{"extension_metadata", "key,value"},
	{"sessions", "id,project,cwd,started_at,ended_at,message_count"},
	{"messages", "id,session_id,role,content,timestamp,tool_calls"},
	{"session_files", "path,session_id,size,mtime_ms,indexed_at"},
	{"memories", "id,project,target,category,content,failure_reason,tool_state,corrected_to,created,last_referenced,hit_count,last_hit_at"},
}

func copyReadableTable(ctx context.Context, source, target *sql.DB, table recoveryTable) (int, error) {
	rows, err := source.QueryContext(ctx, "SELECT "+recoverySelection(table)+" FROM "+table.name+" NOT INDEXED")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	names := strings.Split(table.columns, ",")
	values, pointers := make([]any, len(names)), make([]any, len(names))
	for i := range values {
		pointers[i] = &values[i]
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	count := 0
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return count, err
		}
		if table.name == "extension_metadata" && rebuiltMetadataExcluded(values[0]) {
			continue
		}
		if _, err := target.ExecContext(ctx, "INSERT INTO "+table.name+"("+table.columns+") VALUES ("+marks+")", values...); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func recoverySelection(table recoveryTable) string {
	names := strings.Split(table.columns, ",")
	for i, name := range names {
		switch name {
		case "created", "last_referenced", "last_hit_at":
			names[i] = "CAST(" + name + " AS TEXT)"
		}
	}
	return strings.Join(names, ",")
}

func rebuiltMetadataExcluded(key any) bool {
	text, ok := key.(string)
	return ok && (strings.HasPrefix(text, "mdsync:v1:") || text == "fts5_tokenizer_version")
}

func isRecoverableReadFailure(err error) bool {
	return isDatabaseCorruption(err)
}

func finalizeRebuild(ctx context.Context, db *sql.DB) error {
	for _, statement := range []string{
		"INSERT INTO message_fts(message_fts) VALUES ('rebuild')",
		"INSERT INTO memory_fts(memory_fts) VALUES ('rebuild')",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := checkDatabase(ctx, db); err != nil {
		return err
	}
	var busy, log, checkpointed int
	if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("rebuild checkpoint busy")
	}
	return nil
}

var databaseSuffixes = []string{"", "-wal", "-shm"}

func requireRecoveryOwner(ctx context.Context, lease *Lease) error {
	owned, err := lease.CurrentOwner(ctx)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("database recovery lease lost")
	}
	return nil
}

type databaseMove struct{ path, backup string }

func moveDatabaseFiles(ctx context.Context, move databaseMove, lease *Lease) (moved []string, err error) {
	path, backup := move.path, move.backup
	for _, suffix := range databaseSuffixes {
		if err := requireRecoveryOwner(ctx, lease); err != nil {
			return moved, errors.Join(err, restoreDatabaseFiles(path, backup))
		}
		info, statErr := os.Lstat(path + suffix)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr == nil && !info.Mode().IsRegular() {
			statErr = errors.New("database file set must contain regular files")
		}
		if statErr == nil {
			statErr = os.Rename(path+suffix, backup+suffix)
		}
		if statErr != nil {
			return moved, errors.Join(statErr, restoreDatabaseFiles(path, backup))
		}
		moved = append(moved, backup+suffix)
	}
	return moved, nil
}

func restoreDatabaseFiles(path, backup string) error {
	var failures []error
	for _, suffix := range databaseSuffixes {
		_, err := os.Lstat(backup + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil {
			err = restoreFile(backup+suffix, path+suffix)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		return errors.Join(ErrRestoreFailed, errors.Join(failures...))
	}
	return nil
}

func rollbackDatabase(path, failedBackup string, candidate os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		return errors.Join(ErrRestoreFailed, err)
	}
	if !os.SameFile(current, candidate) {
		return ErrRestoreFailed
	}
	if err := os.Rename(path, failedBackup); err != nil {
		return errors.Join(ErrRestoreFailed, err)
	}
	for _, suffix := range databaseSuffixes[1:] {
		if err := os.Rename(path+suffix, failedBackup+suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.Join(ErrRestoreFailed, err)
		}
	}
	return nil
}

func removeDatabaseFiles(path string) error {
	var failures []error
	for _, suffix := range databaseSuffixes {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (d *Database) interruptedRecovery() (bool, error) {
	_, err := os.Lstat(d.path)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	backups, err := d.databaseBackups()
	return slices.ContainsFunc(backups, func(backup databaseBackup) bool { return backup.main }), err
}

func (d *Database) restoreInterrupted(ctx context.Context, lease *Lease) error {
	interrupted, err := d.interruptedRecovery()
	if err != nil || !interrupted {
		return err
	}
	backups, err := d.databaseBackups()
	if err != nil {
		return err
	}
	if err := requireRecoveryOwner(ctx, lease); err != nil {
		return err
	}
	index := slices.IndexFunc(backups, func(backup databaseBackup) bool {
		return backup.main && !strings.HasSuffix(backup.name, "-failed")
	})
	if index < 0 {
		index = slices.IndexFunc(backups, func(backup databaseBackup) bool { return backup.main })
	}
	if index < 0 {
		return ErrRestoreFailed
	}
	backup := filepath.Join(filepath.Dir(d.path), backups[index].name)
	for _, suffix := range databaseSuffixes {
		if _, err := os.Lstat(d.path + suffix); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := os.Lstat(backup + suffix); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := restoreFile(backup+suffix, d.path+suffix); err != nil {
			return errors.Join(ErrRestoreFailed, err)
		}
	}
	return nil
}
