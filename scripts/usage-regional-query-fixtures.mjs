/** Pinned-source oracle for the regional MiniMax, Z.AI and Moonshot Query seam. */
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { registerHooks } from "node:module";
import pkg from "../node_modules/@narumitw/pi-usage/package.json" with { type: "json" };
import { normalizeMiniMaxUsagePayload } from "../node_modules/@narumitw/pi-usage/src/providers/minimax.ts";
import { normalizeMoonshotBalancePayload } from "../node_modules/@narumitw/pi-usage/src/providers/moonshot.ts";

assert.equal(pkg.version, "0.68.3");
registerHooks({ resolve(specifier, context, nextResolve) {
  if (specifier === "@earendil-works/pi-coding-agent") return { url: "data:text/javascript,export function readStoredCredential(){throw new Error('credential reads forbidden')};export function getAgentDir(){throw new Error('personal roots forbidden')}", shortCircuit: true };
  return nextResolve(specifier, context);
} });
const { adapterForProvider } = await import("../node_modules/@narumitw/pi-usage/src/query.ts");

const root = new URL("../", import.meta.url);
const output = new URL("extensions/pig-status/internal/usage/testdata/regional-query-acceptance.json", root);
const sources = ["providers/minimax.ts", "providers/moonshot.ts", "providers/zai.ts", "providers/zai-errors.ts", "query.ts"].map((path) => `node_modules/@narumitw/pi-usage/src/${path}`);
const capturedAt = 1800000000123;
const bases = {
  minimax: "https://api.minimax.io/v1", "minimax-cn": "https://api.minimaxi.com/v1",
  zai: "https://api.z.ai/v1", "zai-coding-cn": "https://open.bigmodel.cn/v1",
  moonshotai: "https://api.moonshot.ai/v1", "moonshotai-cn": "https://api.moonshot.cn/v1",
};
const canary = "raw-provider-canary fixture-secret";

const row = { model_name: "MiniMax M2", current_interval_total_count: 100, current_interval_usage_count: 20, current_interval_remaining_percent: 80, current_interval_status: 1, start_time: 1800000000001, end_time: 1800018000999, current_weekly_total_count: 200, current_weekly_usage_count: 150, current_weekly_remaining_percent: 75, current_weekly_status: 2, weekly_start_time: 1800000000001, weekly_end_time: 1800604800999, weekly_boost_permille: 1500 };
const balance = { base_resp: { status_code: 0 }, available_amount: "12345678901234567890.000000001", cash_balance: "-0.000000002", voucher_balance: "0", credit_balance: "2.000", owed_amount: "0.000000003" };
const tokenPlan = (change = () => {}) => {
  const payload = { base_resp: { status_code: 0 }, model_remains: [structuredClone(row)] };
  change(payload, payload.model_remains[0]);
  return payload;
};
const minimaxCases = [
  ["balance", "account-balance", balance],
  ["count-directions", "token-plan", tokenPlan()],
  ["model-groups", "token-plan", tokenPlan((p) => { p.model_remains.push({ ...row, model_name: " MiniMax\u001b[31m M2 " }, { ...row, model_name: "中文", current_interval_status: 3, current_weekly_status: 3 }); })],
  ["zero-count-percent", "token-plan", tokenPlan((_, r) => { Object.assign(r, { current_interval_total_count: 0, current_interval_usage_count: 0, current_interval_remaining_percent: 12.5, current_weekly_total_count: 0, current_weekly_usage_count: 0, current_weekly_remaining_percent: 0 }); })],
  ["missing-percent-means-remaining", "token-plan", tokenPlan((_, r) => { delete r.current_interval_remaining_percent; delete r.current_weekly_remaining_percent; })],
  ["balance-numeric-not-decimal", "account-balance", { ...balance, available_amount: 1 }],
  ["balance-negative-available", "account-balance", { ...balance, available_amount: "-1" }],
  ["failed-success-code", "token-plan", tokenPlan((p) => { p.base_resp = { status_code: 1, status_msg: canary }; })],
  ["no-rows", "token-plan", tokenPlan((p) => { p.model_remains = []; })],
  ["row-not-object", "token-plan", tokenPlan((p) => { p.model_remains = [null]; })],
  ["counts-inconsistent", "token-plan", tokenPlan((_, r) => { r.current_interval_remaining_percent = 45; })],
  ["timestamps-reversed", "token-plan", tokenPlan((_, r) => { r.end_time = 1; })],
  ["unsupported-status", "token-plan", tokenPlan((_, r) => { r.current_interval_status = 4; })],
  ["unsafe-integer", "token-plan", tokenPlan((_, r) => { r.current_interval_total_count = Number.MAX_SAFE_INTEGER + 1; })],
  ["invalid-boost", "token-plan", tokenPlan((_, r) => { r.weekly_boost_permille = 10001; })],
];

