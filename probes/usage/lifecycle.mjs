/** Probe-only controls around actual registered production commands/events in a disposable PiG host. */
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { beginBranchObservation, endBranchObservation, saveBranchObservations } from "./branch-runtime.mjs";
import { createResponseBarrier } from "./response-barrier.mjs";
import { writeHostAuth } from "./settings-auth.mjs";
import { cases, now } from "./cases.mjs";

const openrouter = "https://openrouter.ai/api/v1/key";
const deepseek = "https://api.deepseek.com/user/balance";
const whoami = "https://api.commandcode.ai/alpha/whoami";
const credits = "https://api.commandcode.ai/alpha/billing/credits?orgId=fixture-org";
const subscriptions = "https://api.commandcode.ai/alpha/billing/subscriptions?orgId=fixture-org";
const accounts = "https://api.fireworks.ai/v1/accounts?pageSize=200";
const summary = "https://api.commandcode.ai/alpha/usage/summary?orgId=fixture-org";
const billing = "https://api.fireworks.ai/v1/accounts/fixture-other/billing/summary?startTime=2030-02-16T00%3A00%3A00Z&endTime=2030-03-18T00%3A00%3A00Z";

export function codexAccess(accountId) {
  return `fixture.${Buffer.from(JSON.stringify({ "https://api.openai.com/auth": { chatgpt_account_id: accountId } })).toString("base64url")}.signature`;
}
const codexModel = { provider: "openai-codex", modelId: "fixture-lifecycle" };
const codexUsage = (available) => {
  const usage = structuredClone(cases.find((item) => item.id === "openai-codex:normal").routes.usage);
  usage.rate_limit_reset_credits.available_count = available;
  return usage;
};
const directory = (tiers) => ({ models: [{ slug: "fixture-lifecycle", ...tiers }, { slug: "fixture-other-model" }] });
const credit = (id, title, hours) => ({ id, status: "available", reset_type: "codex_rate_limits", title, description: `${title} restores the current windows.`, expires_at: new Date(now + hours * 3_600_000).toISOString() });
const resetCredits = { available_count: 2, credits: [credit("credit-later", "Later reset", 48), credit("credit-earlier", "Earlier reset", 24)] };
const unavailable = (secret) => ({ fixtureStatus: 503, body: { message: `${secret} fixture-refresh \u001b[31m` } });
const resetMenu = (...tail) => ["Redeem usage limit reset", ...tail];
const prompt = "Synthetic fixture prompt";
export const codexModelCost = { input: 2, output: 8, cacheRead: 0.5, cacheWrite: 0 };
const modelReply = (serviceTier) => [
  { type: "response.created", response: { id: "resp_fixture", status: "in_progress" } },
  { type: "response.output_item.added", output_index: 0, item: { type: "message", id: "msg_fixture", role: "assistant", status: "in_progress", content: [] } },
  { type: "response.content_part.added", item_id: "msg_fixture", output_index: 0, content_index: 0, part: { type: "output_text", text: "" } },
  { type: "response.output_text.delta", item_id: "msg_fixture", output_index: 0, content_index: 0, delta: "Synthetic fixture reply" },
  { type: "response.output_item.done", output_index: 0, item: { type: "message", id: "msg_fixture", role: "assistant", status: "completed", content: [{ type: "output_text", text: "Synthetic fixture reply", annotations: [] }] } },
  { type: "response.completed", response: { id: "resp_fixture", status: "completed", ...(serviceTier ? { service_tier: serviceTier } : {}),
    usage: { input_tokens: 1000, output_tokens: 500, total_tokens: 1500, input_tokens_details: { cached_tokens: 200 } } } },
];

