// Package recovery_test qualifies recovery through a built disposable public-API caller.
package recovery_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type recoveryRequest struct {
	Action   string
	Root     string
	SQL      string
	Content  string
	Cancel   bool
	Limit    *int
	WindowMS *int64
	Backups  *int
}

type recoverySnapshot struct {
	Rows      map[string][]string
	Schema    []string
	Contents  []string
	Search    []string
	Integrity string
}

type recoveryReply struct {
	Calls             int
	IntegrityComplete bool
	Drained           bool
	PostCloseCalls    int
	PostCloseRejected bool
	Error             string
	Failed            bool
	Snapshot          recoverySnapshot
	PageSize          int64
	RootPage          int64
}

type recoveryFixture struct {
	root, knowledge, path string
	geometry              recoveryReply
	before                recoverySnapshot
}

type recoveryProcess struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	encode  *json.Encoder
	decode  *json.Decoder
	pidfile string
	once    sync.Once
}

var recoveryBinary string
var recoveryScratch string

func TestMain(m *testing.M) {
	root := os.Getenv("HERMES_RECOVERY_SCRATCH")
	scratch, err := os.MkdirTemp(root, "recovery-acceptance-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	recoveryScratch = scratch
	recoveryBinary = filepath.Join(scratch, "recoveryprocess")
	command := exec.Command("go", "build", "-modcacherw", "-o", recoveryBinary, "./testdata/recoveryprocess")
	var buildOutput bytes.Buffer
	command.Stdout = &buildOutput
	command.Stderr = &buildOutput
	err = command.Start()
	if err == nil {
		directory := filepath.Join(scratch, "pids")
		err = os.MkdirAll(directory, 0o700)
		if err == nil {
			err = os.WriteFile(filepath.Join(directory, fmt.Sprint(command.Process.Pid)), []byte("running-adapter-build"), 0o600)
		}
		waited := command.Wait()
		if err == nil {
			err = waited
		}
		if writeErr := os.WriteFile(filepath.Join(directory, fmt.Sprint(command.Process.Pid)), []byte("joined-adapter-build"), 0o600); err == nil {
			err = writeErr
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "STRUCTURAL_ABSENT_SEAM: disposable public API adapter did not compile; executed test leaves=0\n%s", buildOutput.Bytes())
		os.Exit(2)
	}
	code := m.Run()
	os.Exit(code)
}

func recoveryMust(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func recoveryEqual(t testing.TB, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v; want %#v", got, want)
	}
}

func recoveryRange(t testing.TB, got, min, max int) {
	t.Helper()
	if got < min || got > max {
		t.Fatalf("got %d; require [%d,%d]", got, min, max)
	}
}

func recoveryStart(t testing.TB) *recoveryProcess {
	t.Helper()
	return recoveryLaunch(t, func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, recoveryBinary) })
}

func recoveryLaunch(t testing.TB, build func(context.Context) *exec.Cmd) *recoveryProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	command := build(ctx)
	input, err := command.StdinPipe()
	recoveryMust(t, err)
	output, err := command.StdoutPipe()
	recoveryMust(t, err)
	stderr, err := os.CreateTemp(recoveryScratch, "child-stderr-")
	recoveryMust(t, err)
	command.Stderr = stderr
	recoveryMust(t, command.Start())
	pids := filepath.Join(recoveryScratch, "pids")
	recoveryMust(t, os.MkdirAll(pids, 0o700))
	p := &recoveryProcess{cmd: command, input: input, encode: json.NewEncoder(input), decode: json.NewDecoder(output), pidfile: filepath.Join(pids, fmt.Sprint(command.Process.Pid))}
	recoveryMust(t, os.WriteFile(p.pidfile, []byte("running"), 0o600))
	t.Cleanup(func() { p.stop(t); cancel(); recoveryMust(t, stderr.Close()) })
	return p
}

func (p *recoveryProcess) call(t testing.TB, request recoveryRequest) recoveryReply {
	t.Helper()
	recoveryMust(t, p.encode.Encode(request))
	var reply recoveryReply
	recoveryMust(t, p.decode.Decode(&reply))
	return reply
}

