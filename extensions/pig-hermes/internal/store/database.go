package store

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/paths"
)

type RecoveryOptions struct {
	Lock            RecoveryLockOptions
	CircuitLimit    int
	CircuitWindow   time.Duration
	BackupRetention *int
	Timeout         time.Duration
}

type RecoveryResult struct {
	Strategy         string
	BackupPaths      []string
	RecoveredRows    map[string]int
	UnreadableTables []string
	Degraded         bool
}

type RecoveryError struct {
	Result RecoveryResult
	Cause  error
}

func (e *RecoveryError) Error() string { return "database recovery failed; inspect retained backups" }
func (e *RecoveryError) Unwrap() error { return e.Cause }

type DatabaseStatus struct {
	Recovery          RecoveryResult
	IntegrityComplete bool
	Degraded          bool
	Reason            string
}

type Database struct {
	mu        sync.Mutex
	db        *sql.DB
	path      string
	options   RecoveryOptions
	status    DatabaseStatus
	stopping  atomic.Bool
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

var (
	ErrRecoveryCircuit  = errors.New("database recovery circuit is open")
	ErrDatabaseCorrupt  = errors.New("database integrity check failed")
	ErrRestoreFailed    = errors.New("database restore failed; backups retained")
	ErrDatabaseDegraded = errors.New("database is degraded; explicit recovery required")
)

func resolveRecoveryOptions(options RecoveryOptions) (RecoveryOptions, error) {
	if options.CircuitLimit == 0 {
		options.CircuitLimit = 3
	}
	if options.CircuitWindow == 0 {
		options.CircuitWindow = 300000 * time.Millisecond
	}
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.BackupRetention == nil {
		options.BackupRetention = new(3)
	} else {
		options.BackupRetention = new(*options.BackupRetention)
	}
	options.Lock = RecoveryLockOptions{cloneTiming(options.Lock.WaitMS), cloneTiming(options.Lock.PollMS), cloneTiming(options.Lock.StaleMS)}
	if options.CircuitLimit < 1 || options.CircuitWindow < 0 || options.Timeout < 0 || *options.BackupRetention < 0 {
		return options, errors.New("invalid recovery bounds")
	}
	return options, nil
}

func cloneTiming(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return new(*value)
}

func OpenDatabase(ctx context.Context, path string, options RecoveryOptions) (*Database, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("database path must be absolute")
	}
	options, err := resolveRecoveryOptions(options)
	if err != nil {
		return nil, err
	}
	identity, err := paths.Canonical(path)
	if err != nil {
		return nil, err
	}
	coordination, err := RecoveryCoordination(identity, options.Lock)
	if err != nil {
		return nil, err
	}
	if err := coordination.validate(); err != nil {
		return nil, err
	}
	d := &Database{path: identity, options: options, done: make(chan struct{})}
	if err := d.open(ctx); err != nil {
		return nil, errors.Join(err, d.closeHandle())
	}
	scanCtx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	d.cancel = cancel
	go func() {
		defer close(d.done)
		defer cancel()
		_ = d.CheckIntegrity(scanCtx)
	}()
	return d, nil
}

func (d *Database) open(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, d.options.Timeout)
	defer cancel()
	interrupted, err := d.interruptedRecovery()
	if err != nil {
		return err
	}
	if interrupted {
		_, err := d.recover(ctx)
		return err
	}
	d.db, err = Open(ctx, d.path)
	if !isDatabaseCorruption(err) {
		return err
	}
	_, err = d.recover(ctx)
	return err
}

func isDatabaseCorruption(err error) bool {
	if errors.Is(err, ErrDatabaseCorrupt) {
		return true
	}
	failure, ok := errors.AsType[*sqlite.Error](err)
	return ok && (failure.Code()&0xff == sqlite3.SQLITE_CORRUPT || failure.Code()&0xff == sqlite3.SQLITE_NOTADB)
}

func (d *Database) Do(ctx context.Context, operation func(*sql.DB) error) error {
	if d.stopping.Load() {
		return sql.ErrConnDone
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping.Load() {
		return sql.ErrConnDone
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.status.Degraded {
		return ErrDatabaseDegraded
	}
	err := operation(d.db)
	if !isDatabaseCorruption(err) {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, d.options.Timeout)
	defer cancel()
	if _, recoveryErr := d.recover(ctx); recoveryErr != nil {
		return errors.Join(err, recoveryErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err = operation(d.db)
	if isDatabaseCorruption(err) {
		d.status.Degraded, d.status.Reason = true, "retry-corrupt"
	}
	return err
}

func (d *Database) Recover(ctx context.Context) (RecoveryResult, error) {
	if d.stopping.Load() {
		return RecoveryResult{}, sql.ErrConnDone
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping.Load() {
		return RecoveryResult{}, sql.ErrConnDone
	}
	ctx, cancel := context.WithTimeout(ctx, d.options.Timeout)
	defer cancel()
	return d.recover(ctx)
}

func (d *Database) CheckIntegrity(ctx context.Context) error {
	if d.stopping.Load() {
		return sql.ErrConnDone
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping.Load() {
		return sql.ErrConnDone
	}
	ctx, cancel := context.WithTimeout(ctx, d.options.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	err := checkDatabase(ctx, d.db)
	if isDatabaseCorruption(err) {
		_, err = d.recover(ctx)
	}
	d.status.IntegrityComplete = true
	if err != nil && !d.stopping.Load() {
		d.status.Degraded, d.status.Reason = true, "integrity-failed"
	}
	return err
}

func (d *Database) Status() DatabaseStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := d.status
	result.Recovery.BackupPaths = slices.Clone(result.Recovery.BackupPaths)
	result.Recovery.UnreadableTables = slices.Clone(result.Recovery.UnreadableTables)
	result.Recovery.RecoveredRows = maps.Clone(result.Recovery.RecoveredRows)
	return result
}

func (d *Database) Close() error {
	d.closeOnce.Do(func() {
		d.stopping.Store(true)
		d.cancel()
		<-d.done
		d.mu.Lock()
		defer d.mu.Unlock()
		d.closeErr = d.closeHandle()
		if d.status.Degraded {
			d.closeErr = errors.Join(ErrDatabaseDegraded, d.closeErr)
		}
	})
	return d.closeErr
}

func (d *Database) closeHandle() error {
	if d.db == nil {
		return nil
	}
	err := d.db.Close()
	d.db = nil
	return err
}

func checkDatabase(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrDatabaseDegraded
	}
	rows, err := db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return errors.Join(err, rows.Close())
		}
		if result != "ok" {
			return errors.Join(ErrDatabaseCorrupt, rows.Close())
		}
		count++
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if count == 0 {
		return ErrDatabaseCorrupt
	}
	return checkSearchIndexes(ctx, db)
}

func checkSearchIndexes(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"message_fts", "memory_fts"} {
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+"("+table+", rank) VALUES ('integrity-check', 1)"); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	violated := rows.Next()
	err = errors.Join(rows.Err(), rows.Close())
	if violated {
		return errors.Join(ErrDatabaseCorrupt, err)
	}
	return err
}
