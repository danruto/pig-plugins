package completionprobe

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"sync"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
)

func Extension() *sdk.Extension {
	ext := sdk.New("completion-probe")
	model := map[string]any{
		"id": "fixture", "name": "Offline fixture", "provider": "hermes-probe", "api": "hermes-probe", "baseUrl": "http://127.0.0.1:1",
		"input": []string{"text"}, "contextWindow": 200_000, "maxTokens": 4096,
		"cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
	}
	background := maps.Clone(model)
	background["id"] = "background-fixture"
	stream := sdk.ProviderStreamSimpleFunc(func(ctx sdk.Context, model, request, options map[string]any) (*sdk.ModelEventStream, error) {
		if model["id"] == "background-fixture" {
			deadline := time.NewTimer(15 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				if _, err := os.Stat(os.Getenv("PROBE_RELEASE")); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					return nil, errors.New("background request cancelled")
				case <-deadline.C:
					return nil, errors.New("foreground RPC did not release background completion")
				case <-tick.C:
				}
			}
		}
		result := map[string]any{
			"role": "assistant", "api": "hermes-probe", "provider": "hermes-probe", "model": "fixture",
			"content": []any{map[string]any{"type": "text", "text": "offline completion"}},
			"usage": map[string]any{"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 2, "cost": map[string]any{"total": 0}},
			"stopReason": "stop", "timestamp": time.Now().UnixMilli(),
		}
		s := sdk.CreateAssistantMessageEventStream()
		s.Push(map[string]any{"type": "done", "reason": "stop", "message": result})
		return s, nil
	})
	ext.RegisterProvider("hermes-probe", sdk.ProviderConfig{
		"api": "hermes-probe", "apiKey": "fixture", "baseUrl": "http://127.0.0.1:1", "models": []map[string]any{model, background}, "streamSimple": stream,
	})
	var workers sync.WaitGroup
	_, err := ext.Events().On("hermes-probe:background", func(ctx sdk.Context, _ any) error {
		result, err := ctx.Complete(background, map[string]any{"messages": []any{map[string]any{"role": "user", "content": "fixture"}}}, map[string]any{"apiKey": "fixture"})
		if err != nil {
			return err
		}
		return os.WriteFile(os.Getenv("PROBE_REPORT")+".background", result, 0o600)
	})
	if err != nil {
		panic(err)
	}
	ext.OnSessionShutdown(func(_ sdk.Context, _ map[string]any) (any, error) {
		workers.Wait()
		return nil, nil
	})
	ext.OnSessionStart(func(ctx sdk.Context, _ map[string]any) (any, error) {
		path := os.Getenv("PROBE_REPORT")
		if path == "" {
			return nil, errors.New("PROBE_REPORT must name a disposable report file")
		}
		request := map[string]any{"systemPrompt": "Return a fixture.", "messages": []any{map[string]any{"role": "user", "content": "fixture", "timestamp": time.Now().UnixMilli()}}}
		options := map[string]any{"apiKey": "fixture", "cacheRetention": "none"}
		direct, directErr := ctx.Complete(model, request, options)
		report := map[string]any{"contextComplete": json.RawMessage(direct)}
		if directErr != nil {
			report["contextCompleteError"] = directErr.Error()
		}
		report["registryComplete"] = ctx.ModelRegistry().Complete(model, request, options)
		report["registryStreamSimple"] = ctx.ModelRegistry().StreamSimple(model, request, options).Result()
		data, err := json.Marshal(report)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
		workers.Go(func() {
			if err := ext.Events().Emit("hermes-probe:background", nil); err != nil {
				if writeErr := os.WriteFile(path+".error", []byte(err.Error()), 0o600); writeErr != nil {
					panic(writeErr)
				}
			}
		})
		return nil, nil
	})
	return ext
}
