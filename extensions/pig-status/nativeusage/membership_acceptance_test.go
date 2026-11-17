package nativeusage_test

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/nativeusage"
)

const (
	hostFrameDeadline = 10 * time.Second
	hostSessionID     = "01a1e221-69f7-75ca-9cf0-06f11fba16ea"
	hostSettingsFile  = "pi-usage.json"
)

type hostEnvelope struct {
	Type     string          `json:"type"`
	ID       string          `json:"id,omitempty"`
	Register json.RawMessage `json:"register,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
	Call     *hostCallFrame  `json:"call,omitempty"`
}

type hostCallFrame struct {
	Method string          `json:"method"`
	Args   json.RawMessage `json:"args,omitempty"`
}

type hostRegistration struct {
	Handlers []struct {
		Event     string `json:"event"`
		HandlerID int    `json:"handler_id"`
	} `json:"handlers"`
}

type hostCall struct {
	method string
	args   map[string]any
}

type hostResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *hostFailure    `json:"error"`
}

type hostFailure struct {
	Message string `json:"message"`
}

type fakeHost struct {
	conn     net.Conn
	agentDir string
	handlers map[string]int
	calls    []hostCall
	answers  map[string]any
}

func startExtension(t *testing.T, mode string, hasUI bool) *fakeHost {
	t.Helper()
	agentDir := t.TempDir()
	t.Setenv("PIG_EXT_HOST_INFO", fmt.Sprintf(`{"agentDir":%q}`, agentDir))
	extensionSide, hostSide := net.Pipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = sdk.ServeFactory(extensionSide, func() *sdk.Extension { return nativeusage.Extension() })
	}()
	host := &fakeHost{
		conn:     hostSide,
		agentDir: agentDir,
		handlers: map[string]int{},
		answers: map[string]any{
			"getSessionID": map[string]any{"sessionId": hostSessionID},
			"getModelInfo": map[string]any{"id": "deepseek-chat", "provider": "deepseek"},
			"getModel":     map[string]any{"id": "deepseek-chat", "provider": "deepseek", "api": "openai-completions"},
		},
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
			"cwd":   agentDir,
			"mode":  mode,
			"width": 120,
			"state": map[string]any{"hasUI": hasUI},
		},
	})
	return host
}

func (h *fakeHost) write(t *testing.T, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	if uint64(len(body)) > math.MaxUint32 {
		t.Fatalf("a frame of %d bytes exceeds the protocol limit", len(body))
	}
	_ = h.conn.SetWriteDeadline(time.Now().Add(hostFrameDeadline))
	header := binary.BigEndian.AppendUint32(nil, uint32(len(body))) //nolint:gosec // the length was bounded by the protocol maximum above
	if _, err := h.conn.Write(append(header, body...)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func (h *fakeHost) read(t *testing.T) hostEnvelope {
	t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(hostFrameDeadline))
	var head [4]byte
	if _, err := io.ReadFull(h.conn, head[:]); err != nil {
		t.Fatalf("read frame header: %v", err)
	}
	body := make([]byte, binary.BigEndian.Uint32(head[:]))
	if _, err := io.ReadFull(h.conn, body); err != nil {
		t.Fatalf("read frame body: %v", err)
	}
	var envelope hostEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode frame %q: %v", body, err)
	}
	return envelope
}

func (h *fakeHost) awaitRegistration(t *testing.T) {
	t.Helper()
	envelope := h.read(t)
	if envelope.Type != "register" {
		t.Fatalf("first frame = %q, want the extension's registration", envelope.Type)
	}
	var registration hostRegistration
	if err := json.Unmarshal(envelope.Register, &registration); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	for _, handler := range registration.Handlers {
		h.handlers[handler.Event] = handler.HandlerID
	}
}

func (h *fakeHost) request(t *testing.T, id string, body map[string]any) hostResponse {
	t.Helper()
	h.write(t, map[string]any{"type": "request", "id": id, "request": body})
	for {
		envelope := h.read(t)
		if envelope.Call != nil {
			args := map[string]any{}
			if len(envelope.Call.Args) > 0 {
				_ = json.Unmarshal(envelope.Call.Args, &args)
			}
			h.calls = append(h.calls, hostCall{method: envelope.Call.Method, args: args})
			h.write(t, map[string]any{"type": "call_result", "id": envelope.ID, "call_result": map[string]any{"result": h.answers[envelope.Call.Method]}})
			continue
		}
		if envelope.Type == "response" && envelope.ID == id {
			response := hostResponse{}
			if err := json.Unmarshal(envelope.Response, &response); err != nil {
				t.Fatalf("decode response %q: %v", envelope.Response, err)
			}
			return response
		}
	}
}

func (h *fakeHost) command(t *testing.T, name string) hostResponse {
	t.Helper()
	return h.request(t, name, map[string]any{"method": "command", "tool": name, "args": ""})
}

func (h *fakeHost) event(t *testing.T, name string, payload map[string]any) hostResponse {
	t.Helper()
	return h.request(t, name, map[string]any{"method": "event", "event": name, "handler_id": h.handlers[name], "args": payload})
}

func (h *fakeHost) methods() []string {
	methods := make([]string, 0, len(h.calls))
	for _, call := range h.calls {
		methods = append(methods, call.method)
	}
	return methods
}

func (h *fakeHost) notification() (string, bool) {
	for _, call := range h.calls {
		if call.method != "ui.notify" {
			continue
		}
		message, _ := call.args["message"].(string)
		return message, true
	}
	return "", false
}

func (h *fakeHost) calledModel(from int) bool {
	for _, call := range h.calls[from:] {
		if call.method == "getModel" {
			return true
		}
	}
	return false
}

func (r hostResponse) returnedNothing() bool {
	return r.Error == nil && (len(r.Result) == 0 || string(r.Result) == "null")
}

func requireNothingReturned(t *testing.T, response hostResponse) {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("the request failed: %s", response.Error.Message)
	}
	if !response.returnedNothing() {
		t.Fatalf("the request returned %s, want nothing", response.Result)
	}
}

func TestUsageAndFastWithoutUIAskForAUIModeAndNothingElse(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		wantMessage string
	}{
		{name: "usage", command: "usage", wantMessage: "/usage requires TUI or RPC mode"},
		{name: "fast", command: "fast", wantMessage: "/fast requires TUI or RPC mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := startExtension(t, "print", false)

			response := host.command(t, tt.command)

			requireNothingReturned(t, response)
			if got := host.methods(); len(got) != 1 || got[0] != "ui.notify" {
				t.Fatalf("/%s without a UI called the host for %v, want only a notification", tt.command, got)
			}
			if message, _ := host.calls[0].args["message"].(string); message != tt.wantMessage {
				t.Fatalf("/%s notified %q, want %q", tt.command, message, tt.wantMessage)
			}
		})
	}
}

func TestFastCommandWithoutACodexModelRefusesWithoutPersistingAToggle(t *testing.T) {
	host := startExtension(t, "rpc", true)

	response := host.command(t, "fast")

	requireNothingReturned(t, response)
	message, notified := host.notification()
	if !notified {
		t.Fatalf("/fast on a non-Codex model called %v, want a notification that it cannot apply", host.methods())
	}
	if message == "" {
		t.Fatalf("/fast on a non-Codex model notified nothing the user can read")
	}
	if _, err := os.Stat(filepath.Join(host.agentDir, hostSettingsFile)); !os.IsNotExist(err) {
		t.Fatalf("/fast wrote %s for a model it cannot apply", hostSettingsFile)
	}
}

func TestProviderHooksLeaveRequestsAndMessagesTheyCannotRewriteUntouched(t *testing.T) {
	host := startExtension(t, "rpc", true)

	t.Run("a non-codex request keeps its payload", func(t *testing.T) {
		before := len(host.calls)

		response := host.event(t, "before_provider_request", map[string]any{
			"payload": map[string]any{"model": "deepseek-chat", "provider": "deepseek", "messages": []any{}},
		})

		requireNothingReturned(t, response)
		if !host.calledModel(before) {
			t.Fatalf("the hook did not resolve the selected model through the host: %v", host.methods()[before:])
		}
	})

	t.Run("a non-assistant message is not corrected", func(t *testing.T) {
		before := len(host.calls)

		response := host.event(t, "message_end", map[string]any{
			"message": map[string]any{"role": "user", "content": []any{}},
		})

		requireNothingReturned(t, response)
		if calls := host.methods()[before:]; len(calls) != 0 {
			t.Fatalf("a user message made the hook call %v, want no host call", calls)
		}
	})
}
