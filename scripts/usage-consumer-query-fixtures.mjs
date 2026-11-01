/** Independent pinned-source oracle for the public consumer Query seam. */
import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { registerHooks } from "node:module";

registerHooks({ resolve(specifier, context, nextResolve) {
  if (specifier === "@earendil-works/pi-coding-agent") return { url: "data:text/javascript,export function readStoredCredential(){throw new Error('credential reads forbidden')};export function getAgentDir(){throw new Error('personal roots forbidden')}", shortCircuit: true };
  return nextResolve(specifier, context);
} });
const { adapterForProvider, SUPPORTED_ADAPTERS, XAI_ADAPTER } = await import("../node_modules/@narumitw/pi-usage/src/query.ts");

const output = new URL("../extensions/pig-status/internal/usage/testdata/consumer-query-acceptance.json", import.meta.url);
const account = { user: { userName: "Synthetic User" }, org: { id: "org_15.safe" } };
const credits = { credits: { monthlyCredits: 12.5, purchasedCredits: 3, freeCredits: 0.25 }, windowLimits: { limited: true, fiveHour: { used: 8, cap: 10, resetAt: "1800000000123" }, weekly: { used: 27, cap: 20, resetAt: 1800000000 } } };
const subscription = { data: { planId: "individual-goat", status: "active", currentPeriodStart: " 2027-01-01T00:00:00Z ", currentPeriodEnd: "2027-02-01T00:00:00Z" } };
const summary = { totalCost: 4.75, totalCount: 17, totalTokensIn: 111, totalTokensOut: 222 };
const cc = { "/alpha/whoami": account, "/alpha/billing/credits": credits, "/alpha/billing/subscriptions": subscription, "/alpha/usage/summary": summary };
const zen = { usage: { rolling: { status: "ok", percent: 22.5, resetsAt: "2027-02-30T24:00:00Z" }, weekly: { status: "rate-limited", percent: 123, resetsAt: "1969-12-31T23:59:59.999Z" }, monthly: { status: "temporarily\u001b[31m unavailable\n" } } };
const wallet = { balance: { type: "BOOSTER", amount: "1250000000", amountLeft: "1" }, monthlyChargeLimit: { currency: "usd", priceInCents: "1234" }, monthlyUsed: { currency: "USD", priceInCents: "17" }, monthlyChargeLimitEnabled: true };
const kimi = { usage: { limit: "100", remaining: "63", resetTime: "2027-02-28T10:30:00+02:00" }, limits: [{ name: "Five hour", window: { duration: 5, timeUnit: "TIME_UNIT_HOUR" }, detail: { limit: "20" } }, { window: { duration: "1", timeUnit: "TIME_UNIT_DAY" }, detail: { limit: 30, used: 4 } }], boosterWallet: wallet };
const user = { userId: "canonical.user-15", subscriptionTier: "SuperGrok" };
const billing = { config: { monthlyLimit: { val: 10000 }, used: { val: 1250 }, onDemandCap: { val: 300 }, onDemandUsed: { val: 450 }, prepaidBalance: { val: -23 }, currentPeriod: { type: "USAGE_PERIOD_TYPE_WEEKLY", start: "2027-01-01T00:00:00Z", end: "2027-01-08T00:00:00Z" } } };
const bases = { "command-code": "https://api.commandcode.ai/v1", "opencode-go": "https://opencode.ai/zen/go/v1", "kimi-coding": "https://api.kimi.com/coding", xai: "https://api.x.ai/v1" };
const normalRoutes = { "command-code": cc, "opencode-go": { "/zen/go/v1/usage": zen }, "kimi-coding": { "/coding/v1/usages": kimi }, xai: { "/v1/user": user, "/v1/billing": billing } };
const fixtures = [];
function add(provider, name, routes = normalRoutes[provider], failures = {}) {
  fixtures.push({ provider, name: `${provider}:${name}`, base: bases[provider], routes: structuredClone(routes), failures });
}
for (const provider of Object.keys(bases)) add(provider, "normal");
add("command-code", "personal-invalid-org", { ...cc, "/alpha/whoami": { user: { name: "Personal" }, org: { id: "../bad" } }, "/alpha/billing/subscriptions": { data: { planId: "individual_custom-tier", status: "trial", currentPeriodStart: " " } }, "/alpha/usage/summary": { totalCost: 0, totalTokens: 333 } });
add("command-code", "unlimited-credit-metric", { ...cc, "/alpha/billing/credits": { credits: { monthlyCredits: 0, freeCredits: 2 }, windowLimits: { limited: false } }, "/alpha/usage/summary": {} });
add("command-code", "malformed-account", { ...cc, "/alpha/whoami": { user: {}, org: null } });
for (const path of ["/alpha/billing/credits", "/alpha/billing/subscriptions", "/alpha/usage/summary"]) {
  for (const mode of ["http", "json", "shape", "transport", "overflow"]) add("command-code", `optional-${path.split("/").at(-1)}-${mode}`, cc, { [path]: mode });
}
add("opencode-go", "malformed-all", { "/zen/go/v1/usage": { usage: { rolling: { status: "ok", percent: "42" }, weekly: { status: "ok", percent: -1 } } } });
add("opencode-go", "missing-and-invalid-dates", { "/zen/go/v1/usage": { usage: { monthly: { status: "ok", percent: 0, resetsAt: "not-a-date" } } } });
add("kimi-coding", "ratios-and-invalid-wallet", { "/coding/v1/usages": { usages: { limit_5h: { used_ratio: 0.25, reset_time: "2027-02-30T00:00:00Z" }, limit_week: { used_ratio: 0 }, limit_month_total: { used_ratio: 1, reset_time: "2027-03-01T00:00:00Z" } }, boosterWallet: { ...wallet, monthlyUsed: { priceInCents: 1, currency: "EUR" } } } });
add("kimi-coding", "duplicates-suppress-ratio", { "/coding/v1/usages": { usage: { limit: 10, used: "bad" }, limits: [{ window: { duration: 5, timeUnit: "TIME_UNIT_HOUR" }, detail: { limit: 20 } }, { window: { duration: 300, timeUnit: "TIME_UNIT_MINUTE" }, detail: { limit: 20, used: 1 } }], usages: { limit_5h: { used_ratio: 0.5 }, limit_week: { used_ratio: 0.7 }, limit_month_total: { used_ratio: 0.3 } }, booster_wallet: { ...wallet, monthlyChargeLimitEnabled: false } } });
add("kimi-coding", "wallet-rounding", { "/coding/v1/usages": { boosterWallet: { ...wallet, balance: { type: "BOOSTER", amount: "1500000", amountLeft: "1499999" } } } });
add("kimi-coding", "malformed-all", { "/coding/v1/usages": { usage: { limit: 10, used: null }, limits: "bad", usages: [] } });
add("xai", "preferred-percent", { "/v1/user": { userId: user.userId, subscriptionTier: null }, "/v1/billing": { config: { creditUsagePercent: 30.25, monthlyLimit: "ignored", used: { val: 1.5 }, billingPeriodStart: "2027-01-01", billingPeriodEnd: "2027-02-01" } } });
add("xai", "no-config", { "/v1/user": { userId: user.userId }, "/v1/billing": { config: null } });
add("xai", "empty-config", { "/v1/user": { userId: user.userId }, "/v1/billing": { config: {} } });
add("xai", "zero-wrapper-period", { "/v1/user": user, "/v1/billing": { config: { monthlyLimit: {}, used: { val: -101 }, currentPeriod: { type: "USAGE_PERIOD_TYPE_CUSTOM_PERIOD", end: "1969-12-31T23:59:59.999Z" } } } });
for (const [name, config] of [["bad-cents", { used: { val: 1.5 } }], ["bad-percent", { creditUsagePercent: 101 }], ["bad-period", { currentPeriod: [] }]]) add("xai", name, { "/v1/user": user, "/v1/billing": { config } });
add("xai", "bad-tier", { "/v1/user": { ...user, subscriptionTier: 4 }, "/v1/billing": billing });