func (p *recoveryProcess) success(t testing.TB, request recoveryRequest) recoveryReply {
	t.Helper()
	result := p.call(t, request)
	recoveryEqual(t, result.Failed, false)
	return result
}

func (p *recoveryProcess) stop(t testing.TB) {
	t.Helper()
	p.once.Do(func() {
		recoveryMust(t, p.input.Close())
		err := p.cmd.Wait()
		recoveryMust(t, err)
		recoveryMust(t, os.WriteFile(p.pidfile, []byte("joined"), 0o600))
	})
}

func (p *recoveryProcess) kill(t testing.TB) {
	t.Helper()
	p.once.Do(func() {
		recoveryMust(t, p.cmd.Process.Kill())
		err := p.cmd.Wait()
		var exit *exec.ExitError
		if !errorAsExit(err, &exit) {
			t.Fatalf("expected killed process, got %v", err)
		}
		err = p.input.Close()
		if !errors.Is(err, os.ErrClosed) {
			recoveryMust(t, err)
		}
		recoveryMust(t, os.WriteFile(p.pidfile, []byte("killed-and-joined"), 0o600))
	})
}

func errorAsExit(err error, exit **exec.ExitError) bool {
	value, ok := err.(*exec.ExitError)
	*exit = value
	return ok
}

func recoveryNew(t testing.TB) recoveryFixture {
	t.Helper()
	root, err := os.MkdirTemp(recoveryScratch, "store-")
	recoveryMust(t, err)
	f := recoveryFixture{root: root, knowledge: filepath.Join(root, "store"), path: filepath.Join(root, "store", "sessions.db")}
	p := recoveryStart(t)
	p.success(t, recoveryRequest{Action: "seed", Root: root})
	p.success(t, recoveryRequest{Action: "exec", SQL: `PRAGMA wal_checkpoint(TRUNCATE)`})
	f.before = p.success(t, recoveryRequest{Action: "inspect"}).Snapshot
	f.geometry = p.success(t, recoveryRequest{Action: "geometry"})
	p.stop(t)
	return f
}

func recoveryBytes(t testing.TB, path string) []byte {
	t.Helper()
	bytes, err := os.ReadFile(path)
	recoveryMust(t, err)
	return bytes
}

func recoveryHeader(t testing.TB, path string) string {
	t.Helper()
	bytes := recoveryBytes(t, path)
	recoveryRange(t, len(bytes), 16, 1<<30)
	return string(bytes[:16])
}

func recoveryDamageIndex(t testing.TB, f recoveryFixture) {
	t.Helper()
	recoveryRange(t, int(f.geometry.RootPage), 2, 1<<20)
	file, err := os.OpenFile(f.path, os.O_WRONLY, 0)
	recoveryMust(t, err)
	_, err = file.WriteAt(make([]byte, f.geometry.PageSize), (f.geometry.RootPage-1)*f.geometry.PageSize)
	recoveryMust(t, err)
	recoveryMust(t, file.Sync())
	recoveryMust(t, file.Close())
}

func recoveryGarbage(t testing.TB, f recoveryFixture) {
	t.Helper()
	recoveryMust(t, os.WriteFile(f.path, []byte("synthetic-corruption-not-a-database"), 0o600))
}

func recoveryRecover(t testing.TB, f recoveryFixture) recoveryReply {
	t.Helper()
	p := recoveryStart(t)
	p.success(t, recoveryRequest{Action: "manager", Root: f.root})
	result := p.call(t, recoveryRequest{Action: "recover"})
	p.stop(t)
	return result
}

func recoveryInspect(t testing.TB, f recoveryFixture) recoverySnapshot {
	t.Helper()
	p := recoveryStart(t)
	p.success(t, recoveryRequest{Action: "raw", Root: f.root})
	result := p.success(t, recoveryRequest{Action: "inspect"}).Snapshot
	p.stop(t)
	return result
}

func recoveryDocument(t testing.TB, f recoveryFixture) string {
	t.Helper()
	return string(recoveryBytes(t, filepath.Join(f.knowledge, "MEMORY.md")))
}

