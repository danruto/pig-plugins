package lifecycle

import (
	"context"
	"encoding/json"
	"errors"

	sdk "github.com/MichaelKinsy/PiG/extensions/sdk"
	"github.com/danruto/pig-plugins/extensions/pig-status/internal/usage"
)

type hostPort struct{ call sdk.Context }

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
	metadata := usage.HostMetadata{SessionID: id}
	if model != nil {
		metadata.ProviderID, metadata.ModelID = model.Provider, model.ID
	}
	return metadata, ctx.Err()
}

func (p hostPort) SessionAuth(ctx context.Context, options map[string]any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := p.call.GetSessionAuth(options)
	if err != nil {
		return nil, err
	}
	return result, ctx.Err()
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
	result, err := p.call.CollectEvents(sdk.CollectEventsOptions{Channel: request.Channel, Kind: request.Kind, Data: map[string]any{"provider": request.ProviderID}, SessionID: request.SessionID, TimeoutMS: request.TimeoutMS})
	if err != nil {
		return usage.CollectionResult{}, err
	}
	if result.SessionID != request.SessionID {
		return usage.CollectionResult{}, errors.New("collection session changed")
	}
	values := make([]any, len(result.Values))
	for index, raw := range result.Values {
		if err := json.Unmarshal(raw, &values[index]); err != nil {
			return usage.CollectionResult{}, err
		}
	}
	return usage.CollectionResult{SessionID: result.SessionID, Values: values}, ctx.Err()
}
