package nativeusage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
	"github.com/danruto/pig-plugins/extensions/pig-status/nativeusage"
)

const (
	readinessEmitChannel  = "oauth:credential-readiness:v1"
	readinessProbeCommand = "readiness-probe"
	readinessRequestMS    = 150
	readinessContextCap   = 2 * time.Second
)

func readinessProbeFactory() *sdk.Extension {
	ext := sdk.New(readinessProbeCommand)
	ext.RegisterCommand(readinessProbeCommand, sdk.CommandOptions{Handler: func(call sdk.Context, providers string) error {
		session, err := call.GetSessionID()
		if err != nil {
			return err
		}
		port := nativeusage.Port(call)
		for _, provider := range strings.Split(providers, ",") {
			ctx, cancel := context.WithTimeout(context.Background(), readinessContextCap)
			_, _ = port.Collect(ctx, usage.CollectionRequest{
				Channel:    readinessEmitChannel,
				Kind:       "readiness",
				ProviderID: provider,
				SessionID:  session,
				TimeoutMS:  readinessRequestMS,
			})
			cancel()
		}
		return nil
	}})
	return ext
}

func TestHeldReadinessKeepsOneEmitInFlightPerProvider(t *testing.T) {
	host := startReadinessProbeHost(t)

	held := host.driveHoldingReadiness(t, readinessProbeCommand, map[string]any{
		"method": "command",
		"tool":   readinessProbeCommand,
		"args":   "deepseek,deepseek,deepseek,openrouter",
	})

	if want := []string{"deepseek", "openrouter"}; !slices.Equal(providersOf(held), want) {
		t.Fatalf("four readiness collections with readiness held emitted for %v, want one emit per provider %v", providersOf(held), want)
	}
	if want := []string{"provider,sessionId", "provider,sessionId"}; !slices.Equal(fieldsOf(held), want) {
		t.Fatalf("the readiness emits carried %v, want only the provider and session ID", fieldsOf(held))
	}
	if want := []string{hostSessionID, hostSessionID}; !slices.Equal(sessionsOf(held), want) {
		t.Fatalf("the readiness emits carried sessions %v, want the current session %v", sessionsOf(held), want)
	}
}

func providersOf(held []map[string]any) []string {
	providers := make([]string, 0, len(held))
	for _, payload := range held {
		providers = append(providers, wireArgString(payload, "provider"))
	}
	slices.Sort(providers)
	return providers
}

func fieldsOf(held []map[string]any) []string {
	fields := make([]string, 0, len(held))
	for _, payload := range held {
		fields = append(fields, strings.Join(slices.Sorted(maps.Keys(payload)), ","))
	}
	return fields
}

func sessionsOf(held []map[string]any) []string {
	sessions := make([]string, 0, len(held))
	for _, payload := range held {
		sessions = append(sessions, wireArgString(payload, "sessionId"))
	}
	return sessions
}

func (h *wireHost) driveHoldingReadiness(t *testing.T, id string, request map[string]any) []map[string]any {
	t.Helper()
	held := []map[string]any{}
	h.send(t, id, request)
	for {
		frame, err := h.readWireFrame(t)
		if err != nil {
			t.Fatalf("the %s request did not return within the %v cap while readiness stayed held: %v", id, hostFrameDeadline, err)
		}
		if frame.Call == nil {
			if frame.Type == "response" && frame.ID == id {
				return held
			}
			continue
		}
		args := map[string]any{}
		if len(frame.Call.Args) > 0 {
			_ = json.Unmarshal(frame.Call.Args, &args)
		}
		if frame.Call.Method == "events.emit" && wireArgString(args, "channel") == readinessEmitChannel {
			payload, _ := args["json"].(map[string]any)
			held = append(held, payload)
			continue
		}
		h.answerCall(t, frame.ID, frame.Call.Method)
	}
}

func startReadinessProbeHost(t *testing.T) *wireHost {
	t.Helper()
	agentDir := t.TempDir()
	t.Setenv("PIG_EXT_HOST_INFO", fmt.Sprintf(`{"agentDir":%q}`, agentDir))
	extensionSide, hostSide := net.Pipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = sdk.ServeFactory(extensionSide, func() *sdk.Extension { return readinessProbeFactory() })
	}()
	host := &wireHost{
		fakeHost: &fakeHost{
			conn:     hostSide,
			agentDir: agentDir,
			handlers: map[string]int{},
			answers: map[string]any{
				"getSessionID":          map[string]any{"sessionId": hostSessionID},
				"getModelInfo":          map[string]any{"id": "deepseek-chat", "provider": "deepseek"},
				"getModel":              map[string]any{"id": "deepseek-chat", "provider": "deepseek", "api": "openai-completions", "baseUrl": "https://api.deepseek.com/v1"},
				"getModelRegistryState": map[string]any{"providers": map[string]any{"deepseek": map[string]any{"configured": true}, "openrouter": map[string]any{"configured": true}}},
				"getProviderAuth":       map[string]any{"auth": map[string]any{"apiKey": "fixture-secret"}, "source": "api_key"},
				"getModelAuth":          map[string]any{"apiKey": "fixture-secret"},
				"isUsingOAuth":          false,
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
			"cwd":   agentDir,
			"mode":  "rpc",
			"width": 120,
			"state": map[string]any{"hasUI": true},
		},
	})
	return host
}
