package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	codexFastCacheTTL          = 5 * time.Minute
	codexFastFailureBackoff    = 30 * time.Second
	codexFastMaxAccounts       = 32
	codexFastMaxDirectoryBytes = 8 << 20
	codexFastDirectoryEndpoint = "https://chatgpt.com/backend-api/codex/models"
	codexFastDirectoryLabel    = "Codex model directory"
	codexFastUnfetchedReason   = "Codex Fast capability information has not been fetched for this account."
	codexFastUnverifiedReason  = "Codex Fast capability is unknown: the active Codex account could not be verified."
	codexFastFailurePrefix     = "Codex Fast capability is unknown: "
	codexFastModelsReason      = "The Codex model directory returned invalid models."
	codexFastModelIDReason     = "The Codex model directory returned an invalid or duplicate model ID."
	codexFastTiersReason       = "The Codex model directory returned invalid service tiers."
	codexFastAccountSalt       = "codex-fast-account\x00"
	codexFastAuthSalt          = "codex-fast-auth\x00"
)

var (
	errCodexFastModels  = errors.New("codex model directory returned invalid models")
	errCodexFastModelID = errors.New("codex model directory returned an invalid or duplicate model ID")
	errCodexFastTiers   = errors.New("codex model directory returned invalid service tiers")
)

type codexFastEntry struct {
	models      map[string]*bool
	fetchedAt   time.Time
	fingerprint string
	retryAt     time.Time
	issue       string
}

type codexFastRequest struct {
	cancel context.CancelFunc
	done   chan struct{}
	ok     bool
}

type codexFastFetch struct {
	key         string
	fingerprint string
	generation  int
	sequence    int
	model       CodexModel
	status      int
}

type catalogRead struct {
	refresh bool
	force   bool
}

type codexFastCatalog struct {
	mu            sync.Mutex
	client        Client
	current       func(context.Context) (FastScope, error)
	identify      func(context.Context) (CodexAccount, error)
	now           func() time.Time
	timeout       time.Duration
	clientVersion string
	accounts      map[string]*codexFastEntry
	order         []string
	requests      map[string]*codexFastRequest
	latest        map[string]int
	sequence      int
	generation    int
	active        string
	issue         string
}

func newCodexFastCatalog(options CodexFastOptions, timeout time.Duration, now func() time.Time) *codexFastCatalog {
	return &codexFastCatalog{
		client:        options.Client,
		current:       options.Current,
		identify:      options.Identify,
		now:           now,
		timeout:       timeout,
		clientVersion: options.ClientVersion,
		accounts:      map[string]*codexFastEntry{},
		requests:      map[string]*codexFastRequest{},
		latest:        map[string]int{},
		issue:         UnknownFastCapability.Reason,
	}
}

func (c *codexFastCatalog) read(ctx context.Context, scope FastScope, request catalogRead) (FastCapability, bool) {
	fetch := codexFastFetch{generation: c.generationNow(), model: scope.Model}
	account, err := c.identifyAccount(ctx)
	if err != nil {
		c.recordIdentificationFailure(fetch.generation)
		return FastCapability{}, false
	}
	fetch.key = codexFastAccountKey(account.AccountID)
	fetch.fingerprint = codexFastAuthKey(account)
	if !c.activate(fetch) || !c.settled(ctx, scope, fetch.generation) {
		return FastCapability{}, false
	}
	if request.refresh && !c.load(ctx, account, fetch, request) {
		return FastCapability{}, false
	}
	if !c.settled(ctx, scope, fetch.generation) {
		return FastCapability{}, false
	}
	return c.get(&scope.Model), true
}

func (c *codexFastCatalog) generationNow() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

func (c *codexFastCatalog) activate(fetch codexFastFetch) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != fetch.generation {
		return false
	}
	c.active = fetch.key
	c.issue = codexFastUnfetchedReason
	return true
}

func (c *codexFastCatalog) recordIdentificationFailure(generation int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return
	}
	c.active = ""
	c.issue = codexFastUnverifiedReason
}

