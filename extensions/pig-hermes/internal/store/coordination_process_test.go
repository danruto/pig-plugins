package store_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type coordinationRequest struct {
	Action     string `json:"action"`
	Operation  string `json:"operation"`
	AgentRoot  string `json:"agentRoot"`
	Identity   string `json:"identity"`
	ToolTarget string `json:"toolTarget"`
	Target     string `json:"target"`
	Database   string `json:"database"`
	Key        string `json:"key"`
	StaleMS    int64  `json:"staleMS"`
	TimeoutMS  int64  `json:"timeoutMS"`
}

type coordinationReply struct {
	Ready     bool   `json:"ready"`
	PID       int    `json:"pid"`
	Acquired  bool   `json:"acquired"`
	Renewed   bool   `json:"renewed"`
	ElapsedMS int64  `json:"elapsedMS"`
	Error     string `json:"error"`
}

type coordinationRow struct {
	Key         string
	Token       string
	PID         int
	Incarnation sql.NullString
	AcquiredAt  int64
}

type coordinationPeer struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  *json.Decoder
	stderr  bytes.Buffer
	pid     int
}

func TestCoordinationProcessPeer(t *testing.T) {
	serveCoordinationPeer(t)
}

func serveCoordinationPeer(t testing.TB) {
	t.Helper()
	if os.Getenv("HERMES_COORDINATION_PEER") != "native" {
		t.Skip("subprocess helper")
	}
	must(t, json.NewEncoder(os.Stdout).Encode(coordinationReply{Ready: true, PID: os.Getpid()}))
	scanner := bufio.NewScanner(os.Stdin)
	var lease coordinationLease
	for scanner.Scan() {
		var request coordinationRequest
		must(t, json.Unmarshal(scanner.Bytes(), &request))
		if request.Action == "exit" {
			return
		}
		reply := nativeCoordinationReply(t.Context(), request, &lease)
		must(t, json.NewEncoder(os.Stdout).Encode(reply))
	}
	must(t, scanner.Err())
}

func nativeCoordinationReply(ctx context.Context, request coordinationRequest, lease *coordinationLease) coordinationReply {
	start := time.Now()
	reply := coordinationReply{}
	var err error
	switch request.Action {
	case "acquire":
		acquired, acquireErr := acquireWithDeadline(ctx, request)
		err = acquireErr
		if acquired != nil {
			*lease = acquired
			reply.Acquired = true
		}
	case "renew":
		if *lease != nil {
			reply.Renewed, err = (*lease).Renew(ctx)
		}
	case "release":
		if *lease != nil {
			err = (*lease).Release(ctx)
		}
	default:
		panic("unrecognized peer action: " + request.Action)
	}
	if err != nil {
		reply.Error = err.Error()
	}
	reply.ElapsedMS = time.Since(start).Milliseconds()
	return reply
}

func acquireWithDeadline(ctx context.Context, request coordinationRequest) (coordinationLease, error) {
	if request.TimeoutMS <= 0 {
		return acquireNativeCoordination(ctx, request)
	}
	limited, cancel := context.WithTimeout(ctx, time.Duration(request.TimeoutMS)*time.Millisecond)
	defer cancel()
	return acquireNativeCoordination(limited, request)
}

func startCoordinationPeer(t testing.TB, language string, env ...string) *coordinationPeer {
	t.Helper()
	peer := &coordinationPeer{}
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	t.Cleanup(cancel)
	executable, err := os.Executable()
	must(t, err)
	peer.command = exec.CommandContext(ctx, executable, "-test.run=^TestCoordinationProcessPeer$")
	if language == "typescript" {
		cwd, cwdErr := os.Getwd()
		must(t, cwdErr)
		peer.command = exec.CommandContext(ctx, "node", "--import", "tsx", filepath.Join(cwd, "testdata", "coordination-peer.mjs"))
		peer.command.Dir = filepath.Clean(filepath.Join(cwd, "../../../.."))
	}
	peer.command.Env = coordinationPeerEnvironment(t, env)
	peer.command.Stderr = &peer.stderr
	peer.input, err = peer.command.StdinPipe()
	must(t, err)
	output, err := peer.command.StdoutPipe()
	must(t, err)
	peer.output = json.NewDecoder(output)
	must(t, peer.command.Start())
	peer.pid = peer.command.Process.Pid
	registerCoordinationPID(t, peer.pid)
	t.Cleanup(func() { stopCoordinationPeer(t, peer) })
	var ready coordinationReply
	decodeCoordinationReply(t, peer, &ready)
	same(t, ready.Ready, true)
	same(t, ready.PID, peer.pid)
	return peer
}

