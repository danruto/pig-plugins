/** Independent receipt checks and an exact registered-handler evidence/residual index, not qualification. */
import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { codexModelCost, lifecycleFixtures } from "./lifecycle.mjs";
import { readLedger } from "./ledger-artifact.mjs";

export function verifyLifecycle(expected) {
  assert.deepEqual(expected.lifecycle.map((row) => row.id), lifecycleFixtures.map((row) => row.id));
  const row = (name) => expected.lifecycle.find((item) => item.id === `lifecycle:${name}`);
  const ui = (name) => expected.commandObservations.filter((item) => item.command === `lifecycle:command:${name}`);
  const text = (name) => ui(name).map((item) => item.title ?? item.message).join("\n");
  const requests = (name) => expected.requests.filter((item) => item.id === `lifecycle:${name}`);
  assert.match(text("arguments"), /does not accept arguments/u);
  assert.match(text("no-other"), /No other supported provider has configured runtime auth/u);
  for (const [name, provider, origin] of [["current", "deepseek", "api.deepseek.com"], ["other", "openrouter", "openrouter.ai"]]) {
    const receipt = row(`command:${name}`);
    assert.ok(receipt.changes.some((change) => change.providerId === provider && change.reason === "updated"));
    assert.equal(requests(`command:${name}`).length, 1);
    assert.equal(new URL(requests(`command:${name}`)[0].url).hostname, origin);
    assert.match(text(name), name === "current" ? /USD balance/u : /OpenRouter Usage · Configured/u);
  }
  for (const name of ["current-failure", "other-failure", "target-failure"]) {
    assert.match(text(name), /Query failed:.*503/u);
    assert.match(text(name), /<redacted>/u);
    assert.doesNotMatch(text(name), /fixture-host-access|Bearer fixture-access/u);
    assert.equal(requests(`command:${name}`).length, 1);
    assert.ok(row(`command:${name}`).changes.some((change) => change.reason === "query-failed"));
  }
  assert.match(text("target-cancel"), /Select account for Fireworks/u);
  assert.equal(row("command:target-cancel").settings, undefined);
  assert.equal(requests("command:target-cancel").some((request) => request.url.includes("/billing/")), false);
  assert.deepEqual(row("command:target").settings, { selectedTargets: { fireworks: "fixture-other" } });
  assert.match(text("target"), /Account: fixture-other/u);
  const billing = requests("command:target").filter((request) => request.url.includes("/billing/"));
  assert.equal(billing.length, 1);
  assert.match(billing[0].url, /accounts\/fixture-other\/billing\/summary\?/u);
  assert.match(text("all-partial"), /DeepSeek API Balance · Current/u);
  assert.match(text("all-partial"), /OpenRouter · Configured\nQuery failed/u);
  assert.match(text("all-partial"), /Command Code plan details were unavailable/u);
  assert.match(text("all-partial"), /Fireworks API Spend · Configured/u);
  assert.deepEqual([...new Set(requests("command:all-partial").map((request) => new URL(request.url).hostname))].sort(), ["api.commandcode.ai", "api.deepseek.com", "api.fireworks.ai", "openrouter.ai"]);
  for (const [name, provider, measurements] of [["capabilities-current", "deepseek", ["balance"]], ["capabilities-other", "fireworks", ["spend"]]]) {
    assert.deepEqual(row(`api:${name}`).response, { status: "ready", providerId: provider, supported: true, measurements, supportsRefresh: true });
    assert.deepEqual(requests(`api:${name}`), []);
  }
  for (const [name, provider, fixture] of [["query-current", "deepseek", "deepseek:normal"], ["query-other", "openrouter", "openrouter:normal"], ["query-target", "fireworks", "fireworks:remembered-target"], ["query-partial", "command-code", "command-code:optional-failure"]]) {
    const response = row(`api:${name}`).response;
    assert.equal(response.status, "ready");
    assert.equal(response.providerId, provider);
    assert.equal(response.source, "refresh");
    assert.ok(response.expiresAt > response.report.capturedAt);
    if (expected.results.length) {
      const baseline = expected.results.find((item) => item.implementation === "dist" && item.id === fixture).apiReport;
      assert.deepEqual(response.report, { ...baseline, providerName: response.report.providerName });
    }
    assert.ok(requests(`api:${name}`).length > 0);
  }
  for (const [name, provider, reason] of [["query-selection", "fireworks", "selection-required"], ["query-cache-only", "openrouter", "cache-miss"], ["query-unsupported", "fixture-unsupported", "unsupported"], ["query-auth-unavailable", "xai", "auth-unavailable"], ["query-failure", "deepseek", "query-failed"], ["cancel-inflight", "deepseek", "cancelled"], ["stale-model", "deepseek", "stale-context"]]) {
    assert.deepEqual(row(`api:${name}`).response, { status: "unavailable", providerId: provider, reason });
  }
  for (const name of ["cancel-inflight", "stale-model"]) {
    const receipt = row(`api:${name}`);
    assert.deepEqual(receipt.trace.filter((event) => event.kind !== "request"), [{ kind: "response-held" }, { kind: "response-released" }]);
    assert.equal(receipt.trace[0].url, "https://api.deepseek.com/user/balance");
    assert.equal(receipt.changes.some((change) => change.providerId === "deepseek" && change.reason === "updated"), false);
  }
  const successorStatuses = expected.commandObservations.filter((item) => item.command === "lifecycle:api:stale-model" && item.method === "setStatus");
  assert.ok(successorStatuses.some((item) => item.statusKey === "usage" && item.statusText === "openrouter $74.50 left"));
  assert.equal(successorStatuses.some((item) => item.statusText?.startsWith("deepseek ")), false);
  const stale = row("api:stale-model");
  assert.equal(stale.successor.status, "ready");
  assert.equal(stale.successor.providerId, "openrouter");
  assert.ok(stale.changes.some((change) => change.providerId === "deepseek" && change.reason === "invalidated"));
  assert.ok(stale.changes.some((change) => change.providerId === "openrouter" && change.reason === "updated"));
  assert.deepEqual(row("api:cancel-inflight").changes, []);
  verifyCodexLifecycle(expected);
}

