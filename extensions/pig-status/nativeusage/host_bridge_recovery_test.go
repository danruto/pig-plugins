package nativeusage

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

const (
	bridgeProbeCommand      = "readiness-probe"
	bridgeReadinessChannel  = "oauth:credential-readiness:v1"
	bridgeReadinessProvider = "deepseek"
	bridgeSessionID         = "01a1e221-69f7-75ca-9cf0-06f11fba16ea"
	bridgeRotatedSessionID  = "01a1e221-69f7-75ca-9cf0-06f11fba16eb"
	bridgeRequestMS         = 500
	bridgeRetryInterval     = 2 * time.Millisecond
)

type bridgeCall struct {
	Method string
}

type bridgeCallFrame struct {
	Method string          `json:"method"`
	Args   json.RawMessage `json:"args"`
}

type bridgeFrame struct {
	Type string           `json:"type"`
	ID   string           `json:"id"`
	Call *bridgeCallFrame `json:"call"`
}

type bridgeHost struct {
	conn        net.Conn
	answers     map[string]any
	emitted     chan int
	ready       chan struct{}
	mu          sync.Mutex
	failNext    int
	failRereads int
	emitFailed  bool
	emits       int
	calls       []bridgeCall
	sessions    []string
}

func startBridgeHost(t *testing.T, ext *sdk.Extension, answers map[string]any) *bridgeHost {
	t.Helper()
	extensionSide, hostSide := net.Pipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = sdk.ServeFactory(extensionSide, func() *sdk.Extension { return ext })
	}()
	host := &bridgeHost{conn: hostSide, answers: answers, emitted: make(chan int, 8), ready: make(chan struct{})}
	t.Cleanup(func() {
		_ = hostSide.Close()
		select {
		case <-served:
		case <-time.After(lifecycleDeadline):
			t.Errorf("the extension did not stop within %v of the host closing the connection", lifecycleDeadline)
		}
	})
	go host.serve()
	select {
	case <-host.ready:
	case <-time.After(lifecycleDeadline):
		t.Fatal("the extension did not reach its ready state within the cap")
	}
	return host
}

func (h *bridgeHost) serve() {
	for {
		frame, err := h.readFrame()
		if err != nil {
			return
		}
		if frame.Call == nil {
			if frame.Type == "register" {
				_ = h.writeFrame(map[string]any{"type": "ready", "ready": map[string]any{
					"cwd": ".", "mode": "rpc", "width": 120, "state": map[string]any{"hasUI": true},
				}})
				close(h.ready)
			}
			continue
		}
		h.answerCall(frame)
	}
}

func (h *bridgeHost) answerCall(frame bridgeFrame) {
	args := map[string]any{}
	if len(frame.Call.Args) > 0 {
		_ = json.Unmarshal(frame.Call.Args, &args)
	}
	h.mu.Lock()
	h.calls = append(h.calls, bridgeCall{Method: frame.Call.Method})
	sequence := 0
	failing := false
	if frame.Call.Method == "events.emit" {
		h.emits++
		sequence = h.emits
		if h.failNext > 0 {
			h.failNext--
			failing = true
		}
	}
	if frame.Call.Method == "events.emit" && failing {
		h.emitFailed = true
	}
	readFailed := false
	if frame.Call.Method == "getSessionID" && h.emitFailed && h.failRereads > 0 {
		h.failRereads--
		readFailed = true
	}
	session := h.nextSession(frame.Call.Method)
	h.mu.Unlock()
	if frame.Call.Method == "events.emit" {
		select {
		case h.emitted <- sequence:
		default:
		}
	}
	if failing || readFailed {
		_ = h.writeFrame(map[string]any{"type": "call_result", "id": frame.ID, "call_result": map[string]any{"error": map[string]any{"message": "synthetic host bridge failure"}}})
		return
	}
	result := h.answers[frame.Call.Method]
	if session != "" {
		result = map[string]any{"sessionId": session}
	}
	_ = h.writeFrame(map[string]any{"type": "call_result", "id": frame.ID, "call_result": map[string]any{"result": result}})
}

func (h *bridgeHost) nextSession(method string) string {
	if method != "getSessionID" || len(h.sessions) == 0 {
		return ""
	}
	session := h.sessions[0]
	if len(h.sessions) > 1 {
		h.sessions = h.sessions[1:]
	}
	return session
}

