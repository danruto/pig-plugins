// Package usage collects bounded account reports from resolved host authentication.
// OAuth snapshots must come from the host's fresh effective and provider auth,
// with JSON-shaped credential candidates and successful readiness acquisition.
// CompanionReady records that acquisition even when no companion is available.
// Callbacks run synchronously within the query budget and must honor cancellation.
// The caller owns session/model fencing and must reject changed host configuration.
// Only companion headers, never native OpenAI credentials, reach ChatGPT.
// Fireworks target discovery is separate from querying an explicitly selected slug.
// QueryFireworks never substitutes accounts; the host owns selection and target fencing.
// Regional snapshots with no SelectedProviderID describe the queried/current provider.
// Sibling Moonshot queries require Source and SourceLabel from host auth status;
// the host must revalidate their provenance along with the credential and origin.
// xAI resolves fresh matching OAuth snapshots before consumer identity and billing
// requests. Its canonical user ID is sent only to billing, never inferred locally.
// Schedulable excludes menu-only xAI. Collector owns a bounded current-account cache,
// complete host/model/target/settings fences, expiry and reset projections. Its
// refresh calls are awaited; the adapter independently owns and joins scheduling.
// Query remains the explicit menu query seam.
package usage

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Report struct {
	ProviderID   string    `json:"providerId"`
	ProviderName string    `json:"providerName"`
	CapturedAt   int64     `json:"capturedAt"`
	Source       string    `json:"source"`
	Semantics    Semantics `json:"semantics"`
	Buckets      []Bucket  `json:"buckets"`
	Metrics      []Metric  `json:"metrics"`
	AccountLabel string    `json:"accountLabel,omitempty"`
	Notes        []string  `json:"notes,omitempty"`
}

type Semantics struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type Bucket struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Used          *float64 `json:"used,omitempty"`
	Remaining     *float64 `json:"remaining,omitempty"`
	Limit         *float64 `json:"limit,omitempty"`
	Unit          string   `json:"unit"`
	GroupID       string   `json:"groupId,omitempty"`
	GroupLabel    string   `json:"groupLabel,omitempty"`
	ModelKeys     []string `json:"modelKeys,omitempty"`
	Period        string   `json:"period,omitempty"`
	WindowMinutes *float64 `json:"windowMinutes,omitempty"`
	ResetsAt      *float64 `json:"resetsAt,omitempty"`
}

type Metric struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Value    any    `json:"value"`
	Unit     string `json:"unit,omitempty"`
	Currency string `json:"currency,omitempty"`
}

type ResolvedAuth struct {
	APIKey             string
	SelectedProviderID string
	Source             string
	SourceLabel        *string
	ModelBaseURL       string
	Headers            http.Header
	Revalidate         func(context.Context) error
	ResolveOAuth       func(context.Context) (OAuthSnapshot, error)
	UseCodexUsage      bool
}

const maxQueryDuration = 15 * time.Second

var querySlots = make(chan struct{}, 2)

type Client struct {
	Transport http.RoundTripper
}

func (c Client) Query(ctx context.Context, id string, auth ResolvedAuth, timeout time.Duration) (Report, error) {
	return c.query(ctx, providerID(id), auth, "", timeout)
}

func (c Client) QueryFireworks(ctx context.Context, auth ResolvedAuth, targetID string, timeout time.Duration) (Report, error) {
	return c.query(ctx, providerFireworks, auth, targetID, timeout)
}

func (c Client) query(ctx context.Context, id providerID, auth ResolvedAuth, targetID string, timeout time.Duration) (Report, error) {
	p, ok := lookupProvider(id)
	if !ok {
		return Report{}, ErrUsageUnsupported
	}
	if !p.allowsOrigin(auth.ModelBaseURL) {
		return Report{}, ErrUsageOrigin
	}
	if !moonshotAuthAllowed(id, auth) {
		return Report{}, errors.New("moonshot AI sibling credentials require region-specific provenance")
	}
	if id == providerFireworks && !fireworksAccountID.MatchString(targetID) {
		return Report{}, errors.New("fireworks billing requires a safe selected account slug")
	}
	headers, err := p.requestHeaders(auth)
	if err != nil {
		return Report{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, min(timeout, maxQueryDuration))
	defer cancel()
	if err := acquireQuerySlot(ctx); err != nil {
		return Report{}, err
	}
	defer releaseQuerySlot()
	if p.query != nil {
		report, err := p.query(c, ctx, p, auth, headers)
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		return report, err
	}
	return c.fetchShared(ctx, p, auth, sharedRequest{headers: headers, targetID: targetID})
}

type sharedRequest struct {
	headers  http.Header
	targetID string
}

func (c Client) fetchShared(ctx context.Context, p provider, auth ResolvedAuth, req sharedRequest) (Report, error) {
	if err := auth.revalidate(ctx); err != nil {
		return Report{}, err
	}
	endpoint, err := p.endpointFor(auth, req.headers, req.targetID)
	if err != nil {
		return Report{}, err
	}
	payload, err := c.fetch(ctx, endpoint, p.description, req.headers)
	if err != nil {
		return Report{}, err
	}
	if err := auth.revalidate(ctx); err != nil {
		return Report{}, err
	}
	report, err := p.normalize(p.id, endpoint, req.targetID, payload, time.Now().UnixMilli())
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func acquireQuerySlot(ctx context.Context) error {
	select {
	case querySlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseQuerySlot() {
	<-querySlots
}

func (a ResolvedAuth) revalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.Revalidate == nil {
		return errors.New("usage requires request-boundary revalidation")
	}
	if err := a.Revalidate(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrUsageChanged) {
			return ErrUsageChanged
		}
		return errors.New("usage authentication revalidation failed")
	}
	return ctx.Err()
}

func sameHTTPSOrigin(base, endpoint string) bool {
	model, err := url.Parse(base)
	if err != nil {
		return false
	}
	target, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return model.Scheme == "https" && model.User == nil &&
		(model.Port() == "" || model.Port() == "443") &&
		strings.EqualFold(model.Hostname(), target.Hostname())
}
