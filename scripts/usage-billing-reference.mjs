import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import pkg from "../node_modules/@narumitw/pi-usage/package.json" with { type: "json" };
import { normalizeBasetenBillingUsagePayload } from "../node_modules/@narumitw/pi-usage/src/providers/baseten.ts";
import { normalizeOpenRouterKeyPayload } from "../node_modules/@narumitw/pi-usage/src/providers/openrouter.ts";
import { normalizeFireworksBillingSummaryPayload, normalizeFireworksAccountsPayload } from "../node_modules/@narumitw/pi-usage/src/providers/fireworks.ts";

assert.equal(pkg.version, "0.68.3");
const capturedAt = 1735776000000;
const accountID = "synthetic-account";
const fixtures = [];
function add(providerID, name, payload, kind = "report") {
  const fixture = { name: `${providerID}:${name}`, providerID, kind, capturedAt, payload };
  if (providerID === "fireworks" && kind === "report") fixture.accountID = accountID;
  try {
    fixture.expected = kind === "accounts" ? normalizeFireworksAccountsPayload(payload)
      : providerID === "fireworks" ? normalizeFireworksBillingSummaryPayload(payload, accountID, capturedAt)
      : providerID === "baseten" ? normalizeBasetenBillingUsagePayload(payload, capturedAt)
      : normalizeOpenRouterKeyPayload(payload, capturedAt);
  } catch (error) {
    if (!(error instanceof Error) || error.name !== "Error" || error.code) throw error;
    fixture.error = error.message;
    if (kind === "accounts" && name === "accounts-duplicate") {
      fixture.nativeError = "Fireworks accounts response repeated an account.";
    }
  }
  fixtures.push(fixture);
}
for (const [name, usage] of [
  ["exact-decimals", { total: "12345678901234567890.000000001", credits_used: "2.500", subtotal: "0.000000001" }],
  ["numbers", { total: 1.25, credits_used: 0, subtotal: 0.000001 }],
  ["missing", undefined], ["null", null], ["bad-object", []],
]) add("baseten", name, { model_apis_usage: usage, dedicated_usage: { total: 10 }, training_usage: { total: 20 } });
for (const [name, value] of [["negative", -1], ["signed-zero", "-0"], ["exponent", 1e-7], ["leading-zero", "01"], ["boolean", true], ["long", "1".repeat(65)], ["missing-total", undefined]]) {
  add("baseten", name, { model_apis_usage: { total: value, credits_used: "0", subtotal: "1" } });
}
for (const field of ["credits_used", "subtotal"]) {
  add("baseten", `bad-${field}`, { model_apis_usage: { total: "10", credits_used: "1", subtotal: "9", [field]: null } });
}
add("openrouter", "limit-and-spend", { data: { limit: 10, limit_remaining: 4, limit_reset: " daily\nwindow ", usage_daily: 1, usage_weekly: 2, usage_monthly: 3, usage: 6, label: "\u001b[31mSynthetic\u001b[0m\nkey", is_free_tier: true } });
add("openrouter", "uncapped", { data: { limit: null, usage: 0 } });
add("openrouter", "unknown-cap", { data: { usage_daily: 0.000001 } });
add("openrouter", "unknown-remaining", { data: { limit: 10, limit_remaining: "4", usage: "6" } });
add("openrouter", "remaining-over-limit", { data: { limit: 0, limit_remaining: 1 } });
add("openrouter", "invalid-optionals", { data: { limit: -1, usage: 3, usage_daily: -1, usage_weekly: "2", usage_monthly: null, is_free_tier: "true", label: "x".repeat(100) } });
for (const [name, payload] of [["missing-data", {}], ["bad-data", { data: [] }], ["no-displayable", { data: { limit: null, usage: "1" } }]]) add("openrouter", name, payload);
function item(currency, units, nanos, series) {
  return { totalCost: { currencyCode: currency, ...(units === undefined ? {} : { units }), ...(nanos === undefined ? {} : { nanos }) }, ...(series === undefined ? {} : { series }) };
}
add("fireworks", "currencies-series-exact", { lineItems: [
  item("EUR", "1", 500000001, "TRAINING"), item("USD", "9223372036854775807", 999999999, "SERVERLESS"),
  item("USD", "9223372036854775807", 999999999, "SERVERLESS"), item("EUR", "-1", -500000000, "DEDICATED_DEPLOYMENT"),
  item("USD", "-9223372036854775808", -999999999, "TRAINING"), item("JPY", undefined, undefined),
  item("EUR", 0, -1, "FUTURE_SERIES"), item("USD", "0002", "000000001", null),
] });
add("fireworks", "zero-sum-preserves-series", { lineItems: [item("USD", 0, 1, "SERVERLESS"), item("USD", 0, -1, "SERVERLESS")] });
add("fireworks", "negative-fraction", { lineItems: [item("USD", "0", "-100000001", "TRAINING")] });
add("fireworks", "empty", {});
add("fireworks", "empty-array", { lineItems: [] });
for (const [name, payload] of [["null-lines", { lineItems: null }], ["bad-lines", { lineItems: {} }], ["bad-row", { lineItems: [null] }], ["bad-money", { lineItems: [{ totalCost: [] }] }], ["bad-series", { lineItems: [item("USD", "0", 0, 5)] }]]) add("fireworks", name, payload);
for (const [name, key, value] of [
  ["currency-case", "currencyCode", "usd"], ["currency-long", "currencyCode", "USDX"], ["currency-missing", "currencyCode", undefined],
  ["null-units", "units", null], ["fractional-units", "units", 1.5], ["unsafe-number", "units", Number.MAX_SAFE_INTEGER + 1],
  ["safe-number", "units", Number.MAX_SAFE_INTEGER], ["int64-overflow", "units", "9223372036854775808"], ["int64-underflow", "units", "-9223372036854775809"],
  ["long-units", "units", "0".repeat(21)], ["units-plus", "units", "+1"], ["units-whitespace", "units", " 1"],
  ["null-nanos", "nanos", null], ["nanos-upper", "nanos", 1000000000], ["nanos-lower", "nanos", -1000000000],
  ["long-nanos", "nanos", "0".repeat(12)], ["nanos-fraction", "nanos", 0.1], ["mixed-signs", "nanos", -1],
]) {
  const row = item("USD", "1", 0, "SERVERLESS");
  row.totalCost[key] = value;
  add("fireworks", name, { lineItems: [row] });
}
for (const [name, payload] of [
  ["ordered", { accounts: [{ name: "accounts/one._~-2" }, { name: "accounts/Two" }] }], ["empty", { accounts: [] }],
  ["missing", {}], ["bad-array", { accounts: null }], ["bad-row", { accounts: [null] }], ["missing-name", { accounts: [{}] }],
  ["unsafe-name", { accounts: [{ name: "accounts/../other" }] }], ["wrong-prefix", { accounts: [{ name: "projects/account" }] }],
  ["duplicate", { accounts: [{ name: "accounts/synthetic-account" }, { name: "accounts/synthetic-account" }] }],
]) add("fireworks", `accounts-${name}`, payload, "accounts");
const hashes = {};
for (const path of ["src/providers/baseten.ts", "src/providers/openrouter.ts", "src/providers/fireworks.ts", "src/query.ts", "src/types.ts", "src/core.ts"]) {
  hashes[path] = createHash("sha256").update(await readFile(new URL(`../node_modules/@narumitw/pi-usage/${path}`, import.meta.url))).digest("hex");
}
const output = `${JSON.stringify({ upstream: { name: pkg.name, version: pkg.version, hashes }, fixtures }, null, 2)}\n`;
const destination = new URL("../extensions/pig-status/internal/usage/testdata/billing-reference.json", import.meta.url);
if (process.argv[2] === "--check") {
  assert.equal(await readFile(destination, "utf8"), output, "Billing reference differs from installed pinned source");
} else {
  assert.equal(process.argv.length, 2, "Expected no arguments or --check");
  await writeFile(destination, output);
}
console.log(`native billing reference: ${fixtures.length} cases match ${pkg.name} ${pkg.version}`);
