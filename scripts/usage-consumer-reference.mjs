import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { registerHooks } from "node:module";
import pkg from "../node_modules/@narumitw/pi-usage/package.json" with { type: "json" };
import { normalizeCommandCodeUsagePayload } from "../node_modules/@narumitw/pi-usage/src/providers/command-code.ts";
import { normalizeOpenCodeZenPayload } from "../node_modules/@narumitw/pi-usage/src/providers/opencode-zen.ts";
import { normalizeKimiCodingUsagePayload } from "../node_modules/@narumitw/pi-usage/src/providers/kimi-coding.ts";
import { normalizeXaiBillingPayload } from "../node_modules/@narumitw/pi-usage/src/providers/xai.ts";

assert.equal(pkg.version, "0.68.3");
const capturedAt = 1735776000000;
const fixtures = [];
function add(providerID, name, payload, extra = {}) {
  const fixture = { name: `${providerID}:${name}`, providerID, capturedAt, payload, ...extra };
  try {
    fixture.expected = providerID === "command-code" ? normalizeCommandCodeUsagePayload(payload, capturedAt)
      : providerID === "opencode-go" ? normalizeOpenCodeZenPayload(payload, capturedAt)
      : providerID === "kimi-coding" ? normalizeKimiCodingUsagePayload(payload, capturedAt)
      : normalizeXaiBillingPayload(payload, extra.tier, capturedAt);
  } catch (error) {
    if (!(error instanceof Error) || error.name !== "Error" || error.code) throw error;
    fixture.error = error.message;
  }
  fixtures.push(fixture);
}
const command = { account: { user: { userName: "\u001b[31mSynthetic\u001b[0m\nAccount" }, org: { id: "synthetic-org" } }, credits: { credits: { monthlyCredits: 12.5, purchasedCredits: 2, freeCredits: 1 }, windowLimits: { limited: true, fiveHour: { used: 2.5, cap: 10, resetAt: capturedAt + 999 }, weekly: { used: 40, cap: 30, resetAt: "1735776001" } } }, subscription: { data: { planId: "individual-goat", status: "active", currentPeriodEnd: "2025-02-01T01:00:00+01:00" } }, usage: { totalCost: 4.5, totalCount: 0, totalTokens: 1234 } };
add("command-code", "normal", command);
add("command-code", "nonfinite-credit-total", { account: command.account, credits: { credits: { monthlyCredits: 1e308, purchasedCredits: 1e308 } }, usage: { totalCost: 1 } }, { nativeError: "Command Code credit total was not finite." });
add("command-code", "optional-failures", { account: command.account, usage: { totalCost: 1, totalTokensIn: 3, totalTokensOut: 4 } });
add("command-code", "plan-only", { account: { user: { name: "Fallback" } }, subscription: { data: { planId: "individual_new__plan", status: " trial " } } });
add("command-code", "credits-only", { account: command.account, credits: { credits: { monthlyCredits: 0, freeCredits: -1 }, windowLimits: { limited: false, fiveHour: { used: "1", cap: 10 } } } });
add("command-code", "empty-data", { account: command.account });
add("command-code", "unsafe-account", { ...command, account: { user: { userName: "\u001b[31m" } } });
add("command-code", "malformed-optionals", { account: command.account, credits: [], subscription: { data: {} }, usage: { totalCost: -1, totalTokens: "1", totalTokensIn: 0 } });
add("opencode-go", "normal", { usage: { rolling: { status: "ok", percent: 25, resetsAt: "2025-01-02T03:04:05.999Z" }, weekly: { status: "rate-limited", percent: 150 }, monthly: { status: "\u001b[31munavailable\u001b[0m\nnow" } } });
add("opencode-go", "no-display", { usage: { rolling: { status: "ok", percent: "1" }, weekly: { status: "error" }, monthly: null } });
add("opencode-go", "invalid-root", {});
const kimi = { usage: { limit: "100", remaining: "70", resetTime: "2025-01-02T03:04:05.999+01:00" }, limits: [{ name: "\u001b[31mCustom\u001b[0m\nFive", window: { duration: "5", timeUnit: "TIME_UNIT_HOUR" }, detail: { limit: "10" } }, { window: { duration: 1, timeUnit: "TIME_UNIT_DAY" }, detail: { limit: 5, used: 7 } }], usages: { limit_5h: { used_ratio: 0.9 }, limit_week: { used_ratio: 0.9 }, limit_month_total: { used_ratio: 0.25, reset_time: "2025-02-01T00:00:00Z" } } };
add("kimi-coding", "normal", kimi);
add("kimi-coding", "ratio-only", { usages: { limit_5h: { used_ratio: 0 }, limit_week: { used_ratio: 1 }, limit_month_total: { used_ratio: 0.4 } } });
add("kimi-coding", "duplicates-suppress-ratio", { ...kimi, limits: [...kimi.limits, kimi.limits[0]], usage: { limit: 100, used: null } });
add("kimi-coding", "malformed-rows", { limits: [null, { window: { duration: 1.5, timeUnit: "TIME_UNIT_HOUR" } }, { window: { duration: 2, timeUnit: "TIME_UNIT_MINUTE" }, detail: { limit: "10", used: " 2" } }], usages: { limit_week: { used_ratio: 0.25 }, limit_month_total: { used_ratio: -1 } } });
const wallet = { balance: { type: "BOOSTER", amount: "1500000", amountLeft: "1" }, monthlyChargeLimit: { priceInCents: "1000", currency: "cny" }, monthlyUsed: { priceInCents: 125, currency: "CNY" }, monthlyChargeLimitEnabled: true };
add("kimi-coding", "wallet-only", { boosterWallet: wallet });
add("kimi-coding", "legacy-wallet-unlimited", { booster_wallet: { ...wallet, monthlyChargeLimitEnabled: false, balance: { ...wallet.balance, amountLeft: "invalid" } } });
add("kimi-coding", "wallet-currency-mismatch", { ...kimi, boosterWallet: { ...wallet, monthlyUsed: { priceInCents: 1, currency: "USD" } } });
add("kimi-coding", "empty", {});
for (const [name, resetTime] of [["leap", "2024-02-29T00:00:00Z"], ["invalid-day", "2025-02-29T00:00:00Z"], ["invalid-hour", "2025-01-01T24:00:00Z"], ["pre-epoch", "1969-12-31T23:59:59Z"], ["offset", "2025-01-02T03:04:05+23:59"]]) add("kimi-coding", `date-${name}`, { usage: { limit: 1, resetTime } });
for (const amount of [499999, 1499999, 1500000, 1500001, 9007199254740991]) add("kimi-coding", `money-${amount}`, { boosterWallet: { ...wallet, balance: { type: "BOOSTER", amount, amountLeft: amount } } });
add("xai", "normal", { config: { creditUsagePercent: 25.5, monthlyLimit: "ignored", currentPeriod: { type: "USAGE_PERIOD_TYPE_WEEKLY", start: "2025-01-01T00:00:00Z", end: "2025-01-08T00:00:00Z" }, onDemandCap: { val: 1000 }, onDemandUsed: { val: 1200 }, prepaidBalance: { val: -123 } } }, { tier: "\u001b[31mSynthetic\u001b[0m\nTier" });
add("xai", "legacy-cents", { config: { monthlyLimit: {}, used: { val: -5 }, billingPeriodStart: "2025-01-01", billingPeriodEnd: "2025-02-01" } });
add("xai", "period-only", { config: { currentPeriod: { type: "USAGE_PERIOD_TYPE_CUSTOM_PERIOD", end: "2025-01-02T01:02:03.999Z" } } });
add("xai", "null-config", { config: null }, { tier: "Basic" });
add("xai", "empty-config", { config: {} });
for (const [name, config] of [["config-shape", []], ["period-shape", { currentPeriod: [] }], ["unsafe-cents", { used: { val: 9007199254740992 } }], ["null-cents", { used: { val: null } }], ["bad-percent", { creditUsagePercent: 101 }], ["string-cents", { prepaidBalance: { val: "100" } }], ["invalid-date", { billingPeriodEnd: "invalid" }]]) add("xai", name, { config });
add("xai", "tier-too-long", { config: null }, { tier: "😀".repeat(81) });
for (const planId of ["individual-ßpecial", "individual-ﬃle", "individual-ŉew", "individual-ΐota", "individual-𐐨plan", "individual-éclair", "individual-ﬄ" + "a".repeat(30)]) {
  add("command-code", `unicode-plan-${planId}`, { account: command.account, subscription: { data: { planId } } });
}
for (const currency of ["ßd", "ﬃ", "ﬄ", "ſd", "ßdd", "éur"]) {
  add("kimi-coding", `unicode-currency-${currency}`, { boosterWallet: { balance: wallet.balance, monthlyUsed: { priceInCents: 1, currency } } });
}
add("kimi-coding", "expanded-limit-currency", { boosterWallet: { balance: wallet.balance, monthlyChargeLimit: { priceInCents: 1, currency: "ﬃ" }, monthlyUsed: { priceInCents: 1, currency: "ffi" }, monthlyChargeLimitEnabled: true } });
for (const resetTime of ["2025-01-01\n00:00:00Z", "\u001b[31m2025-01-01T00:00:00Z\u001b[0m"]) {
  add("kimi-coding", `strict-date-${resetTime}`, { usage: { limit: 1, resetTime } });
}
function addConsumerDate(providerID, date) {
  const payload = providerID === "command-code" ? { account: command.account, credits: command.credits, usage: command.usage, subscription: { data: { currentPeriodEnd: date } } }
    : providerID === "opencode-go" ? { usage: { rolling: { status: "ok", percent: 1, resetsAt: date } } }
    : { config: { currentPeriod: { end: date } } };
  add(providerID, `date-${date}`, payload);
}
for (const date of ["2025-01-01 00:00:00Z", " 2025-01-01T00:00:00Z ", "2025-02-30T00:00:00Z", "2025-01-01T24:00:00Z", "1969-12-31T23:59:59.999Z", "+010000-01-01T00:00:00Z", "Wed, 01 Jan 2025 00:00:00 GMT", "\u001b[31m2025-01-01\u001b[0m", "2025-01-01\n00:00:00Z"]) {
  for (const providerID of ["opencode-go", "command-code", "xai"]) addConsumerDate(providerID, date);
}
for (const date of ["2025-01-01\t 00:00:00Z", "2025-01-01\u00a000:00:00Z", "2025-01-01\u008500:00:00Z", " 2025-01-01 00:00:00Z ", "2025-01-01\nT00:00:00Z", "2025-01-01T24:00:00.0001Z", "+275760-09-13T00:00:00Z", "+275760-09-13T00:00:00.001Z", "2025-01-01T00:00:00.9999Z", "2025-01-01 00:00:00+24:00", "0025-01-01 00:00:00Z", "0050-01-01 00:00:00Z", "0001-02-03 00:00:00Z", "-000000-02-03T00:00:00Z", "-000000-02-03 00:00:00Z", "Wed, 01 Jan 2025 00:00:00 EST", "January 1, 2025 00:00:00 GMT", "Wed,\n01 Jan 2025 00:00:00 GMT", "Wed,\u00a001 Jan 2025 00:00:00 GMT", "Wed,\u008501 Jan 2025 00:00:00 GMT", "Wed,\u001b[31m01 Jan 2025 00:00:00 GMT", "2025-01-01\u000001:00:00Z"]) {
  addConsumerDate("opencode-go", date);
}
const hooks = registerHooks({ resolve(specifier, context, nextResolve) {
  if (specifier === "@earendil-works/pi-coding-agent") return { url: "data:text/javascript,export function readStoredCredential(){throw new Error('Credential access forbidden')}", shortCircuit: true };
  return nextResolve(specifier, context);
} });
const { resolveUsageAuth, XAI_ADAPTER, usageAdapters } = await import("../node_modules/@narumitw/pi-usage/src/query.ts");
const candidate = { type: "oauth", access: "synthetic-access", refresh: "synthetic-refresh", expires: capturedAt + 10000 };
for (const [name, candidates, effective] of [["normal", [candidate], { apiKey: candidate.access }], ["duplicates", [candidate, candidate], { apiKey: candidate.access }], ["incomplete", [{ ...candidate, refresh: "" }], { apiKey: candidate.access }], ["mismatch", [{ ...candidate, access: "other" }], { apiKey: candidate.access }], ["api-key", [], { apiKey: "api-key" }], ["bearer-override", [candidate], { apiKey: "wrong", headers: { Authorization: `Bearer ${candidate.access}` } }]]) {
  const model = { provider: "xai", baseUrl: "https://api.x.ai/v1" };
  const ctx = { model, modelRegistry: { getAvailable: () => [model], getAll: () => [model], getProvider: () => ({}), getApiKeyAndHeaders: async () => ({ ok: true, ...effective }), getProviderAuth: async () => ({ auth: effective }) } };
  const f = { name: `xai:auth-${name}`, kind: "auth", effective, candidates };
  try { const auth = await resolveUsageAuth(ctx, XAI_ADAPTER, new Uint8Array(32), undefined, async () => ({ ok: true, candidates, offeredCount: candidates.length })); f.expected = auth.headers.Authorization; }
  catch (error) { if (!(error instanceof Error) || error.name !== "Error") throw error; f.error = error.message; }
  fixtures.push(f);
}
fixtures.push({ name: "scheduling", kind: "scheduling", expected: usageAdapters().filter(a => a.publishesStatusline !== false).map(a => a.id) });
hooks.deregister();
const hashes = {};
for (const path of ["src/providers/command-code.ts", "src/providers/opencode-zen.ts", "src/providers/kimi-coding.ts", "src/providers/xai.ts", "src/query.ts", "src/usage.ts", "src/usage-helpers.ts", "src/core.ts", "src/types.ts"]) hashes[path] = createHash("sha256").update(await readFile(new URL(`../node_modules/@narumitw/pi-usage/${path}`, import.meta.url))).digest("hex");
const output = `${JSON.stringify({ upstream: { name: pkg.name, version: pkg.version, hashes }, fixtures }, null, 2)}\n`;
const destination = new URL("../extensions/pig-status/internal/usage/testdata/consumer-reference.json", import.meta.url);
if (process.argv[2] === "--check") assert.equal(await readFile(destination, "utf8"), output, "Remaining-provider reference differs from actual pinned source");
else { assert.equal(process.argv.length, 2); await writeFile(destination, output); }
console.log(`native remaining-provider reference: ${fixtures.length} cases match ${pkg.name} ${pkg.version}; xAI is menu-only`);
