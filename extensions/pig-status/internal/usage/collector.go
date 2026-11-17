package usage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"
)

const cacheLifetime = 5 * time.Minute
const failureBackoff = 30 * time.Second
const accountCacheLimit = 32

var errCollectionTarget = errors.New("usage requires an explicitly selected billing target")

type CollectionSettings struct {
	Targets       map[string]string
	UseCodexUsage bool
}

type Window struct {
	Label       string
	UsedPercent float64
	ResetsAt    time.Time
	Countdown   time.Duration
}

type Cached struct {
	Windows   []Window
	ExpiresAt time.Time
	RefreshAt time.Time
	Reason    string
}

type accountEntry struct {
	key       [32]byte
	report    *Report
	expiresAt time.Time
	retryAt   time.Time
	reason    string
}

type Collector struct {
	mu         sync.Mutex
	client     Client
	settings   CollectionSettings
	generation uint64
	sequence   uint64
	active     [32]byte
	entries    []accountEntry
	refreshAt  time.Time
	reason     string
}

func NewCollector(client Client) *Collector { return &Collector{client: client} }

func (c *Collector) Configure(settings CollectionSettings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings = CollectionSettings{Targets: maps.Clone(settings.Targets), UseCodexUsage: settings.UseCodexUsage}
	c.generation++
	c.sequence++
	c.active = [32]byte{}
	c.entries = nil
	c.reason = ""
	c.refreshAt = time.Time{}
}

type collectionTicket struct {
	generation uint64
	sequence   uint64
	settings   CollectionSettings
}

func (c *Collector) begin() collectionTicket {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sequence++
	return collectionTicket{generation: c.generation, sequence: c.sequence, settings: c.settings}
}

func (c *Collector) current(ticket collectionTicket) bool {
	return c.generation == ticket.generation && c.sequence == ticket.sequence
}

func (c *Collector) Refresh(ctx context.Context, resolver Resolver) error {
	ticket := c.begin()
	ctx, cancel := context.WithTimeout(ctx, maxQueryDuration)
	defer cancel()
	identity, err := acquireCollection(ctx, resolver, ticket)
	if err != nil {
		return c.fail(ticket, err)
	}
	if c.backingOff(ticket, identity.key, time.Now()) {
		if providerID(identity.provider) == providerFireworks {
			c.publishBackoff(ticket, identity.key)
		} else {
			c.reuse(ticket, identity.key, time.Now())
		}
		return errors.New("usage collection failed")
	}
	if err := c.validateTarget(ctx, identity, ticket); err != nil {
		return c.recordFailure(ticket, identity.key, err)
	}
	if c.reuse(ticket, identity.key, time.Now()) {
		return ctx.Err()
	}
	return c.fetch(ctx, ticket, identity)
}

func (c *Collector) fetch(ctx context.Context, ticket collectionTicket, identity collectionIdentity) error {
	targetID := ""
	var report Report
	var err error
	if providerID(identity.provider) == providerFireworks {
		targetID = ticket.settings.Targets[string(providerFireworks)]
		report, err = c.client.QueryFireworks(ctx, identity.auth, targetID, hostBudget(ctx))
	} else {
		report, err = c.client.Query(ctx, identity.provider, identity.auth, hostBudget(ctx))
	}
	if err != nil {
		return c.recordFailure(ticket, identity.key, err)
	}
	if err := ctx.Err(); err != nil {
		return c.recordFailure(ticket, identity.key, err)
	}
	if report.ProviderID != identity.provider {
		return c.recordFailure(ticket, identity.key, ErrUsageChanged)
	}
	return c.commit(ticket, identity.key, Outcome{ProviderID: identity.provider, TargetID: targetID, Status: "ok", Report: &report}, time.Now())
}

type collectionIdentity struct {
	auth     ResolvedAuth
	provider string
	key      [32]byte
}

