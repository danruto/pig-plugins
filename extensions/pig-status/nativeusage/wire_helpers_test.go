package nativeusage_test

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/nativeusage"
)

const wireCodexModelID = "gpt-5.4"

const wireCodexAPI = "openai-codex-responses"

const wireCodexBaseURL = "https://chatgpt.com/backend-api/codex"

type wireCallFrame struct {
	Method          string          `json:"method"`
	Args            json.RawMessage `json:"args"`
	ParentRequestID string          `json:"parent_request_id"`
}

type wireRequestFrame struct {
	Method string          `json:"method"`
	Args   json.RawMessage `json:"args"`
}

type wireFrame struct {
	Type    string            `json:"type"`
	ID      string            `json:"id"`
	Call    *wireCallFrame    `json:"call"`
	Request *wireRequestFrame `json:"request"`
	Notify  map[string]any    `json:"-"`
	Result  json.RawMessage   `json:"response"`
}

type wireCallRecord struct {
	Method          string
	ParentRequestID string
	Args            map[string]any
	ObservedDuring  string
}

type wireHeldCall struct {
	FrameID string
	Method  string
}

type wireExchange struct {
	Calls                        []wireCallRecord
	Held                         *wireHeldCall
	HeldIndex                    int
	ReturnedWhileACallStayedHeld bool
}

type wireHost struct {
	*fakeHost
	bus         map[string]string
	hold        func(string) bool
	whileHeld   func()
	held        *wireHeldCall
	heldIndex   int
	dialog      func(map[string]any) string
	notices     []map[string]any
	dispatches  map[string]string
	dispatchIDs map[string]bool
	seq         int
	wireCalls   []wireCallRecord
}

func wireCodexModel() map[string]any {
	return map[string]any{"id": wireCodexModelID, "provider": "openai-codex", "api": wireCodexAPI, "baseUrl": wireCodexBaseURL}
}

func serveCodexModel(host *wireHost) {
	host.answers["getModel"] = wireCodexModel()
	host.answers["getModelInfo"] = map[string]any{"id": wireCodexModelID, "provider": "openai-codex"}
	host.answers["getModelRegistryState"] = map[string]any{"providers": map[string]any{}}
}

func sessionStartRequest() map[string]any {
	return map[string]any{"method": "event", "event": "session_start", "args": map[string]any{}}
}

func modelSelectRequest() map[string]any {
	return map[string]any{"method": "event", "event": "model_select", "args": map[string]any{"model": wireCodexModel()}}
}

func fastCommandRequest() map[string]any {
	return map[string]any{"method": "command", "tool": "fast", "args": ""}
}

func usageCommandRequest() map[string]any {
	return map[string]any{"method": "command", "tool": "usage", "args": ""}
}

func streamingTurnRequest() map[string]any {
	return map[string]any{"method": "event", "event": "before_provider_request", "args": map[string]any{
		"payload": map[string]any{"model": wireCodexModelID, "provider": "openai-codex", "messages": []any{}},
	}}
}

func holdAnyCall(string) bool {
	return true
}

func holdNoCall(string) bool {
	return false
}

func holdCodexAccountRead(method string) bool {
	return method == "getProviderAuth" || method == "getModelAuth"
}

func (h *fakeHost) readWireFrame(t *testing.T) (wireFrame, error) {
	t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(hostFrameDeadline))
	var head [4]byte
	if _, err := io.ReadFull(h.conn, head[:]); err != nil {
		return wireFrame{}, err
	}
	payload := make([]byte, binary.BigEndian.Uint32(head[:]))
	if _, err := io.ReadFull(h.conn, payload); err != nil {
		return wireFrame{}, err
	}
	var frame wireFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return wireFrame{}, fmt.Errorf("decode frame %q: %w", payload, err)
	}
	return frame, nil
}

func (h *wireHost) answerCall(t *testing.T, frameID, method string) {
	t.Helper()
	h.write(t, map[string]any{"type": "call_result", "id": frameID, "call_result": map[string]any{"result": h.answers[method]}})
}

func (h *wireHost) send(t *testing.T, id string, request map[string]any) {
	t.Helper()
	if event, ok := request["event"].(string); ok {
		request["handler_id"] = h.handlers[event]
	}
	h.write(t, map[string]any{"type": "request", "id": id, "request": request})
}

