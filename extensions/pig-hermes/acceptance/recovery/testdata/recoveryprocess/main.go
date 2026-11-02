// Command recoveryprocess calls staged public storage APIs on synthetic roots.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/paths"
	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"
)

type request struct {
	Action   string
	Root     string
	SQL      string
	Content  string
	Cancel   bool
	Limit    *int
	WindowMS *int64
	Backups  *int
}

type snapshot struct {
	Rows      map[string][]string
	Schema    []string
	Contents  []string
	Search    []string
	Integrity string
}

type reply struct {
	Calls             int
	IntegrityComplete bool
	Drained           bool
	PostCloseCalls    int
	PostCloseRejected bool
	Error             string
	Failed            bool
	Snapshot          snapshot
	PageSize          int64
	RootPage          int64
}

type driver struct {
	db       *sql.DB
	memory   *store.Memory
	recovery *nativeRecovery
	root     string
}

func main() {
	d := &driver{}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	lastFailed := false
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			os.Exit(2)
		}
		result, err := d.act(req)
		if err != nil {
			result.Error = err.Error()
		}
		result.Failed = err != nil
		lastFailed = result.Failed
		if err := encoder.Encode(result); err != nil {
			os.Exit(3)
		}
	}
	if err := scanner.Err(); err != nil {
		os.Exit(4)
	}
	if err := d.close(); err != nil {
		fmt.Fprintln(os.Stderr, "cleanup:", err)
		if !lastFailed {
			os.Exit(5)
		}
	}
}

func (d *driver) close() error {
	if d.recovery != nil {
		return d.recovery.close()
	}
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

func (d *driver) resolved() paths.Resolved {
	return paths.Resolved{AgentRoot: d.root, Knowledge: filepath.Join(d.root, "store"), Projects: filepath.Join(d.root, "projects"), Database: filepath.Join(d.root, "store", "sessions.db")}
}

func (d *driver) act(req request) (reply, error) {
	ctx := context.Background()
	if req.Cancel {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		ctx = canceled
	}
	if req.Root != "" {
		d.root = req.Root
	}
	switch req.Action {
	case "seed":
		db, err := store.Open(ctx, d.resolved().Database)
		if err != nil {
			return reply{}, err
		}
		d.db = db
		_, err = db.ExecContext(ctx, seedSQL)
		return reply{}, err
	case "raw":
		db, err := sql.Open("sqlite", d.resolved().Database)
		if err != nil {
			return reply{}, err
		}
		db.SetMaxOpenConns(1)
		d.db = db
		return reply{}, db.PingContext(ctx)
	case "exec":
		err := d.withDB(ctx, func(db *sql.DB) error { _, err := db.ExecContext(ctx, req.SQL); return err })
		return reply{}, err
	case "geometry":
		var result reply
		err := d.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&result.PageSize)
		if err != nil {
			return result, err
		}
		err = d.db.QueryRowContext(ctx, `SELECT rootpage FROM sqlite_schema WHERE name='memory_fts_data'`).Scan(&result.RootPage)
		return result, err
	case "inspect":
		s, err := readSnapshot(ctx, d.db)
		return reply{Snapshot: s}, err
	case "close":
		err := d.close()
		d.db = nil
		d.recovery = nil
		d.memory = nil
		return reply{}, err
	case "manager":
		manager, err := newNativeRecovery(d.resolved().Database, req)
		d.recovery = manager
		return reply{}, err
	case "open":
		return reply{}, d.recovery.open(ctx)
	case "recover":
		return reply{}, d.recovery.recover(ctx)
	case "memory":
		return reply{}, d.managedMemory(ctx)
	case "add":
		result, err := d.memory.Add(ctx, store.Scope{Target: "memory"}, store.Mutation{Content: req.Content})
		if err == nil && (!result.DocumentSaved || !result.SearchReconciled) {
			err = errors.New("incomplete mutation")
		}
		return reply{}, err
	case "sync":
		return reply{}, syncNative(ctx, d.memory)
	case "corruption-loop":
		return d.corruptionLoop(ctx)
	case "drain":
		return d.drain(ctx)
	case "maintain":
		return reply{}, d.maintainNative(ctx, d.resolved())
	default:
		return reply{}, errors.New("unknown driver request")
	}
}

const seedSQL = `
INSERT INTO memories(target,content,created,last_referenced,category,hit_count,last_hit_at) VALUES ('memory','東京日本語 checkpoint','2026-01-01','2026-01-02','insight',7,'2026-01-03');
INSERT INTO memories(target,content,created,last_referenced,category,hit_count) VALUES ('memory','native checkpoint','2026-01-04','2026-01-04','preference',2);
INSERT INTO sessions(id,project,cwd,started_at,message_count) VALUES ('synthetic-session','project-東京','/synthetic/project','2026-01-01',1);
INSERT INTO messages(id,session_id,role,content,timestamp) VALUES ('synthetic-message','synthetic-session','user','synthetic provenance 日本語','2026-01-01');
INSERT INTO session_files(path,session_id,size,mtime_ms,indexed_at) VALUES ('/synthetic/pig/session.jsonl','synthetic-session',123,456,'2026-01-02');
INSERT INTO extension_metadata(key,value) VALUES ('checkpoint','{"synthetic":true}');
`

func stringsQuery(ctx context.Context, db *sql.DB, query string) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func readSnapshot(ctx context.Context, db *sql.DB) (snapshot, error) {
	result := snapshot{Rows: map[string][]string{}}
	tables := map[string]string{
		"memories":      `SELECT json_array(id,project,target,category,content,created,last_referenced,hit_count,last_hit_at,failure_reason,tool_state,corrected_to) FROM memories ORDER BY id`,
		"sessions":      `SELECT json_array(id,project,cwd,started_at,ended_at,message_count) FROM sessions ORDER BY id`,
		"messages":      `SELECT json_array(id,session_id,role,content,timestamp) FROM messages ORDER BY id`,
		"session_files": `SELECT json_array(path,session_id,size,mtime_ms,indexed_at) FROM session_files ORDER BY path`,
		"metadata":      `SELECT json_array(key,value) FROM extension_metadata WHERE key NOT LIKE 'mdsync:%' ORDER BY key`,
	}
	for name, query := range tables {
		values, err := stringsQuery(ctx, db, query)
		if err != nil {
			return result, fmt.Errorf("read %s: %w", name, err)
		}
		result.Rows[name] = values
	}
	var err error
	result.Schema, err = stringsQuery(ctx, db, `SELECT type||'|'||name||'|'||coalesce(sql,'') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		return result, err
	}
	result.Contents, err = stringsQuery(ctx, db, `SELECT content FROM memories ORDER BY content`)
	if err != nil {
		return result, err
	}
	result.Search, err = stringsQuery(ctx, db, `SELECT content FROM memory_fts WHERE memory_fts MATCH '"checkpoint" OR "durable"' ORDER BY content`)
	if err != nil {
		return result, err
	}
	err = db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result.Integrity)
	return result, err
}