func stopCoordinationPeer(t testing.TB, peer *coordinationPeer) {
	t.Helper()
	if peer.command.ProcessState != nil {
		return
	}
	err := json.NewEncoder(peer.input).Encode(coordinationRequest{Action: "exit"})
	if err != nil {
		t.Logf("peer exit request: %v", err)
	}
	err = peer.input.Close()
	if err != nil {
		t.Logf("peer input close: %v", err)
	}
	joined := make(chan error, 1)
	go func() { joined <- peer.command.Wait() }()
	select {
	case err = <-joined:
		if err != nil {
			t.Errorf("peer exit: %v stderr=%s", err, &peer.stderr)
		}
	case <-time.After(8 * time.Second):
		t.Error("peer did not stop after exit barrier")
		if err = peer.command.Process.Kill(); err != nil {
			t.Log(err)
		}
		if err = <-joined; err != nil {
			t.Log(err)
		}
	}
}

func decodeCoordinationReply(t testing.TB, peer *coordinationPeer, reply *coordinationReply) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- peer.output.Decode(reply) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("peer protocol: %v stderr=%s", err, &peer.stderr)
		}
	case <-time.After(15 * time.Second):
		killErr := peer.command.Process.Kill()
		if killErr != nil {
			t.Log(killErr)
		}
		t.Fatalf("peer response deadline: %v stderr=%s", <-done, &peer.stderr)
	}
}

func askCoordination(t testing.TB, peer *coordinationPeer, request coordinationRequest) coordinationReply {
	t.Helper()
	must(t, json.NewEncoder(peer.input).Encode(request))
	var reply coordinationReply
	decodeCoordinationReply(t, peer, &reply)
	return reply
}

func acquireCoordination(t testing.TB, peer *coordinationPeer, request coordinationRequest) coordinationReply {
	t.Helper()
	request.Action = "acquire"
	return askCoordination(t, peer, request)
}

func ownedCoordination(t testing.TB, peer *coordinationPeer, request coordinationRequest) coordinationReply {
	t.Helper()
	reply := acquireCoordination(t, peer, request)
	same(t, reply.Error, "")
	same(t, reply.Acquired, true)
	return reply
}

func releaseCoordination(t testing.TB, peer *coordinationPeer) {
	t.Helper()
	reply := askCoordination(t, peer, coordinationRequest{Action: "release"})
	same(t, reply.Error, "")
}

func coordinationDB(t testing.TB, path string) *sql.DB {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o700))
	db := raw(t, path)
	execSQL(t, db, `CREATE TABLE IF NOT EXISTS locks(lock_key TEXT PRIMARY KEY,token TEXT NOT NULL,pid INTEGER NOT NULL,incarnation TEXT,acquired_at INTEGER NOT NULL)`)
	return db
}

func leaseRow(t testing.TB, db *sql.DB, key string) coordinationRow {
	t.Helper()
	var row coordinationRow
	must(t, db.QueryRowContext(t.Context(), `SELECT lock_key,token,pid,incarnation,acquired_at FROM locks WHERE lock_key=?`, key).Scan(&row.Key, &row.Token, &row.PID, &row.Incarnation, &row.AcquiredAt))
	return row
}