func acquireCollection(ctx context.Context, resolver Resolver, ticket collectionTicket) (collectionIdentity, error) {
	metadata, err := resolver.metadata(ctx)
	if err != nil {
		return collectionIdentity{}, err
	}
	if !Schedulable(metadata.ProviderID) {
		return collectionIdentity{}, ErrUsageUnsupported
	}
	auth, snapshot, companionRevision, err := resolver.resolve(ctx, "", ResolveOptions{UseCodexUsage: ticket.settings.UseCodexUsage}, nil)
	if err != nil {
		return collectionIdentity{}, err
	}
	if !matchesMetadata(metadata, snapshot) {
		return collectionIdentity{}, ErrUsageChanged
	}
	state, err := resolver.modelState(ctx, snapshot)
	if err != nil {
		return collectionIdentity{}, err
	}
	key, err := collectionKey(snapshot, state, ticket, companionRevision)
	if err != nil {
		return collectionIdentity{}, err
	}
	return collectionIdentity{auth: auth, provider: snapshot.Provider, key: key}, nil
}

func (c *Collector) validateTarget(ctx context.Context, identity collectionIdentity, ticket collectionTicket) error {
	if providerID(identity.provider) != providerFireworks {
		return nil
	}
	targets, err := c.client.discoverTargets(ctx, identity.auth, hostBudget(ctx))
	if err != nil {
		return err
	}
	if !selectedTarget(targets, ticket.settings.Targets[string(providerFireworks)]) {
		if err := identity.auth.revalidate(ctx); err != nil {
			return err
		}
		return errCollectionTarget
	}
	if c.cached(ticket, identity.key, time.Now()) {
		return identity.auth.revalidate(ctx)
	}
	return nil
}

func hostBudget(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline)
	}
	return maxQueryDuration
}

func collectionKey(snapshot hostSnapshot, state HostModelState, ticket collectionTicket, companionRevision string) ([32]byte, error) {
	data, err := json.Marshal(struct {
		Session           string
		Model             map[string]any
		Registered        HostModelState
		Revision          string
		CompanionRevision string
		Provider          string
		Target            string
		Generation        uint64
		Companion         bool
	}{Session: snapshot.SessionID, Model: snapshot.SelectedModel, Registered: state, Revision: snapshot.Revision, CompanionRevision: companionRevision, Provider: snapshot.Provider, Target: ticket.settings.Targets[snapshot.Provider], Generation: ticket.generation, Companion: ticket.settings.UseCodexUsage})
	if err != nil {
		return [32]byte{}, ErrUsageHost
	}
	return sha256.Sum256(data), nil
}

func (c *Collector) fail(ticket collectionTicket, err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current(ticket) {
		c.active = [32]byte{}
		c.reason = failureReason(err)
		c.refreshAt = time.Now().Add(failureBackoff)
	}
	return err
}

func failureReason(err error) string {
	if errors.Is(err, errCollectionTarget) {
		return "selection-required"
	}
	return failedOutcome("", err).Reason
}

func (c *Collector) failKeyed(ticket collectionTicket, key [32]byte, err error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(ticket) {
		return err
	}
	now := time.Now()
	c.sweep(now)
	entry := c.store(key, func(entry accountEntry) accountEntry {
		entry.retryAt = now.Add(failureBackoff)
		entry.reason = failureReason(err)
		return entry
	})
	c.active = [32]byte{}
	c.reason = entry.reason
	c.refreshAt = entry.retryAt
	return err
}

func (c *Collector) recordFailure(ticket collectionTicket, key [32]byte, err error) error {
	if errors.Is(err, ErrUsageChanged) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errCollectionTarget) {
		return c.failKeyed(ticket, key, err)
	}
	return c.commit(ticket, key, failedOutcome("", err), time.Now())
}

func (c *Collector) backingOff(ticket collectionTicket, key [32]byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(ticket) {
		return false
	}
	c.sweep(now)
	index := slices.IndexFunc(c.entries, func(entry accountEntry) bool { return entry.key == key })
	return index >= 0 && now.Before(c.entries[index].retryAt)
}