function verifyCodexLifecycle(expected) {
  const row = (name) => expected.lifecycle.find((item) => item.id === `lifecycle:${name}`);
  const ui = (name) => expected.commandObservations.filter((item) => item.command === `lifecycle:${name}`);
  const text = (name) => ui(name).map((item) => item.title ?? item.message ?? item.statusText).join("\n");
  const requests = (name) => expected.requests.filter((item) => item.id === `lifecycle:${name}`);
  const routes = (name) => requests(name).map((item) => `${item.method} ${new URL(item.url).pathname}`);
  const consumes = (name) => requests(name).filter((item) => item.url.endsWith("/consume")).map((item) => JSON.parse(item.body));
  const credits = "GET /backend-api/wham/rate-limit-reset-credits";
  const consume = "POST /backend-api/wham/rate-limit-reset-credits/consume";
  const usage = "GET /backend-api/wham/usage";
  const models = "GET /backend-api/codex/models";
  const accessToken = /fixture\.eyJ[\w-]+\.signature/u;
  for (const item of expected.requests.filter((request) => /^lifecycle:(codex|reset|fast):/u.test(request.id))) {
    assert.equal(new URL(item.url).origin, "https://chatgpt.com");
    assert.match(item.headers.authorization, /^Bearer fixture\.eyJ/u);
    if (!item.url.endsWith("/wham/usage")) assert.ok(item.headers["chatgpt-account-id"], `Missing Codex account header: ${item.id}`);
    if (item.url.includes("/codex/models")) assert.equal(item.redirect, "error");
  }
  for (const item of expected.commandObservations.filter((observation) => /^lifecycle:(codex|reset|fast):/u.test(observation.command))) {
    assert.doesNotMatch(JSON.stringify(item), accessToken, `Codex credential disclosed in UI: ${item.command}`);
  }

  assert.deepEqual(routes("codex:select"), [usage, models]);
  assert.ok(ui("codex:select").some((item) => item.method === "setStatus" && /^codex \d/u.test(item.statusText)));
  assert.ok(row("codex:select").changes.some((change) => change.providerId === "openai-codex" && change.reason === "updated"));

  assert.deepEqual(routes("reset:redeem"), [credits, consume, usage]);
  assert.deepEqual(consumes("reset:redeem").map((body) => body.credit_id), ["credit-later"]);
  assert.match(text("reset:redeem"), /Usage limit resets\n2 usage limit resets available\./u);
  assert.match(text("reset:redeem"), /Use this reset\?\nLater reset\nExpires .*\nLater reset restores the current windows\.\nThis consumes one earned reset for the current OpenAI Codex account\./u);
  assert.match(text("reset:redeem"), /Usage reset\. You have 1 usage limit reset left\./u);
  assert.deepEqual(row("reset:redeem").changes.map((change) => `${change.providerId}:${change.reason}`), ["openai-codex:invalidated", "openai-codex:updated"]);

  const retried = consumes("reset:retry");
  assert.equal(retried.length, 2);
  assert.deepEqual(retried[1], retried[0], "A retried reset must reuse its redemption identity");
  assert.equal(retried[0].credit_id, "credit-earlier");
  assert.match(text("reset:retry"), /Couldn't reset usage: Codex usage-limit reset consume endpoint returned 503[^\n]*<redacted>[^\n]*\. Try again with the same request\./u);
  assert.match(text("reset:retry"), /Usage reset was already completed\. You have 1 usage limit reset left\./u);
  assert.deepEqual(routes("reset:retry"), [credits, consume, consume, usage]);

  for (const name of ["reset:cancel", "reset:empty"]) assert.deepEqual(routes(name), [credits]);
  assert.deepEqual(row("reset:cancel").changes, []);
  assert.match(text("reset:empty"), /You don't have any usage limit resets available\./u);

  assert.deepEqual(routes("reset:fallback"), [credits, consume, usage]);
  assert.deepEqual(Object.keys(consumes("reset:fallback")[0]), ["redeem_request_id"], "A generic fallback reset must not invent a credit ID");
  assert.match(text("reset:fallback"), /Usage limit resets\n1 usage limit reset available\./u);
  assert.match(text("reset:fallback"), /Use this reset\?\nFull reset\nDoes not expire\.\nReset your current usage limits\./u);
  assert.match(text("reset:fallback"), /Your usage does not need a reset right now\./u);

  const invalid = consumes("reset:invalid-outcome");
  assert.equal(invalid.length, 2);
  assert.deepEqual(invalid[1], invalid[0]);
  assert.match(text("reset:invalid-outcome"), /unknown outcome code/u);
  assert.match(text("reset:invalid-outcome"), /invalid windows_reset value/u);
  assert.match(text("reset:invalid-outcome"), /Couldn't reset usage\. Please try again\./u);
  assert.deepEqual(row("reset:invalid-outcome").changes, []);

  assert.deepEqual(routes("reset:no-credit"), [credits, consume, usage]);
  assert.match(text("reset:no-credit"), /No usage limit resets are available\./u);

  const identities = ["reset:redeem", "reset:retry", "reset:fallback", "reset:invalid-outcome", "reset:no-credit"].map((name) => consumes(name)[0].redeem_request_id);
  assert.equal(new Set(identities).size, identities.length, "Separate reset confirmations must not share a redemption identity");

  for (const name of ["reset:account-mismatch", "reset:disabled", "fast:enable", "fast:menu-disable", "fast:command-disable"]) assert.deepEqual(requests(name), [], `Unexpected provider request: ${name}`);
  assert.ok(ui("reset:account-mismatch").some((item) => item.method === "notify" && item.message === "Couldn't load usage limit resets: The OpenAI Codex OAuth credential did not include a valid account ID."));
  const disabledMenus = ui("reset:disabled").filter((item) => item.method === "select");
  assert.equal(disabledMenus.length, 2, "A disabled reset action must leave the usage menu open");
  assert.match(disabledMenus[0].title, /Usage limit resets: 0 available/u);

  assert.equal(row("fast:enable").settings.codexFastMode, true);
  assert.match(text("fast:enable"), /Codex Fast mode enabled\./u);
  assert.ok(ui("fast:enable").some((item) => item.method === "setStatus" && /^codex fast \d/u.test(item.statusText)));
  assert.equal(row("fast:menu-disable").settings.codexFastMode, false);
  assert.match(text("fast:menu-disable"), /Fast mode: On/u);
  assert.match(text("fast:menu-disable"), /Codex Fast mode disabled; standard routing will be used\./u);
  assert.deepEqual(routes("fast:menu-enable"), [usage]);
  assert.equal(row("fast:menu-enable").settings.codexFastMode, true);
  assert.match(text("fast:menu-enable"), /Fast mode: Off/u);
  assert.ok(ui("fast:menu-enable").some((item) => item.method === "setStatus" && /^codex fast \d/u.test(item.statusText)));
  assert.equal(row("fast:command-disable").settings.codexFastMode, false);
  assert.match(text("fast:command-disable"), /Codex Fast mode disabled; standard routing will be used\./u);
  assert.deepEqual(routes("fast:unknown-menu"), [models, usage]);
  assert.equal(requests("fast:unknown-menu")[0].headers["chatgpt-account-id"], "fixture-third-account");
  const unknownMenu = ui("fast:unknown-menu").find((item) => item.method === "select");
  assert.match(unknownMenu.title, /Fast mode: Unknown · Codex Fast capability is unknown: Codex model directory returned 503[^\n]*<redacted>/u);
  assert.equal(unknownMenu.options.some((option) => option.startsWith("Turn Fast mode")), false, "Unknown capability must not offer enabling Fast");
  assert.equal(row("fast:unknown-menu").settings.codexFastMode, false);
  for (const [name, account, message] of [["fast:unsupported", "fixture-second-account", /^fixture-lifecycle does not advertise Codex Fast support\.$/u], ["fast:directory-failure", "fixture-third-account", /^Codex Fast capability is unknown: Codex model directory returned 503[^\n]*<redacted>/u]]) {
    assert.deepEqual(routes(name), [models]);
    assert.equal(requests(name)[0].headers["chatgpt-account-id"], account);
    assert.ok(ui(name).some((item) => item.method === "notify" && item.notifyType === "warning" && message.test(item.message)));
    assert.equal(row(name).settings.codexFastMode, false);
  }
  const invalidModels = /^Codex Fast capability is unknown: The Codex model directory returned invalid models\.$/u;
  const invalidModel = /^Codex Fast capability is unknown: The Codex model directory returned an invalid or duplicate model ID\.$/u;
  const invalidTiers = /^Codex Fast capability is unknown: The Codex model directory returned invalid service tiers\.$/u;
  const unadvertised = /^fixture-lifecycle does not advertise Codex Fast support\.$/u;
  const noInformation = /^The Codex model directory has no Fast capability information for fixture-lifecycle\.$/u;
  for (const [name, message] of [["models-not-array", invalidModels], ["model-not-record", invalidModel], ["slug-not-string", invalidModel], ["slug-blank", invalidModel], ["slug-duplicate", invalidModel],
    ["tiers-not-array", invalidTiers], ["tier-not-record", invalidTiers], ["tier-id-not-string", invalidTiers], ["legacy-not-array", invalidTiers], ["legacy-not-string", invalidTiers],
    ["tiers-without-priority", unadvertised], ["no-tier-information", noInformation], ["model-absent", noInformation], ["models-empty", noInformation], ["forbidden", /^Codex Fast capability is unknown: Codex model directory returned 403 Forbidden: /u]]) {
    const fixture = `fast:directory-${name}`;
    assert.deepEqual(routes(fixture), [models], fixture);
    assert.equal(requests(fixture)[0].headers["chatgpt-account-id"], `fixture-directory-${name}`);
    assert.deepEqual(ui(fixture).map((item) => [item.method, item.notifyType]), [["notify", "warning"]], fixture);
    assert.match(ui(fixture)[0].message, message, fixture);
    assert.equal(row(fixture).settings.codexFastMode, false, fixture);
  }
  assert.deepEqual(routes("fast:directory-legacy-fast"), [models, usage]);
  assert.match(text("fast:directory-legacy-fast"), /Codex Fast mode enabled\./u);
  assert.equal(row("fast:directory-legacy-fast").settings.codexFastMode, true);

  for (const [name, message] of [["candidates-conflict", "Conflicting OAuth credentials match the active OpenAI Codex runtime account."], ["candidates-unready", "OpenAI Codex OAuth credential readiness failed closed."]]) {
    assert.deepEqual(requests(`reset:${name}`), [], `A failed reset credential check must not reach the reset endpoint: ${name}`);
    assert.ok(ui(`reset:${name}`).some((item) => item.method === "notify" && item.message === `Couldn't load usage limit resets: ${message}`), name);
  }
  assert.deepEqual(requests("footer:fence"), []);
  assert.deepEqual(row("footer:fence").trace.map((event) => [event.variant, event.footerQueried]),
    [["null", false], ["foreign-session", false], ["other-session-id", false], ["other-provider", false], ["current", true]], "The footer must refresh only for its own session, session ID and provider");
  assert.deepEqual([requests("api:query-backoff"), row("api:query-backoff").response], [[], { status: "unavailable", providerId: "deepseek", reason: "query-failed" }],
    "A refresh inside the failure backoff must answer from the recorded failure without a provider request");
  assert.deepEqual([requests("status:openai-api-key"), ui("status:openai-api-key").map((item) => item.statusText)], [[], ["checking", undefined]],
    "An API-key native OpenAI model must clear the usage status without a provider request");
  assert.deepEqual(requests("command:other-custom-origin"), [], "A provider registered with a custom base URL must never receive a usage credential");
  assert.ok(ui("command:other-custom-origin").some((item) => item.method === "select" && /^Provider usage\nBaseten · Configured\nAuthentication unavailable: No runtime credential is configured for Baseten\.$/u.test(item.title)));
  assert.ok(ui("command:other-custom-origin").some((item) => item.method === "select" && /Unsupported: OpenAI API-key usage reporting is not supported; ChatGPT plan status requires native \/login openai OAuth\./u.test(item.title)));
  assert.deepEqual(routes("reset:candidates-source-throws"), [credits], "PiG contains a throwing credential-source listener, so discovery proceeds to the reset endpoint");
  assert.equal(/discovery failed closed/u.test(text("reset:candidates-source-throws")), false);
  assert.ok(expected.bridgeHostErrors.includes("Event handler error (oauth:credential-source:v1): Error: Synthetic credential source failure"), "The host must log, not propagate, the listener failure");
  assert.deepEqual(routes("reset:candidates-readiness-throws"), [credits], "PiG contains a throwing credential-readiness listener, so readiness succeeds and discovery proceeds to the reset endpoint");
  assert.equal(/readiness failed closed/u.test(text("reset:candidates-readiness-throws")), false);
  assert.ok(expected.bridgeHostErrors.includes("Event handler error (oauth:credential-readiness:v1): Error: Synthetic credential readiness listener failure"), "The host must log, not propagate, the readiness listener failure");
  assert.deepEqual(routes("reset:consume-conflict"), [credits], "A conflicting credential at confirmation must not consume a reset");
  assert.match(text("reset:consume-conflict"), /Couldn't reset usage: Conflicting OAuth credentials match the active OpenAI Codex runtime account\./u);

  const responses = "POST /backend-api/codex/responses";
  const modelBodies = (name) => requests(name).filter((item) => item.method === "POST" && item.url.endsWith("/codex/responses")).map((item) => JSON.parse(item.body));
  const replies = (name) => ui(name).filter((item) => item.type === "message_end").map((item) => item.message);
  const standardCost = (usage) => Object.entries(codexModelCost).reduce((total, [key, rate]) => total + usage[key] * rate / 1_000_000, 0);
  const websocket = requests("fast:prompt-priority").filter((item) => item.method === "GET");
  assert.deepEqual(websocket.map((item) => [item.headers.upgrade, item.response]), [["websocket", 426]]);
  for (const [name, tier, multiplier, fastMode] of [["prompt-priority", "priority", 2, true], ["prompt-priced", "priority", 2, true], ["prompt-unknown", "default", 1, true], ["prompt-disabled", "default", 1, false]]) {
    const fixture = `fast:${name}`;
    assert.deepEqual(routes(fixture).filter((route) => route.endsWith("/codex/responses")), [...(name === "prompt-priority" ? ["GET /backend-api/codex/responses"] : []), responses], fixture);
    assert.deepEqual(modelBodies(fixture).map((body) => [body.model, body.service_tier]), [["fixture-lifecycle", tier]], fixture);
    const [reply] = replies(fixture);
    assert.equal(replies(fixture).length, 1, fixture);
    assert.equal(reply.model, "fixture-lifecycle");
    assert.ok(Math.abs(reply.usage.cost.total - multiplier * standardCost(reply.usage)) < 1e-12, `${fixture} cost ${reply.usage.cost.total}`);
    assert.equal(row(fixture).settings.codexFastMode, fastMode, fixture);
  }
  assert.match(text("fast:prompt-disabled"), /Codex Fast mode disabled; standard routing will be used\./u);

  const unknownStatus = /^codex \(Fast unknown\) \d/u;
  assert.deepEqual(routes("fast:unknown-account"), [usage]);
  assert.equal(row("fast:unknown-account").settings.codexFastMode, true);
  assert.deepEqual(routes("fast:unknown-status"), [models]);
  assert.equal(requests("fast:unknown-status")[0].headers["chatgpt-account-id"], "fixture-unknown-status");
  assert.deepEqual(ui("fast:unknown-status").map((item) => item.method === "setStatus" ? (unknownStatus.test(item.statusText) ? "unknown" : item.statusText) : item.method), ["checking", "unknown", "checking", "unknown", "select"],
    "The unfetched and the failed capability must each publish a decorated status before the menu");
  const unknownStatusMenu = ui("fast:unknown-status").findIndex((item) => item.method === "select");
  assert.match(ui("fast:unknown-status")[unknownStatusMenu].title, /Fast mode: Unknown · Codex Fast capability is unknown: Codex model directory returned 503[^\n]*<redacted>/u);
  assert.deepEqual(ui("fast:unknown-status")[unknownStatusMenu].options.filter((option) => option.startsWith("Turn Fast mode")), ["Turn Fast mode off"]);
  assert.equal(row("fast:unknown-status").settings.codexFastMode, true);
  assert.deepEqual(routes("fast:prompt-unknown"), [responses]);
  assert.deepEqual(ui("fast:prompt-unknown").filter((item) => item.method === "setStatus").map((item) => unknownStatus.test(item.statusText) ? "unknown" : item.statusText), ["checking", "unknown"]);

  assert.deepEqual([requests("accounts:ignored"), ui("accounts:ignored"), row("accounts:ignored").changes], [[], [], []], "A malformed or foreign account selection must not touch usage state");
  assert.deepEqual(ui("accounts:current").map((item) => [item.method, item.statusText === undefined ? undefined : item.statusText.replace(/\d.*$/u, "")]), [["setStatus", undefined], ["setStatus", "checking"], ["setStatus", "codex "]]);
  assert.deepEqual(routes("accounts:current"), [usage]);
  assert.deepEqual(row("accounts:current").changes.map((change) => `${change.providerId}:${change.reason}`), ["openai-codex:updated"]);
}

export async function lifecycleEvidence(project, capture) {
  const retained = join(project, "docs/compatibility/usage");
  const expected = JSON.parse(await readFile(join(retained, "expected.json"), "utf8"));
  const ledger = JSON.parse(await readLedger(join(retained, "branch-ledger.json.gz")));
  verifyLifecycle(expected);
  const fixtures = ledger.fixtureMappings.flatMap((mapping, index) => {
    if (!/^(lifecycle|protocol):/u.test(mapping.fixtureId)) return [];
    if (mapping.fixtureId.startsWith("lifecycle:")) assert.ok(mapping.productionDistBranchOutcomes.length > 0, `Missing production-dist witness: ${mapping.fixtureId}`);
    return [{ id: mapping.fixtureId, inputs: mapping.inputs, outputs: mapping.output, requests: mapping.requests,
      commandUI: expected.commandObservations.flatMap((row, uiIndex) => row.command === mapping.fixtureId ? [`expected.json#/commandObservations/${uiIndex}`] : []),
      branchEvidence: `branch-ledger.json#/fixtureMappings/${index}`, productionDistBranchOutcomes: mapping.productionDistBranchOutcomes }];
  });
  const family = ["src/usage.ts", "src/usage-api.ts", "src/usage-targets.ts", "src/usage-settings-ui.ts", "src/core.ts", "src/usage-helpers.ts"];
  const sourceOutcomes = ledger.obligations.flatMap((obligation, index) => {
    if (!family.includes(obligation.source)) return [];
    const evidence = obligation.evidence.filter((item) => item.implementation === "production-dist");
    const status = obligation.status === "out-of-scope" ? "out of scope (ADR 0004)" : evidence.length ? "witnessed registered dist" : "pending registered dist";
    return [{ ...obligation, evidence, status, ledger: `branch-ledger.json#/obligations/${index}` }];
  });
  const result = { generatedBy: "GENERATED FROM CODE; DO NOT EDIT. Run node probes/usage-baseline.mjs --capture", qualification: "INCOMPLETE; bounded registered Node slice, not full lifecycle parity; public usage API outcomes are out of scope (ADR 0004)", fixtures, sourceOutcomes };
  const path = join(retained, "lifecycle.json");
  if (capture) await writeFile(path, JSON.stringify(result, null, 2) + "\n");
  else assert.deepEqual(result, JSON.parse(await readFile(path, "utf8")), "Registered lifecycle evidence drift");
  console.log(`usage registered lifecycle: ${fixtures.length} fixture mappings; ${sourceOutcomes.filter((row) => row.status === "witnessed registered dist").length} witnessed family outcomes; ${sourceOutcomes.filter((row) => row.status === "pending registered dist").length} pending registered-dist outcomes; ${sourceOutcomes.filter((row) => row.status === "out of scope (ADR 0004)").length} out of scope`);
}
