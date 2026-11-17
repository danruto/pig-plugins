package nativeusage

import (
	"context"
	"errors"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

const queryTimeout = 15 * time.Second

var errNoHostCall = errors.New("no host call is available")

type callKey struct{}

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

func registerCommands(ext *sdk.Extension, runtime *Runtime, settings *usage.SettingsStore) {
	owned := newOwnedWork(ext)
	fast := usage.NewCodexFast(usage.CodexFastOptions{
		Settings: settings,
		Client:   usage.Client{},
		Current:  currentScope,
		Identify: identifyAccount,
		Spawn:    owned.spawn,
	})
	ext.RegisterCommand("usage", sdk.CommandOptions{
		Description: "Show usage for the current runtime account",
		Handler:     usageHandler(runtime, settings),
	})
	ext.RegisterCommand("fast", sdk.CommandOptions{
		Description: "Toggle Codex Fast mode",
		Handler:     fastHandler(fast),
	})
	ext.OnSessionStart(sessionStart(fast))
	ext.OnSessionShutdown(func(sdk.Context, map[string]any) (any, error) {
		owned.stop()
		fast.Shutdown()
		return nil, nil
	})
	ext.OnEvent("model_select", refreshCapabilities(owned, fast))
	ext.OnEvent("before_provider_request", beforeProviderRequest(fast))
	ext.OnEvent("message_end", messageEnd(fast))
}

func usageHandler(runtime *Runtime, settings *usage.SettingsStore) sdk.CommandFunc {
	return func(call sdk.Context, args string) error {
		if !call.HasUI() {
			call.Notify(usage.ErrUsageNeedsUI.Error(), "warning")
			return nil
		}
		ctx, done := callContext(call)
		defer done()
		command := usage.UsageCommand{
			Dispatcher: usage.Dispatcher{Resolver: usage.Resolver{Host: Port(call)}, Client: usage.Client{}},
			Settings:   settings,
			Timeout:    queryTimeout,
			Configure:  configure(runtime, call),
			Resets: usage.NewCodexResets(usage.CodexResetsOptions{
				Client:   usage.Client{},
				Current:  currentScope,
				Identify: identifyAccount,
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

func fastHandler(fast *usage.CodexFast) sdk.CommandFunc {
	return func(call sdk.Context, args string) error {
		if !call.HasUI() {
			call.Notify(usage.ErrFastNeedsUI.Error(), "warning")
			return nil
		}
		ctx, done := callContext(call)
		defer done()
		return fast.Run(ctx, commandUI{call: call}, args)
	}
}

func sessionStart(fast *usage.CodexFast) sdk.EventFunc {
	return func(call sdk.Context, _ map[string]any) (any, error) {
		ctx, done := callContext(call)
		defer done()
		fast.PrepareSession(ctx, commandUI{call: call})
		return nil, nil
	}
}

func refreshCapabilities(owned *ownedWork, fast *usage.CodexFast) sdk.EventFunc {
	return func(sdk.Context, map[string]any) (any, error) {
		owned.spawn(func(ctx context.Context) { fast.RefreshCapabilities(ctx, false) })
		return nil, nil
	}
}

func beforeProviderRequest(fast *usage.CodexFast) sdk.EventFunc {
	return func(call sdk.Context, data map[string]any) (any, error) {
		ctx, done := callContext(call)
		defer done()
		rewritten, ok := fast.BeforeProviderRequest(ctx, data["payload"])
		if !ok {
			return nil, nil
		}
		return rewritten, nil
	}
}

func messageEnd(fast *usage.CodexFast) sdk.EventFunc {
	return func(call sdk.Context, data map[string]any) (any, error) {
		ctx, done := callContext(call)
		defer done()
		rewritten, ok := fast.MessageEnd(ctx, data["message"])
		if !ok {
			return nil, nil
		}
		return map[string]any{"message": rewritten}, nil
	}
}

func currentScope(ctx context.Context) (usage.FastScope, error) {
	call, ok := callFrom(ctx)
	if !ok {
		return usage.FastScope{}, errNoHostCall
	}
	return fastScope(ctx, call)
}

func identifyAccount(ctx context.Context) (usage.CodexAccount, error) {
	call, ok := callFrom(ctx)
	if !ok {
		return usage.CodexAccount{}, errNoHostCall
	}
	return usage.CodexAccountFromResolver(usage.Resolver{Host: Port(call)})(ctx)
}

func callContext(call sdk.Context) (context.Context, func()) {
	ctx, done := watchedContext(context.Background(), call.Done())
	return context.WithValue(ctx, callKey{}, call), done
}

func callFrom(ctx context.Context) (sdk.Context, bool) {
	call, ok := ctx.Value(callKey{}).(sdk.Context)
	return call, ok
}

func watchedContext(parent context.Context, stop <-chan struct{}) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		cancel()
		<-watched
	}
}

func fastScope(ctx context.Context, call sdk.Context) (usage.FastScope, error) {
	if err := ctx.Err(); err != nil {
		return usage.FastScope{}, err
	}
	session, err := call.GetSessionID()
	if err != nil {
		return usage.FastScope{}, err
	}
	scope := usage.FastScope{SessionID: session}
	if err := ctx.Err(); err != nil {
		return usage.FastScope{}, err
	}
	info, err := call.GetModelInfo()
	if err != nil || info == nil {
		return scope, err
	}
	if err := ctx.Err(); err != nil {
		return usage.FastScope{}, err
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