func (c *codexFastCatalog) settled(ctx context.Context, scope FastScope, generation int) bool {
	c.mu.Lock()
	unchanged := c.generation == generation
	c.mu.Unlock()
	if !unchanged || c.current == nil {
		return false
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	fresh, err := c.current(callCtx)
	if err != nil {
		return false
	}
	return fresh.SessionID == scope.SessionID &&
		fresh.Model.Provider == scope.Model.Provider &&
		fresh.Model.ID == scope.Model.ID
}

func (c *codexFastCatalog) load(ctx context.Context, account CodexAccount, fetch codexFastFetch, request catalogRead) bool {
	c.mu.Lock()
	if !request.force && c.skipLocked(fetch) {
		c.mu.Unlock()
		return true
	}
	if shared := c.requests[fetch.fingerprint]; shared != nil {
		c.mu.Unlock()
		select {
		case <-shared.done:
			return shared.ok
		case <-ctx.Done():
			return false
		}
	}
	c.sequence++
	fetch.sequence = c.sequence
	c.latest[fetch.key] = fetch.sequence
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	shared := &codexFastRequest{cancel: cancel, done: make(chan struct{})}
	c.requests[fetch.fingerprint] = shared
	c.mu.Unlock()

	ok := c.fetch(requestCtx, account, fetch)
	cancel()
	c.mu.Lock()
	shared.ok = ok
	if c.requests[fetch.fingerprint] == shared {
		delete(c.requests, fetch.fingerprint)
	}
	if c.latest[fetch.key] == fetch.sequence {
		delete(c.latest, fetch.key)
	}
	close(shared.done)
	c.mu.Unlock()
	return ok
}

func (c *codexFastCatalog) fetch(ctx context.Context, account CodexAccount, fetch codexFastFetch) bool {
	if !c.revalidated(ctx, account) {
		return false
	}
	payload, status, err := c.client.fetchBounded(ctx, usageRequest{
		endpoint:    codexFastDirectoryURL(c.clientVersion),
		description: codexFastDirectoryLabel,
		headers:     directoryHeaders(account),
		maxBytes:    codexFastMaxDirectoryBytes,
	})
	fetch.status = status
	var models map[string]*bool
	if err == nil {
		models, err = normalizeDirectory(payload)
	}
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		c.recordFailure(fetch, err)
		return true
	}
	if !c.revalidated(ctx, account) {
		return false
	}
	c.record(fetch, models)
	return true
}

func (c *codexFastCatalog) revalidated(ctx context.Context, account CodexAccount) bool {
	if account.Revalidate == nil {
		return true
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return account.Revalidate(callCtx) == nil
}

func (c *codexFastCatalog) record(fetch codexFastFetch, models map[string]*bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.liveLocked(fetch) {
		return
	}
	c.storeLocked(fetch.key, &codexFastEntry{
		models:      models,
		fetchedAt:   c.now(),
		fingerprint: fetch.fingerprint,
	})
}

func (c *codexFastCatalog) recordFailure(fetch codexFastFetch, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.liveLocked(fetch) {
		return
	}
	entry := &codexFastEntry{retryAt: c.now().Add(codexFastFailureBackoff), issue: codexFastFailurePrefix + codexFastFailureReason(err)}
	transient := fetch.status == 0 || fetch.status == 429 || fetch.status >= 500
	if previous := c.accounts[fetch.key]; transient && previous != nil && previous.models != nil {
		entry.models = previous.models
		entry.fetchedAt = previous.fetchedAt
		entry.fingerprint = previous.fingerprint
	}
	c.storeLocked(fetch.key, entry)
}

func codexFastFailureReason(err error) string {
	switch {
	case errors.Is(err, errCodexFastModels):
		return codexFastModelsReason
	case errors.Is(err, errCodexFastModelID):
		return codexFastModelIDReason
	case errors.Is(err, errCodexFastTiers):
		return codexFastTiersReason
	default:
		return sanitize(displayText(err))
	}
}

func displayText(err error) string {
	var shown interface{ Display() string }
	if errors.As(err, &shown) {
		return shown.Display()
	}
	return err.Error()
}

func (c *codexFastCatalog) liveLocked(fetch codexFastFetch) bool {
	return c.generation == fetch.generation && c.latest[fetch.key] == fetch.sequence
}

func (c *codexFastCatalog) skipLocked(fetch codexFastFetch) bool {
	if c.retryLocked(fetch.key) {
		return true
	}
	if c.capabilityLocked(&fetch.model).Kind == FastUnknown {
		return false
	}
	entry := c.accounts[fetch.key]
	return entry != nil && entry.models != nil && entry.fingerprint == fetch.fingerprint &&
		c.now().Sub(entry.fetchedAt) < codexFastCacheTTL
}

func (c *codexFastCatalog) retryLocked(key string) bool {
	entry := c.accounts[key]
	return entry != nil && !entry.retryAt.IsZero() && c.now().Before(entry.retryAt)
}

func (c *codexFastCatalog) storeLocked(key string, entry *codexFastEntry) {
	if _, seen := c.accounts[key]; !seen {
		if len(c.order) >= codexFastMaxAccounts {
			delete(c.accounts, c.order[0])
			c.order = c.order[1:]
		}
		c.order = append(c.order, key)
	}
	c.accounts[key] = entry
}

func (c *codexFastCatalog) get(model *CodexModel) FastCapability {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capabilityLocked(model)
}

func (c *codexFastCatalog) capabilityLocked(model *CodexModel) FastCapability {
	entry := c.accounts[c.active]
	if entry != nil && model != nil {
		if supported, listed := entry.models[model.ID]; listed && supported != nil {
			if *supported {
				return FastCapability{Kind: FastSupported}
			}
			return FastCapability{Kind: FastUnsupported}
		}
	}
	return FastCapability{Kind: FastUnknown, Reason: c.reasonLocked(entry, model)}
}

func (c *codexFastCatalog) reasonLocked(entry *codexFastEntry, model *CodexModel) string {
	if entry != nil && entry.issue != "" {
		return entry.issue
	}
	if entry != nil && entry.models != nil && model != nil {
		return "The Codex model directory has no Fast capability information for " + sanitize(model.ID) + "."
	}
	return c.issue
}

func (c *codexFastCatalog) identifyAccount(ctx context.Context) (CodexAccount, error) {
	if c.identify == nil {
		return CodexAccount{}, errors.New("codex fast requires an account source")
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	account, err := c.identify(callCtx)
	if err != nil {
		return CodexAccount{}, err
	}
	if account.AccountID == "" {
		return CodexAccount{}, errors.New("active codex account ID is unavailable")
	}
	return account, nil
}

func (c *codexFastCatalog) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
}

func (c *codexFastCatalog) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked()
	c.accounts = map[string]*codexFastEntry{}
	c.order = nil
}

