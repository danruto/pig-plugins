import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { registerHooks } from "node:module";
import pkg from "../node_modules/@narumitw/pi-usage/package.json" with { type: "json" };
import { miniMaxUsageKind, normalizeMiniMaxUsagePayload } from "../node_modules/@narumitw/pi-usage/src/providers/minimax.ts";
import { normalizeMoonshotBalancePayload } from "../node_modules/@narumitw/pi-usage/src/providers/moonshot.ts";
import { normalizeZaiQuotaPayload, normalizeZaiSubscriptionPayload } from "../node_modules/@narumitw/pi-usage/src/providers/zai.ts";
import { zaiPayloadError, zaiResponseError } from "../node_modules/@narumitw/pi-usage/src/providers/zai-errors.ts";

assert.equal(pkg.version, "0.68.3");
const capturedAt = 1735776000000;
const fixtures = [];
function add(providerID, name, payload, kind = "report", extra = {}) {
  const fixture = { name: `${providerID}:${name}`, providerID, capturedAt, payload, kind, ...extra };
  try {
    fixture.expected = kind === "plan" ? normalizeZaiSubscriptionPayload(payload) ?? null
      : kind === "payload-error" ? zaiPayloadError(payload) ?? ""
      : kind === "response-error" ? zaiResponseError(extra.status, extra.body) ?? ""
      : providerID.startsWith("minimax") ? normalizeMiniMaxUsagePayload(providerID, extra.usageKind ?? "token-plan", payload, capturedAt)
      : providerID.startsWith("moonshot") ? normalizeMoonshotBalancePayload(providerID, payload, capturedAt)
      : normalizeZaiQuotaPayload(providerID, providerID === "zai" ? "Z.AI" : "Z.AI Coding CN", payload, capturedAt, extra.plan);
  } catch (error) {
    if (kind === "plan" && error instanceof RangeError) fixture.expected = null;
    else {
      if (!(error instanceof Error) || error.name !== "Error" || error.code) throw error;
      fixture.error = error.message;
    }
  }
  fixtures.push(fixture);
}
const row = { model_name: "MiniMax M2", current_interval_usage_count: 80, current_interval_total_count: 100, current_interval_remaining_percent: 80, current_interval_status: 1, start_time: capturedAt, end_time: capturedAt + 300 * 60000 + 999, current_weekly_usage_count: 20, current_weekly_total_count: 100, current_weekly_remaining_percent: 80, current_weekly_status: 2, weekly_start_time: capturedAt, weekly_end_time: capturedAt + 10080 * 60000, weekly_boost_permille: 1500 };
function mini(name, change, providerID = "minimax") {
  const payload = { base_resp: { status_code: 0 }, model_remains: [structuredClone(row)] };
  change?.(payload, payload.model_remains[0]);
  add(providerID, name, payload);
}
mini("normal");
mini("regional-groups", (p, r) => { r.model_name = "\u001b[31mM2\u001b[0m\nModel"; p.model_remains.push({ ...r }, { ...r, model_name: "模型" }, { ...r, model_name: " " }, { ...r, model_name: "İModel" }); }, "minimax-cn");
mini("no-count-or-percent", (_, r) => { for (const key of Object.keys(r)) if (key.includes("remaining_percent") || key.includes("status")) delete r[key]; });
mini("unlimited", (_, r) => { r.current_interval_status = 3; delete r.current_interval_total_count; r.current_weekly_status = 3; delete r.current_weekly_usage_count; });
mini("zero-percent", (_, r) => { for (const prefix of ["current_interval_", "current_weekly_"]) { r[`${prefix}usage_count`] = 0; r[`${prefix}total_count`] = 0; r[`${prefix}remaining_percent`] = 0; } });
mini("tie-count", (_, r) => { r.current_interval_usage_count = 50; r.current_interval_remaining_percent = 50; });
mini("tolerance-edge", (_, r) => { r.current_interval_remaining_percent = 79; });
mini("short-window", (_, r) => { r.end_time = r.start_time; r.weekly_end_time = r.weekly_start_time + 30000; });
for (const [name, key, value] of [
  ["unsupported-status", "current_interval_status", 0], ["bad-status", "current_weekly_status", "1"],
  ["bad-percent", "current_interval_remaining_percent", "80"], ["over-percent", "current_weekly_remaining_percent", 101],
  ["over-count", "current_interval_usage_count", 101], ["inconsistent-percent", "current_interval_remaining_percent", 50],
  ["bad-total", "current_weekly_total_count", -1], ["unsafe-count", "current_interval_usage_count", Number.MAX_SAFE_INTEGER + 1],
  ["fraction-count", "current_interval_total_count", 1.5], ["invalid-name", "model_name", 1],
  ["boost-cap", "weekly_boost_permille", 10001], ["bad-boost", "weekly_boost_permille", -1],
  ["bad-time", "start_time", 0], ["string-time", "weekly_end_time", "1735776000000"], ["reverse-time", "end_time", capturedAt - 1],
]) mini(name, (_, r) => { r[key] = value; });
mini("zero-count-mismatch", (_, r) => { r.current_interval_total_count = 0; });
mini("zero-without-percent", (_, r) => { r.current_interval_usage_count = r.current_interval_total_count = 0; delete r.current_interval_remaining_percent; });
mini("null-optionals", (_, r) => { r.current_interval_status = r.current_interval_remaining_percent = r.weekly_boost_permille = null; });
mini("bad-success", (p) => { p.base_resp.status_code = "0"; });
mini("empty", (p) => { p.model_remains = []; });
mini("bad-row", (p) => { p.model_remains = [null]; });
const balance = { base_resp: { status_code: 0 }, available_amount: "12345678901234567890.001", cash_balance: "-2.500", voucher_balance: "0", credit_balance: "1.00", owed_amount: "0" };
for (const provider of ["minimax", "minimax-cn"]) add(provider, "balance", balance, "report", { usageKind: "account-balance" });
for (const [name, value] of [["negative", "-0"], ["number", 1], ["leading-zero", "01"], ["exponent", "1e2"], ["long", "1".repeat(65)], ["missing", undefined]]) add("minimax", `balance-${name}`, { ...balance, owed_amount: value }, "report", { usageKind: "account-balance" });
for (const key of ["sk-api-synthetic", "sk-cp-synthetic", "SK-api-synthetic", ""]) fixtures.push({ name: `minimax:kind-${key}`, kind: "usage-kind", key, expected: miniMaxUsageKind(key) });
for (const provider of ["moonshotai", "moonshotai-cn"]) add(provider, "regional-balance", { code: 0, status: true, data: { available_balance: 1e-7, voucher_balance: 0, cash_balance: -1.25 } });
const quota = { data: { level: " Lite\nplan ", limits: [ { type: "TOKENS_LIMIT", unit: 3, percentage: 25, number: 5, nextResetTime: capturedAt + 999 }, { type: "CREDIT_LIMIT", unit: 6, currentValue: 120, usage: 100, number: 2, nextResetTime: 0 }, { type: "TIME_LIMIT", currentValue: 2, usage: 10, usageDetails: [{ modelCode: "Web Search", usage: 2 }, { modelCode: "模型", usage: 0 }, { modelCode: "İModel", usage: 1 }] } ] } };
add("zai", "window-overflow", { data: { limits: [{ type: "TOKENS_LIMIT", unit: 3, number: 1e308, percentage: 25 }] } }, "report", { nativeError: "Z.AI quota window was not finite." });
for (const provider of ["zai", "zai-coding-cn"]) {
  for (const [name, unit, number] of [
    ["round-sub-half-week", 6, 0.000049603174603174596],
    ["round-half-week", 6, 0.5 / 10080],
    ["round-above-half-week", 6, 0.5000000000000001 / 10080],
    ["round-sub-half-hour", 3, 0.49999999999999994 / 60],
    ["round-half-hour", 3, 0.5 / 60],
    ["round-large-hour", 3, 75059993789508.28],
    ["round-large-week", 6, 446785677318.5017],
    ["round-large-hour-label", 3, 4503599627370497],
    ["round-half-hour-label", 3, 0.5],
  ]) add(provider, name, { data: { limits: [{ type: "TOKENS_LIMIT", unit, number, percentage: 25 }] } });
}
add("zai", "normal", quota);
add("zai-coding-cn", "regional-plan", quota, "report", { plan: { name: "Pro", renewsAt: "2025-02-01" } });
add("zai", "custom-fallback-windows", { data: { limits: [{ type: "CREDIT_LIMIT", unit: 3, number: 1.25, percentage: 150 }, { type: "TOKENS_LIMIT", unit: 6, percentage: 20 }, { type: "TOKENS_LIMIT", unit: 3, number: -1, percentage: 0 }] } });
add("zai", "invalid-optional-rows", { data: { limits: [null, [], { type: "OTHER", percentage: 10 }, { type: "TOKENS_LIMIT", unit: "3", percentage: 10 }, { type: "TOKENS_LIMIT", unit: 3, percentage: -1 }, { type: "TIME_LIMIT", usageDetails: [null, { modelCode: "partial", usage: 1 }] }, { type: "TOKENS_LIMIT", unit: 6, currentValue: "4", usage: 10, percentage: 10 }, { type: "TIME_LIMIT", currentValue: 0, usage: 0, usageDetails: [{ modelCode: "\u001b[31mSanitized\u001b[0m\nTool", usage: 2 }, { modelCode: "bad", usage: -1 }, {}] }] } });
for (const [name, payload] of [["empty", { data: {} }], ["invalid-data", { data: [] }], ["only-metrics", { data: { limits: [{ type: "TIME_LIMIT", usageDetails: [{ modelCode: "search", usage: 1 }] }] } }], ["error", { code: 1309 }]]) add("zai", name, payload);
for (const [name, payload] of [
  ["legacy-first", { data: [null, {}, { productName: "Legacy", nextRenewTime: "2025-02-01anything" }, { productName: "Other" }] }],
  ["priority", { data: [{ productName: "Valid", status: "VALID" }, { productName: "Current", inCurrentPeriod: 1 }, { productName: "Best", status: " valid ", inCurrentPeriod: true, nextRenewTime: capturedAt }] }],
  ["current-only", { data: [{ productName: "Expired", status: "EXPIRED", inCurrentPeriod: true }, { productName: "Current", inCurrentPeriod: true }] }],
  ["valid-only", { data: [{ productName: "Inactive", inCurrentPeriod: false }, { productName: "Valid", status: "VALID", nextRenewTime: 0 }] }],
  ["inactive", { data: [{ productName: "Old", status: "EXPIRED" }, { productName: "Not current", status: "VALID", inCurrentPeriod: 0 }] }],
  ["bad-state", { data: [{ productName: "\u001b[31mSafe\u001b[0m\nPlan", status: "", inCurrentPeriod: "true", nextRenewTime: -1 }] }],
  ["expanded-year", { data: [{ productName: "Future", nextRenewTime: 253402300800000 }] }],
  ["invalid-date-discards-plan", { data: [{ productName: "Earlier" }, { productName: "Bad", nextRenewTime: 8640000000000001 }] }],
  ["failed", { success: false, data: [{ productName: "Not returned" }] }], ["not-array", { data: {} }],
]) add("zai", `plan-${name}`, payload, "plan");
for (const code of [1000, 1001, 1003, 1005, 1113, 1200, 1210, 1211, 1212, 1213, 1214, 1215, 1220, 1221, 1222, 1230, 1234, 1261, 1301, 1302, 1305, 1308, 1309, 1310, 1311, 1313, 1314, 1315, 1316, 1317, 1318, 1319, 1320, 1321, 9999]) add("zai", `fixed-code-${code}`, { error: { code, message: "secret\u001b[31m" } }, "payload-error");
for (const [name, payload] of [["success", { code: 200 }], ["null-code", { code: null }], ["bad-code", { error: { code: "01309" } }], ["top-fallback", { code: "1309", error: {} }], ["nested-null", { code: 1309, error: { code: null } }], ["nested-success", { error: { code: 0 } }], ["false-success", { success: false }]]) add("zai", name, payload, "payload-error");
for (const [status, body] of [[200, "not json secret"], [200, "{}"], [401, "secret"], [403, "{}"], [400, "{}"], [429, "{}"], [500, "{}"], [503, "{}"], [402, '{"code":"1310"}']]) add("zai", `http-${status}`, {}, "response-error", { status, body });
const hooks = registerHooks({ resolve(specifier, context, nextResolve) {
  if (specifier === "@earendil-works/pi-coding-agent") return { url: "data:text/javascript,export function readStoredCredential(){throw new Error('Credential access forbidden')}", shortCircuit: true };
  return nextResolve(specifier, context);
} });
const { providerIsConfigured } = await import("../node_modules/@narumitw/pi-usage/src/query.ts");
for (const providerID of ["moonshotai", "moonshotai-cn"]) {
  for (const [name, selectedProviderID, source, sourceLabel] of [["current-shared", providerID, "environment", "MOONSHOT_API_KEY"], ["unknown-sibling", "other", undefined, undefined], ["stored-sibling", "other", "auth.json", undefined], ["shared-sibling", "other", "environment", "OTHER, MOONSHOT_API_KEY"], ["unknown-env", "other", "environment", undefined], ["regional-sibling", "other", "environment", "MOONSHOT_CN_API_KEY"], ["substring", "other", "environment", "OTHER_MOONSHOT_API_KEY"], ["empty-label", "other", "environment", ""]]) {
    const expected = providerIsConfigured({ model: { provider: selectedProviderID }, modelRegistry: { getProviderAuthStatus: () => ({ configured: true, source, label: sourceLabel }) } }, providerID);
    fixtures.push({ name: `${providerID}:auth-${name}`, kind: "auth", providerID, selectedProviderID, source, sourceLabel, expected });
  }
}
hooks.deregister();
const hashes = {};
for (const path of ["src/providers/minimax.ts", "src/providers/moonshot.ts", "src/providers/zai.ts", "src/providers/zai-errors.ts", "src/query.ts", "src/core.ts", "src/types.ts"]) hashes[path] = createHash("sha256").update(await readFile(new URL(`../node_modules/@narumitw/pi-usage/${path}`, import.meta.url))).digest("hex");
const output = `${JSON.stringify({ upstream: { name: pkg.name, version: pkg.version, hashes }, fixtures }, null, 2)}\n`;
const destination = new URL("../extensions/pig-status/internal/usage/testdata/regional-reference.json", import.meta.url);
if (process.argv[2] === "--check") assert.equal(await readFile(destination, "utf8"), output, "Regional reference differs from actual pinned source");
else { assert.equal(process.argv.length, 2); await writeFile(destination, output); }
console.log(`native regional reference: ${fixtures.length} cases match ${pkg.name} ${pkg.version}`);