func (h *bridgeHost) readFrame() (bridgeFrame, error) {
	_ = h.conn.SetReadDeadline(time.Now().Add(lifecycleDeadline))
	var head [4]byte
	if _, err := io.ReadFull(h.conn, head[:]); err != nil {
		return bridgeFrame{}, err
	}
	body := make([]byte, binary.BigEndian.Uint32(head[:]))
	if _, err := io.ReadFull(h.conn, body); err != nil {
		return bridgeFrame{}, err
	}
	var frame bridgeFrame
	if err := json.Unmarshal(body, &frame); err != nil {
		return bridgeFrame{}, err
	}
	return frame, nil
}

func (h *bridgeHost) writeFrame(value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_ = h.conn.SetWriteDeadline(time.Now().Add(lifecycleDeadline))
	_, err = h.conn.Write(append(binary.BigEndian.AppendUint32(nil, uint32(len(body))), body...)) //nolint:gosec // the frame body is a small JSON document
	return err
}

func (h *bridgeHost) failEmits(count int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failNext = count
}

func (h *bridgeHost) failSessionRereadsAfterAFailedEmit(count int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failRereads = count
}

func (h *bridgeHost) serveSession(session string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions = append(h.sessions, session)
}

func (h *bridgeHost) emitCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.emits
}

func (h *bridgeHost) callsOf() []bridgeCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]bridgeCall(nil), h.calls...)
}

func (h *bridgeHost) request(t *testing.T, id, tool string) {
	t.Helper()
	err := h.writeFrame(map[string]any{"type": "request", "id": id, "request": map[string]any{"method": "command", "tool": tool, "args": ""}})
	if err != nil {
		t.Fatalf("send the %s request: %v", id, err)
	}
}

func bridgeAnswers(session string) map[string]any {
	return map[string]any{
		"getSessionID":          map[string]any{"sessionId": session},
		"getModelInfo":          map[string]any{"id": "deepseek-chat", "provider": "deepseek"},
		"getModel":              map[string]any{"id": "deepseek-chat", "provider": "deepseek", "api": "openai-completions", "baseUrl": "https://api.deepseek.com/v1"},
		"getModelRegistryState": map[string]any{"providers": map[string]any{"deepseek": map[string]any{"configured": true}}},
		"getProviderAuth":       map[string]any{"auth": map[string]any{"apiKey": "fixture-secret"}, "source": "api_key"},
		"getModelAuth":          map[string]any{"apiKey": "fixture-secret"},
		"isUsingOAuth":          false,
	}
}

func readinessCollector(collected chan<- error) func(sdk.Context, string) error {
	return func(call sdk.Context, _ string) error {
		ctx, cancel := context.WithTimeout(context.Background(), lifecycleDeadline)
		defer cancel()
		_, err := Port(call).Collect(ctx, usage.CollectionRequest{
			Channel:    bridgeReadinessChannel,
			Kind:       "readiness",
			ProviderID: bridgeReadinessProvider,
			SessionID:  bridgeSessionID,
			TimeoutMS:  bridgeRequestMS,
		})
		collected <- err
		return nil
	}
}

func awaitCollected(t *testing.T, collected <-chan error) error {
	t.Helper()
	select {
	case err := <-collected:
		return err
	case <-time.After(lifecycleDeadline):
		t.Fatalf("the readiness collection did not settle within the %v cap", lifecycleDeadline)
		return nil
	}
}

func requireSessionRereadAfterEmit(t *testing.T, calls []bridgeCall) {
	t.Helper()
	emit, reread := -1, -1
	for index, call := range calls {
		if call.Method == "events.emit" {
			emit = index
		}
		if call.Method == "getSessionID" && emit >= 0 && reread < 0 {
			reread = index
		}
	}
	if reread < 0 {
		t.Fatalf("the readiness collection did not re-read the session after the failed emit: %v", calls)
	}
}

func wakeUntilEmit(t *testing.T, runtime *Runtime, host *bridgeHost, want int) {
	t.Helper()
	retry := time.NewTicker(bridgeRetryInterval)
	defer retry.Stop()
	limit := time.NewTimer(lifecycleDeadline)
	defer limit.Stop()
	for {
		if host.emitCount() >= want {
			return
		}
		runtime.wake()
		select {
		case <-limit.C:
			t.Fatalf("the scheduler emitted %d times, want %d, within the %v cap", host.emitCount(), want, lifecycleDeadline)
		case <-host.emitted:
		case <-retry.C:
		}
	}
}

