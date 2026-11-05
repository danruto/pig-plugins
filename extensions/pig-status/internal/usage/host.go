package usage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"
)

type HostPort interface {
	Metadata(context.Context) (HostMetadata, error)
	SessionAuth(context.Context, map[string]any) (map[string]any, error)
	Inventory(context.Context, []string) (HostInventory, error)
	ModelState(context.Context, string, string) (HostModelState, error)
	Collect(context.Context, CollectionRequest) (CollectionResult, error)
}

type HostMetadata struct {
	SessionID  string
	ProviderID string
	ModelID    string
}

type HostModelState struct {
	Model           map[string]any
	ProviderBaseURL string
}

type HostInventory struct {
	Models    []map[string]any
	Available []map[string]any
	Providers map[string]HostProvider
}

type HostProvider struct {
	Configured bool
}

type CollectionRequest struct {
	Channel    string
	Kind       string
	ProviderID string
	SessionID  string
	TimeoutMS  int
}

type CollectionResult struct {
	SessionID string
	Values    []any
}

type Resolver struct{ Host HostPort }
type ResolveOptions struct{ UseCodexUsage bool }

var (
	ErrNoUsageAuth      = errors.New("usage authentication unavailable")
	ErrUsageHost        = errors.New("usage host acquisition failed")
	ErrUsageChanged     = errors.New("usage host identity changed")
	ErrUsageOrigin      = errors.New("usage requires the official provider origin")
	ErrUsageUnsupported = errors.New("unsupported usage provider")
)

type hostAuthResult struct {
	Auth struct {
		APIKey  string             `json:"apiKey"`
		Headers map[string]*string `json:"headers"`
		BaseURL string             `json:"baseUrl"`
	} `json:"auth"`
	Source string            `json:"source"`
	Env    map[string]string `json:"env"`
}

type hostSnapshot struct {
	SessionID     string          `json:"sessionId"`
	Revision      string          `json:"revision"`
	Provider      string          `json:"provider"`
	SelectedModel map[string]any  `json:"selectedModel"`
	Model         map[string]any  `json:"model"`
	ProviderAuth  *hostAuthResult `json:"providerAuth"`
	EffectiveAuth *hostAuthResult `json:"effectiveAuth"`
	Credential    any             `json:"credential"`
	Provenance    struct {
		Configured bool    `json:"configured"`
		Source     string  `json:"source"`
		Label      *string `json:"label"`
	} `json:"provenance"`
}

type hostResolution struct {
	resolver   Resolver
	options    map[string]any
	snapshot   hostSnapshot
	modelState HostModelState
	companion  *hostResolution
	inventory  *HostInventory
}

func hostFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUsageHost
}

func (r Resolver) metadata(ctx context.Context) (HostMetadata, error) {
	if err := ctx.Err(); err != nil {
		return HostMetadata{}, err
	}
	metadata, err := r.Host.Metadata(ctx)
	if err != nil || metadata.SessionID == "" {
		return HostMetadata{}, hostFailure(ctx)
	}
	return metadata, ctx.Err()
}

func (r Resolver) snapshot(ctx context.Context, options map[string]any) (hostSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return hostSnapshot{}, err
	}
	request := make(map[string]any, len(options)+1)
	for key, value := range options {
		request[key] = value
	}
	request["timeoutMs"] = hostTimeout(ctx)
	raw, err := r.Host.SessionAuth(ctx, request)
	if err != nil {
		return hostSnapshot{}, hostFailure(ctx)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return hostSnapshot{}, ErrUsageHost
	}
	var snapshot hostSnapshot
	if json.Unmarshal(data, &snapshot) != nil || snapshot.SessionID == "" || snapshot.Revision == "" || snapshot.Provider == "" {
		return hostSnapshot{}, ErrUsageHost
	}
	return snapshot, ctx.Err()
}

func hostTimeout(ctx context.Context) int {
	if deadline, ok := ctx.Deadline(); ok {
		return max(1, min(15000, int(time.Until(deadline)/time.Millisecond)))
	}
	return 15000
}

func (r Resolver) inventory(ctx context.Context) (HostInventory, error) {
	if err := ctx.Err(); err != nil {
		return HostInventory{}, err
	}
	inventory, err := r.Host.Inventory(ctx, append([]string(nil), usageProviderIDs...))
	if err != nil {
		return HostInventory{}, hostFailure(ctx)
	}
	inventory = selectedInventory(inventory)
	data, err := json.Marshal(inventory)
	if err != nil {
		return HostInventory{}, ErrUsageHost
	}
	var owned HostInventory
	if json.Unmarshal(data, &owned) != nil {
		return HostInventory{}, ErrUsageHost
	}
	return owned, ctx.Err()
}

