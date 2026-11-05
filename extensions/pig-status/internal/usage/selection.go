package usage

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"sync"
	"time"
)

type Selection struct {
	Mode          string
	ProviderID    string
	Targets       map[string]string
	UseCodexUsage bool
	Timeout       time.Duration
}

type Outcome struct {
	ProviderID string   `json:"providerId"`
	TargetID   string   `json:"targetId,omitempty"`
	Status     string   `json:"status"`
	Reason     string   `json:"reason,omitempty"`
	Report     *Report  `json:"report,omitempty"`
	Choices    []Target `json:"choices,omitempty"`
}

type Dispatcher struct {
	Resolver Resolver
	Client   Client
}

type selectionPlan struct {
	selection Selection
	ids       []string
	owner     *selectionFence
	inventory HostInventory
}

type selectionFence struct {
	mu       sync.Mutex
	metadata HostMetadata
	admitted bool
	model    map[string]any
}

func matchesMetadata(metadata HostMetadata, snapshot hostSnapshot) bool {
	return snapshot.SessionID == metadata.SessionID && modelString(snapshot.SelectedModel, "provider") == metadata.ProviderID && modelString(snapshot.SelectedModel, "id") == metadata.ModelID
}

func (f *selectionFence) admit(snapshot hostSnapshot) error {
	if !matchesMetadata(f.metadata, snapshot) {
		return ErrUsageChanged
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.admitted && !reflect.DeepEqual(f.model, snapshot.SelectedModel) {
		return ErrUsageChanged
	}
	f.admitted = true
	f.model = snapshot.SelectedModel
	return nil
}

func (f *selectionFence) verify(snapshot hostSnapshot) error {
	if !matchesMetadata(f.metadata, snapshot) {
		return ErrUsageChanged
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.admitted || !reflect.DeepEqual(f.model, snapshot.SelectedModel) {
		return ErrUsageChanged
	}
	return nil
}

type pendingOutcome struct {
	outcome Outcome
	auth    ResolvedAuth
}

func selectProviders(selection Selection, current string, inventory HostInventory) ([]string, error) {
	switch selection.Mode {
	case "current":
		if selection.ProviderID != "" {
			return nil, errors.New("current selection cannot name another provider")
		}
		return []string{current}, nil
	case "provider":
		if selection.ProviderID == "" {
			return nil, errors.New("provider selection requires a provider")
		}
		return []string{selection.ProviderID}, nil
	case "all":
		if selection.ProviderID != "" {
			return nil, errors.New("all selection cannot name another provider")
		}
		ids := []string{}
		for _, id := range usageProviderIDs {
			if id == current || inventory.Providers[id].Configured {
				ids = append(ids, id)
			}
		}
		return ids, nil
	default:
		return nil, errors.New("explicit usage selection required")
	}
}

var usageProviderIDs = []string{"baseten", "openai", "openai-codex", "deepseek", "github-copilot", "openrouter", "vercel-ai-gateway", "command-code", "fireworks", "opencode-go", "kimi-coding", "minimax", "minimax-cn", "moonshotai", "moonshotai-cn", "zai", "zai-coding-cn", "xai"}

func (d Dispatcher) Query(ctx context.Context, selection Selection) ([]Outcome, error) {
	selection.Targets = maps.Clone(selection.Targets)
	if selection.Timeout <= 0 {
		return nil, errors.New("usage requires a positive query budget")
	}
	ctx, cancel := context.WithTimeout(ctx, min(selection.Timeout, maxQueryDuration))
	defer cancel()
	current, err := d.Resolver.metadata(ctx)
	if err != nil {
		return nil, err
	}
	inventory, err := d.Resolver.inventory(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := selectProviders(selection, current.ProviderID, inventory)
	if err != nil {
		return nil, err
	}
	pending := d.querySelected(ctx, selectionPlan{selection: selection, ids: ids, owner: &selectionFence{metadata: current}, inventory: inventory})
	results := make([]Outcome, len(pending))
	for i, entry := range pending {
		if entry.outcome.Status == "ok" || entry.outcome.Status == "selection-required" {
			if err := entry.auth.revalidate(ctx); err != nil {
				entry.outcome = failedOutcome(entry.outcome.ProviderID, err)
			}
		}
		results[i] = entry.outcome
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fresh, err := d.Resolver.metadata(ctx)
	if err != nil {
		return nil, err
	}
	if fresh != current {
		return nil, ErrUsageChanged
	}
	return results, nil
}

func (d Dispatcher) querySelected(ctx context.Context, plan selectionPlan) []pendingOutcome {
	pending := make([]pendingOutcome, len(plan.ids))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(2, len(plan.ids)) {
		workers.Go(func() {
			for index := range jobs {
				pending[index] = d.queryOne(ctx, plan, plan.ids[index])
			}
		})
	}
	for i := range plan.ids {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return pending
		}
	}
	close(jobs)
	workers.Wait()
	return pending
}

func (d Dispatcher) queryOne(ctx context.Context, plan selectionPlan, providerID string) pendingOutcome {
	selection := plan.selection
	auth, snapshot, err := d.Resolver.resolve(ctx, providerID, ResolveOptions{UseCodexUsage: selection.UseCodexUsage}, &plan.inventory)
	if err != nil {
		return pendingOutcome{outcome: failedOutcome(providerID, err)}
	}
	if err := plan.owner.admit(snapshot); err != nil {
		return pendingOutcome{outcome: failedOutcome(providerID, err)}
	}
	auth.Revalidate = d.fence(auth.Revalidate, providerID, plan.owner)
	outcome := Outcome{ProviderID: providerID, Status: "ok"}
	if providerID == "fireworks" {
		return d.queryTarget(ctx, selection, auth)
	}
	report, err := d.Client.Query(ctx, providerID, auth, selection.Timeout)
	if err != nil {
		return pendingOutcome{outcome: failedOutcome(providerID, err)}
	}
	outcome.Report = &report
	return pendingOutcome{outcome: outcome, auth: auth}
}

func (d Dispatcher) fence(check func(context.Context) error, providerID string, owner *selectionFence) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := check(ctx); err != nil {
			return err
		}
		fresh, err := d.Resolver.snapshot(ctx, map[string]any{"provider": providerID})
		if err != nil {
			return err
		}
		return owner.verify(fresh)
	}
}

func (d Dispatcher) queryTarget(ctx context.Context, selection Selection, auth ResolvedAuth) pendingOutcome {
	targets, err := d.Client.ListTargets(ctx, "fireworks", auth, selection.Timeout)
	if err != nil {
		return pendingOutcome{outcome: failedOutcome("fireworks", err)}
	}
	if len(targets) == 0 {
		return pendingOutcome{outcome: failedOutcome("fireworks", errors.New("billing target discovery returned no choices"))}
	}
	targetID := selection.Targets["fireworks"]
	outcome := Outcome{ProviderID: "fireworks", Status: "selection-required", Choices: targets}
	if !selectedTarget(targets, targetID) {
		return pendingOutcome{outcome: outcome, auth: auth}
	}
	report, err := d.Client.QueryTarget(ctx, "fireworks", auth, targetID, selection.Timeout)
	if err != nil {
		return pendingOutcome{outcome: failedOutcome("fireworks", err)}
	}
	return pendingOutcome{outcome: Outcome{ProviderID: "fireworks", TargetID: targetID, Status: "ok", Report: &report}, auth: auth}
}

func selectedTarget(targets []Target, targetID string) bool {
	if !fireworksAccountID.MatchString(targetID) {
		return false
	}
	for _, target := range targets {
		if target.ID == targetID {
			return true
		}
	}
	return false
}

func failedOutcome(providerID string, err error) Outcome {
	status, reason := "failed", "query-failed"
	switch {
	case errors.Is(err, ErrNoUsageAuth):
		status, reason = "no-auth", "authentication-unavailable"
	case errors.Is(err, ErrUsageUnsupported):
		status, reason = "unsupported", "unsupported-provider"
	case errors.Is(err, ErrUsageChanged):
		reason = "identity-changed"
	case errors.Is(err, ErrUsageOrigin):
		reason = "origin-rejected"
	case errors.Is(err, ErrUsageHost):
		reason = "host-acquisition-failed"
	case errors.Is(err, context.Canceled):
		reason = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timed-out"
	}
	return Outcome{ProviderID: providerID, Status: status, Reason: reason}
}