export const lifecycleFixtures = [
  { id: "lifecycle:command:arguments", message: "/usage invalid", actions: [] },
  { id: "lifecycle:command:no-other", message: "/usage", actions: ["View another configured provider…", "Close"] },
  { id: "lifecycle:command:current", message: "/usage", actions: ["Refresh current usage", "Close"] },
  { id: "lifecycle:command:current-failure", message: "/usage", actions: ["Refresh current usage", "Close"] },
  { id: "lifecycle:command:other", message: "/usage", actions: ["View another configured provider…", "OpenRouter", "Close"] },
  { id: "lifecycle:command:other-failure", message: "/usage", actions: ["View another configured provider…", "OpenRouter", "Close"] },
  { id: "lifecycle:api:query-selection", options: { providerId: "fireworks", refresh: true } },
  { id: "lifecycle:command:target-cancel", message: "/usage", actions: ["View another configured provider…", "Fireworks", null, "Close"] },
  { id: "lifecycle:command:target", message: "/usage", actions: ["View another configured provider…", "Fireworks", "fixture-other", "Close"] },
  { id: "lifecycle:command:target-failure", message: "/usage", actions: ["View another configured provider…", "Fireworks", "Close"] },
  { id: "lifecycle:command:all-partial", message: "/usage", actions: ["View all configured providers…", "Close"], responseChoreography: [
    { url: openrouter, releaseAfterRequest: deepseek },
    { url: deepseek, releaseAfterRequest: subscriptions },
    { url: whoami },
    { url: credits, afterResponses: [whoami], releaseAfterRequest: subscriptions },
    { url: subscriptions, afterResponses: [whoami], releaseAfterRequest: accounts },
    { url: accounts, releaseAfterRequest: summary },
    { url: summary, afterResponses: [credits, subscriptions] },
    { url: billing, afterResponses: [accounts] },
  ] },
  ...[
    ["capabilities-current", { capabilities: true }],
    ["capabilities-other", { capabilities: true, providerId: "fireworks" }],
    ["query-current", { refresh: true }],
    ["query-other", { providerId: "openrouter", refresh: true }],
    ["query-cache-only", { providerId: "openrouter" }],
    ["query-unsupported", { providerId: "fixture-unsupported", refresh: true }],
    ["query-auth-unavailable", { providerId: "xai", refresh: true }],
    ["query-target", { providerId: "fireworks", refresh: true }],
    ["query-partial", { providerId: "command-code", refresh: true }],
    ["cancel-inflight", { providerId: "deepseek", refresh: true, barrier: true, cancel: true }],
    ["stale-model", { providerId: "deepseek", refresh: true, barrier: true, replaceModel: true }],
    ["query-failure", { providerId: "deepseek", refresh: true }],
    ["query-backoff", { providerId: "deepseek", refresh: true }],
  ].map(([name, options]) => ({ id: `lifecycle:api:${name}`, options })),
  { id: "lifecycle:codex:select", selectModel: codexModel, credential: {}, awaitStatus: "^codex \\d", responseOrder: ["models", "usage"], routes: { usage: codexUsage(2), models: directory({ service_tiers: [{ id: "priority" }] }) } },
  { id: "lifecycle:reset:redeem", message: "/usage", actions: resetMenu("Later reset", "Yes, use reset", "Close"),
    routes: { "rate-limit-reset-credits": resetCredits, consume: { code: "reset", windows_reset: 2 }, usage: codexUsage(1) } },
  ...["conflict", "unready"].map((mode) => ({ id: `lifecycle:reset:candidates-${mode}`, message: "/usage", actions: resetMenu("Close"), armCandidates: { after: "Redeem usage limit reset", mode } })),
  { id: "lifecycle:reset:candidates-source-throws", message: "/usage", actions: resetMenu(null, "Close"), armCandidates: { after: "Redeem usage limit reset", mode: "source-throws" },
    routes: { "rate-limit-reset-credits": resetCredits } },
  { id: "lifecycle:reset:candidates-readiness-throws", message: "/usage", actions: resetMenu(null, "Close"), armCandidates: { after: "Redeem usage limit reset", mode: "readiness-throws" },
    routes: { "rate-limit-reset-credits": resetCredits } },
  { id: "lifecycle:reset:consume-conflict", message: "/usage", actions: resetMenu("Earlier reset", "Yes, use reset", "Back", null), armCandidates: { after: "Yes, use reset", mode: "conflict" },
    routes: { "rate-limit-reset-credits": resetCredits } },
  { id: "lifecycle:reset:retry", message: "/usage", actions: resetMenu("Earlier reset", "Yes, use reset", "Try again", "Close"),
    routes: { "rate-limit-reset-credits": resetCredits, consume: { sequence: [unavailable(codexAccess("fixture-account")), { code: "already_redeemed" }] }, usage: codexUsage(1) } },
  { id: "lifecycle:reset:cancel", message: "/usage", actions: resetMenu("Earlier reset", "No, go back", null, "Close"), routes: { "rate-limit-reset-credits": resetCredits } },
  { id: "lifecycle:reset:fallback", message: "/usage", actions: resetMenu("Full reset", "Yes, use reset", "Back to usage", "Close"),
    routes: { "rate-limit-reset-credits": unavailable("fixture-unavailable"), consume: { code: "nothing_to_reset", windows_reset: "0" }, usage: codexUsage(1) } },
  { id: "lifecycle:reset:empty", message: "/usage", actions: resetMenu("Close"), routes: { "rate-limit-reset-credits": { available_count: 0, credits: [] } } },
  { id: "lifecycle:reset:invalid-outcome", message: "/usage", actions: resetMenu("Earlier reset", "Yes, use reset", "Try again", "Back", null, null, null, null, null, "Close"),
    routes: { "rate-limit-reset-credits": resetCredits, consume: { sequence: [{ code: "fixture-unknown" }, { code: "no_credit", windows_reset: -1 }] } } },
  { id: "lifecycle:reset:account-mismatch", message: "/usage", credential: { storedAccountId: "fixture-other-account" }, actions: resetMenu("Close") },
  { id: "lifecycle:reset:no-credit", message: "/usage", credential: {}, actions: resetMenu("Earlier reset", "Yes, use reset", "Close"),
    routes: { "rate-limit-reset-credits": resetCredits, consume: { code: "no_credit" }, usage: codexUsage(0) } },
  { id: "lifecycle:reset:disabled", message: "/usage", actions: resetMenu("Close") },
  { id: "lifecycle:fast:enable", message: "/fast", awaitStatus: "^codex fast \\d" },
  { id: "lifecycle:fast:menu-disable", message: "/usage", actions: ["Turn Fast mode off"], awaitStatus: "^codex \\d" },
  { id: "lifecycle:fast:unknown-menu", message: "/usage", credential: { token: "fixture-third-account" }, actions: ["Close"],
    routes: { usage: codexUsage(1), models: unavailable(codexAccess("fixture-third-account")) } },
  { id: "lifecycle:fast:menu-enable", message: "/usage", credential: {}, actions: ["Turn Fast mode on"], awaitStatus: "^codex fast \\d", routes: { usage: codexUsage(1) } },
  { id: "lifecycle:fast:command-disable", message: "/fast", awaitStatus: "^codex \\d" },
  { id: "lifecycle:fast:unsupported", message: "/fast", credential: { token: "fixture-second-account" }, routes: { models: directory({ additional_speed_tiers: ["standard"] }) } },
  { id: "lifecycle:fast:directory-failure", message: "/fast", credential: { token: "fixture-third-account" }, routes: { models: unavailable(codexAccess("fixture-third-account")) } },
  ...[
    ["models-not-array", { models: "fixture" }],
    ["models-empty", { models: [] }],
    ["model-not-record", { models: [42] }],
    ["slug-not-string", { models: [{ slug: 42 }] }],
    ["slug-blank", { models: [{ slug: " " }] }],
    ["slug-duplicate", { models: [{ slug: "fixture-lifecycle" }, { slug: "fixture-lifecycle" }] }],
    ["tiers-not-array", directory({ service_tiers: "priority" })],
    ["tier-not-record", directory({ service_tiers: ["priority"] })],
    ["tier-id-not-string", directory({ service_tiers: [{ id: 1 }] })],
    ["legacy-not-array", directory({ additional_speed_tiers: "fast" })],
    ["legacy-not-string", directory({ additional_speed_tiers: [1] })],
    ["tiers-without-priority", directory({ service_tiers: [{ id: "standard" }] })],
    ["no-tier-information", directory({})],
    ["model-absent", { models: [{ slug: "fixture-other-model", service_tiers: [{ id: "priority" }] }] }],
    ["forbidden", { fixtureStatus: 403, body: { message: "fixture-forbidden" } }],
    ["legacy-fast", directory({ additional_speed_tiers: ["standard", "fast"] })],
  ].map(([name, models]) => ({ id: `lifecycle:fast:directory-${name}`, message: "/fast", credential: { token: `fixture-directory-${name}` }, routes: { models, ...(name === "legacy-fast" ? { usage: codexUsage(1) } : {}) },
    ...(name === "legacy-fast" ? { awaitStatus: "^codex fast \\d" } : {}) })),
  { id: "lifecycle:fast:prompt-priority", prompt, modelResponses: [modelReply()] },
  { id: "lifecycle:fast:prompt-priced", prompt, modelResponses: [modelReply("priority")] },
  { id: "lifecycle:fast:unknown-account", accounts: ["current"], credential: { token: "fixture-unknown-status" }, awaitStatus: "^codex fast \\d", routes: { usage: codexUsage(1) } },
  { id: "lifecycle:fast:unknown-status", message: "/usage", actions: ["Close"], routes: { models: unavailable(codexAccess("fixture-unknown-status")) },
    statusGates: { models: "^codex \\(Fast unknown\\) \\d" } },
  { id: "lifecycle:fast:prompt-unknown", prompt, modelResponses: [modelReply()] },
  { id: "lifecycle:fast:prompt-disabled", message: "/fast", messageStatus: "^codex \\d", prompt, awaitStatus: "^codex \\d", modelResponses: [modelReply()] },
  { id: "lifecycle:accounts:ignored", accounts: ["null", "string", "array", "throwing-getter", "missing-session", "string-session", "array-session", "numeric-session-id", "numeric-provider", "foreign-session", "other-session-id", "other-provider"] },
  { id: "lifecycle:accounts:current", accounts: ["current"], awaitStatus: "^codex \\d", routes: { usage: codexUsage(1) } },
  { id: "lifecycle:footer:fence", changed: ["null", "foreign-session", "other-session-id", "other-provider", "current"] },
  { id: "lifecycle:status:openai-api-key", register: { provider: "openai", baseUrl: "https://api.openai.com/v1" }, selectModel: { provider: "openai", modelId: "fixture-lifecycle" }, awaitStatus: "^undefined$" },
  { id: "lifecycle:command:other-custom-origin", register: { provider: "baseten", baseUrl: "https://proxy.fixture.invalid/v1" }, message: "/usage", actions: ["View another configured provider…", "Baseten", "Close"] },
];
export const codexBearers = [...new Set(lifecycleFixtures.flatMap((fixture) => fixture.credential ? [fixture.credential.token ?? "fixture-account"] : []))].map((accountId) => `Bearer ${codexAccess(accountId)}`);
const lifecycleById = new Map(lifecycleFixtures.map((fixture) => [fixture.id, fixture]));

