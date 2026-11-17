package usage

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	basetenUsageEndpoint = "https://api.baseten.co/v1/billing/usage_summary"
	basetenModelOrigin   = "https://inference.baseten.co"
)

type providerID string

const (
	providerBaseten         providerID = "baseten"
	providerOpenAI          providerID = "openai"
	providerOpenAICodex     providerID = "openai-codex"
	providerDeepSeek        providerID = "deepseek"
	providerGitHubCopilot   providerID = "github-copilot"
	providerOpenRouter      providerID = "openrouter"
	providerVercelAIGateway providerID = "vercel-ai-gateway"
	providerCommandCode     providerID = "command-code"
	providerFireworks       providerID = "fireworks"
	providerOpenCodeGo      providerID = "opencode-go"
	providerKimiCoding      providerID = "kimi-coding"
	providerMiniMax         providerID = "minimax"
	providerMiniMaxCN       providerID = "minimax-cn"
	providerMoonshotAI      providerID = "moonshotai"
	providerMoonshotAICN    providerID = "moonshotai-cn"
	providerZai             providerID = "zai"
	providerZaiCodingCN     providerID = "zai-coding-cn"
	providerXai             providerID = "xai"
)

type provider struct {
	id          providerID
	endpoint    string
	description string
	origin      func(base string) bool
	skipHeaders bool
	scheduled   bool
	identity    func(OAuthSnapshot, ResolvedAuth) (subscriptionIdentity, error)
	query       func(Client, context.Context, provider, ResolvedAuth, http.Header) (Report, error)
	url         func(ResolvedAuth, http.Header, string, time.Time) (string, error)
	normalize   func(id providerID, endpoint, targetID string, payload map[string]any, capturedAt int64) (Report, error)
}

//nolint:gochecknoglobals // the index is filled once from the read-only table
var providerIndex struct {
	once sync.Once
	byID map[providerID]provider
}