func selectedInventory(inventory HostInventory) HostInventory {
	providers := make(map[string]HostProvider)
	for id, provider := range inventory.Providers {
		if endpoint, _ := providerEndpoint(id); endpoint != "" {
			providers[id] = provider
		}
	}
	return HostInventory{Models: firstOfficialModels(inventory.Models), Available: firstOfficialModels(inventory.Available), Providers: providers}
}

func firstOfficialModels(models []map[string]any) []map[string]any {
	result := []map[string]any{}
	seen := make(map[string]bool)
	for _, model := range models {
		id := modelString(model, "provider")
		if seen[id] || !officialProviderOrigin(id, modelString(model, "baseUrl")) {
			continue
		}
		seen[id] = true
		result = append(result, model)
	}
	return result
}

func (r Resolver) ready(ctx context.Context, snapshot hostSnapshot) error {
	_, err := r.collect(ctx, snapshot, "oauth:credential-readiness:v1", "readiness")
	return err
}

func (r Resolver) collect(ctx context.Context, snapshot hostSnapshot, channel, kind string) ([]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := r.Host.Collect(ctx, CollectionRequest{Channel: channel, Kind: kind, ProviderID: snapshot.Provider, SessionID: snapshot.SessionID, TimeoutMS: hostTimeout(ctx)})
	if err != nil {
		return nil, hostFailure(ctx)
	}
	if result.SessionID != snapshot.SessionID {
		return nil, ErrUsageChanged
	}
	return result.Values, ctx.Err()
}

func requestedModel(snapshot hostSnapshot, inventory HostInventory, providerID string) (map[string]any, error) {
	if providerID == snapshot.Provider {
		if snapshot.Model == nil {
			return nil, ErrNoUsageAuth
		}
		return snapshot.Model, nil
	}
	if !inventory.Providers[providerID].Configured {
		return nil, ErrNoUsageAuth
	}
	for _, models := range [][]map[string]any{inventory.Available, inventory.Models} {
		for _, model := range models {
			if model["provider"] == providerID && officialProviderOrigin(providerID, modelString(model, "baseUrl")) {
				return model, nil
			}
		}
	}
	return nil, ErrNoUsageAuth
}

func modelString(model map[string]any, key string) string {
	value, _ := model[key].(string)
	return value
}

func (r Resolver) readyCurrent(ctx context.Context, providerID string) (hostSnapshot, string, error) {
	if providerID == "" {
		metadata, err := r.metadata(ctx)
		if err != nil {
			return hostSnapshot{}, "", err
		}
		providerID = metadata.ProviderID
	}
	if providerID == "" {
		return hostSnapshot{}, "", ErrNoUsageAuth
	}
	if endpoint, _ := providerEndpoint(providerID); endpoint == "" {
		return hostSnapshot{}, "", ErrUsageUnsupported
	}
	options := map[string]any{"provider": providerID}
	current, err := r.snapshot(ctx, options)
	if err != nil {
		return hostSnapshot{}, "", err
	}
	if err := r.ready(ctx, current); err != nil {
		return hostSnapshot{}, "", err
	}
	fresh, err := r.snapshot(ctx, options)
	if err != nil {
		return hostSnapshot{}, "", err
	}
	if fresh.SessionID != current.SessionID || !reflect.DeepEqual(current.SelectedModel, fresh.SelectedModel) {
		return hostSnapshot{}, "", ErrUsageChanged
	}
	fresh.Provider = modelString(fresh.SelectedModel, "provider")
	fresh.Model = fresh.SelectedModel
	return fresh, providerID, nil
}