export function plannedLifecycleResponse(id, route, ordinal) {
  const planned = lifecycleById.get(id)?.routes?.[route];
  return planned?.sequence ? planned.sequence[ordinal] : planned;
}

export function serverSentEvents(events) {
  return events.map((event) => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join("");
}

export function portableModelRequestBody(body, root) {
  const payload = JSON.parse(body.replaceAll(root, "$ROOT"));
  assert.equal(typeof payload.prompt_cache_key, "string", "Codex model request must carry its session cache key");
  return JSON.stringify({ ...payload, prompt_cache_key: "$SESSION" });
}

export function assertPlannedResponsesUsed(ordinals, modelOrdinals) {
  for (const fixture of lifecycleFixtures) {
    if (fixture.modelResponses) assert.equal(modelOrdinals.get(fixture.id) ?? 0, fixture.modelResponses.length, `Unused planned model responses: ${fixture.id}`);
    for (const [route, planned] of Object.entries(fixture.routes ?? {})) {
      const used = ordinals.get(`${fixture.id} ${route}`) ?? 0;
      if (planned.sequence) assert.equal(used, planned.sequence.length, `Unused planned responses: ${fixture.id} ${route}`);
      else assert.ok(used > 0, `Unused planned response: ${fixture.id} ${route}`);
    }
  }
}

const redemptionAliases = new Map();
export function portableRedemptionBody(body) {
  const payload = JSON.parse(body);
  const id = payload.redeem_request_id;
  assert.match(id, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/u, "Codex reset redemption ID must be a random UUID");
  if (!redemptionAliases.has(id)) redemptionAliases.set(id, `$REDEMPTION-${redemptionAliases.size + 1}`);
  return JSON.stringify({ ...payload, redeem_request_id: redemptionAliases.get(id) });
}

function accountsSelection(variant, session, sessionId, providerId) {
  const valid = { session, sessionId, providerId };
  switch (variant) {
    case "null": return null;
    case "string": return "fixture-selection";
    case "array": return [valid];
    case "throwing-getter": return Object.defineProperty({}, "session", { get() { throw new Error("Synthetic selection getter failure"); } });
    case "missing-session": return { sessionId, providerId };
    case "string-session": return { ...valid, session: "fixture-session" };
    case "array-session": return { ...valid, session: [] };
    case "numeric-session-id": return { ...valid, sessionId: 1 };
    case "numeric-provider": return { ...valid, providerId: 1 };
    case "foreign-session": return { ...valid, session: {} };
    case "other-session-id": return { ...valid, sessionId: "fixture-other-session" };
    case "other-provider": return { ...valid, providerId: "deepseek" };
    case "current": return valid;
    default: throw new Error(`Unknown accounts selection variant: ${variant}`);
  }
}

function changedEvent(variant, session, sessionId, providerId) {
  const valid = { session, sessionId, providerId, reason: "updated", changedAt: now };
  switch (variant) {
    case "null": return null;
    case "foreign-session": return { ...valid, session: {} };
    case "other-session-id": return { ...valid, sessionId: "fixture-other-session" };
    case "other-provider": return { ...valid, providerId: "fixture-other-provider" };
    case "current": return valid;
    default: throw new Error(`Unknown usage change variant: ${variant}`);
  }
}

function codexCredential({ token = "fixture-account", storedAccountId = token }) {
  return { type: "oauth", access: codexAccess(token), refresh: `fixture-refresh-${token}`, expires: now + 3_600_000, accountId: storedAccountId };
}

const offlineOAuth = { name: "Synthetic offline OAuth", getApiKey: (value) => value.access,
  login: async () => { throw new Error("Fixture must not log in"); }, refreshToken: async () => { throw new Error("Fixture must not refresh OAuth"); } };

function providerConfig(baseUrl, apiKey = "fixture-host-access", api = "openai-completions", cost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }) {
  return { api, baseUrl, apiKey, models: [{ id: "fixture-lifecycle", name: "Synthetic lifecycle", reasoning: false, input: ["text"], cost, contextWindow: 128000, maxTokens: 1024 }] };
}