func (c *Collector) publishBackoff(ticket collectionTicket, key [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(ticket) {
		return
	}
	c.sweep(time.Now())
	index := slices.IndexFunc(c.entries, func(entry accountEntry) bool { return entry.key == key })
	if index < 0 {
		return
	}
	entry := c.entries[index]
	c.active = [32]byte{}
	c.reason = entry.reason
	c.refreshAt = entry.retryAt
}

func (c *Collector) reuse(ticket collectionTicket, key [32]byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.reusable(ticket, key, now)
	if ok {
		c.activate(entry)
	}
	return ok
}

func (c *Collector) cached(ticket collectionTicket, key [32]byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.reusable(ticket, key, now)
	return ok
}

func (c *Collector) reusable(ticket collectionTicket, key [32]byte, now time.Time) (accountEntry, bool) {
	if !c.current(ticket) {
		return accountEntry{}, false
	}
	c.sweep(now)
	index := slices.IndexFunc(c.entries, func(entry accountEntry) bool {
		return entry.key == key && ((entry.report != nil && entry.retryAt.IsZero()) || now.Before(entry.retryAt))
	})
	if index < 0 {
		return accountEntry{}, false
	}
	return c.entries[index], true
}

func (c *Collector) commit(ticket collectionTicket, key [32]byte, outcome Outcome, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current(ticket) {
		return ErrUsageChanged
	}
	c.sweep(now)
	entry := c.store(key, func(entry accountEntry) accountEntry { return completedEntry(entry, outcome, now) })
	c.activate(entry)
	if entry.reason != "" {
		return errors.New("usage collection failed")
	}
	return nil
}

func (c *Collector) store(key [32]byte, update func(accountEntry) accountEntry) accountEntry {
	entry := accountEntry{key: key}
	if index := slices.IndexFunc(c.entries, func(existing accountEntry) bool { return existing.key == key }); index >= 0 {
		entry = c.entries[index]
		c.entries = slices.Delete(c.entries, index, index+1)
	}
	entry = update(entry)
	c.entries = append(c.entries, entry)
	if len(c.entries) > accountCacheLimit {
		clear(c.entries[:len(c.entries)-accountCacheLimit])
		c.entries = c.entries[len(c.entries)-accountCacheLimit:]
	}
	return entry
}

func (c *Collector) activate(entry accountEntry) {
	c.active, c.reason, c.refreshAt = entry.key, entry.reason, entry.retryAt
	if entry.report != nil && (c.refreshAt.IsZero() || entry.expiresAt.Before(c.refreshAt)) {
		c.refreshAt = entry.expiresAt
	}
}

func completedEntry(entry accountEntry, outcome Outcome, now time.Time) accountEntry {
	if outcome.Status == "ok" && outcome.Report != nil {
		entry.report = outcome.Report
		entry.expiresAt = now.Add(cacheLifetime)
		entry.retryAt = time.Time{}
		entry.reason = ""
		return entry
	}
	entry.retryAt = now.Add(failureBackoff)
	entry.reason = outcome.Reason
	if entry.reason == "" {
		entry.reason = outcome.Status
	}
	return entry
}

func (c *Collector) sweep(now time.Time) {
	entries := c.entries[:0]
	for _, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			entry.report = nil
		}
		if entry.report != nil || now.Before(entry.retryAt) {
			entries = append(entries, entry)
		}
	}
	clear(c.entries[len(entries):])
	c.entries = entries
}

func (c *Collector) Snapshot(now time.Time) Cached {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := Cached{RefreshAt: c.refreshAt, Reason: c.reason}
	index := slices.IndexFunc(c.entries, func(entry accountEntry) bool {
		return entry.key == c.active && entry.report != nil && now.Before(entry.expiresAt)
	})
	if index >= 0 {
		result.ExpiresAt = c.entries[index].expiresAt
		result.Windows = reportWindows(*c.entries[index].report, now)
	}
	return result
}
