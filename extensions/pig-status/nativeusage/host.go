package nativeusage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"

	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

var errUnobservableCollection = errors.New("credential collection is not observable over the native event bus")

type hostPort struct {
	call  sdk.Context
	emits *emitGate
}

type emitKey struct {
	channel  string
	provider string
	session  string
}

type pendingEmit struct {
	done chan struct{}
	err  error
}

type emitGate struct {
	mu       sync.Mutex
	inflight map[emitKey]*pendingEmit
}

func newEmitGate() *emitGate { return &emitGate{inflight: map[emitKey]*pendingEmit{}} }

func Port(call sdk.Context) usage.HostPort { return ownerPort(call, newEmitGate()) }

func ownerPort(call sdk.Context, emits *emitGate) usage.HostPort {
	return hostPort{call: call, emits: emits}
}

func (g *emitGate) join(key emitKey, run func() error) *pendingEmit {
	g.mu.Lock()
	defer g.mu.Unlock()
	if existing, held := g.inflight[key]; held {
		return existing
	}
	entry := &pendingEmit{done: make(chan struct{})}
	g.inflight[key] = entry
	go func() {
		entry.err = run()
		g.mu.Lock()
		delete(g.inflight, key)
		g.mu.Unlock()
		close(entry.done)
	}()
	return entry
}

func (p hostPort) Metadata(ctx context.Context) (usage.HostMetadata, error) {
	if err := ctx.Err(); err != nil {
		return usage.HostMetadata{}, err
	}
	id, err := p.call.GetSessionID()
	if err != nil {
		return usage.HostMetadata{}, err
	}
	model, err := p.call.GetModelInfo()
	if err != nil {
		return usage.HostMetadata{}, err
	}
	if err := p.sameSession(id); err != nil {
		return usage.HostMetadata{}, err
	}
	metadata := usage.HostMetadata{SessionID: id}
	if model != nil {
		metadata.ProviderID, metadata.ModelID = model.Provider, model.ID
	}
	return metadata, ctx.Err()
}

func (p hostPort) sameSession(id string) error {
	current, err := p.call.GetSessionID()
	if err != nil {
		return err
	}
	if current != id {
		return usage.ErrUsageChanged
	}
	return nil
}

func (p hostPort) SessionAuth(ctx context.Context, options map[string]any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionID, err := p.call.GetSessionID()
	if err != nil {
		return nil, err
	}
	selected, err := p.selected()
	if err != nil {
		return nil, err
	}
	provider, model := requested(options, selected)
	providerAuth, err := p.providerAuth(provider)
	if err != nil {
		return nil, err
	}
	effectiveAuth, err := p.modelAuth(provider, requestedID(model, selected, provider))
	if err != nil {
		return nil, err
	}
	provenance, err := p.provenance(provider)
	if err != nil {
		return nil, err
	}
	if err := p.sameSession(sessionID); err != nil {
		return nil, err
	}
	revision, err := authRevision(authIdentity{Session: sessionID, Selected: selected, Model: model, ProviderAuth: providerAuth, EffectiveAuth: effectiveAuth})
	if err != nil {
		return nil, err
	}
	snapshot := map[string]any{
		"sessionId":     sessionID,
		"revision":      revision,
		"provider":      provider,
		"selectedModel": selected,
		"model":         model,
		"providerAuth":  providerAuth,
		"effectiveAuth": effectiveAuth,
		"provenance":    provenance,
	}
	return snapshot, ctx.Err()
}

func (p hostPort) selected() (map[string]any, error) {
	info, err := p.call.GetModelInfo()
	if err != nil || info == nil {
		return nil, err
	}
	return p.call.ModelRegistry().Find(info.Provider, info.ID)
}

func (p hostPort) providerAuth(provider string) (map[string]any, error) {
	if provider == "" {
		return nil, nil
	}
	return p.call.ModelRegistry().GetProviderAuth(provider)
}

func (p hostPort) modelAuth(provider, modelID string) (map[string]any, error) {
	if provider == "" || modelID == "" {
		return nil, nil
	}
	raw, err := p.call.GetModelAuth(provider, modelID)
	if err != nil {
		return nil, err
	}
	var resolved struct {
		OK      bool               `json:"ok"`
		APIKey  string             `json:"apiKey"`
		Headers map[string]*string `json:"headers"`
		BaseURL string             `json:"baseUrl"`
		Env     map[string]string  `json:"env"`
		Source  string             `json:"source"`
	}
	if err := json.Unmarshal(raw, &resolved); err != nil {
		return nil, err
	}
	if !resolved.OK {
		return nil, nil
	}
	auth := map[string]any{"auth": map[string]any{"apiKey": resolved.APIKey, "headers": resolved.Headers, "baseUrl": resolved.BaseURL}}
	if resolved.Env != nil {
		auth["env"] = resolved.Env
	}
	if resolved.Source != "" {
		auth["source"] = resolved.Source
	}
	return auth, nil
}

