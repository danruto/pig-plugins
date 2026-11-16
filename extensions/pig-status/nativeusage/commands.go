package nativeusage

import (
	"context"
	"sync"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

const queryTimeout = 15 * time.Second

type commandUI struct{ call sdk.Context }

func (u commandUI) HasUI() bool { return u.call.HasUI() }

func (u commandUI) Select(_ context.Context, title string, options []string) (string, bool, error) {
	result, chosen, err := u.call.Select(title, options)
	if err != nil || !chosen {
		return "", false, err
	}
	value, isString := choice(result)
	return value, isString, nil
}

func (u commandUI) Notify(message, level string) { u.call.Notify(message, level) }

func choice(result any) (string, bool) {
	value, isString := result.(string)
	return value, isString
}

type hostCall struct {
	mu   sync.RWMutex
	call sdk.Context
}

func (h *hostCall) set(call sdk.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.call = call
}

func (h *hostCall) latest() sdk.Context {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.call
}

func (h *hostCall) scope(context.Context) (usage.FastScope, error) {
	return fastScope(h.latest())
}

func scopeOf(call sdk.Context) func(context.Context) (usage.FastScope, error) {
	return func(context.Context) (usage.FastScope, error) { return fastScope(call) }
}

func (h *hostCall) account(ctx context.Context) (usage.CodexAccount, error) {
	identify := usage.CodexAccountFromResolver(usage.Resolver{Host: Port(h.latest())})
	return identify(ctx)
}

func registerCommands(ext *sdk.Extension, runtime *Runtime, settings *usage.SettingsStore) {
	calls := &hostCall{}
	fast := usage.NewCodexFast(usage.CodexFastOptions{
		Settings: settings,
		Client:   usage.Client{},
		Current:  calls.scope,
		Identify: calls.account,
	})
	ext.RegisterCommand("usage", sdk.CommandOptions{
		Description: "Show usage for the current runtime account",
		Handler:     usageHandler(runtime, settings, calls),
	})
	ext.RegisterCommand("fast", sdk.CommandOptions{
		Description: "Toggle Codex Fast mode",
		Handler:     fastHandler(fast, calls),
	})
	ext.OnSessionStart(sessionStart(fast, calls))
	ext.OnSessionShutdown(func(sdk.Context, map[string]any) (any, error) {
		fast.Shutdown()
		return nil, nil
	})
	ext.OnEvent("model_select", refreshCapabilities(fast, calls))
	ext.OnEvent("before_provider_request", beforeProviderRequest(fast, calls))
	ext.OnEvent("message_end", messageEnd(fast, calls))
}

func usageHandler(runtime *Runtime, settings *usage.SettingsStore, calls *hostCall) sdk.CommandFunc {
	return func(call sdk.Context, args string) error {
		if !call.HasUI() {
			call.Notify(usage.ErrUsageNeedsUI.Error(), "warning")
			return nil
		}
		calls.set(call)
		ctx, done := callContext(call)
		defer done()
		resolver := usage.Resolver{Host: Port(call)}
		command := usage.UsageCommand{
			Dispatcher: usage.Dispatcher{Resolver: resolver, Client: usage.Client{}},
			Settings:   settings,
			Timeout:    queryTimeout,
			Configure:  configure(runtime, call),
			Resets: usage.NewCodexResets(usage.CodexResetsOptions{
				Client:   usage.Client{},
				Current:  scopeOf(call),
				Identify: usage.CodexAccountFromResolver(resolver),
			}),
		}
		return command.Run(ctx, commandUI{call: call}, args)
	}
}

func configure(runtime *Runtime, call sdk.Context) func(usage.CollectionSettings) {
	return func(settings usage.CollectionSettings) {
		if err := runtime.Configure(settings); err != nil {
			call.Notify("Native usage could not apply the saved settings.", "warning")
		}
	}
}

func fastHandler(fast *usage.CodexFast, calls *hostCall) sdk.CommandFunc {
	return func(call sdk.Context, args string) error {
		if !call.HasUI() {
			call.Notify(usage.ErrFastNeedsUI.Error(), "warning")
			return nil
		}
		calls.set(call)
		ctx, done := callContext(call)
		defer done()
		return fast.Run(ctx, commandUI{call: call}, args)
	}
}

func sessionStart(fast *usage.CodexFast, calls *hostCall) sdk.EventFunc {
	return func(call sdk.Context, _ map[string]any) (any, error) {
		calls.set(call)
		ctx, done := callContext(call)
		defer done()
		fast.PrepareSession(ctx, commandUI{call: call})
		return nil, nil
	}
}

func refreshCapabilities(fast *usage.CodexFast, calls *hostCall) sdk.EventFunc {
	return func(call sdk.Context, _ map[string]any) (any, error) {
		calls.set(call)
		ctx, done := callContext(call)
		defer done()
		fast.RefreshCapabilities(ctx, false)
		return nil, nil
	}
}

func beforeProviderRequest(fast *usage.CodexFast, calls *hostCall) sdk.EventFunc {
	return func(call sdk.Context, data map[string]any) (any, error) {
		calls.set(call)
		ctx, done := callContext(call)
		defer done()
		rewritten, ok := fast.BeforeProviderRequest(ctx, data["payload"])
		if !ok {
			return nil, nil
		}
		return rewritten, nil
	}
}

func messageEnd(fast *usage.CodexFast, calls *hostCall) sdk.EventFunc {
	return func(call sdk.Context, data map[string]any) (any, error) {
		calls.set(call)
		ctx, done := callContext(call)
		defer done()
		rewritten, ok := fast.MessageEnd(ctx, data["message"])
		if !ok {
			return nil, nil
		}
		return map[string]any{"message": rewritten}, nil
	}
}

func callContext(call sdk.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-call.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		cancel()
		<-joined
	}
}

func fastScope(call sdk.Context) (usage.FastScope, error) {
	session, err := call.GetSessionID()
	if err != nil {
		return usage.FastScope{}, err
	}
	scope := usage.FastScope{SessionID: session}
	info, err := call.GetModelInfo()
	if err != nil || info == nil {
		return scope, err
	}
	model, err := call.ModelRegistry().Find(info.Provider, info.ID)
	if err != nil {
		return usage.FastScope{}, err
	}
	codex, ok := usage.CodexModelFrom(model)
	if !ok {
		return scope, nil
	}
	scope.Model, scope.HasModel = codex, true
	return scope, nil
}