func recoveryArtifacts(t testing.TB, root string) []string {
	t.Helper()
	result := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(entry.Name(), ".recovery-") || strings.Contains(entry.Name(), ".retired-") {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			result = append(result, relative)
		}
		return nil
	})
	recoveryMust(t, err)
	sort.Strings(result)
	return result
}

func recoveryBackups(t testing.TB, f recoveryFixture) []string {
	t.Helper()
	names, err := filepath.Glob(f.path + ".corrupt-*")
	recoveryMust(t, err)
	result := []string{}
	for _, name := range names {
		if !strings.HasSuffix(name, "-wal") && !strings.HasSuffix(name, "-shm") {
			result = append(result, name)
		}
	}
	return result
}

func recoveryRepeatedSuccesses(t testing.TB, f recoveryFixture) int {
	t.Helper()
	for range 4 {
		recoveryGarbage(t, f)
		recoveryEqual(t, recoveryRecover(t, f).Failed, false)
	}
	return 4
}

func recoveryWrite(t testing.TB, path, content string) {
	t.Helper()
	recoveryMust(t, os.MkdirAll(filepath.Dir(path), 0o700))
	recoveryMust(t, os.WriteFile(path, []byte(content), 0o600))
}

func recoverySeedSnapshots(t testing.TB, f recoveryFixture) []string {
	t.Helper()
	expected := []string{}
	for _, dir := range []string{"store", "projects/alpha", "projects/beta"} {
		recoveryWrite(t, filepath.Join(f.root, dir, "MEMORY.md"), "durable dormant document")
		for index := range 40 {
			name := fmt.Sprintf(".MEMORY.md.recovery-%013d-00000000-0000-4000-8000-%012d", time.Now().UnixMilli(), index)
			path := filepath.Join(f.root, dir, name)
			recoveryWrite(t, path, fmt.Sprint(index))
			stamp := time.Now().Add(time.Duration(index-40) * time.Minute)
			recoveryMust(t, os.Chtimes(path, stamp, stamp))
			if index >= 8 {
				expected = append(expected, filepath.Join(dir, name))
			}
		}
		fresh := filepath.Join(dir, ".MEMORY.md.retired-1790000000000-00000000-0000-4000-8000-000000000001")
		recoveryWrite(t, filepath.Join(f.root, fresh), "fresh retired snapshot")
		expected = append(expected, fresh)
		stale := filepath.Join(f.root, dir, ".MEMORY.md.retired-1790000000000-00000000-0000-4000-8000-000000000002")
		recoveryWrite(t, stale, "old retired snapshot")
		old := time.Now().Add(-31 * 24 * time.Hour)
		recoveryMust(t, os.Chtimes(stale, old, old))
	}
	sort.Strings(expected)
	return expected
}

func recoveryWaitDocument(t testing.TB, f recoveryFixture) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(f.knowledge, "MEMORY.md"))
		if err == nil && strings.Contains(string(data), "crash durable marker") {
			return string(data)
		}
		runtime.Gosched()
	}
	t.Fatal("document publication barrier not reached while peer holds BEGIN IMMEDIATE")
	return ""
}