func (p hostPort) provenance(provider string) (map[string]any, error) {
	if provider == "" {
		return map[string]any{"configured": false}, nil
	}
	status, err := p.call.ModelRegistry().GetProviderAuthStatus(provider)
	if err != nil {
		return nil, err
	}
	provenance := map[string]any{"configured": status.Configured, "source": status.Source}
	if status.Label != "" {
		provenance["label"] = status.Label
	}
	return provenance, nil
}

func requested(options, selected map[string]any) (string, map[string]any) {
	provider, _ := options["provider"].(string)
	if provider == "" {
		provider = modelField(selected, "provider")
	}
	model, _ := options["model"].(map[string]any)
	if model == nil && provider == modelField(selected, "provider") {
		model = selected
	}
	return provider, model
}

func requestedID(model, selected map[string]any, provider string) string {
	if id := modelField(model, "id"); id != "" {
		return id
	}
	if provider == modelField(selected, "provider") {
		return modelField(selected, "id")
	}
	return ""
}

func modelField(model map[string]any, key string) string {
	value, _ := model[key].(string)
	return value
}

type authIdentity struct {
	Session       string         `json:"session"`
	Selected      map[string]any `json:"selected"`
	Model         map[string]any `json:"model"`
	ProviderAuth  map[string]any `json:"providerAuth"`
	EffectiveAuth map[string]any `json:"effectiveAuth"`
}

func authRevision(identity authIdentity) (string, error) {
	data, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (p hostPort) Inventory(ctx context.Context, providerIDs []string) (usage.HostInventory, error) {
	if err := ctx.Err(); err != nil {
		return usage.HostInventory{}, err
	}
	registry := p.call.ModelRegistry()
	all, err := registry.GetAll()
	if err != nil {
		return usage.HostInventory{}, err
	}
	available, err := registry.GetAvailable()
	if err != nil {
		return usage.HostInventory{}, err
	}
	providers := make(map[string]usage.HostProvider)
	for _, model := range available {
		id, _ := model["provider"].(string)
		providers[id] = usage.HostProvider{Configured: true}
	}
	for _, id := range providerIDs {
		if providers[id].Configured {
			continue
		}
		status, err := registry.GetProviderAuthStatus(id)
		if err != nil {
			return usage.HostInventory{}, err
		}
		providers[id] = usage.HostProvider{Configured: status.Configured}
	}
	return usage.HostInventory{Models: all, Available: available, Providers: providers}, ctx.Err()
}

func (p hostPort) ModelState(ctx context.Context, providerID, modelID string) (usage.HostModelState, error) {
	if err := ctx.Err(); err != nil {
		return usage.HostModelState{}, err
	}
	model, err := p.call.ModelRegistry().Find(providerID, modelID)
	if err != nil {
		return usage.HostModelState{}, err
	}
	config, err := p.call.ModelRegistry().GetRegisteredProviderConfig(providerID)
	if err != nil {
		return usage.HostModelState{}, err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return usage.HostModelState{}, err
	}
	var base struct {
		BaseURL string `json:"baseUrl"`
	}
	if err := json.Unmarshal(data, &base); err != nil {
		return usage.HostModelState{}, err
	}
	return usage.HostModelState{Model: model, ProviderBaseURL: base.BaseURL}, ctx.Err()
}

func (p hostPort) Collect(ctx context.Context, request usage.CollectionRequest) (usage.CollectionResult, error) {
	if err := ctx.Err(); err != nil {
		return usage.CollectionResult{}, err
	}
	registry := p.call.ModelRegistry()
	usingOAuth, err := registry.IsUsingOAuth(map[string]any{"provider": request.ProviderID})
	if err != nil {
		return usage.CollectionResult{}, err
	}
	providerAuth, err := p.providerAuth(request.ProviderID)
	if err != nil {
		return usage.CollectionResult{}, err
	}
	if err := observable(request.Kind, usingOAuth, providerAuth); err != nil {
		return usage.CollectionResult{}, err
	}
	if err := p.emit(ctx, request); err != nil {
		return usage.CollectionResult{}, err
	}
	sessionID, err := p.call.GetSessionID()
	if err != nil {
		return usage.CollectionResult{}, err
	}
	if sessionID != request.SessionID {
		return usage.CollectionResult{}, usage.ErrUsageChanged
	}
	return usage.CollectionResult{SessionID: sessionID}, ctx.Err()
}

func (p hostPort) emit(ctx context.Context, request usage.CollectionRequest) error {
	settled := p.emits.join(
		emitKey{channel: request.Channel, provider: request.ProviderID, session: request.SessionID},
		func() error {
			return p.call.Events().Emit(request.Channel, map[string]any{"provider": request.ProviderID, "sessionId": request.SessionID})
		},
	)
	timer := time.NewTimer(time.Duration(max(1, request.TimeoutMS)) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-settled.done:
		if settled.err != nil {
			return usage.ErrUsageChanged
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return context.DeadlineExceeded
	}
	return nil
}

func observable(kind string, usingOAuth bool, providerAuth map[string]any) error {
	source, _ := providerAuth["source"].(string)
	if kind != "readiness" || usingOAuth || source == "OAuth" {
		return errUnobservableCollection
	}
	return nil
}
