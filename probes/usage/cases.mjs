/** Synthetic inputs only. Expected observations are captured separately from the pinned backend. */
const now = 1_900_000_000_000;
const iso = new Date(now + 3_600_000).toISOString();
const window = { used_percent: 23, limit_window_seconds: 18_000, reset_at: now / 1000 + 3600 };
const minimax = {
  base_resp: { status_code: 0 },
  model_remains: [{
    model_name: "Synthetic model", start_time: now - 14_400_000, end_time: now + 3_600_000,
    weekly_start_time: now - 601_200_000, weekly_end_time: now + 3_600_000,
    current_interval_total_count: 100, current_interval_usage_count: 77, current_interval_remaining_percent: 77,
    current_weekly_total_count: 1000, current_weekly_usage_count: 900, current_weekly_remaining_percent: 90,
  }],
};
const balance = {
  base_resp: { status_code: 0 }, available_amount: "98.00001", cash_balance: "-1.25",
  voucher_balance: "2.50", credit_balance: "100.00", owed_amount: "1.25",
};
const quota = { success: true, data: { level: "Synthetic plan", limits: [
  { type: "CREDIT_LIMIT", unit: 3, number: 5, percentage: 23, nextResetTime: now + 3_600_000 },
  { type: "TOKENS_LIMIT", unit: 6, currentValue: 12, usage: 100, nextResetTime: now + 3_600_000 },
  { type: "TIME_LIMIT", currentValue: 3, usage: 100, usageDetails: [{ modelCode: "synthetic-tool", usage: 3 }] },
] } };
const base = [
  ["baseten", "https://inference.baseten.co/v1", { usage_summary: { model_apis_usage: { total: "20.000001", credits_used: "2.25", subtotal: "17.750001" } } }],
  ["openai", "https://api.openai.com/v1", {}],
  ["openai-codex", "https://chatgpt.com/backend-api/codex", { usage: { rate_limit: { primary_window: window, secondary_window: { ...window, limit_window_seconds: 604800 } }, credits: { has_credits: true, balance: "12.5" }, rate_limit_reset_credits: { available_count: 2 }, additional_rate_limits: [{ metered_feature: "spark", limit_name: "Spark", rate_limit: { primary_window: window } }], plan_type: "synthetic" } }],
  ["deepseek", "https://api.deepseek.com/v1", { balance: { is_available: true, balance_infos: [
    { currency: "USD", total_balance: "1.000000001", granted_balance: "0.000000001", topped_up_balance: "1.000000000" },
    { currency: "CNY", total_balance: "110.00", granted_balance: "10.00", topped_up_balance: "100.00" },
  ] } }],
  ["github-copilot", "https://api.individual.githubcopilot.com", { user: { quota_snapshots: { premium_interactions: { token_based_billing: true, entitlement: 1500, remaining: 1200 } }, quota_reset_date_utc: iso, copilot_plan: "synthetic" } }],
  ["openrouter", "https://openrouter.ai/api/v1", { key: { data: { limit: 100, limit_remaining: 74.5, usage: 25.5, usage_daily: 1.25, usage_weekly: 5, usage_monthly: 20, limit_reset: "monthly", label: "synthetic" } } }],
  ["vercel-ai-gateway", "https://ai-gateway.vercel.sh/v1", { credits: { balance: "95.500000001", total_used: "4.499999999" } }],
  ["command-code", "https://api.commandcode.ai/v1", {
    whoami: { user: { userName: "synthetic" }, org: { id: "fixture-org" } },
    credits: { credits: { monthlyCredits: 75, purchasedCredits: 2, freeCredits: 3 }, windowLimits: { fiveHour: { used: 1, cap: 10, resetAt: now + 3_600_000 }, weekly: { used: 2, cap: 100, resetAt: now + 3_600_000 } } },
    subscriptions: { data: { currentPeriodStart: new Date(now - 86400_000).toISOString(), currentPeriodEnd: iso, planId: "individual-pro", status: "active" } },
    summary: { totalCost: 20, totalCount: 10, totalTokensIn: 100, totalTokensOut: 20 },
  }],
  ["fireworks", "https://api.fireworks.ai/inference/v1", { accounts: { accounts: [{ name: "accounts/fixture-account" }] }, summary: { lineItems: [
    { series: "SERVERLESS", totalCost: { currencyCode: "USD", units: "12", nanos: 345678901 } },
    { series: "TRAINING", totalCost: { currencyCode: "USD", nanos: 1 } },
    { totalCost: { currencyCode: "CNY", units: "3" } },
  ] } }],
  ["opencode-go", "https://opencode.ai/zen/go/v1", { usage: { usage: { rolling: { status: "ok", percent: 23, resetsAt: iso }, weekly: { status: "rate-limited", percent: 100 }, monthly: { status: "unknown", percent: 0 } } } }],
  ["kimi-coding", "https://api.kimi.com/coding", { usages: {
    usage: { limit: "100", remaining: "90", resetTime: iso }, limits: [{ window: { duration: 5, timeUnit: "TIME_UNIT_HOUR" }, detail: { limit: "10" } }],
    usages: { limit_month_total: { used_ratio: 0.25, reset_time: iso } },
    boosterWallet: { balance: { type: "BOOSTER", amount: 1000000000, amountLeft: 500000000 }, monthlyUsed: { priceInCents: 100, currency: "USD" }, monthlyChargeLimit: { priceInCents: 0, currency: "USD" }, monthlyChargeLimitEnabled: true },
  } }],
  ["minimax", "https://api.minimax.io/anthropic", { remains: minimax }],
  ["minimax-cn", "https://api.minimaxi.com/anthropic", { remains: minimax }],
  ["moonshotai", "https://api.moonshot.ai/v1", { balance: { code: 0, status: true, data: { available_balance: 49.58894, voucher_balance: 50, cash_balance: -0.41106 } } }],
  ["moonshotai-cn", "https://api.moonshot.cn/v1", { balance: { code: 0, status: true, data: { available_balance: 49.58894, voucher_balance: 50, cash_balance: -0.41106 } } }],
  ["zai", "https://api.z.ai/api/coding/paas/v4", { limit: quota, list: { success: true, data: [{ productName: "Synthetic plan", status: "VALID", inCurrentPeriod: true, nextRenewTime: iso }] } }],
  ["zai-coding-cn", "https://open.bigmodel.cn/api/coding/paas/v4", { limit: quota, list: { success: true, data: [{ productName: "Synthetic plan", status: "VALID", inCurrentPeriod: true, nextRenewTime: iso }] } }],
  ["xai", "https://api.x.ai/v1", { user: { userId: "fixture-user", subscriptionTier: "synthetic" }, billing: { config: { creditUsagePercent: 23, currentPeriod: { type: "USAGE_PERIOD_TYPE_WEEKLY", start: new Date(now - 604800_000).toISOString(), end: iso }, onDemandCap: { val: 1000 }, onDemandUsed: { val: 125 }, prepaidBalance: { val: 999 } } } }],
];
const cases = base.map(([provider, baseUrl, routes]) => ({ id: `${provider}:normal`, provider, baseUrl, routes }));
function variant(provider, name, change) {
  const item = structuredClone(cases.find((item) => item.id === `${provider}:normal`));
  item.id = `${provider}:${name}`;
  change(item);
  cases.push(item);
}
for (const [provider] of base) {
  if (provider === "openai") continue;
  variant(provider, "malformed", (item) => { item.response = "malformed"; });
  variant(provider, "http-error", (item) => { item.response = "http-error"; });
  variant(provider, "redirect", (item) => { item.response = "redirect"; });
  variant(provider, "guard-reject", (item) => { item.rejectGuard = 1; });
}
variant("openai", "companion", (item) => {
  item.companion = true;
  item.routes = { apps: { items: [{ id: "fixture-client", allowed_usage_percent: 50, windows: [window] }] }, usage: { rate_limit: { primary_window: window } } };
});
variant("openai", "companion-pagination", (item) => { item.companion = true; item.routes = { apps: { items: [], has_more: true } }; });
variant("openai", "missing-companion", (item) => { item.companionEnabled = true; });
variant("openai", "guard-reject", (item) => { item.rejectGuard = 1; });
variant("baseten", "empty", (item) => { item.routes.usage_summary = {}; });
variant("openai-codex", "optional-malformed", (item) => { item.routes.usage.additional_rate_limits.push({ metered_feature: "bad", rate_limit: { primary_window: "bad" } }); });
variant("github-copilot", "legacy-overage", (item) => { item.routes.user.quota_snapshots.premium_interactions = { entitlement: 300, remaining: -2, overage_count: 1 }; });
variant("github-copilot", "free", (item) => { item.routes.user = { limited_user_quotas: { chat: 40 }, monthly_quotas: { chat: 50 } }; });
variant("github-copilot", "unlimited", (item) => { item.routes.user.quota_snapshots.premium_interactions = { unlimited: true }; });
variant("openrouter", "uncapped", (item) => { item.routes.key.data.limit = null; item.routes.key.data.is_free_tier = true; });
for (const provider of ["minimax", "minimax-cn"]) {
  variant(provider, "balance", (item) => { item.apiKey = "sk-api-fixture-access"; item.routes = { query_balance: balance }; });
  variant(provider, "legacy-count", (item) => { item.routes.remains.model_remains[0].current_interval_usage_count = 23; });
  variant(provider, "unlimited", (item) => { Object.assign(item.routes.remains.model_remains[0], { current_interval_status: 3, current_weekly_status: 3 }); });
  variant(provider, "contradictory", (item) => { item.routes.remains.model_remains[0].current_interval_remaining_percent = 50; });
}
for (const provider of ["zai", "zai-coding-cn"]) {
  variant(provider, "optional-failure", (item) => { item.failRoutes = ["list"]; });
  variant(provider, "business-error", (item) => { item.routes.limit = { code: 1113, msg: "fixture-access" }; });
}
variant("command-code", "optional-failure", (item) => { item.failRoutes = ["subscriptions", "summary"]; });
variant("command-code", "guard-changed", (item) => { item.rejectGuard = 3; });
variant("fireworks", "multiple-targets", (item) => { item.routes.accounts.accounts.push({ name: "accounts/fixture-other" }); });
variant("fireworks", "remembered-target", (item) => { item.routes.accounts.accounts.push({ name: "accounts/fixture-other" }); item.target = "fixture-other"; });
variant("fireworks", "missing-target", (item) => { item.target = "fixture-missing"; });
variant("fireworks", "pagination", (item) => { item.routes.accounts.nextPageToken = "fixture-page"; item.pages = [{ accounts: [{ name: "accounts/fixture-next" }] }]; item.target = "fixture-next"; });
variant("fireworks", "empty", (item) => { item.routes.summary = {}; });
variant("kimi-coding", "ratio-fallback", (item) => { item.routes.usages = { usages: { limit_5h: { used_ratio: 0.23, reset_time: iso }, limit_week: { used_ratio: 0.5 }, limit_month_total: { used_ratio: 0.25, reset_time: iso } } }; });
variant("kimi-coding", "malformed-count-precedence", (item) => { item.routes.usages.usage.remaining = "bad"; item.routes.usages.usages.limit_week = { used_ratio: 0.01 }; });
variant("xai", "legacy", (item) => { item.routes.billing.config = { monthlyLimit: { val: 10000 }, used: { val: 125 }, billingPeriodStart: iso, billingPeriodEnd: iso }; });
variant("xai", "null", (item) => { item.routes.billing.config = null; item.routes.user.subscriptionTier = null; });
variant("deepseek", "oversize", (item) => { item.response = "oversize"; });
variant("deepseek", "invalid-json", (item) => { item.response = "invalid-json"; });
variant("deepseek", "abort", (item) => { item.aborted = true; });
export { cases, now };
