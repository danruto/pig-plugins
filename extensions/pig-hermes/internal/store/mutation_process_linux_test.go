package store_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/paths"
	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"
)

type mutationPeerResult struct {
	Result store.MutationResult
	Error  string
}

func TestMutationProcessPeer(t *testing.T) {
	runMutationProcessPeer(t, os.Getenv("HERMES_MUTATION_PROCESS_ROOT"))
}

func runMutationProcessPeer(t testing.TB, root string) {
	t.Helper()
	if root == "" {
		t.Skip("subprocess helper")
	}
	resolved := paths.Resolved{AgentRoot: root, Knowledge: filepath.Join(root, "knowledge"), Database: filepath.Join(root, "knowledge", "sessions.db"), Projects: filepath.Join(root, "projects")}
	must(t, os.MkdirAll(resolved.Knowledge, 0o700))
	db := native(t, resolved.Database)
	memory, err := store.NewMemory(db, parsedSettings(t, `{}`), resolved)
	must(t, err)
	f := mutationFixture{db: db, memory: memory, resolved: resolved}
	addMemory(t, f, store.Scope{Target: "memory"}, "accepted local")
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_, err = fmt.Fprintf(os.Stdout, "READY %d\n", unix.Gettid())
	must(t, err)
	_, err = bufio.NewReader(os.Stdin).ReadString('\n')
	must(t, err)
	result, mutationErr := memory.Add(t.Context(), store.Scope{Target: "memory"}, store.Mutation{Content: "pending local"})
	response := mutationPeerResult{Result: result}
	if mutationErr != nil {
		response.Error = mutationErr.Error()
	}
	must(t, json.NewEncoder(os.Stdout).Encode(response))
}

type mutationPeer struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  *bufio.Reader
	stderr  bytes.Buffer
	tid     int
}

func startMutationPeer(t testing.TB, root string) *mutationPeer {
	t.Helper()
	executable, err := os.Executable()
	must(t, err)
	peer := &mutationPeer{}
	peer.command = exec.CommandContext(t.Context(), executable, "-test.run=^TestMutationProcessPeer$")
	peer.command.Env = append(os.Environ(), "HERMES_MUTATION_PROCESS_ROOT="+root, "GOMAXPROCS=1")
	peer.command.Stderr = &peer.stderr
	peer.input, err = peer.command.StdinPipe()
	must(t, err)
	output, err := peer.command.StdoutPipe()
	must(t, err)
	peer.output = bufio.NewReader(output)
	must(t, peer.command.Start())
	registerMutationPID(t, peer.command.Process.Pid)
	t.Cleanup(func() {
		if peer.command.ProcessState == nil {
			killErr := peer.command.Process.Kill()
			if killErr != nil && killErr != os.ErrProcessDone {
				t.Log(killErr)
			}
			waitErr := peer.command.Wait()
			if waitErr != nil {
				t.Logf("peer cleanup: %v %s", waitErr, &peer.stderr)
			}
		}
	})
	line, err := peer.output.ReadString('\n')
	must(t, err)
	fields := strings.Fields(line)
	same(t, len(fields), 2)
	same(t, fields[0], "READY")
	peer.tid, err = strconv.Atoi(fields[1])
	must(t, err)
	return peer
}

func registerMutationPID(t testing.TB, pid int) {
	t.Helper()
	path := os.Getenv("HERMES_ACCEPTANCE_PID_FILE")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	must(t, err)
	_, err = fmt.Fprintf(file, "%d mutation acceptance peer (joined)\n", pid)
	must(t, err)
	must(t, file.Close())
}

func peekMutationPath(pid int, address uint64) (string, error) {
	buffer := make([]byte, 0, 256)
	for len(buffer) < 4096 {
		word := make([]byte, 8)
		_, err := unix.PtracePeekData(pid, uintptr(address)+uintptr(len(buffer)), word)
		if err != nil {
			return "", err
		}
		for _, value := range word {
			if value == 0 {
				return string(buffer), nil
			}
			buffer = append(buffer, value)
		}
	}
	return "", fmt.Errorf("unterminated syscall path")
}