func (r Resolver) acquire(ctx context.Context, providerID string, inventory *HostInventory) (hostResolution, error) {
	current, providerID, err := r.readyCurrent(ctx, providerID)
	if err != nil {
		return hostResolution{}, err
	}
	if inventory == nil && providerID != current.Provider {
		owned, err := r.inventory(ctx)
		if err != nil {
			return hostResolution{}, err
		}
		inventory = &owned
	}
	candidates := HostInventory{}
	if inventory != nil {
		candidates = *inventory
	}
	model, err := requestedModel(current, candidates, providerID)
	if err != nil {
		return hostResolution{}, err
	}
	options := map[string]any{"provider": providerID, "model": model}
	snapshot, err := r.snapshot(ctx, options)
	if err != nil {
		return hostResolution{}, err
	}
	if snapshot.SessionID != current.SessionID || snapshot.Provider != providerID || !reflect.DeepEqual(snapshot.SelectedModel, current.SelectedModel) {
		return hostResolution{}, ErrUsageChanged
	}
	resolution := hostResolution{resolver: r, options: options, snapshot: snapshot, inventory: inventory}
	resolution.modelState, err = r.modelState(ctx, snapshot)
	if err != nil {
		return hostResolution{}, err
	}
	if providerID != current.Provider && !reflect.DeepEqual(model, resolution.modelState.Model) {
		return hostResolution{}, ErrUsageChanged
	}
	return resolution, resolution.validateOrigins()
}

func (h hostResolution) validateOrigins() error {
	s := h.snapshot
	bases := []string{modelString(s.Model, "baseUrl"), h.modelState.ProviderBaseURL}
	for _, auth := range []*hostAuthResult{s.ProviderAuth, s.EffectiveAuth} {
		if auth != nil {
			bases = append(bases, auth.Auth.BaseURL)
		}
	}
	if bases[0] == "" {
		return ErrUsageOrigin
	}
	for _, base := range bases {
		if base != "" && !officialProviderOrigin(s.Provider, base) {
			return ErrUsageOrigin
		}
	}
	return nil
}

func (h hostResolution) revalidate(ctx context.Context) error {
	if err := h.resolver.ready(ctx, h.snapshot); err != nil {
		return err
	}
	fresh, err := h.resolver.snapshot(ctx, h.options)
	if err != nil {
		return err
	}
	if fresh.SessionID != h.snapshot.SessionID || fresh.Revision != h.snapshot.Revision {
		return ErrUsageChanged
	}
	state, err := h.resolver.modelState(ctx, fresh)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(state, h.modelState) {
		return ErrUsageChanged
	}
	return ctx.Err()
}

func (r Resolver) modelState(ctx context.Context, snapshot hostSnapshot) (HostModelState, error) {
	if err := ctx.Err(); err != nil {
		return HostModelState{}, err
	}
	state, err := r.Host.ModelState(ctx, snapshot.Provider, modelString(snapshot.Model, "id"))
	if err != nil {
		return HostModelState{}, hostFailure(ctx)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return HostModelState{}, ErrUsageHost
	}
	var owned HostModelState
	if json.Unmarshal(data, &owned) != nil {
		return HostModelState{}, ErrUsageHost
	}
	return owned, ctx.Err()
}

func requestFromHost(auth *hostAuthResult) RequestAuth {
	if auth == nil {
		return RequestAuth{}
	}
	var headers http.Header
	if auth.Auth.Headers != nil {
		headers = make(http.Header, len(auth.Auth.Headers))
	}
	for name, value := range auth.Auth.Headers {
		if value == nil {
			headers[name] = nil
		} else {
			headers[name] = []string{*value}
		}
	}
	return RequestAuth{APIKey: auth.Auth.APIKey, Headers: headers, BaseURL: auth.Auth.BaseURL}
}

func effectiveHostAuth(snapshot hostSnapshot) *hostAuthResult {
	if snapshot.Provider == modelString(snapshot.SelectedModel, "provider") {
		return snapshot.EffectiveAuth
	}
	return snapshot.ProviderAuth
}

func (r Resolver) Resolve(ctx context.Context, providerID string, options ResolveOptions) (ResolvedAuth, error) {
	auth, _, err := r.resolve(ctx, providerID, options, nil)
	return auth, err
}

func (r Resolver) resolve(ctx context.Context, providerID string, options ResolveOptions, inventory *HostInventory) (ResolvedAuth, hostSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, maxQueryDuration)
	defer cancel()
	resolution, err := r.acquire(ctx, providerID, inventory)
	if err != nil {
		return ResolvedAuth{}, hostSnapshot{}, err
	}
	auth, err := resolution.resolved(ctx, options)
	return auth, resolution.snapshot, err
}

