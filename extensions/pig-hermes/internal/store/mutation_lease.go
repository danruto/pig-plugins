package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type mutationLease struct {
	db               *sql.DB
	path, key, token string
}
type pendingRelease struct{ path, key, token string }

var pendingMutationReleases = struct {
	sync.Mutex
	rows map[pendingRelease]bool
}{rows: make(map[pendingRelease]bool)}

func randomToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	token[6], token[8] = token[6]&0x0f|0x40, token[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", token[:4], token[4:6], token[6:8], token[8:10], token[10:]), nil
}

func processIncarnation(pid int) string {
	if pid <= 0 {
		return ""
	}
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return ""
		}
		end := strings.LastIndex(string(data), ") ")
		if end < 0 {
			return ""
		}
		fields := strings.Fields(string(data)[end+2:])
		if len(fields) > 19 {
			return fields[19]
		}
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return true
	}
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func openMutationCoordinator(ctx context.Context, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = file.Close()
	}
	if err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path}
	uri.RawQuery = url.Values{"_txlock": {"immediate"}, "_pragma": {"busy_timeout(5000)"}}.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := initializeCoordinator(ctx, db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func initializeCoordinator(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS locks (lock_key TEXT PRIMARY KEY,token TEXT NOT NULL,pid INTEGER NOT NULL,incarnation TEXT,acquired_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	var found int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('locks') WHERE name='incarnation'`).Scan(&found); err != nil {
		return err
	}
	if found != 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, "ALTER TABLE locks ADD COLUMN incarnation TEXT")
	if err == nil {
		return nil
	}
	if check := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('locks') WHERE name='incarnation'`).Scan(&found); check != nil {
		return errors.Join(err, check)
	}
	if found == 0 {
		return err
	}
	return nil
}

func acquireMutation(ctx context.Context, identity string) (*mutationLease, error) {
	path := filepath.Join(filepath.Dir(filepath.Dir(identity)), ".pi-hermes-locks.sqlite")
	db, err := openMutationCoordinator(ctx, path)
	if err != nil {
		return nil, err
	}
	lease := &mutationLease{db: db, path: path, key: "mutation:" + identity}
	lease.token, err = randomToken()
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	lease.retryPending(ctx)
	sweepDeadMutationRows(ctx, db, path)
	if err := lease.wait(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return lease, nil
}

func (l *mutationLease) wait(ctx context.Context) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		acquired, err := l.tryAcquire(ctx)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("memory mutation lock contention; retry when the other writer finishes")
		case <-tick.C:
		}
	}
}

func (l *mutationLease) tryAcquire(ctx context.Context) (bool, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var oldToken string
	var pid int
	var incarnation sql.NullString
	var acquired int64
	err = tx.QueryRowContext(ctx, "SELECT token,pid,incarnation,acquired_at FROM locks WHERE lock_key=?", l.key).Scan(&oldToken, &pid, &incarnation, &acquired)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	now := time.Now().UnixMilli()
	if err == nil && !leaseReclaimable(pid, incarnation, now-acquired) {
		return false, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO locks(lock_key,token,pid,incarnation,acquired_at) VALUES (?,?,?,?,?) ON CONFLICT(lock_key) DO UPDATE SET token=excluded.token,pid=excluded.pid,incarnation=excluded.incarnation,acquired_at=excluded.acquired_at WHERE locks.token=?`, l.key, l.token, os.Getpid(), nullable(processIncarnation(os.Getpid())), now, oldToken)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func leaseReclaimable(pid int, incarnation sql.NullString, age int64) bool {
	if age >= 300000 {
		return true
	}
	observed := processIncarnation(pid)
	alive := observed != "" || processAlive(pid)
	if !alive {
		return true
	}
	if !incarnation.Valid || observed == "" {
		return false
	}
	return incarnation.String != observed
}

func (l *mutationLease) release() error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	row := pendingRelease{l.path, l.key, l.token}
	err := deleteMutationLease(ctx, l.db, row)
	pendingMutationReleases.Lock()
	if err != nil {
		pendingMutationReleases.rows[row] = true
	} else {
		delete(pendingMutationReleases.rows, row)
	}
	pendingMutationReleases.Unlock()
	return errors.Join(err, l.db.Close())
}

func deleteMutationLease(ctx context.Context, db *sql.DB, row pendingRelease) error {
	var err error
	for range 3 {
		_, err = db.ExecContext(ctx, "DELETE FROM locks WHERE lock_key=? AND token=?", row.key, row.token)
		if err == nil {
			return nil
		}
	}
	return err
}

func (l *mutationLease) retryPending(ctx context.Context) {
	pendingMutationReleases.Lock()
	var rows []pendingRelease
	for row := range pendingMutationReleases.rows {
		if row.path == l.path && row.key == l.key {
			rows = append(rows, row)
		}
	}
	pendingMutationReleases.Unlock()
	for _, row := range rows {
		if deleteMutationLease(ctx, l.db, row) != nil {
			continue
		}
		pendingMutationReleases.Lock()
		delete(pendingMutationReleases.rows, row)
		pendingMutationReleases.Unlock()
	}
}

var mutationSweepTimes = struct {
	sync.Mutex
	paths map[string]time.Time
}{paths: make(map[string]time.Time)}

func mutationSweepDue(path string) bool {
	mutationSweepTimes.Lock()
	defer mutationSweepTimes.Unlock()
	now := time.Now()
	if now.Sub(mutationSweepTimes.paths[path]) < time.Minute {
		return false
	}
	mutationSweepTimes.paths[path] = now
	return true
}

func mutationHolderGone(pid int) bool {
	return pid > 0 && pid != os.Getpid() && !processAlive(pid) && processIncarnation(pid) == ""
}

func sweepDeadMutationRows(ctx context.Context, db *sql.DB, path string) {
	if !mutationSweepDue(path) {
		return
	}
	rows, err := db.QueryContext(ctx, "SELECT lock_key,token,pid,acquired_at FROM locks WHERE acquired_at<=? LIMIT 128", time.Now().UnixMilli()-60000)
	if err != nil {
		return
	}
	type candidate struct {
		key, token string
		pid        int
		acquired   int64
	}
	var candidates []candidate
	for rows.Next() {
		var row candidate
		if err := rows.Scan(&row.key, &row.token, &row.pid, &row.acquired); err != nil {
			break
		}
		candidates = append(candidates, row)
	}
	scanErr := rows.Err()
	closeErr := rows.Close()
	if scanErr != nil || closeErr != nil {
		return
	}
	for _, row := range candidates {
		if !mutationHolderGone(row.pid) {
			continue
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM locks WHERE lock_key=? AND token=? AND acquired_at=?", row.key, row.token, row.acquired); err != nil {
			return
		}
	}
}

func nullable(text string) any {
	if text == "" {
		return nil
	}
	return text
}