function armedCandidateMode() {
  try { return readFileSync(process.env.USAGE_FIXTURE_CANDIDATE_ARM, "utf8"); }
  catch (error) { if (error.code === "ENOENT") return undefined; throw error; }
}

function registerCandidateSources(pi) {
  pi.events.on("oauth:credential-source:v1", (request) => {
    const mode = armedCandidateMode();
    if (request.provider !== "openai-codex" || mode === undefined) return;
    if (mode === "source-throws") throw new Error("Synthetic credential source failure");
    if (mode !== "conflict") return;
    const stored = JSON.parse(readFileSync(join(process.env.PIG_CODING_AGENT_DIR, "auth.json"), "utf8"))["openai-codex"];
    request.offer({ ...stored, refresh: "fixture-conflicting-refresh" });
  });
  pi.events.on("oauth:credential-readiness:v1", (request) => {
    if (request.provider !== "openai-codex") return;
    const mode = armedCandidateMode();
    if (mode === "readiness-throws") throw new Error("Synthetic credential readiness listener failure");
    if (mode === "unready") request.waitUntil(Promise.reject(new Error("Synthetic credential readiness failure")));
  });
}

function orderedResponses([first, then], trace) {
  const delivered = createResponseBarrier();
  let firstUrl;
  return async (url, response) => {
    const route = new URL(url).pathname.split("/").at(-1);
    if (route === first) {
      await response.clone().arrayBuffer();
      firstUrl = url;
      delivered.release();
    } else if (route === then) {
      await delivered.hold();
      await new Promise((resolve) => { setImmediate(resolve); });
      trace.push({ kind: "response-ordered", url, after: firstUrl });
    }
  };
}