func recoveryFrozenRead(t testing.TB, f recoveryFixture) bool {
	t.Helper()
	repo, err := filepath.Abs("../../../..")
	recoveryMust(t, err)
	source := os.Getenv("HERMES_PACKAGE")
	if source == "" {
		t.Skip("set HERMES_PACKAGE to the pinned source for frozen-reader rollback qualification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "--import", "tsx", filepath.Join(repo, "probes/hermes-sqlite/configuration-fixture.mjs"), "checkpoint-read")
	command.Dir = repo
	command.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+f.root, "HERMES_PACKAGE="+source)
	output := recoveryCommandOutput(t, command)
	return strings.Contains(string(output), `"readable":2`)
}

func recoveryCommandOutput(t testing.TB, command *exec.Cmd) []byte {
	t.Helper()
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	recoveryMust(t, command.Start())
	directory := filepath.Join(recoveryScratch, "pids")
	recoveryMust(t, os.MkdirAll(directory, 0o700))
	receipt := filepath.Join(directory, fmt.Sprint(command.Process.Pid))
	recoveryMust(t, os.WriteFile(receipt, []byte("running-frozen-reader"), 0o600))
	err := command.Wait()
	recoveryMust(t, os.WriteFile(receipt, []byte("joined-frozen-reader"), 0o600))
	if err != nil {
		t.Fatalf("frozen public reader: %v\n%s", err, output.Bytes())
	}
	return output.Bytes()
}

func recoveryAcceptedCommitVisible(t testing.TB, peer recoveryReply, rows []string, marker string) {
	t.Helper()
	if peer.Failed {
		return
	}
	for _, row := range rows {
		if strings.Contains(row, marker) {
			return
		}
	}
	t.Fatalf("accepted peer commit %q detached from current database path; shared-writer RELEASE BLOCKER", marker)
}

func recoveryMaintenanceFiles(t testing.TB, root string) map[string]string {
	t.Helper()
	return recoveryMaintenanceInventory(t, root, func(name string) bool {
		return strings.Contains(name, ".recovery-") || strings.Contains(name, ".retired-") || strings.Contains(name, ".conflict-")
	})
}

func recoveryMaintenanceDocuments(t testing.TB, root string) map[string]string {
	t.Helper()
	return recoveryMaintenanceInventory(t, root, func(name string) bool {
		return name == "MEMORY.md" || name == "USER.md" || name == "failures.md"
	})
}

func recoveryMaintenanceInventory(t testing.TB, root string, include func(string) bool) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !include(entry.Name()) {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular maintenance artifact: %s", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = fmt.Sprintf("%x:%d", hash.Sum(nil), size)
		return nil
	})
	recoveryMust(t, err)
	return result
}

func recoveryAssertMaintenance(t testing.TB, root string, before map[string]string, expected []string) map[string]string {
	t.Helper()
	after := recoveryMaintenanceFiles(t, root)
	kept := map[string]bool{}
	wantActive, active, conflicts := []string{}, []string{}, []string{}
	wantRetired, retired := map[string][]string{}, map[string][]string{}
	for _, path := range expected {
		kept[path] = true
		recoveryEqual(t, after[path], before[path])
		if strings.Contains(filepath.Base(path), ".recovery-") {
			wantActive = append(wantActive, path)
		} else {
			dir := filepath.Dir(path)
			wantRetired[dir] = append(wantRetired[dir], before[path])
		}
	}
	for path, digest := range before {
		if strings.Contains(filepath.Base(path), ".recovery-") && !kept[path] {
			dir := filepath.Dir(path)
			wantRetired[dir] = append(wantRetired[dir], digest)
		}
	}
	counts, sizes := map[string]int{}, map[string]int{}
	for path, digest := range after {
		dir := filepath.Dir(path)
		name := filepath.Base(path)
		kind := ".retired-"
		maxAge := 30 * 24 * time.Hour
		switch {
		case strings.Contains(name, ".recovery-"):
			kind = ".recovery-"
			maxAge = 7 * 24 * time.Hour
			active = append(active, path)
		case strings.Contains(name, ".retired-"):
			retired[dir] = append(retired[dir], digest)
		default:
			kind = ".conflict-"
			conflicts = append(conflicts, path)
		}
		info, err := os.Lstat(filepath.Join(root, path))
		recoveryMust(t, err)
		document, _, _ := strings.Cut(name, kind)
		group := filepath.Join(dir, document+kind)
		counts[group]++
		sizes[group] += int(info.Size())
		if time.Since(info.ModTime()) > maxAge {
			t.Fatalf("maintenance retained expired %s: %s", kind, path)
		}
	}
	for group, count := range counts {
		recoveryRange(t, count, 1, 32)
		recoveryRange(t, sizes[group], 0, 64*1024*1024)
	}
	for dir := range wantRetired {
		sort.Strings(wantRetired[dir])
	}
	for dir := range retired {
		sort.Strings(retired[dir])
	}
	sort.Strings(wantActive)
	sort.Strings(active)
	recoveryEqual(t, active, wantActive)
	recoveryEqual(t, retired, wantRetired)
	recoveryEqual(t, conflicts, []string{})
	return after
}
