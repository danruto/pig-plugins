package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
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

type Lease struct {
	db               *sql.DB
	path, key, token string
	coordination     Coordination
	stopHeartbeat    context.CancelFunc
	heartbeatDone    chan struct{}
	releaseOnce      sync.Once
	releaseErr       error
}
type pendingRelease struct{ path, key, token string }

var pendingLeaseReleases = struct {
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

func openCoordinator(ctx context.Context, path string) (*sql.DB, error) {
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

var ErrLockContention = errors.New("shared Hermes lock contention; retry when the other writer finishes")

func (c Coordination) Acquire(ctx context.Context) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	db, err := openCoordinator(ctx, c.Database)
	if err != nil {
		return nil, err
	}
	lease := &Lease{db: db, path: c.Database, key: c.Key, coordination: c}
	lease.token, err = randomToken()
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := lease.wait(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	lease.startHeartbeat()
	return lease, nil
}

func (c Coordination) validate() error {
	if !filepath.IsAbs(c.Database) || c.Key == "" {
		return errors.New("coordination requires an absolute database and nonempty key")
	}
	for _, value := range []float64{c.WaitMS, c.PollMS, c.StaleMS} {
		if math.IsInf(value, 0) || math.IsNaN(value) {
			return errors.New("coordination timing must be finite")
		}
	}
	if c.WaitMS < 0 || c.PollMS < 0 || c.RenewInterval < 0 {
		return errors.New("coordination wait, poll and renewal interval must be nonnegative")
	}
	return nil
}

func (l *Lease) wait(ctx context.Context) error {
	started := time.Now()
	for {
		l.retryPending(ctx)
		sweepDeadLeaseRows(ctx, l.db, l.path)
		acquired, err := l.tryAcquire(ctx)
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
		remaining := l.coordination.WaitMS - time.Since(started).Seconds()*1000
		if remaining <= 0 {
			return ErrLockContention
		}
		if err := waitLeasePoll(ctx, min(remaining, l.coordination.PollMS)); err != nil {
			return err
		}
	}
}

func waitLeasePoll(ctx context.Context, milliseconds float64) error {
	started := time.Now()
	for {
		remaining := milliseconds - time.Since(started).Seconds()*1000
		if remaining <= 0 {
			return ctx.Err()
		}
		if err := waitLeaseTimer(ctx, time.Duration(min(remaining, 3600000)*float64(time.Millisecond))); err != nil {
			return err
		}
	}
}

func waitLeaseTimer(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (l *Lease) tryAcquire(ctx context.Context) (bool, error) {
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
	if err == nil && !leaseReclaimable(incarnation, observeProcess(pid), float64(now-acquired), l.coordination.StaleMS) {
		return false, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO locks(lock_key,token,pid,incarnation,acquired_at) VALUES (?,?,?,?,?) ON CONFLICT(lock_key) DO UPDATE SET token=excluded.token,pid=excluded.pid,incarnation=excluded.incarnation,acquired_at=excluded.acquired_at WHERE locks.token=?`, l.key, l.token, os.Getpid(), nullable(processIncarnation(os.Getpid())), now, oldToken)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

type processObservation struct {
	incarnation string
	alive       bool
}

func observeProcess(pid int) processObservation {
	incarnation := processIncarnation(pid)
	return processObservation{incarnation: incarnation, alive: incarnation != "" || processAlive(pid)}
}

func leaseReclaimable(incarnation sql.NullString, observed processObservation, ageMS, staleMS float64) bool {
	if staleMS > 0 && ageMS >= staleMS {
		return true
	}
	if !observed.alive {
		return true
	}
	if !incarnation.Valid || observed.incarnation == "" {
		return false
	}
	return incarnation.String != observed.incarnation
}

func (l *Lease) Release() error {
	l.releaseOnce.Do(func() {
		if l.stopHeartbeat != nil {
			l.stopHeartbeat()
			<-l.heartbeatDone
		}
		l.releaseErr = l.releaseOwned()
	})
	return l.releaseErr
}

func (l *Lease) releaseOwned() error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	row := pendingRelease{l.path, l.key, l.token}
	err := deleteOwnedLease(ctx, l.db, row)
	pendingLeaseReleases.Lock()
	if err != nil {
		pendingLeaseReleases.rows[row] = true
	} else {
		delete(pendingLeaseReleases.rows, row)
	}
	pendingLeaseReleases.Unlock()
	return errors.Join(err, l.db.Close())
}

func deleteOwnedLease(ctx context.Context, db *sql.DB, row pendingRelease) error {
	var err error
	for range 3 {
		_, err = db.ExecContext(ctx, "DELETE FROM locks WHERE lock_key=? AND token=?", row.key, row.token)
		if err == nil {
			return nil
		}
	}
	return err
}

func (l *Lease) retryPending(ctx context.Context) {
	pendingLeaseReleases.Lock()
	var rows []pendingRelease
	for row := range pendingLeaseReleases.rows {
		if row.path == l.path && row.key == l.key {
			rows = append(rows, row)
		}
	}
	pendingLeaseReleases.Unlock()
	for _, row := range rows {
		if deleteOwnedLease(ctx, l.db, row) != nil {
			continue
		}
		pendingLeaseReleases.Lock()
		delete(pendingLeaseReleases.rows, row)
		pendingLeaseReleases.Unlock()
	}
}

var leaseSweepTimes = struct {
	sync.Mutex
	paths map[string]time.Time
}{paths: make(map[string]time.Time)}

func leaseSweepDue(path string) bool {
	leaseSweepTimes.Lock()
	defer leaseSweepTimes.Unlock()
	now := time.Now()
	if now.Sub(leaseSweepTimes.paths[path]) < time.Minute {
		return false
	}
	leaseSweepTimes.paths[path] = now
	return true
}

func leaseHolderGone(pid int) bool {
	return pid > 0 && pid != os.Getpid() && !processAlive(pid) && processIncarnation(pid) == ""
}

func sweepDeadLeaseRows(ctx context.Context, db *sql.DB, path string) {
	if !leaseSweepDue(path) {
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
			_ = rows.Close()
			return
		}
		candidates = append(candidates, row)
	}
	scanErr := rows.Err()
	closeErr := rows.Close()
	if scanErr != nil || closeErr != nil {
		return
	}
	for _, row := range candidates {
		if !leaseHolderGone(row.pid) {
			continue
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM locks WHERE lock_key=? AND token=? AND acquired_at=?", row.key, row.token, row.acquired); err != nil {
			return
		}
	}
}

func (l *Lease) Token() string { return l.token }

func (l *Lease) CurrentOwner(ctx context.Context) (bool, error) {
	var token string
	err := l.db.QueryRowContext(ctx, "SELECT token FROM locks WHERE lock_key=?", l.key).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && token == l.token, err
}

func (l *Lease) Renew(ctx context.Context) (bool, error) {
	result, err := l.db.ExecContext(ctx, "UPDATE locks SET acquired_at=? WHERE lock_key=? AND token=?", time.Now().UnixMilli(), l.key, l.token)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (l *Lease) startHeartbeat() {
	if l.coordination.RenewInterval == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.stopHeartbeat = cancel
	l.heartbeatDone = make(chan struct{})
	go func() {
		defer close(l.heartbeatDone)
		ticker := time.NewTicker(l.coordination.RenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				owned, err := l.Renew(ctx)
				if err == nil && !owned {
					return
				}
			}
		}
	}()
}

func nullable(text string) any {
	if text == "" {
		return nil
	}
	return text
}