export function registerLifecycle(pi, getSession) {
  registerCandidateSources(pi);
  const receipts = [];
  let active;
  let pending;
  let controller;
  let trace;
  let barrier;
  let changes;
  let unsubscribe;
  let synthetic = false;
  pi.on("session_shutdown", async () => {
    controller?.abort();
    barrier?.release();
    if (pending) await pending;
    unsubscribe?.();
  });
  pi.registerCommand("fixture-lifecycle", {
    description: "Disposable registered usage invocation controls",
    handler: async (args, ctx) => {
      const [operation, id] = args.split(" ");
      if (operation === "setup") {
        for (const [provider, baseUrl] of [["openrouter", "https://openrouter.ai/api/v1"], ["fireworks", "https://api.fireworks.ai/inference/v1"], ["command-code", "https://api.commandcode.ai/v1"]]) {
          pi.registerProvider(provider, providerConfig(baseUrl));
        }
        const refreshed = await ctx.modelRegistry.refresh({ allowNetwork: false });
        assert.equal(refreshed.errors.size, 0);
        return;
      }
      if (operation === "codex-setup") {
        const { apiKey: _apiKey, ...config } = providerConfig("https://chatgpt.com/backend-api/codex", undefined, "openai-codex-responses", codexModelCost);
        pi.registerProvider("openai-codex", { ...config, oauth: offlineOAuth });
        return;
      }
      if (operation === "begin") {
        assert.equal(active, undefined);
        active = lifecycleFixtures.find((item) => item.id === id);
        assert.ok(active, `Unknown lifecycle fixture: ${id}`);
        if (["lifecycle:command:other-failure", "lifecycle:api:query-other"].includes(id)) {
          pi.registerProvider("openrouter", providerConfig("https://openrouter.ai/api/v1", id.endsWith("other-failure") ? "fixture-access" : "fixture-host-access"));
          const refreshed = await ctx.modelRegistry.refresh({ allowNetwork: false, providers: ["openrouter"] });
          assert.equal(refreshed.errors.size, 0);
        }
        if (active.register) {
          pi.registerProvider(active.register.provider, providerConfig(active.register.baseUrl));
          const refreshed = await ctx.modelRegistry.refresh({ allowNetwork: false, providers: [active.register.provider] });
          assert.equal(refreshed.errors.size, 0);
        }
        if (active.credential) {
          const path = join(process.env.PIG_CODING_AGENT_DIR, "auth.json");
          const document = JSON.parse(await readFile(path, "utf8"));
          document["openai-codex"] = codexCredential(active.credential);
          await writeHostAuth(path, JSON.stringify(document), { mode: 0o600 });
          const refreshed = await ctx.modelRegistry.refresh({ allowNetwork: false, providers: ["openai-codex"] });
          assert.equal(refreshed.errors.size, 0);
        }
        process.env.USAGE_FIXTURE_CASE = id;
        trace = [];
        changes = [];
        globalThis[Symbol.for("usage-fixture-trace")] = trace;
        unsubscribe = pi.events.on("usage:changed:v1", (event) => {
          if (synthetic) return;
          const session = getSession();
          assert.equal(event.session, session.session);
          assert.equal(event.sessionId, session.sessionId);
          changes.push({ providerId: event.providerId, reason: event.reason, changedAt: event.changedAt });
        });
        if (active.responseOrder) globalThis[Symbol.for("usage-fixture-barrier")] = orderedResponses(active.responseOrder, trace);
        await beginBranchObservation(id);
        return;
      }
      if (operation === "query") {
        assert.ok(active?.options);
        const options = active.options;
        controller = new AbortController();
        const { session, sessionId } = getSession();
        assert.ok(session);
        if (options.barrier) {
          barrier = createResponseBarrier();
          globalThis[Symbol.for("usage-fixture-barrier")] = async () => {
            globalThis[Symbol.for("usage-fixture-barrier")] = undefined;
            trace.push({ kind: "response-held" });
            await barrier.hold();
            trace.push({ kind: "response-released" });
          };
        }
        let offered = false;
        pending = new Promise((resolve) => {
          pi.events.emit(options.capabilities ? "usage:capabilities:v1" : "usage:query:v1", {
            session, sessionId, signal: controller.signal,
            ...(options.providerId === undefined ? {} : { providerId: options.providerId }),
            ...(options.refresh === undefined ? {} : { refresh: options.refresh }),
            respond(value) { offered = true; resolve(value); return true; },
          });
        });
        assert.equal(offered, true);
        if (options.barrier) await barrier.entered;
        if (options.cancel) controller.abort();
        return;
      }
      if (operation === "changed") {
        assert.ok(active?.changed);
        const { session, sessionId } = getSession();
        assert.ok(session);
        let queries = 0;
        const stop = pi.events.on("usage:query:v1", () => { queries += 1; });
        synthetic = true;
        try {
          for (const variant of active.changed) {
            const before = queries;
            pi.events.emit("usage:changed:v1", changedEvent(variant, session, sessionId, ctx.model?.provider));
            trace.push({ kind: "usage-changed", variant, footerQueried: queries > before });
          }
        } finally { synthetic = false; stop(); }
        return;
      }
      if (operation === "accounts") {
        assert.ok(active?.accounts);
        const { session, sessionId } = getSession();
        assert.ok(session);
        for (const variant of active.accounts) pi.events.emit("accounts:selection-committed:v1", accountsSelection(variant, session, sessionId, ctx.model?.provider));
        return;
      }
      if (operation === "end") {
        assert.ok(active);
        if (barrier) {
          barrier.release();
          await barrier.completed;
        }
        const response = pending ? await pending : undefined;
        let successor;
        if (active.options?.replaceModel) {
          const { session, sessionId } = getSession();
          successor = await new Promise((resolve) => pi.events.emit("usage:query:v1", { session, sessionId, signal: new AbortController().signal, refresh: true, respond(value) { resolve(value); return true; } }));
        }
        await new Promise((done) => setImmediate(done));
        await endBranchObservation();
        await rm(process.env.USAGE_FIXTURE_CANDIDATE_ARM, { force: true });
        unsubscribe();
        const settingsPath = join(process.env.PIG_CODING_AGENT_DIR, "pi-usage.json");
        let settings;
        try { settings = JSON.parse(await readFile(settingsPath, "utf8")); }
        catch (error) { if (error.code !== "ENOENT") throw error; }
        receipts.push({ id: active.id, ...(successor === undefined ? {} : { successor }), ...(response === undefined ? {} : { response }), trace, changes, ...(settings === undefined ? {} : { settings }) });
        active = pending = controller = trace = changes = barrier = unsubscribe = undefined;
        globalThis[Symbol.for("usage-fixture-trace")] = undefined;
        globalThis[Symbol.for("usage-fixture-barrier")] = undefined;
        process.env.USAGE_FIXTURE_CASE = "host";
        return;
      }
      if (operation === "finish") {
        assert.equal(active, undefined);
        await saveBranchObservations(process.env.USAGE_FIXTURE_BRANCH_OUTPUT);
        const observed = JSON.parse(await readFile(process.env.USAGE_FIXTURE_OUTPUT, "utf8"));
        observed.lifecycle = receipts;
        await writeFile(process.env.USAGE_FIXTURE_OUTPUT, JSON.stringify(observed, null, 2) + "\n", { mode: 0o600 });
        return;
      }
      throw new Error(`Unknown lifecycle operation: ${operation}`);
    },
  });
}