func leaseKeys(t testing.TB, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT lock_key FROM locks ORDER BY lock_key`)
	must(t, err)
	defer func() { must(t, rows.Close()) }()
	keys := []string{}
	for rows.Next() {
		var key string
		must(t, rows.Scan(&key))
		keys = append(keys, key)
	}
	must(t, rows.Err())
	return keys
}

func within(t testing.TB, value, min, max int64) {
	t.Helper()
	if value < min || value > max {
		t.Fatalf("got %d, want [%d,%d]", value, min, max)
	}
}

func seedCoordination(t testing.TB, db *sql.DB, row coordinationRow) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `INSERT OR REPLACE INTO locks(lock_key,token,pid,incarnation,acquired_at) VALUES(?,?,?,?,?)`, row.Key, row.Token, row.PID, row.Incarnation, row.AcquiredAt)
	must(t, err)
}

func waitForCoordinationRenewal(t testing.TB, db *sql.DB, initial coordinationRow) coordinationRow {
	t.Helper()
	deadline := time.Now().Add(12500 * time.Millisecond)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		row := leaseRow(t, db, initial.Key)
		if row.AcquiredAt > initial.AcquiredAt {
			return row
		}
		select {
		case <-ticker.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	t.Fatal("consolidation acquired_at was not renewed within protocol cadence")
	return coordinationRow{}
}

func consolidationRequest(root string) coordinationRequest {
	identity := filepath.Join(root, "knowledge", "MEMORY.md")
	return coordinationRequest{Operation: "consolidation", AgentRoot: root, ToolTarget: "memory", Target: "global", Identity: identity, Key: "memory:global:" + canonicalDocumentHash(identity)}
}

func defaultConsolidationDB(root string) string {
	return filepath.Join(root, "pi-hermes-memory", ".consolidation-locks", "locks.sqlite")
}

func requestEnvironment(wait string) []string {
	if wait == "unset" {
		return []string{"PI_HERMES_CONSOLIDATION_LOCK_DIR=", "HERMES_ACCEPTANCE_UNSET_WAIT=1"}
	}
	return []string{"PI_HERMES_CONSOLIDATION_LOCK_DIR=", "PI_HERMES_CONSOLIDATION_LOCK_WAIT_MS=" + wait}
}

func coordinationPeerEnvironment(t testing.TB, extra []string) []string {
	t.Helper()
	env := append(os.Environ(), "HERMES_COORDINATION_PEER=native", "PI_CODING_AGENT_DIR="+t.TempDir(), "GOMAXPROCS=2")
	env = append(env, extra...)
	for _, entry := range extra {
		if entry == "HERMES_ACCEPTANCE_UNSET_WAIT=1" {
			return removeCoordinationWait(env)
		}
	}
	return env
}

func removeCoordinationWait(env []string) []string {
	filtered := []string{}
	for _, entry := range env {
		if !strings.HasPrefix(entry, "PI_HERMES_CONSOLIDATION_LOCK_WAIT_MS=") {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func coordinationTokenCount(t testing.TB, db *sql.DB, token string) int {
	t.Helper()
	var count int
	must(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM locks WHERE token=?`, token).Scan(&count))
	return count
}

func killCoordinationPeer(t testing.TB, peer *coordinationPeer) {
	t.Helper()
	must(t, peer.command.Process.Kill())
	err := peer.command.Wait()
	if err == nil {
		t.Fatal("killed peer exited successfully")
	}
}

func registerCoordinationPID(t testing.TB, pid int) {
	t.Helper()
	path := os.Getenv("HERMES_ACCEPTANCE_PID_FILE")
	if path == "" {
		path = filepath.Join(t.TempDir(), "pids")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	must(t, err)
	_, err = fmt.Fprintf(file, "%d coordination acceptance peer (fixture joins on cleanup)\n", pid)
	must(t, err)
	must(t, file.Close())
}

func coordinationDeadPID(t testing.TB) int {
	t.Helper()
	command := exec.CommandContext(t.Context(), "true")
	must(t, command.Start())
	registerCoordinationPID(t, command.Process.Pid)
	must(t, command.Wait())
	return command.Process.Pid
}

func seedDeadCoordinationRows(t testing.TB, db *sql.DB, pid int) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	must(t, err)
	for i := range 8192 {
		_, err = tx.ExecContext(t.Context(), `INSERT INTO locks VALUES(?,?,?,NULL,?)`, fmt.Sprintf("dead:%05d", i), "dead", pid, time.Now().Add(-61*time.Second).UnixMilli())
		must(t, err)
	}
	must(t, tx.Commit())
}

func assertLeaseOwner(t testing.TB, db *sql.DB, request coordinationRequest, peer *coordinationPeer) {
	t.Helper()
	same(t, leaseKeys(t, db), []string{request.Key})
	row := leaseRow(t, db, request.Key)
	same(t, row.PID, peer.pid)
	same(t, len(row.Token) > 0, true)
	same(t, row.Incarnation.Valid && strings.TrimSpace(row.Incarnation.String) != "", true)
	within(t, row.AcquiredAt, time.Now().Add(-2*time.Second).UnixMilli(), time.Now().Add(time.Second).UnixMilli())
}