func mutationRenameSource(pid int) (string, error) {
	var information [88]byte
	_, _, errno := unix.Syscall6(unix.SYS_PTRACE, 0x420e, uintptr(pid), uintptr(len(information)), uintptr(unsafe.Pointer(&information[0])), 0, 0)
	if errno != 0 {
		return "", errno
	}
	if information[0] != 1 {
		return "", nil
	}
	number := binary.NativeEndian.Uint64(information[24:32])
	var address uint64
	switch {
	case number == uint64(unix.SYS_RENAMEAT), number == uint64(unix.SYS_RENAMEAT2):
		address = binary.NativeEndian.Uint64(information[40:48])
	case runtime.GOARCH == "amd64" && number == 82:
		address = binary.NativeEndian.Uint64(information[32:40])
	default:
		return "", nil
	}
	return peekMutationPath(pid, address)
}

func injectExternalEdits(t testing.TB, peer *mutationPeer, path string, conflicts int) string {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	must(t, unix.PtraceAttach(peer.tid))
	attached := true
	defer func() {
		if attached {
			must(t, unix.PtraceDetach(peer.tid))
		}
	}()
	var status unix.WaitStatus
	_, err := unix.Wait4(peer.tid, &status, unix.WALL, nil)
	must(t, err)
	must(t, unix.PtraceSetOptions(peer.tid, unix.PTRACE_O_TRACESYSGOOD))
	_, err = io.WriteString(peer.input, "start\n")
	must(t, err)
	must(t, peer.input.Close())
	external := readDocument(t, path)
	injected := 0
	signal := 0
	for injected < conflicts {
		must(t, unix.PtraceSyscall(peer.tid, signal))
		_, err = unix.Wait4(peer.tid, &status, unix.WALL, nil)
		must(t, err)
		if !status.Stopped() {
			t.Fatalf("peer exited before syscall barrier: %v stderr=%s", status, &peer.stderr)
		}
		signal = int(status.StopSignal())
		if signal != int(syscall.SIGTRAP)|0x80 {
			continue
		}
		signal = 0
		source, err := mutationRenameSource(peer.tid)
		must(t, err)
		if source != path {
			continue
		}
		injected++
		external += fmt.Sprintf("\n§\naccepted external %d <!-- created=2026-01-01, last=2026-01-01 -->", injected)
		must(t, os.WriteFile(path, []byte(external), 0o600))
	}
	must(t, unix.PtraceDetach(peer.tid))
	attached = false
	return external
}

func finishMutationPeer(t testing.TB, peer *mutationPeer) mutationPeerResult {
	t.Helper()
	var result mutationPeerResult
	err := json.NewDecoder(peer.output).Decode(&result)
	if err != nil {
		t.Fatalf("decode peer result: %v stderr=%s", err, &peer.stderr)
	}
	_, err = io.Copy(io.Discard, peer.output)
	must(t, err)
	must(t, peer.command.Wait())
	return result
}

func TestMemoryExternalEditAfterFingerprintRetriesWithoutLostUpdates(t *testing.T) {
	root := t.TempDir()
	peer := startMutationPeer(t, root)
	path := filepath.Join(root, "knowledge", "MEMORY.md")
	external := injectExternalEdits(t, peer, path, 1)

	response := finishMutationPeer(t, peer)

	same(t, response.Error, "")
	same(t, response.Result.Success, true)
	same(t, response.Result.DocumentSaved, true)
	same(t, response.Result.SearchReconciled, true)
	same(t, response.Result.Attempts, 2)
	same(t, strings.HasPrefix(readDocument(t, path), external+"\n§\npending local <!-- created="), true)
	db := native(t, filepath.Join(root, "knowledge", "sessions.db"))
	same(t, inventory(t, db), []string{"memory|<global>|<none>|accepted external 1", "memory|<global>|<none>|accepted local", "memory|<global>|<none>|pending local"})
}

func TestMemoryExternalEditRetryExhaustionReloadsDiskTruth(t *testing.T) {
	root := t.TempDir()
	peer := startMutationPeer(t, root)
	path := filepath.Join(root, "knowledge", "MEMORY.md")
	external := injectExternalEdits(t, peer, path, 3)

	response := finishMutationPeer(t, peer)

	same(t, response.Result.Success, false)
	same(t, response.Result.DocumentSaved, false)
	same(t, response.Result.Attempts, 3)
	same(t, readDocument(t, path), external)
	same(t, len(response.Result.Entries), 4)
	same(t, strings.Contains(strings.Join(response.Result.Entries, "\n"), "pending local"), false)
	same(t, strings.Contains(strings.Join(response.Result.Entries, "\n"), "accepted external 3"), true)
}
