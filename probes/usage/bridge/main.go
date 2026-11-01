// Package main is a probe-only Go peer for the public usage event protocol. It records what a native
// extension receives from Node usage events and emits the closest request a Go sender can express, so
// the Go/Node boundary is characterized with real PiG processes. It is never a package member.
package main

import (
	"fmt"
	"os"
	"slices"
	"sync"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
)

const probeKey = "usageBridgeProbe"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ext := sdk.New("bridge")
	var mu sync.Mutex
	var armed []func()
	if _, err := ext.Events().On("usage-bridge:arm", func(ctx sdk.Context, _ any) error {
		mu.Lock()
		defer mu.Unlock()
		for _, channel := range []string{"usage:query:v1", "usage:capabilities:v1", "usage:changed:v1"} {
			unsubscribe, err := ctx.Events().On(channel, func(ctx sdk.Context, data any) error {
				payload, ok := data.(map[string]any)
				if !ok {
					return nil
				}
				probe, ok := payload[probeKey].(string)
				if !ok {
					return nil
				}
				return ctx.Events().Emit("usage-bridge:received", map[string]any{probeKey: probe, "channel": channel, "view": describe(ctx, payload)})
			})
			if err != nil {
				return err
			}
			armed = append(armed, unsubscribe)
		}
		return ctx.Events().Emit("usage-bridge:armed", nil)
	}); err != nil {
		return err
	}
	if _, err := ext.Events().On("usage-bridge:disarm", func(ctx sdk.Context, _ any) error {
		mu.Lock()
		defer mu.Unlock()
		for _, unsubscribe := range armed {
			unsubscribe()
		}
		armed = nil
		return ctx.Events().Emit("usage-bridge:disarmed", nil)
	}); err != nil {
		return err
	}
	if _, err := ext.Events().On("usage-bridge:request", func(ctx sdk.Context, data any) error {
		payload, ok := data.(map[string]any)
		if !ok {
			return fmt.Errorf("usage bridge request is %T", data)
		}
		probe, _ := payload[probeKey].(string)
		channel, _ := payload["channel"].(string)
		request := map[string]any{probeKey: probe, "session": map[string]any{}, "signal": map[string]any{}}
		sessionID, err := ctx.GetSessionID()
		if err != nil {
			return err
		}
		request["sessionId"] = sessionID
		for _, key := range []string{"providerId", "refresh"} {
			if value, ok := payload[key]; ok {
				request[key] = value
			}
		}
		emitted := ctx.Events().Emit(channel, request)
		outcome := map[string]any{probeKey: probe, "error": nil}
		if emitted != nil {
			outcome["error"] = emitted.Error()
		}
		return ctx.Events().Emit("usage-bridge:emitted", outcome)
	}); err != nil {
		return err
	}
	return ext.Run()
}

func describe(ctx sdk.Context, payload map[string]any) map[string]any {
	fields := map[string]string{}
	for key, value := range payload {
		fields[key] = kind(value)
	}
	sessionID, err := ctx.GetSessionID()
	view := map[string]any{"fields": fields, "sessionIdMatchesHost": err == nil && payload["sessionId"] == sessionID}
	for _, key := range []string{"session", "signal"} {
		if object, ok := payload[key].(map[string]any); ok {
			view[key+"Keys"] = sortedKeys(object)
		}
	}
	for _, key := range []string{"providerId", "refresh", "reason", "changedAt"} {
		if value, ok := payload[key]; ok {
			view[key] = value
		}
	}
	return view
}

func kind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