const originalFetch = globalThis.fetch;
try {
  for (const fixture of fixtures) {
    const wires = [];
    globalThis.fetch = async (input, options) => {
      const url = new URL(input);
      assert.ok(url.hostname === new URL(fixture.base).hostname || (fixture.provider === "xai" && url.hostname === "cli-chat-proxy.grok.com"));
      wires.push({ host: url.host, path: url.pathname, query: Object.fromEntries(url.searchParams) });
      assert.ok(Object.hasOwn(fixture.routes, url.pathname), `unplanned route ${url.pathname}`);
      const mode = fixture.failures[url.pathname];
      if (mode === "transport") throw new Error("fixture-secret raw-provider-canary");
      if (mode === "http") return new Response("fixture-secret raw-provider-canary", { status: 503 });
      if (mode === "json") return new Response("fixture-secret not-json");
      if (mode === "shape") return new Response("[]");
      if (mode === "overflow") return new Response(" ".repeat(65537));
      assert.equal(options.method ?? "GET", "GET");
      return Response.json(fixture.routes[url.pathname]);
    };
    try {
      const report = await adapterForProvider(fixture.provider).query({ headers: { Authorization: "Bearer fixture-secret" }, secrets: ["fixture-secret"] }, new AbortController().signal, 10000, async () => {});
      fixture.expected = { ...report, capturedAt: 1800000000000 };
    } catch (error) {
      assert.ok(error instanceof Error);
      fixture.refused = true;
    }
    fixture.wires = wires;
  }
} finally {
  globalThis.fetch = originalFetch;
}
const document = { source: "@narumitw/pi-usage@0.68.3 actual query adapters and provider normalizers", scheduled: SUPPORTED_ADAPTERS.filter((a) => a.publishesStatusline !== false).map((a) => a.id).sort(), menuOnly: XAI_ADAPTER.id, fixtures };
assert.equal(document.menuOnly, "xai");
assert.equal(XAI_ADAPTER.publishesStatusline, false);
assert.equal(document.scheduled.includes("xai"), false);
const bytes = JSON.stringify(document, null, 2) + "\n";
if (process.argv.includes("--check")) {
  assert.equal(await readFile(output, "utf8"), bytes);
  console.log(`independent consumer oracle: ${fixtures.length} fixtures, source drift check passed`);
} else {
  await writeFile(output, bytes);
  console.log(`independent consumer oracle: ${fixtures.length} fixtures captured`);
}