export async function captureLifecycle(rpc, selectActions, waitStatus, waitAgent) {
  let setup = false;
  for (const fixture of lifecycleFixtures) {
    if (!setup && fixture.id === "lifecycle:command:other") {
      await rpc("lifecycle-setup", "prompt", { message: "/fixture-lifecycle setup" });
      setup = true;
    }
    if (fixture.selectModel?.provider === codexModel.provider) await rpc("lifecycle-codex-setup", "prompt", { message: "/fixture-lifecycle codex-setup" });
    await rpc(`${fixture.id}:begin`, "prompt", { message: `/fixture-lifecycle begin ${fixture.id}` });
    selectActions(fixture);
    if (fixture.prompt) {
      const send = () => rpc(fixture.id, "prompt", { message: fixture.message });
      if (fixture.message) await (fixture.messageStatus ? waitStatus(send, new RegExp(fixture.messageStatus, "u")) : send());
      const ask = () => waitAgent(() => rpc(`${fixture.id}:prompt`, "prompt", { message: fixture.prompt }));
      if (fixture.awaitStatus) await waitStatus(ask, new RegExp(fixture.awaitStatus, "u"));
      else await ask();
    } else if (fixture.message && fixture.awaitStatus) {
      await waitStatus(() => rpc(fixture.id, "prompt", { message: fixture.message }), new RegExp(fixture.awaitStatus, "u"));
    } else if (fixture.message) {
      await rpc(fixture.id, "prompt", { message: fixture.message });
    } else if (fixture.changed) {
      await rpc(`${fixture.id}:changed`, "prompt", { message: "/fixture-lifecycle changed" });
    } else if (fixture.accounts) {
      const select = () => rpc(`${fixture.id}:accounts`, "prompt", { message: "/fixture-lifecycle accounts" });
      if (fixture.awaitStatus) await waitStatus(select, new RegExp(fixture.awaitStatus, "u"));
      else await select();
    } else if (fixture.selectModel) {
      await waitStatus(() => rpc(`${fixture.id}:model`, "set_model", fixture.selectModel), new RegExp(fixture.awaitStatus, "u"));
    } else {
      await rpc(`${fixture.id}:query`, "prompt", { message: "/fixture-lifecycle query" });
      if (fixture.options.replaceModel) await waitStatus(() => rpc(`${fixture.id}:model`, "set_model", { provider: "openrouter", modelId: "fixture-lifecycle" }), /^openrouter \$74\.50 left$/u);
    }
    await rpc(`${fixture.id}:end`, "prompt", { message: "/fixture-lifecycle end" });
    selectActions(undefined);
  }
  await rpc("lifecycle-finish", "prompt", { message: "/fixture-lifecycle finish" });
}