func (h *wireHost) exchange(t *testing.T, id string, request map[string]any, hold func(string) bool) wireExchange {
	t.Helper()
	start := len(h.wireCalls)
	h.hold = hold
	h.send(t, id, request)
	for {
		frame, err := h.readWireFrame(t)
		if err != nil {
			t.Fatalf("the %s request did not return to the host within the %v cap while a host call it issued stayed unanswered: %v", id, hostFrameDeadline, err)
		}
		if h.serve(t, id, frame) {
			return wireExchange{
				Calls:                        h.wireCalls[start:],
				Held:                         h.held,
				HeldIndex:                    h.heldIndex - start,
				ReturnedWhileACallStayedHeld: h.held != nil,
			}
		}
	}
}

func (h *wireHost) awaitCall(t *testing.T, id, method string) []wireCallRecord {
	t.Helper()
	start := len(h.wireCalls)
	deadline := time.Now().Add(hostFrameDeadline)
	for {
		frame, err := h.readWireFrame(t)
		if err != nil {
			t.Fatalf("the %s never called the host method %s within the %v cap after it returned: %v", id, method, hostFrameDeadline, err)
		}
		if h.serve(t, id, frame) {
			return h.wireCalls[start:]
		}
		if len(h.wireCalls) > start && h.wireCalls[len(h.wireCalls)-1].Method == method {
			return h.wireCalls[start:]
		}
		if time.Now().After(deadline) {
			t.Fatalf("the %s never called the host method %s within the %v cap after it returned", id, method, hostFrameDeadline)
		}
	}
}

func (h *wireHost) awaitSettled(t *testing.T, id string, settled func() bool) []map[string]any {
	t.Helper()
	seen := len(h.notices)
	deadline := time.Now().Add(hostFrameDeadline)
	for {
		frame, err := h.readWireFrame(t)
		if err != nil {
			t.Fatalf("the %s never settled within the %v cap: %v", id, hostFrameDeadline, err)
		}
		h.serve(t, id, frame)
		if len(h.notices) > seen || settled() {
			return h.notices[seen:]
		}
		if time.Now().After(deadline) {
			t.Fatalf("the %s never settled within the %v cap", id, hostFrameDeadline)
		}
	}
}

func (h *wireHost) release(t *testing.T) {
	t.Helper()
	if h.held == nil {
		return
	}
	h.answerCall(t, h.held.FrameID, h.held.Method)
	h.held = nil
}

func (h *wireHost) serve(t *testing.T, id string, frame wireFrame) bool {
	t.Helper()
	if frame.Type == "response" {
		if emitID, ok := h.dispatches[frame.ID]; ok {
			delete(h.dispatches, frame.ID)
			h.answerCall(t, emitID, "events.emit")
			return false
		}
		return frame.ID == id
	}
	if frame.Call == nil {
		return false
	}
	args := map[string]any{}
	if len(frame.Call.Args) > 0 {
		_ = json.Unmarshal(frame.Call.Args, &args)
	}
	h.wireCalls = append(h.wireCalls, wireCallRecord{Method: frame.Call.Method, ParentRequestID: frame.Call.ParentRequestID, Args: args, ObservedDuring: id})
	switch frame.Call.Method {
	case "events.on":
		h.bus[wireArgString(args, "channel")] = wireArgString(args, "handlerId")
		h.answerCall(t, frame.ID, frame.Call.Method)
	case "events.emit":
		h.dispatchEvent(t, id, frame, args)
	case "ui.notify":
		h.notices = append(h.notices, args)
		h.answerCall(t, frame.ID, frame.Call.Method)
	case "ui.select":
		if h.dialog == nil {
			h.answerCall(t, frame.ID, frame.Call.Method)
			return false
		}
		h.write(t, map[string]any{"type": "call_result", "id": frame.ID, "call_result": map[string]any{"result": map[string]any{"ok": true, "selected": h.dialog(args)}}})
	default:
		if h.hold != nil && h.held == nil && h.hold(frame.Call.Method) {
			h.held = &wireHeldCall{FrameID: frame.ID, Method: frame.Call.Method}
			h.heldIndex = len(h.wireCalls) - 1
			if h.whileHeld != nil {
				h.whileHeld()
			}
			return false
		}
		h.answerCall(t, frame.ID, frame.Call.Method)
	}
	return false
}

