import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { registerHooks } from "node:module";
import { cases, now } from "../probes/usage/cases.mjs";
import pkg from "../node_modules/@narumitw/pi-usage/package.json" with { type: "json" };
import { normalizeDeepSeekBalancePayload } from "../node_modules/@narumitw/pi-usage/src/providers/deepseek.ts";
import { normalizeVercelAIGatewayCreditsPayload } from "../node_modules/@narumitw/pi-usage/src/providers/vercel-ai-gateway.ts";
import { normalizeMoonshotBalancePayload } from "../node_modules/@narumitw/pi-usage/src/providers/moonshot.ts";
import { normalizeCodexBackendPayload } from "../node_modules/@narumitw/pi-usage/src/providers/codex.ts";
import { normalizeGitHubCopilotUsagePayload } from "../node_modules/@narumitw/pi-usage/src/providers/github-copilot.ts";
import { OPENAI_CHATGPT_ADAPTER } from "../node_modules/@narumitw/pi-usage/src/providers/openai-chatgpt.ts";

const root = new URL("../", import.meta.url);
const upstream = new URL("node_modules/@narumitw/pi-usage/", root);
assert.equal(pkg.version, "0.68.3");
const normalizers = {
  deepseek: normalizeDeepSeekBalancePayload,
  "vercel-ai-gateway": normalizeVercelAIGatewayCreditsPayload,
  moonshotai: (payload, time) => normalizeMoonshotBalancePayload("moonshotai", payload, time),
  "moonshotai-cn": (payload, time) => normalizeMoonshotBalancePayload("moonshotai-cn", payload, time),
};
const fixtures = [];
function add(providerID, name, change = () => {}) {
  const item = cases.find((item) => item.id === `${providerID}:normal`);
  const payload = structuredClone(item.routes[providerID === "vercel-ai-gateway" ? "credits" : "balance"]);
  change(payload);
  const fixture = { name: `${providerID}:${name}`, providerID, capturedAt: now, payload };
  try {
    fixture.expected = normalizers[providerID](payload, now);
  } catch (error) {
    fixture.error = error.message;
  }
  fixtures.push(fixture);
}
add("deepseek", "normal");
add("deepseek", "unavailable", (p) => { p.is_available = false; });
add("deepseek", "signed-exact-decimals", (p) => {
  p.balance_infos = [{ currency: "USD", total_balance: "-0.000000001", granted_balance: "0", topped_up_balance: "1.000000000" }];
});
add("deepseek", "invalid-availability", (p) => { p.is_available = 1; });
add("deepseek", "empty", (p) => { p.balance_infos = []; });
add("deepseek", "null-row", (p) => { p.balance_infos = [null]; });
add("deepseek", "unsupported-currency", (p) => { p.balance_infos[0].currency = "EUR"; });
add("deepseek", "duplicate-currency", (p) => { p.balance_infos.push(p.balance_infos[0]); });
for (const [name, value] of [["leading-zero", "01.25"], ["exponent", "1e3"], ["number", 1.25], ["overlong", "1".repeat(65)]]) {
  add("deepseek", name, (p) => { p.balance_infos[0].total_balance = value; });
}
add("vercel-ai-gateway", "normal");
add("vercel-ai-gateway", "zero", (p) => { p.balance = "0"; p.total_used = "0.000000000"; });
for (const [name, value] of [["missing", undefined], ["negative", "-0"], ["leading-zero", "01"], ["exponent", "1e3"], ["number", 1.25], ["overlong", "1".repeat(65)]]) {
  add("vercel-ai-gateway", name, (p) => { p.balance = value; });
}
add("vercel-ai-gateway", "invalid-spend", (p) => { p.total_used = null; });
for (const provider of ["moonshotai", "moonshotai-cn"]) {
  add(provider, "normal");
  add(provider, "number-format", (p) => { p.data = { available_balance: 0.000001, voucher_balance: 1e-7, cash_balance: -0 }; });
  add(provider, "safe-integer", (p) => { p.data.available_balance = Number.MAX_SAFE_INTEGER; });
  add(provider, "bad-code", (p) => { p.code = "0"; });
  add(provider, "bad-status", (p) => { p.status = 1; });
  add(provider, "bad-data", (p) => { p.data = []; });
  add(provider, "negative-available", (p) => { p.data.available_balance = -1; });
  add(provider, "negative-voucher", (p) => { p.data.voucher_balance = -1; });
  add(provider, "string-amount", (p) => { p.data.cash_balance = "1.25"; });
  add(provider, "unsafe-integer", (p) => { p.data.cash_balance = Number.MAX_SAFE_INTEGER + 1; });
}
const hostImport = registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@earendil-works/pi-coding-agent") {
      return { url: "data:text/javascript,export function readStoredCredential(){throw new Error('Host credential reads are forbidden in the normalizer oracle')}", shortCircuit: true };
    }
    return nextResolve(specifier, context);
  },
});
async function addSubscription(providerID, name, payload) {
  const fixture = { name: `${providerID}:${name}`, providerID, capturedAt: now, payload };
  const originalFetch = globalThis.fetch;
  const originalNow = Date.now;
  try {
    Date.now = () => now;
    if (providerID === "openai") {
      globalThis.fetch = async (url) => {
        assert.ok(["https://chatgpt.com/backend-api/wham/usage/chatpass/apps", "https://chatgpt.com/backend-api/wham/usage"].includes(url));
        return new Response(JSON.stringify(url.endsWith("/apps") ? payload.apps : payload.plan));
      };
      fixture.expected = await OPENAI_CHATGPT_ADAPTER.query({
        openaiClientId: payload.clientId,
        openaiUseCodexUsage: payload.useCodexUsage,
        ...(Object.hasOwn(payload, "apps") ? { openaiCompanion: { headers: { Authorization: "Bearer synthetic-companion" } } } : {}),
      }, new AbortController().signal, 1000, async () => {});
    } else {
      fixture.expected = (providerID === "openai-codex" ? normalizeCodexBackendPayload : normalizeGitHubCopilotUsagePayload)(payload, now);
    }
  } catch (error) {
    if (!(error instanceof Error) || error.name !== "Error" || error.code) throw error;
    fixture.error = error.message;
  } finally {
    globalThis.fetch = originalFetch;
    Date.now = originalNow;
  }
  fixtures.push(fixture);
}
const window = { used_percent: 25, remaining_percent: 75, limit_window_seconds: 301, reset_at: 1735776000 };
await addSubscription("openai-codex", "primary-additional-credits", {
  plan_type: "\u001b[31mPro\u001b[0m\n plan", rate_limit: { primary_window: window, secondary_window: { used_percent: "110", limit_window_seconds: "604800", reset_at: "1735776000" } },
  additional_rate_limits: [{ metered_feature: "spark", limit_name: "Spark", rate_limit: { primary_window: { used_percent: -5, limit_window_seconds: 60 } } }],
  credits: { has_credits: true, balance: "12.5" }, rate_limit_reset_credits: { available_count: "2" },
});
await addSubscription("openai-codex", "optional-partial-group", { rate_limit: { primary_window: window }, additional_rate_limits: [null, { limit_name: "optional", rate_limit: { primary_window: window, secondary_window: "bad" } }, { metered_feature: "skip", rate_limit: [] }], credits: { has_credits: false } });
for (const [name, credits] of [["unlimited", { has_credits: true, unlimited: true }], ["available", { has_credits: true, balance: "bad" }]]) {
  await addSubscription("openai-codex", name, { credits, rate_limit_reset_credits: { available_count: -2 } });
}
await addSubscription("openai-codex", "numeric-strings", { credits: { has_credits: true, balance: "0x10000000000000000" }, rate_limit: { primary_window: { used_percent: "1e1", reset_at: "0", limit_window_seconds: "61" }, secondary_window: { used_percent: "1_0" } }, additional_rate_limits: [{ limit_name: "not-a-JS-number", rate_limit: { primary_window: { used_percent: "0x1p2" } } }] });
await addSubscription("openai-codex", "empty", {});
await addSubscription("openai-codex", "invalid-primary", { rate_limit: [] });
await addSubscription("openai-codex", "invalid-window", { rate_limit: { primary_window: "bad" }, credits: { has_credits: true } });
const premium = { entitlement: 300, remaining: 210 };
await addSubscription("github-copilot", "premium", { login: "synthetic\u001b[31m-user", copilot_plan: " Pro\nplan ", quota_reset_date_utc: "2025-02-01T00:00:00Z", quota_snapshots: { premium_interactions: premium } });
await addSubscription("github-copilot", "credits-overage", { quota_reset_date: "2025-02-01", quota_snapshots: { premium_interactions: { token_based_billing: true, entitlement: 1000, remaining: -4, credits_used: 1004, overage_count: 2 } } });
await addSubscription("github-copilot", "quota-remaining-fallback", { access_type_sku: "free", quota_reset_date_utc: "", quota_reset_date: "2025-02-01T01:00:00+01:00", quota_snapshots: { premium_interactions: { entitlement: 300, remaining: "invalid", quota_remaining: 200, credits_used: -1, overage_count: 7 } } });
await addSubscription("github-copilot", "unlimited", { quota_snapshots: { premium_interactions: { unlimited: true, token_based_billing: true } } });
await addSubscription("github-copilot", "free", { limited_user_quotas: { chat: 8 }, monthly_quotas: { chat: 50 }, limited_user_reset_date: "2025-02-01T00:00:00Z" });
await addSubscription("github-copilot", "legacy-long-month-reset", { quota_reset_date_utc: "February 1, 2025 00:00:00 GMT", quota_snapshots: { premium_interactions: premium } });
await addSubscription("github-copilot", "invalid-preferred-reset", { quota_reset_date_utc: "not-a-date", quota_reset_date: "2025-02-01", quota_snapshots: { premium_interactions: premium } });
await addSubscription("github-copilot", "incomplete", { quota_snapshots: { premium_interactions: { entitlement: "300", remaining: 200 } } });
await addSubscription("github-copilot", "unsupported", {});
await addSubscription("openai", "auth-only", {});
await addSubscription("openai", "companion-unavailable", { useCodexUsage: true });
const companion = { clientId: "native-registration", apps: { items: [{ id: "other", windows: [] }, { id: "native-registration", allowed_usage_percent: 20, windows: [window] }], has_more: false, next_cursor: null }, plan: { rate_limit: { primary_window: window, secondary_window: { used_percent: 5, limit_window_seconds: 604800 } } } };
await addSubscription("openai", "companion", companion);
for (const [name, change] of [
  ["paginated", (p) => { p.apps.has_more = true; }],
  ["unknown-envelope", (p) => { p.apps.total = 2; }],
  ["ambiguous", (p) => { p.apps.items.push(p.apps.items[1]); }],
  ["unreadable-id", (p) => { p.apps.items[0].id = ""; }],
  ["unmatched", (p) => { p.clientId = "missing"; }],
  ["empty-windows", (p) => { p.apps.items[1].windows = []; }],
  ["contradictory-window", (p) => { p.apps.items[1].windows[0].remaining_percent = 20; }],
  ["null-reset", (p) => { p.apps.items[1].windows[0].reset_at = null; }],
  ["bad-allowance", (p) => { p.apps.items[1].allowed_usage_percent = "20"; }],
  ["too-many-apps", (p) => { p.apps.items = Array.from({ length: 129 }, (_, i) => ({ id: String(i) })); }],
  ["too-many-windows", (p) => { p.apps.items[1].windows = Array(33).fill(window); }],
  ["no-plan-windows", (p) => { p.plan = {}; }],
]) { const p = structuredClone(companion); change(p); await addSubscription("openai", name, p); }
hostImport.deregister();
const resetFixtures = [];
function addCopilotReset(name, fields, free = false) {
  const payload = free
    ? { limited_user_quotas: { chat: 8 }, monthly_quotas: { chat: 50 }, ...fields }
    : { quota_snapshots: { premium_interactions: premium }, ...fields };
  const report = normalizeGitHubCopilotUsagePayload(payload, now);
  resetFixtures.push({ name, payload, resetsAt: report.buckets[0].resetsAt ?? null });
}
const resetFields = ["quota_reset_date_utc", "quota_reset_date", "limited_user_reset_date"];
for (const [index, zone] of ["UTC", "GMT", "EST", "EDT", "CST", "CDT", "MST", "MDT", "PST", "PDT", "XYZ", "CEST", "ABCDT", "ChST", "MeST", "GMT+1", "GMT-5", "GMT+0001", "GMT-001", "GMT+0"].entries()) {
  addCopilotReset(`rfc1123:${zone}`, { [resetFields[index % 3]]: `Sat, 01 Feb 2025 00:00:00 ${zone}` }, index % 2 === 1);
}
for (const zone of ["EST", "EDT", "PST", "PDT"]) {
  addCopilotReset(`summer:${zone}`, { quota_reset_date_utc: `Tue, 01 Jul 2025 00:00:00 ${zone}` });
}
for (const [name, fields] of [
  ["preferred-valid", { quota_reset_date_utc: "Sat, 01 Feb 2025 00:00:00 CST", quota_reset_date: "Sat, 01 Feb 2025 00:00:00 PST" }],
  ["preferred-invalid", { quota_reset_date_utc: "Sat, 01 Feb 2025 00:00:00 XYZ", quota_reset_date: "Sat, 01 Feb 2025 00:00:00 EST" }],
  ["empty-first", { quota_reset_date_utc: " \n ", quota_reset_date: "Sat, 01 Feb 2025 00:00:00 MST" }],
  ["invalid-second", { quota_reset_date: "Sat, 01 Feb 2025 00:00:00 ABCDT", limited_user_reset_date: "Sat, 01 Feb 2025 00:00:00 PST" }],
  ["nonstring-first", { quota_reset_date_utc: 1, limited_user_reset_date: "Sat, 01 Feb 2025 00:00:00 PDT" }],
]) {
  addCopilotReset(`premium:${name}`, fields);
  addCopilotReset(`free:${name}`, fields, true);
}
for (const [name, text] of [
  ["numeric-offset", "Sat, 01 Feb 2025 00:00:00 -0500"],
  ["iso", "2025-02-01T00:00:00Z"],
  ["long-month", "February 1, 2025 00:00:00 GMT"],
]) addCopilotReset(name, { quota_reset_date_utc: text });
const hashes = {};
for (const path of ["src/providers/deepseek.ts", "src/providers/vercel-ai-gateway.ts", "src/providers/moonshot.ts", "src/providers/codex.ts", "src/providers/github-copilot.ts", "src/providers/openai-chatgpt.ts", "src/providers/openai-companion-usage.ts", "src/openai-companion-auth.ts", "src/auth-resolution-guard.ts", "src/oauth-credential-source.ts", "src/query.ts", "src/core.ts", "src/types.ts"]) {
  hashes[path] = createHash("sha256").update(await readFile(new URL(path, upstream))).digest("hex");
}
const output = `${JSON.stringify({ upstream: { name: pkg.name, version: pkg.version, hashes }, fixtures }, null, 2)}\n`;
const destination = new URL("extensions/pig-status/internal/usage/testdata/reference.json", root);
if (process.argv[2] === "--check" || process.argv[2] === "--write-copilot-resets") {
  assert.equal(await readFile(destination, "utf8"), output, "Native usage reference differs from the pinned source; regenerate with node --import tsx scripts/usage-reference.mjs");
} else {
  assert.equal(process.argv.length, 2, "Expected no arguments or --check");
  await writeFile(destination, output);
}
console.log(`native usage reference: ${fixtures.length} cases match ${pkg.name} ${pkg.version}`);
const resetOutput = `${JSON.stringify({ upstream: { name: pkg.name, version: pkg.version, sha256: hashes["src/providers/github-copilot.ts"] }, fixtures: resetFixtures }, null, 2)}\n`;
const resetDestination = new URL("extensions/pig-status/internal/usage/testdata/copilot-resets.json", root);
if (process.argv[2] === "--check") {
  assert.equal(await readFile(resetDestination, "utf8"), resetOutput, "Copilot reset reference differs from the pinned source; regenerate with node --import tsx scripts/usage-reference.mjs --write-copilot-resets");
} else {
  await writeFile(resetDestination, resetOutput);
}
console.log(`Copilot reset reference: ${resetFixtures.length} cases match ${pkg.name} ${pkg.version}`);