const quota = { success: true, code: 200, data: { level: "Quota Pro", limits: [
  null,
  { type: "IGNORED", unit: 3, percentage: 3 },
  { type: "TOKENS_LIMIT", unit: 3, number: 2.5, percentage: 12.5, nextResetTime: 1800000000999 },
  { type: "CREDIT_LIMIT", unit: 6, number: 2, currentValue: 120, usage: 100, nextResetTime: 1800604800999 },
  { type: "TIME_LIMIT", currentValue: 3, usage: 10, usageDetails: [{ modelCode: "Web Search", usage: 2 }, { modelCode: "", usage: 4 }, null] },
] } };
const zaiQuotaCases = [
  ["quota", quota],
  ["current-plan", quota, { data: [{ productName: "Expired", status: "INVALID", inCurrentPeriod: false }, { productName: "Current Pro", status: "VALID", inCurrentPeriod: 1, nextRenewTime: 1800000000000 }] }],
  ["legacy-plan-first", quota, { data: [{ productName: "First", nextRenewTime: "2028-04-03T12:00:00Z" }, { productName: "Second" }] }],
  ["inactive-plan-fallback", quota, { data: [{ productName: "Inactive", status: "INVALID" }] }],
  ["invalid-renewal-fallback", quota, { data: [{ productName: "Unusable", nextRenewTime: 1e20 }] }],
  ["percent-fallback-and-clamp", { data: { limits: [{ type: "TOKENS_LIMIT", unit: 3, percentage: 150 }, { type: "CREDIT_LIMIT", unit: 6, percentage: 5, nextResetTime: 1001 }] } }],
  ["invalid-optional-plan", quota, { success: false, error: { code: 1000, message: canary } }],
  ["no-displayable", { data: { limits: [null, { type: "TOKENS_LIMIT", unit: 3, percentage: -1 }] } }],
  ["no-data", { data: null }],
  ["payload-error", { success: false, code: 1308, message: canary }],
];
const zaiErrorCases = [
  ["error-known-code", 403, JSON.stringify({ error: { code: 1317, message: `${canary}\u001b[31m` } })],
  ["error-nested-missing-code", 200, JSON.stringify({ code: 1003, error: { message: "fixture-secret" } })],
  ["error-nested-priority", 500, JSON.stringify({ code: 1000, error: { code: 1302 } })],
  ["error-invalid-string-code", 429, JSON.stringify({ success: false, code: "01317", message: "fixture-secret" })],
  ["error-out-of-range-code", 400, JSON.stringify({ error: { code: 10000 } })],
  ["error-http-fallback", 401, "{}"],
  ["error-http-malformed", 503, `${canary}\u001b[31m`],
  ["error-success-malformed", 200, `${canary}\u001b[31m`],
  ["error-unknown-numeric", 500, JSON.stringify({ error: { code: 9876 } })],
];
const overflow = (unit) => ({ data: { limits: [{ type: "TOKENS_LIMIT", unit, number: Number.MAX_VALUE, percentage: 25 }], account: "account-canary-89342", message: `${canary}\u001b[31m` } });
const moonshotBalance = { code: 0, status: true, data: { available_balance: 1.25, voucher_balance: 0.25, cash_balance: -0.5 } };

function settle(fixture, run) {
  try {
    fixture.expected = run();
  } catch (error) {
    if (!(error instanceof Error) || error.name !== "Error") throw error;
    fixture.error = error.message;
  }
  return fixture;
}

async function query(fixture, respond) {
  const requestPaths = [];
  const queryEvents = [];
  const originalFetch = globalThis.fetch;
  const originalNow = Date.now;
  globalThis.fetch = async (input) => {
    const url = new URL(input);
    assert.equal(url.origin, new URL(fixture.base).origin);
    requestPaths.push(url.pathname);
    queryEvents.push(url.pathname);
    return respond(url.pathname);
  };
  Date.now = () => capturedAt;
  const auth = { model: { baseUrl: fixture.base }, headers: { Authorization: "Bearer fixture-secret" }, secrets: ["fixture-secret"] };
  try {
    return { report: await adapterForProvider(fixture.provider).query(auth, new AbortController().signal, 10000, async () => { queryEvents.push("guard"); }) };
  } catch (error) {
    if (!(error instanceof Error) || error.name !== "Error") throw error;
    return { error: error.message };
  } finally {
    globalThis.fetch = originalFetch;
    Date.now = originalNow;
    Object.assign(fixture, { requestPaths, queryEvents });
  }
}