func (h hostResolution) resolved(ctx context.Context, options ResolveOptions) (ResolvedAuth, error) {
	s := h.snapshot
	effectiveAuth := effectiveHostAuth(s)
	effective := requestFromHost(effectiveAuth)
	if effectiveAuth == nil {
		return ResolvedAuth{}, ErrNoUsageAuth
	}
	auth := ResolvedAuth{APIKey: effective.APIKey, SelectedProviderID: modelString(s.SelectedModel, "provider"), Source: s.Provenance.Source, SourceLabel: s.Provenance.Label, ModelBaseURL: modelString(s.Model, "baseUrl"), Revalidate: h.revalidate, UseCodexUsage: options.UseCodexUsage}
	if !moonshotAuthAllowed(s.Provider, auth) {
		return ResolvedAuth{}, ErrNoUsageAuth
	}
	headers, err := usageAuthorization(effective)
	if err != nil {
		return ResolvedAuth{}, err
	}
	auth.Headers = headers
	if s.Provider == "openai" && options.UseCodexUsage {
		companion, err := h.resolver.acquire(ctx, "openai-codex", h.inventory)
		if err != nil && !errors.Is(err, ErrNoUsageAuth) {
			return ResolvedAuth{}, err
		}
		if err == nil {
			h.companion = &companion
		}
	}
	auth.ResolveOAuth = func(ctx context.Context) (OAuthSnapshot, error) { return h.oauth(ctx, options) }
	if s.Provider == "openai-codex" {
		return h.codex(ctx, auth)
	}
	return auth, nil
}

func usageAuthorization(auth RequestAuth) (http.Header, error) {
	var headers = make(http.Header)
	for name, values := range auth.Headers {
		if !hasHeader(http.Header{name: values}, "Authorization") {
			continue
		}
		if hasHeader(headers, "Authorization") || len(values) != 1 || values[0] == "" {
			return nil, ErrNoUsageAuth
		}
		headers[name] = values
	}
	if !hasHeader(headers, "Authorization") && auth.APIKey != "" {
		headers.Set("Authorization", "Bearer "+auth.APIKey)
	}
	if len(headers) == 0 {
		return nil, ErrNoUsageAuth
	}
	return headers, nil
}

func (h hostResolution) oauth(ctx context.Context, options ResolveOptions) (OAuthSnapshot, error) {
	if err := h.revalidate(ctx); err != nil {
		return OAuthSnapshot{}, err
	}
	offers, err := h.resolver.collect(ctx, h.snapshot, "oauth:credential-source:v1", "offers")
	if err != nil {
		return OAuthSnapshot{}, err
	}
	fresh, err := h.resolver.snapshot(ctx, h.options)
	if err != nil {
		return OAuthSnapshot{}, err
	}
	if fresh.Revision != h.snapshot.Revision || fresh.SessionID != h.snapshot.SessionID {
		return OAuthSnapshot{}, ErrUsageChanged
	}
	candidates := append([]any{}, offers...)
	if fresh.Credential != nil {
		candidates = append(candidates, fresh.Credential)
	}
	snapshot := OAuthSnapshot{Ready: true, Source: sourceOf(fresh.ProviderAuth), ModelBaseURL: modelString(fresh.Model, "baseUrl"), Effective: requestFromHost(effectiveHostAuth(fresh)), Provider: requestFromHost(fresh.ProviderAuth), Candidates: candidates}
	if fresh.Provider != "openai" || !options.UseCodexUsage {
		return snapshot, nil
	}
	snapshot.CompanionReady = true
	if h.companion == nil {
		return snapshot, nil
	}
	companionSnapshot, err := h.companion.oauth(ctx, ResolveOptions{})
	if err != nil {
		return OAuthSnapshot{}, err
	}
	snapshot.Companion = &companionSnapshot
	return snapshot, nil
}

func sourceOf(auth *hostAuthResult) string {
	if auth == nil {
		return ""
	}
	return auth.Source
}

func (h hostResolution) codex(ctx context.Context, auth ResolvedAuth) (ResolvedAuth, error) {
	snapshot, err := h.oauth(ctx, ResolveOptions{})
	if err != nil {
		return ResolvedAuth{}, err
	}
	identity, err := resolveCompanion(snapshot)
	if err != nil {
		return ResolvedAuth{}, err
	}
	auth.Headers = identity.headers
	auth.Revalidate = func(ctx context.Context) error {
		snapshot, err := h.oauth(ctx, ResolveOptions{})
		if err != nil {
			return err
		}
		fresh, err := resolveCompanion(snapshot)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(identity, fresh) {
			return ErrUsageChanged
		}
		return nil
	}
	return auth, nil
}