func (h *wireHost) dispatchEvent(t *testing.T, id string, frame wireFrame, args map[string]any) {
	t.Helper()
	channel := wireArgString(args, "channel")
	handler := h.bus[channel]
	if handler == "" {
		h.answerCall(t, frame.ID, frame.Call.Method)
		return
	}
	h.seq++
	dispatchID := fmt.Sprintf("%s-dispatch-%d", id, h.seq)
	h.dispatches[dispatchID] = frame.ID
	h.dispatchIDs[dispatchID] = true
	h.write(t, map[string]any{"type": "request", "id": dispatchID, "request": map[string]any{
		"method": "events.dispatch",
		"args":   map[string]any{"channel": channel, "handlerId": handler, "json": args["json"]},
	}})
}

func wireArgString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func requireNoNotification(t *testing.T, calls []wireCallRecord) {
	t.Helper()
	if hasMethod(calls, "ui.notify") {
		t.Fatalf("the extension notified the host about settings the agent directory does not hold: %+v", calls)
	}
}

func requireNotification(t *testing.T, calls []wireCallRecord) {
	t.Helper()
	if !hasMethod(calls, "ui.notify") {
		t.Fatalf("the extension did not notify the host about the invalid settings file it read: %+v", calls)
	}
}

func requireErrorNotice(t *testing.T, notices []map[string]any) {
	t.Helper()
	if len(notices) == 0 {
		t.Fatal("the extension refused nothing: it did not notify the host")
	}
	for _, notice := range notices {
		if notice["level"] != "error" {
			t.Fatalf("the extension reported %v at level %v, want a save-failure refusal", notice, notice["level"])
		}
	}
}

func hasMethod(calls []wireCallRecord, method string) bool {
	for _, call := range calls {
		if call.Method == method {
			return true
		}
	}
	return false
}

func (h *wireHost) requireOnlyCallsOf(t *testing.T, exchange wireExchange, from int, want string) {
	t.Helper()
	checked := 0
	for _, call := range exchange.Calls[from:] {
		if call.ObservedDuring != want || h.dispatchIDs[call.ParentRequestID] {
			continue
		}
		checked++
		if call.ParentRequestID != want {
			t.Fatalf("the host call %s was issued for request %q, want %q: it belongs to a handler that already returned", call.Method, call.ParentRequestID, want)
		}
	}
	if checked == 0 {
		t.Fatalf("no host call of the %s request was checked, want reads after the held call", want)
	}
}

func startWireHost(t *testing.T) *wireHost {
	t.Helper()
	agentDir := t.TempDir()
	return startWireHostWithHostInfo(t, fmt.Sprintf(`{"agentDir":%q}`, agentDir), agentDir)
}

func startWireHostWithHostInfo(t *testing.T, hostInfo string, agentDir string) *wireHost {
	t.Helper()
	t.Setenv("PIG_EXT_HOST_INFO", hostInfo)
	extensionSide, hostSide := net.Pipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = sdk.ServeFactory(extensionSide, func() *sdk.Extension { return nativeusage.Extension() })
	}()
	host := &wireHost{
		fakeHost: &fakeHost{
			conn:     hostSide,
			agentDir: agentDir,
			handlers: map[string]int{},
			answers: map[string]any{
				"getSessionID":          map[string]any{"sessionId": hostSessionID},
				"getModelInfo":          map[string]any{"id": "deepseek-chat", "provider": "deepseek"},
				"getModel":              map[string]any{"id": "deepseek-chat", "provider": "deepseek", "baseUrl": "https://api.deepseek.com/v1"},
				"getModelRegistryState": map[string]any{"providers": map[string]any{"deepseek": map[string]any{"configured": true}}},
			},
		},
		bus:         map[string]string{},
		dispatches:  map[string]string{},
		dispatchIDs: map[string]bool{},
	}
	t.Cleanup(func() {
		_ = hostSide.Close()
		select {
		case <-served:
		case <-time.After(hostFrameDeadline):
			t.Errorf("the extension did not stop within %v of the host closing the connection", hostFrameDeadline)
		}
	})
	host.awaitRegistration(t)
	host.write(t, map[string]any{
		"type": "ready",
		"ready": map[string]any{
			"cwd":   ".",
			"mode":  "rpc",
			"width": 120,
			"state": map[string]any{"hasUI": true},
		},
	})
	return host
}