func providerTable() []provider {
	return []provider{
		{
			id: providerBaseten, endpoint: basetenUsageEndpoint, description: "Baseten billing usage endpoint", scheduled: true,
			origin: func(base string) bool {
				return sameHTTPSOrigin(base, basetenModelOrigin) || sameHTTPSOrigin(base, basetenUsageEndpoint)
			},
			url:       basetenURL,
			normalize: payloadNormalizer(normalizeBaseten),
		},
		{
			id: providerOpenAI, endpoint: "https://api.openai.com", description: "OpenAI authentication status", skipHeaders: true, scheduled: true,
			identity:  openAIIdentity,
			query:     Client.queryOAuth,
			normalize: payloadNormalizer(normalizeOpenAI),
		},
		{
			id: providerOpenAICodex, endpoint: "https://chatgpt.com/backend-api/wham/usage", description: "Codex usage endpoint", scheduled: true,
			identity:  openAIIdentity,
			normalize: payloadNormalizer(normalizeCodex),
		},
		{
			id: providerDeepSeek, endpoint: "https://api.deepseek.com/user/balance", description: "DeepSeek API balance endpoint", scheduled: true,
			normalize: payloadNormalizer(normalizeDeepSeek),
		},
		{
			id: providerGitHubCopilot, endpoint: "https://api.github.com/copilot_internal/user", description: "GitHub Copilot usage endpoint", skipHeaders: true, scheduled: true,
			origin: func(base string) bool {
				u, err := url.Parse(base)
				return err == nil && u.Scheme == "https" && u.User == nil && (u.Port() == "" || u.Port() == "443") && copilotHost.MatchString(strings.ToLower(u.Hostname()))
			},
			identity: func(snapshot OAuthSnapshot, _ ResolvedAuth) (subscriptionIdentity, error) {
				return resolveCopilot(snapshot)
			},
			query:     Client.queryOAuth,
			normalize: payloadNormalizer(normalizeCopilot),
		},
		{
			id: providerOpenRouter, endpoint: "https://openrouter.ai/api/v1/key", description: "OpenRouter key endpoint", scheduled: true,
			normalize: payloadNormalizer(normalizeOpenRouter),
		},
		{
			id: providerVercelAIGateway, endpoint: "https://ai-gateway.vercel.sh/v1/credits", description: "Vercel AI Gateway credits endpoint", scheduled: true,
			normalize: payloadNormalizer(normalizeVercel),
		},
		{
			id: providerCommandCode, endpoint: "https://api.commandcode.ai/alpha/whoami", description: "Command Code account endpoint", scheduled: true,
			query: func(c Client, ctx context.Context, p provider, auth ResolvedAuth, headers http.Header) (Report, error) {
				if err := auth.revalidate(ctx); err != nil {
					return Report{}, err
				}
				return c.queryCommandCode(ctx, p, auth, headers)
			},
			normalize: payloadNormalizer(normalizeCommandCode),
		},
		{
			id: providerFireworks, endpoint: "https://api.fireworks.ai/v1/accounts", description: "Fireworks billing summary endpoint", scheduled: true,
			url: fireworksURL,
			normalize: func(_ providerID, _ string, targetID string, payload map[string]any, capturedAt int64) (Report, error) {
				return normalizeFireworks(payload, targetID, capturedAt)
			},
		},
		{
			id: providerOpenCodeGo, endpoint: "https://opencode.ai/zen/go/v1/usage", description: "OpenCode Zen usage endpoint", scheduled: true,
			normalize: payloadNormalizer(normalizeOpenCode),
		},
		{
			id: providerKimiCoding, endpoint: "https://api.kimi.com/coding/v1/usages", description: "Kimi Coding usage endpoint", scheduled: true,
			normalize: payloadNormalizer(normalizeKimi),
		},
		{
			id: providerMiniMax, endpoint: miniMaxEndpoint(providerMiniMax, "token-plan"), description: "MiniMax usage endpoint", scheduled: true,
			url: func(auth ResolvedAuth, headers http.Header, _ string, _ time.Time) (string, error) {
				return miniMaxUsageURL(providerMiniMax, auth, headers)
			},
			normalize: func(id providerID, endpoint, _ string, payload map[string]any, capturedAt int64) (Report, error) {
				return normalizeMiniMax(id, miniMaxKind(endpoint), payload, capturedAt)
			},
		},
		{
			id: providerMiniMaxCN, endpoint: miniMaxEndpoint(providerMiniMaxCN, "token-plan"), description: "MiniMax usage endpoint", scheduled: true,
			url: func(auth ResolvedAuth, headers http.Header, _ string, _ time.Time) (string, error) {
				return miniMaxUsageURL(providerMiniMaxCN, auth, headers)
			},
			normalize: func(id providerID, endpoint, _ string, payload map[string]any, capturedAt int64) (Report, error) {
				return normalizeMiniMax(id, miniMaxKind(endpoint), payload, capturedAt)
			},
		},
		{
			id: providerMoonshotAI, endpoint: "https://api.moonshot.ai/v1/users/me/balance", description: "Moonshot AI balance endpoint", scheduled: true,
			normalize: func(_ providerID, _ string, _ string, payload map[string]any, capturedAt int64) (Report, error) {
				return normalizeMoonshot(providerMoonshotAI, payload, capturedAt)
			},
		},
		{
			id: providerMoonshotAICN, endpoint: "https://api.moonshot.cn/v1/users/me/balance", description: "Moonshot AI balance endpoint", scheduled: true,
			normalize: func(_ providerID, _ string, _ string, payload map[string]any, capturedAt int64) (Report, error) {
				return normalizeMoonshot(providerMoonshotAICN, payload, capturedAt)
			},
		},
		{
			id: providerZai, endpoint: "https://api.z.ai/api/monitor/usage/quota/limit", description: "Z.AI quota endpoint", scheduled: true,
			query: func(c Client, ctx context.Context, p provider, auth ResolvedAuth, headers http.Header) (Report, error) {
				if err := auth.revalidate(ctx); err != nil {
					return Report{}, err
				}
				return c.queryZai(ctx, p, auth, headers)
			},
		},
		{
			id: providerZaiCodingCN, endpoint: "https://open.bigmodel.cn/api/monitor/usage/quota/limit", description: "Z.AI Coding CN quota endpoint", scheduled: true,
			query: func(c Client, ctx context.Context, p provider, auth ResolvedAuth, headers http.Header) (Report, error) {
				if err := auth.revalidate(ctx); err != nil {
					return Report{}, err
				}
				return c.queryZai(ctx, p, auth, headers)
			},
		},
		{
			id: providerXai, endpoint: "https://api.x.ai", description: "xAI consumer usage endpoint", skipHeaders: true,
			identity: func(snapshot OAuthSnapshot, _ ResolvedAuth) (subscriptionIdentity, error) {
				return resolveXai(snapshot)
			},
			query: Client.queryXai,
		},
	}
}

func lookupProvider(id providerID) (provider, bool) {
	providerIndex.once.Do(func() {
		table := providerTable()
		byID := make(map[providerID]provider, len(table))
		for _, p := range table {
			byID[p.id] = p
		}
		providerIndex.byID = byID
	})
	p, ok := providerIndex.byID[id]
	return p, ok
}

func providerIDs() []string {
	table := providerTable()
	ids := make([]string, len(table))
	for i, p := range table {
		ids[i] = string(p.id)
	}
	return ids
}

func payloadNormalizer(normalize func(map[string]any, int64) (Report, error)) func(providerID, string, string, map[string]any, int64) (Report, error) {
	return func(_ providerID, _, _ string, payload map[string]any, capturedAt int64) (Report, error) {
		return normalize(payload, capturedAt)
	}
}

func (p provider) allowsOrigin(base string) bool {
	if p.origin != nil {
		return p.origin(base)
	}
	return sameHTTPSOrigin(base, p.endpoint)
}

func (p provider) requestHeaders(auth ResolvedAuth) (http.Header, error) {
	if p.skipHeaders {
		return nil, nil
	}
	return validatedHeaders(p.id, auth.Headers)
}

func (p provider) endpointFor(auth ResolvedAuth, headers http.Header, targetID string) (string, error) {
	if p.url == nil {
		return p.endpoint, nil
	}
	return p.url(auth, headers, targetID, time.Now())
}