func (c *codexFastCatalog) resetLocked() {
	c.generation++
	c.active = ""
	c.issue = UnknownFastCapability.Reason
	for _, request := range c.requests {
		request.cancel()
	}
	c.requests = map[string]*codexFastRequest{}
	c.latest = map[string]int{}
}

func codexFastDirectoryURL(clientVersion string) string {
	return codexFastDirectoryEndpoint + "?client_version=" + url.QueryEscape(clientVersion)
}

func directoryHeaders(account CodexAccount) http.Header {
	headers := make(http.Header, 2)
	headers["Authorization"] = []string{headerValue(account.Headers, "Authorization")}
	headers["ChatGPT-Account-Id"] = []string{account.AccountID}
	return headers
}

func headerValue(headers http.Header, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) == 1 {
			return values[0]
		}
	}
	return ""
}

func codexFastAccountKey(accountID string) string {
	return fingerprintText(codexFastAccountSalt + accountID)
}

func codexFastAuthKey(account CodexAccount) string {
	return fingerprintText(codexFastAuthSalt + headerValue(account.Headers, "Authorization") + "\x00" + account.AccountID)
}

func fingerprintText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func normalizeDirectory(payload map[string]any) (map[string]*bool, error) {
	items, ok := payload["models"].([]any)
	if !ok {
		return nil, errCodexFastModels
	}
	models := make(map[string]*bool, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, errCodexFastModelID
		}
		slug, ok := object["slug"].(string)
		if !ok || strings.TrimSpace(slug) == "" {
			return nil, errCodexFastModelID
		}
		if _, seen := models[slug]; seen {
			return nil, errCodexFastModelID
		}
		supported, listed, err := directoryTiers(object)
		if err != nil {
			return nil, err
		}
		if !listed {
			models[slug] = nil
			continue
		}
		value := supported
		models[slug] = &value
	}
	return models, nil
}

func directoryTiers(model map[string]any) (bool, bool, error) {
	tiers, hasTiers := model["service_tiers"]
	legacy, hasLegacy := model["additional_speed_tiers"]
	if !hasTiers && !hasLegacy {
		return false, false, nil
	}
	supported := false
	if hasTiers {
		value, err := priorityTier(tiers)
		if err != nil {
			return false, false, err
		}
		supported = value
	}
	if hasLegacy {
		value, err := fastSpeedTier(legacy)
		if err != nil {
			return false, false, err
		}
		supported = supported || value
	}
	return supported, true, nil
}

func priorityTier(value any) (bool, error) {
	entries, ok := value.([]any)
	if !ok {
		return false, errCodexFastTiers
	}
	supported := false
	for _, entry := range entries {
		id, ok := serviceTierID(entry)
		if !ok {
			return false, errCodexFastTiers
		}
		if id == CodexFastServiceTier {
			supported = true
		}
	}
	return supported, nil
}

func fastSpeedTier(value any) (bool, error) {
	entries, ok := value.([]any)
	if !ok {
		return false, errCodexFastTiers
	}
	supported := false
	for _, entry := range entries {
		speed, ok := entry.(string)
		if !ok {
			return false, errCodexFastTiers
		}
		if speed == "fast" {
			supported = true
		}
	}
	return supported, nil
}

func serviceTierID(entry any) (string, bool) {
	object, ok := entry.(map[string]any)
	if !ok {
		return "", false
	}
	id, ok := object["id"].(string)
	return id, ok
}
