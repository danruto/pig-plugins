package usage

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	ErrFastNeedsUI   = errors.New("/fast requires TUI or RPC mode")
	ErrFastArguments = errors.New("/fast does not accept arguments")
)

type FastScope struct {
	SessionID string
	Model     CodexModel
	HasModel  bool
}

type CodexAccount struct {
	AccountID  string
	Headers    http.Header
	Revalidate func(context.Context) error
}

type CodexFastOptions struct {
	Settings      *SettingsStore
	Client        Client
	Current       func(context.Context) (FastScope, error)
	Identify      func(context.Context) (CodexAccount, error)
	Refresh       func()
	Spawn         func(func(context.Context))
	ClientVersion string
	Timeout       time.Duration
	Now           func() time.Time
}

type fastToggle struct {
	enabled    bool
	generation int
	scope      FastScope
}

type pendingFastRequest struct {
	fastRequested bool
	model         CodexModel
	capability    FastCapability
}

type CodexFast struct {
	mu         sync.Mutex
	generation int
	pending    map[string]pendingFastRequest
	catalog    *codexFastCatalog
	settings   *SettingsStore
	current    func(context.Context) (FastScope, error)
	refresh    func()
	spawn      func(func(context.Context))
	timeout    time.Duration
}

func NewCodexFast(options CodexFastOptions) *CodexFast {
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &CodexFast{
		pending:  map[string]pendingFastRequest{},
		catalog:  newCodexFastCatalog(options, timeout, now),
		settings: options.Settings,
		current:  options.Current,
		refresh:  options.Refresh,
		spawn:    options.Spawn,
		timeout:  timeout,
	}
}

func CodexAccountFromResolver(resolver Resolver) func(context.Context) (CodexAccount, error) {
	return func(ctx context.Context) (CodexAccount, error) {
		auth, err := resolver.Resolve(ctx, string(providerOpenAICodex), ResolveOptions{})
		if err != nil {
			return CodexAccount{}, err
		}
		accountID := headerValue(auth.Headers, "ChatGPT-Account-Id")
		authorization := headerValue(auth.Headers, "Authorization")
		if accountID == "" || authorization == "" {
			return CodexAccount{}, errors.New("active codex account headers are unavailable")
		}
		headers := make(http.Header, 2)
		headers.Set("Authorization", authorization)
		headers.Set("ChatGPT-Account-Id", accountID)
		return CodexAccount{AccountID: accountID, Headers: headers, Revalidate: auth.Revalidate}, nil
	}
}

func (f *CodexFast) Run(ctx context.Context, ui CommandUI, args string) error {
	if strings.TrimSpace(args) != "" {
		if !ui.HasUI() {
			return ErrFastArguments
		}
		ui.Notify("/fast does not accept arguments.", "warning")
		return nil
	}
	if !ui.HasUI() {
		return ErrFastNeedsUI
	}
	f.Toggle(ctx, ui, !f.settings.Get().Settings.CodexFastMode)
	return nil
}

func (f *CodexFast) Toggle(ctx context.Context, ui CommandUI, enabled bool) bool {
	return f.apply(ctx, ui, fastToggle{enabled: enabled, generation: f.generationNow()})
}

func (f *CodexFast) apply(ctx context.Context, ui CommandUI, toggle fastToggle) bool {
	scope, ok := f.fastTarget(ctx, ui)
	if !ok {
		return false
	}
	toggle.scope = scope
	if toggle.enabled && !f.confirmCapability(ctx, ui, toggle) {
		return false
	}
	if !f.saveFastMode(ctx, ui, toggle) {
		return false
	}
	if f.refresh != nil {
		f.refresh()
	}
	message := "Codex Fast mode disabled; standard routing will be used."
	if toggle.enabled {
		message = "Codex Fast mode enabled. " + CodexFastUsageWarning
	}
	ui.Notify(message, "info")
	return true
}

func (f *CodexFast) fastTarget(ctx context.Context, ui CommandUI) (FastScope, bool) {
	scope, err := f.scope(ctx)
	if err != nil {
		ui.Notify("Codex Fast mode is unavailable: the active session could not be read.", "warning")
		return FastScope{}, false
	}
	if !scope.HasModel || providerID(scope.Model.Provider) != providerOpenAICodex {
		ui.Notify("/fast is available only for the active OpenAI Codex model.", "warning")
		return FastScope{}, false
	}
	if !IsOfficialCodexModel(&scope.Model) {
		ui.Notify("Fast mode requires the official OpenAI Codex Responses endpoint.", "warning")
		return FastScope{}, false
	}
	if f.settings.Get().Kind == settingsKindInvalid {
		ui.Notify("pi-usage.json is invalid; repair it and run /reload before changing Fast mode.", "error")
		return FastScope{}, false
	}
	return scope, true
}

func (f *CodexFast) confirmCapability(ctx context.Context, ui CommandUI, toggle fastToggle) bool {
	request := catalogRead{refresh: true, force: f.catalog.get(&toggle.scope.Model).Kind != FastSupported}
	capability, ok := f.catalog.read(ctx, toggle.scope, request)
	if ctx.Err() != nil || f.staleGeneration(toggle.generation) {
		return false
	}
	if !ok {
		message := "The active Codex account changed while checking Fast capability."
		if cached := f.catalog.get(&toggle.scope.Model); cached.Kind == FastUnknown {
			message = cached.Reason
		}
		ui.Notify(message, "warning")
		return false
	}
	availability := CodexFastAvailability(&toggle.scope.Model, false, capability)
	if availability.Kind == FastAvailable {
		return true
	}
	if availability.Reason != "" {
		ui.Notify(availability.Reason, "warning")
	}
	return false
}

