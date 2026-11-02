// Command publicationpeer exercises a synthetic Go writer against the frozen TypeScript store.
package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type peer struct {
	root  string
	db    *sql.DB
	locks *sql.DB
}

func open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func acquire(db *sql.DB, key string) error {
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	var token string
	var at int64
	err := db.QueryRow("SELECT token, acquired_at FROM locks WHERE lock_key=?", key).Scan(&token, &at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.Join(err, rollback(db))
	}
	now := time.Now().UnixMilli()
	if err == nil {
		now = max(now, at+300000)
	}
	result, err := db.Exec("INSERT INTO locks(lock_key,token,pid,incarnation,acquired_at) VALUES(?,?,?,NULL,?) ON CONFLICT(lock_key) DO UPDATE SET token=excluded.token,pid=excluded.pid,incarnation=excluded.incarnation,acquired_at=excluded.acquired_at WHERE locks.token=? AND ?-locks.acquired_at>=300000", key, "go-successor", os.Getpid(), now, token, now)
	if err != nil {
		return errors.Join(err, rollback(db))
	}
	count, err := result.RowsAffected()
	if err != nil {
		return errors.Join(err, rollback(db))
	}
	if count != 1 {
		return errors.Join(fmt.Errorf("expected one acquired lease, changed %d", count), rollback(db))
	}
	return commit(db)
}

func rollback(db *sql.DB) error {
	_, err := db.Exec("ROLLBACK")
	return err
}

func commit(db *sql.DB) error {
	_, err := db.Exec("COMMIT")
	return err
}

func (p *peer) publish() error {
	if err := acquire(p.locks, "mutation:"+filepath.Join(p.root, "memory", "MEMORY.md")); err != nil {
		return err
	}
	target := filepath.Join(p.root, "memory", "MEMORY.md")
	content, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	temp := target + ".go-tmp"
	if err := os.WriteFile(temp, append(content, []byte("\n\n§\n\ngo accepted")...), 0600); err != nil {
		return err
	}
	if err := os.Rename(temp, target); err != nil {
		return err
	}
	return p.insert()
}

func (p *peer) insert() error {
	_, err := p.db.Exec("INSERT INTO memories(project,target,category,content,created,last_referenced,hit_count,last_hit_at) VALUES(NULL,'memory','preference','go accepted','2026-01-01','2026-01-01',7,'2026-01-02')")
	return err
}

func (p *peer) rows() (out []map[string]any, err error) {
	rows, err := p.db.Query("SELECT project,target,category,content,hit_count,last_hit_at FROM memories ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out = []map[string]any{}
	for rows.Next() {
		var project, category, last sql.NullString
		var target, content string
		var count int
		if err := rows.Scan(&project, &target, &category, &content, &count, &last); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"project": nullable(project), "target": target, "category": nullable(category), "content": content, "hit_count": count, "last_hit_at": nullable(last)})
	}
	return out, rows.Err()
}

func nullable(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func (p *peer) takeoverRecovery() (err error) {
	db, err := open(filepath.Join(p.root, "memory", ".pi-hermes-locks.sqlite"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	return acquire(db, "recovery:"+filepath.Join(p.root, "memory", "sessions.db"))
}

func (p *peer) action(action string) (any, error) {
	switch action {
	case "publish":
		return map[string]any{"accepted": true}, p.publish()
	case "takeover-recovery":
		return map[string]any{"acquired": true}, p.takeoverRecovery()
	case "insert":
		return map[string]any{"accepted": true}, p.insert()
	case "rows":
		return p.rows()
	case "release":
		_, err := p.locks.Exec("DELETE FROM locks WHERE token='go-successor'")
		return map[string]any{"released": err == nil}, err
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
}

func run() (err error) {
	root := os.Getenv("HERMES_PROBE_ROOT")
	if root == "" {
		return errors.New("explicit disposable HERMES_PROBE_ROOT required")
	}
	db, err := open(filepath.Join(root, "memory", "sessions.db"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	locks, err := open(filepath.Join(root, ".pi-hermes-locks.sqlite"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, locks.Close()) }()
	p := peer{root: root, db: db, locks: locks}
	if _, err := p.rows(); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(map[string]any{"event": "ready"}); err != nil {
		return err
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		result, err := p.action(strings.TrimSpace(scanner.Text()))
		if err != nil {
			return err
		}
		if err := enc.Encode(result); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