const planResponse = (plan) => Response.json(plan ?? { data: [] });

async function zaiQuota(provider, name, payload, plan) {
  const fixture = { provider, name: `${provider}:${name}`, base: bases[provider], kind: "quota", payload, ...(plan ? { plan } : {}) };
  const result = await query(fixture, (path) => path === "/api/biz/subscription/list" ? planResponse(plan) : Response.json(payload));
  return orderQueryResult(fixture, result.error === undefined ? { expected: result.report } : { error: result.error });
}

async function zaiError(provider, name, status, raw) {
  const fixture = { provider, name: `${provider}:${name}`, base: bases[provider], kind: "error", status, raw };
  const result = await query(fixture, (path) => path === "/api/biz/subscription/list" ? planResponse() : new Response(raw, { status }));
  assert.equal(result.report, undefined, `${fixture.name} must fail in the pinned source`);
  return orderQueryResult(fixture, { error: result.error });
}

async function zaiOverflow(provider, unit) {
  const payload = overflow(unit);
  const fixture = { provider, name: `${provider}:finite-${unit === 3 ? "hour" : "week"}-overflow`, base: bases[provider], kind: "quota", payload, nativeRefusal: true };
  const result = await query(fixture, (path) => path === "/api/biz/subscription/list" ? planResponse() : Response.json(payload));
  const windowMinutes = result.report?.buckets[0]?.windowMinutes;
  assert.equal(Number.isFinite(windowMinutes), false, `${fixture.name} must overflow in the pinned source`);
  return orderQueryResult(fixture, { upstream: { windowMinutes: String(windowMinutes), serializedReport: JSON.parse(JSON.stringify(result.report)) } });
}

function orderQueryResult(fixture, result) {
  const { requestPaths, queryEvents, ...inputs } = fixture;
  return { ...inputs, ...result, requestPaths, queryEvents };
}

const fixtures = [];
for (const provider of ["minimax", "minimax-cn"]) {
  for (const [name, kind, payload] of minimaxCases) {
    fixtures.push(settle({ provider, name: `${provider}:${name}`, base: bases[provider], kind, payload: structuredClone(payload) }, () => normalizeMiniMaxUsagePayload(provider, kind, payload, capturedAt)));
  }
}
for (const provider of ["zai", "zai-coding-cn"]) {
  for (const [name, payload, plan] of zaiQuotaCases) fixtures.push(await zaiQuota(provider, name, structuredClone(payload), plan && structuredClone(plan)));
  for (const [name, status, raw] of zaiErrorCases) fixtures.push(await zaiError(provider, name, status, raw));
}
for (const provider of ["zai", "zai-coding-cn"]) {
  for (const unit of [3, 6]) fixtures.push(await zaiOverflow(provider, unit));
}
for (const provider of ["moonshotai", "moonshotai-cn"]) {
  fixtures.push(settle({ provider, name: `${provider}:provenance-control`, base: bases[provider], kind: "balance", payload: structuredClone(moonshotBalance) }, () => normalizeMoonshotBalancePayload(provider, moonshotBalance, capturedAt)));
}

const hashes = await Promise.all(sources.map(async (path) => ({ path, sha256: createHash("sha256").update(await readFile(new URL(path, root))).digest("hex") })));
const bytes = `${JSON.stringify({ version: pkg.version, sources: hashes, fixtures }, null, 2)}\n`;
if (process.argv[2] === "--check") {
  assert.equal(await readFile(output, "utf8"), bytes, "Regional query fixtures differ from the pinned source; regenerate with node --import tsx scripts/usage-regional-query-fixtures.mjs");
  console.log(`regional query oracle: ${fixtures.length} fixtures, source drift check passed`);
} else {
  assert.equal(process.argv.length, 2, "Expected no arguments or --check");
  await writeFile(output, bytes);
  console.log(`regional query oracle: ${fixtures.length} fixtures captured`);
}