func (f *CodexFast) saveFastMode(ctx context.Context, ui CommandUI, toggle fastToggle) bool {
	if _, err := f.settings.SetCodexFast(ctx, toggle.enabled); err != nil {
		if ctx.Err() != nil {
			return false
		}
		if errors.Is(err, ErrSettingsInvalid) || errors.Is(err, ErrSettingsChanged) || errors.Is(err, ErrSettingsUnavailable) {
			ui.Notify(err.Error(), "error")
		} else {
			ui.Notify("Could not save pi-usage.json.", "error")
		}
		return false
	}
	if ctx.Err() != nil || f.staleGeneration(toggle.generation) {
		return false
	}
	fresh, err := f.scope(ctx)
	return err == nil && fresh.SessionID == toggle.scope.SessionID
}

func (f *CodexFast) PrepareSession(ctx context.Context, ui CommandUI) {
	f.bumpGeneration()
	f.catalog.reset()
	f.clearPending()
	state, err := f.settings.Reload(ctx)
	if err != nil {
		if ctx.Err() == nil {
			notifyIfUI(ui, "Could not load pi-usage.json; using defaults.", "warning")
		}
		return
	}
	if state.Kind == settingsKindInvalid {
		notifyIfUI(ui, "Invalid pi-usage.json; using defaults without overwriting it.", "warning")
	}
	if f.refresh != nil {
		f.refresh()
	}
	if state.Kind == settingsKindInvalid {
		return
	}
	if f.spawn != nil {
		f.spawn(func(ctx context.Context) { f.RefreshCapabilities(ctx, false) })
		return
	}
	scope, err := f.scope(ctx)
	if err == nil && scope.HasModel && IsOfficialCodexModel(&scope.Model) {
		f.RefreshCapabilities(ctx, false)
	}
}

func (f *CodexFast) RefreshCapabilities(ctx context.Context, force bool) (FastCapability, bool) {
	scope, err := f.scope(ctx)
	if err != nil || !scope.HasModel || !IsOfficialCodexModel(&scope.Model) {
		return UnknownFastCapability, false
	}
	return f.catalog.read(ctx, scope, catalogRead{refresh: true, force: force})
}

func (f *CodexFast) BeforeProviderRequest(ctx context.Context, payload any) (map[string]any, bool) {
	generation := f.generationNow()
	scope, err := f.scope(ctx)
	if err != nil {
		return nil, false
	}
	enabled := f.settings.Get().Settings.CodexFastMode
	capability := UnknownFastCapability
	if enabled && scope.HasModel && IsOfficialCodexModel(&scope.Model) {
		if cached, ok := f.catalog.read(ctx, scope, catalogRead{}); ok {
			capability = cached
		}
	}
	if f.staleGeneration(generation) {
		return nil, false
	}
	rewritten, ok := RewriteCodexFastPayload(payload, &scope.Model, enabled, capability)
	if scope.HasModel {
		f.storePending(requestKey(scope), pendingFastRequest{
			fastRequested: ok && rewritten["service_tier"] == CodexFastServiceTier,
			model:         scope.Model,
			capability:    capability,
		})
	}
	return rewritten, ok
}

func (f *CodexFast) MessageEnd(ctx context.Context, message any) (map[string]any, bool) {
	object, ok := message.(map[string]any)
	if !ok || object["role"] != "assistant" {
		return nil, false
	}
	provider, providerOK := object["provider"].(string)
	modelID, modelOK := object["model"].(string)
	if !providerOK || !modelOK {
		return nil, false
	}
	scope, err := f.scope(ctx)
	if err != nil {
		return nil, false
	}
	request, found := f.takePending(scope.SessionID + ":" + provider + "/" + modelID)
	if !found {
		return nil, false
	}
	return CorrectCodexFastMessageCost(message, &request.model, request.fastRequested, request.capability)
}

func (f *CodexFast) Shutdown() {
	f.bumpGeneration()
	f.catalog.clear()
	f.clearPending()
}

func (f *CodexFast) Availability(model *CodexModel) FastAvailability {
	return CodexFastAvailability(model, f.settings.Get().Settings.CodexFastMode, f.catalog.get(model))
}

func (f *CodexFast) DecorateStatus(model *CodexModel, status string) string {
	enabled := f.settings.Get().Settings.CodexFastMode
	capability := f.catalog.get(model)
	if enabled && CodexFastAvailability(model, enabled, capability).Kind == FastUnknown && codexStatusPattern.MatchString(status) {
		return "codex (Fast unknown)" + status[len("codex"):]
	}
	return CodexFastStatusLabel(status, CodexFastIsEffective(model, enabled, capability))
}

func requestKey(scope FastScope) string {
	return scope.SessionID + ":" + scope.Model.Provider + "/" + scope.Model.ID
}

func (f *CodexFast) scope(ctx context.Context) (FastScope, error) {
	if f.current == nil {
		return FastScope{}, errors.New("no active session is available")
	}
	callCtx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	return f.current(callCtx)
}

func (f *CodexFast) generationNow() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generation
}

func (f *CodexFast) bumpGeneration() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generation++
}

func (f *CodexFast) staleGeneration(generation int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.generation != generation
}

func (f *CodexFast) storePending(key string, request pendingFastRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending[key] = request
}

func (f *CodexFast) takePending(key string) (pendingFastRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request, found := f.pending[key]
	if found {
		delete(f.pending, key)
	}
	return request, found
}

func (f *CodexFast) clearPending() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = map[string]pendingFastRequest{}
}

func notifyIfUI(ui CommandUI, message, level string) {
	if ui != nil && ui.HasUI() {
		ui.Notify(message, level)
	}
}