func recoveredRuntime(t *testing.T) (*Runtime, *bridgeHost) {
	t.Helper()
	ext := sdk.New("lifecycle")
	views := newViewRecorder()
	runtime, err := Register(ext, Options{Publish: views.publish})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	host := startBridgeHost(t, ext, bridgeAnswers(bridgeSessionID))
	host.failEmits(1)
	if err := runtime.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	views.awaitReason(t, "host-bridge-failed")
	wakeUntilEmit(t, runtime, host, 2)
	wakeUntilEmit(t, runtime, host, 3)
	return runtime, host
}

func TestReadinessEmitFailureKeepsTheSessionIdentity(t *testing.T) {
	collected := make(chan error, 1)
	ext := sdk.New("readiness")
	ext.RegisterCommand(bridgeProbeCommand, sdk.CommandOptions{Handler: readinessCollector(collected)})
	host := startBridgeHost(t, ext, bridgeAnswers(bridgeSessionID))
	host.failEmits(1)

	host.request(t, "readiness", bridgeProbeCommand)

	err := awaitCollected(t, collected)
	if err == nil {
		t.Fatal("a failed readiness emit reported a completed collection")
	}
	if errors.Is(err, usage.ErrUsageChanged) {
		t.Fatalf("the failed readiness emit with an unchanged session reported an identity change: %v", err)
	}
	if !errors.Is(err, errBridge) {
		t.Fatalf("the failed readiness emit reported %v, want the host bridge failure", err)
	}
	requireSessionRereadAfterEmit(t, host.callsOf())
}

func TestReadinessEmitFailureWithAChangedSessionReportsAnIdentityChange(t *testing.T) {
	collected := make(chan error, 1)
	ext := sdk.New("readiness")
	ext.RegisterCommand(bridgeProbeCommand, sdk.CommandOptions{Handler: readinessCollector(collected)})
	host := startBridgeHost(t, ext, bridgeAnswers(bridgeSessionID))
	host.failEmits(1)
	host.serveSession(bridgeRotatedSessionID)

	host.request(t, "readiness", bridgeProbeCommand)

	err := awaitCollected(t, collected)
	if !errors.Is(err, usage.ErrUsageChanged) {
		t.Fatalf("the failed readiness emit read the rotated session but reported %v, want the identity change", err)
	}
}

func TestReadinessEmitFailureWithAnUnreadableSessionDoesNotClaimAnIdentityChange(t *testing.T) {
	collected := make(chan error, 1)
	ext := sdk.New("readiness")
	ext.RegisterCommand(bridgeProbeCommand, sdk.CommandOptions{Handler: readinessCollector(collected)})
	host := startBridgeHost(t, ext, bridgeAnswers(bridgeSessionID))
	host.failEmits(1)
	host.failSessionRereadsAfterAFailedEmit(1)

	host.request(t, "readiness", bridgeProbeCommand)

	err := awaitCollected(t, collected)
	if err == nil {
		t.Fatal("a failed session re-read after a failed emit reported a completed collection")
	}
	if errors.Is(err, usage.ErrUsageChanged) {
		t.Fatalf("the failed session re-read reported an identity change: %v", err)
	}
}

func TestRecoveredBridgeLetsTheNextSessionStartProceed(t *testing.T) {
	runtime, _ := recoveredRuntime(t)

	startErr := runtime.start()

	if startErr != nil {
		t.Fatalf("session start after the bridge recovered reported %v, want no error", startErr)
	}
}

func TestRecoveredBridgeLetsTheNextConfigureProceed(t *testing.T) {
	runtime, _ := recoveredRuntime(t)

	configureErr := runtime.Configure(usage.CollectionSettings{Targets: map[string]string{"fireworks": "target"}})

	if configureErr != nil {
		t.Fatalf("configure after the bridge recovered reported %v, want no error", configureErr)
	}
}

func TestRecoveredBridgeLetsTheNextOwnerCloseProceed(t *testing.T) {
	runtime, _ := recoveredRuntime(t)

	closeErr := runtime.Close()

	if closeErr != nil {
		t.Fatalf("closing a runtime whose bridge recovered reported %v, want no error", closeErr)
	}
}
