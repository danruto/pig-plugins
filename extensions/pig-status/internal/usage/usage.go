// Package usage collects bounded account reports from resolved host authentication.
// OAuth snapshots must come from the host's fresh effective and provider auth,
// with JSON-shaped credential candidates and successful readiness acquisition.
// CompanionReady records that acquisition even when no companion is available.
// Callbacks run synchronously within the query budget and must honor cancellation.
// The caller owns session/model fencing and must reject changed host configuration.
// Only companion headers, never native OpenAI credentials, reach ChatGPT.
// Fireworks target discovery is separate from querying an explicitly selected slug.
// QueryTarget never substitutes accounts; the host owns selection and target fencing.
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

func (c Client) Query(ctx context.Context, providerID string, auth ResolvedAuth, timeout time.Duration) (Report, error) {
	return c.query(ctx, providerID, auth, "", timeout)
}

func (c Client) QueryTarget(ctx context.Context, providerID string, auth ResolvedAuth, targetID string, timeout time.Duration) (Report, error) {
	if providerID != "fireworks" {
		return Report{}, errors.New("usage provider does not support billing targets")
	}
	return c.query(ctx, providerID, auth, targetID, timeout)
}

func (c Client) query(ctx context.Context, providerID string, auth ResolvedAuth, targetID string, timeout time.Duration) (Report, error) {
	if providerID == "fireworks" && !fireworksAccountID.MatchString(targetID) {
		return Report{}, errors.New("Fireworks billing requires a safe selected account slug.")
	}
	endpoint, description := providerEndpoint(providerID)
	if endpoint == "" {
		return Report{}, errors.New("unsupported usage provider")
	}
	if !officialProviderOrigin(providerID, auth.ModelBaseURL) {
		return Report{}, errors.New("usage requires the official provider origin")
	}
	if !moonshotAuthAllowed(providerID, auth) {
		return Report{}, errors.New("Moonshot AI sibling credentials require region-specific provenance.")
	}
	var headers http.Header
	if providerID != "openai" && providerID != "github-copilot" && providerID != "xai" {
		var err error
		headers, err = validatedHeaders(providerID, auth.Headers)
		if err != nil {
			return Report{}, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, min(timeout, maxQueryDuration))
	defer cancel()
	select {
	case querySlots <- struct{}{}:
		defer func() { <-querySlots }()
	case <-ctx.Done():
		return Report{}, ctx.Err()
	}
	if providerID == "xai" {
		return c.queryXai(ctx, auth)
	}
	if providerID == "openai" || providerID == "github-copilot" {
		report, err := c.queryOAuth(ctx, providerID, auth)
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		return report, err
	}
	if err := auth.revalidate(ctx); err != nil {
		return Report{}, err
	}
	if providerID == "command-code" {
		return c.queryCommandCode(ctx, auth, headers)
	}
	if providerID == "minimax" || providerID == "minimax-cn" {
		key := auth.APIKey
		if match := bearerAuthorization.FindStringSubmatch(headers.Get("Authorization")); len(match) == 2 {
			key = match[1]
		}
		if key == "" {
			return Report{}, errors.New("MiniMax runtime API key was unavailable.")
		}
		endpoint = miniMaxEndpoint(providerID, miniMaxUsageKind(key))
	}
	if providerID == "zai" || providerID == "zai-coding-cn" {
		return c.queryZai(ctx, providerID, auth, headers)
	}
	if providerID == "baseten" {
		endpoint = basetenBillingURL(time.Now())
	}
	if providerID == "fireworks" {
		endpoint = fireworksBillingURL(targetID, time.Now())
	}
	payload, err := c.fetch(ctx, endpoint, description, headers)
	if err != nil {
		return Report{}, err
	}
	if err := auth.revalidate(ctx); err != nil {
		return Report{}, err
	}
	var report Report
	if providerID == "fireworks" {
		report, err = normalizeFireworks(payload, targetID, time.Now().UnixMilli())
	} else if providerID == "minimax" || providerID == "minimax-cn" {
		kind := "token-plan"
		if strings.HasSuffix(endpoint, "/account/query_balance") {
			kind = "account-balance"
		}
		report, err = normalizeMiniMax(providerID, kind, payload, time.Now().UnixMilli())
	} else {
		report, err = normalizeReport(providerID, payload, time.Now().UnixMilli())
	}
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return report, nil
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
		return errors.New("usage authentication revalidation failed")
	}
	return ctx.Err()
}

func providerEndpoint(providerID string) (string, string) {
	switch providerID {
	case "command-code":
		return "https://api.commandcode.ai/alpha/whoami", "Command Code account endpoint"
	case "opencode-go":
		return "https://opencode.ai/zen/go/v1/usage", "OpenCode Zen usage endpoint"
	case "kimi-coding":
		return "https://api.kimi.com/coding/v1/usages", "Kimi Coding usage endpoint"
	case "xai":
		return "https://api.x.ai", "xAI consumer usage endpoint"
	case "openai":
		return "https://api.openai.com", "OpenAI authentication status"
	case "openai-codex":
		return "https://chatgpt.com/backend-api/wham/usage", "Codex usage endpoint"
	case "github-copilot":
		return "https://api.github.com/copilot_internal/user", "GitHub Copilot usage endpoint"
	case "baseten":
		return "https://api.baseten.co/v1/billing/usage_summary", "Baseten billing usage endpoint"
	case "fireworks":
		return "https://api.fireworks.ai/v1/accounts", "Fireworks billing summary endpoint"
	case "openrouter":
		return "https://openrouter.ai/api/v1/key", "OpenRouter key endpoint"
	case "deepseek":
		return "https://api.deepseek.com/user/balance", "DeepSeek API balance endpoint"
	case "vercel-ai-gateway":
		return "https://ai-gateway.vercel.sh/v1/credits", "Vercel AI Gateway credits endpoint"
	case "minimax", "minimax-cn":
		return miniMaxEndpoint(providerID, "token-plan"), "MiniMax usage endpoint"
	case "zai":
		return "https://api.z.ai/api/monitor/usage/quota/limit", "Z.AI quota endpoint"
	case "zai-coding-cn":
		return "https://open.bigmodel.cn/api/monitor/usage/quota/limit", "Z.AI Coding CN quota endpoint"
	case "moonshotai":
		return "https://api.moonshot.ai/v1/users/me/balance", "Moonshot AI balance endpoint"
	case "moonshotai-cn":
		return "https://api.moonshot.cn/v1/users/me/balance", "Moonshot AI balance endpoint"
	default:
		return "", ""
	}
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
