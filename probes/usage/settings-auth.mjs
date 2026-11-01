/** Additional frozen settings transactions and real PiG registry/auth-source captures, using only disposable synthetic credentials. */
import assert from "node:assert/strict";
import { appendFile, mkdir, readFile, readdir, rename, rmdir, stat, writeFile } from "node:fs/promises";
import { mkdirSync, rmdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { cases, now } from "./cases.mjs";

const oauthProviders = ["openai", "openai-codex", "github-copilot", "xai"];
const codexAccess = `fixture.${Buffer.from(JSON.stringify({ "https://api.openai.com/auth": { chatgpt_account_id: "fixture-account" } })).toString("base64url")}.signature`;
function credential(provider) {
  return { type: "oauth", access: provider === "openai-codex" ? codexAccess : "fixture-access", refresh: "fixture-refresh", expires: now + 3600000,
    ...(provider === "openai" ? { clientId: "fixture-client", scopes: ["chatgpt.tokens.use.direct"] } : {}),
    ...(provider === "openai-codex" ? { accountId: "fixture-account" } : {}) };
}
function definition(provider, variant) {
  const baseUrl = cases.find((item) => item.provider === provider).baseUrl;
  const headers = new Map([
    ["null-authorization", { Authorization: null }], ["empty-authorization", { Authorization: "" }],
    ["other-authorization", { Authorization: "Basic synthetic" }], ["mismatch", { Authorization: "Bearer fixture-other" }],
    ["ambiguous-authorization", { Authorization: "Bearer fixture-access", authorization: "Bearer fixture-other" }],
  ]).get(variant);
  return { baseUrl: variant === "provider-origin" ? "https://fixture.invalid/v1" : baseUrl, api: "openai-completions",
    models: [{ id: "fixture-auth", name: "Synthetic auth fixture", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 128000, maxTokens: 1024,
      ...(variant === "model-origin" ? { baseUrl: "https://fixture.invalid/v1" } : {}), ...(variant === "provider-origin" ? { baseUrl } : {}), ...(headers ? { headers } : {}) }],
    ...(oauthProviders.includes(provider) ? { oauth: { name: "Synthetic offline OAuth", getApiKey: (value) => value.access,
      login: async () => { throw new Error("Fixture must not log in"); }, refreshToken: async () => { throw new Error("Fixture must not refresh OAuth"); } } } : {}) };
}

export function addSettingsAuthFixtures(fixture) {
  for (const [name, value] of [
    ["scalar", 42], ["string", "settings"], ["selected-null", { selectedTargets: null }],
    ["target-number", { selectedTargets: { deepseek: 1 } }], ["target-empty", { selectedTargets: { deepseek: "" } }],
    ["provider-empty", { selectedTargets: { "": "target" } }], ["provider-long", { selectedTargets: { ["p".repeat(129)]: "target" } }],
    ["valid-target-boundaries", { selectedTargets: { ["p".repeat(128)]: "t".repeat(256), "a._~-": "target" } }],
    ["explicit-false", { openaiUseCodexUsage: false, codexFastMode: false, codexStatusResetCountdown: false, codexStatusPercentage: "remaining" }],
    ["legacy-with-other-target", { fireworksAccountId: "legacy", selectedTargets: { deepseek: "other" } }],
    ["display-unknown", { statuslineDisplay: { future: { keep: true }, template: "", window: "{value}", countdown: "{reset}", separator: "{}" } }],
    ...["template", "window", "countdown", "separator"].flatMap((field) => [
      [`${field}-number`, { statuslineDisplay: { [field]: 42 } }],
      [`${field}-control`, { statuslineDisplay: { [field]: "\u001b[31m" } }],
      ...(field === "separator" ? [] : [[`${field}-placeholder`, { statuslineDisplay: { [field]: "{unknown}" } }], [`${field}-braces`, { statuslineDisplay: { [field]: "}" } }]]),
    ]),
  ]) {
    fixture(`settings:extra-normalize:${name}`, "src/settings.ts", ["normalizeUsageSettings"], 4,
      "Frozen settings validation at scalar, target identifier, explicit default and individual display-field boundaries.", { value },
      async (library) => library.normalizeUsageSettings(value));
  }
  for (const scenario of ["atomic-observer", "delete-patch", "snapshot-isolation", "abort-after-write", "abort-publication", "rollback-write-failure", "rollback-missing-conflict", "non-fireworks-target", "invalid-provider", "queued-abort-recovery", "invalid-display-file", "exact-size"]) {
    fixture(`settings:transaction:${scenario}`, "src/settings.ts", ["createUsageSettingsRuntime", "loadUsageSettings"], 4,
      "Disposable real filesystem observations of queue ordering, atomic/private replacement, snapshot isolation, cancellation and conditional rollback without overwriting competing writes.", { scenario },
      async (library, directory) => {
        await mkdir(directory, { recursive: true });
        const path = join(directory, "pi-usage.json");
        const initial = { unknown: { keep: true }, fireworksAccountId: "legacy", codexFastMode: true };
        let initialText = JSON.stringify(initial);
        if (scenario === "invalid-display-file") initialText = '{"statuslineDisplay":{"separator":"\\n"}}';
        if (scenario === "exact-size") initialText = initialText.padEnd(65536, " ");
        if (scenario !== "rollback-missing-conflict") await writeFile(path, initialText, { mode: 0o644 });
        const trace = [];
        const controller = new AbortController();
        let releaseWrite;
        let enteredWrite;
        const entered = new Promise((resolve) => { enteredWrite = resolve; });
        const released = new Promise((resolve) => { releaseWrite = resolve; });
        const disk = async () => {
          try { return { document: JSON.parse(await readFile(path, "utf8")), mode: (await stat(path)).mode & 0o777 }; }
          catch (error) { if (error.code === "ENOENT") return { missing: true }; throw error; }
        };
        let writes = 0;
        const runtime = library.createUsageSettingsRuntime({ path, operations: {
          async writeFile(temporary, data, options) {
            writes += 1;
            trace.push({ operation: "write", write: writes, flag: options.flag, mode: options.mode, before: await disk() });
            if (scenario === "rollback-write-failure" && writes === 2) throw new Error("Synthetic rollback write failure");
            await writeFile(temporary, data, options);
            if (scenario === "abort-after-write") controller.abort();
            if (scenario === "atomic-observer" && writes === 1) { enteredWrite(); await released; }
          },
          async rename(temporary, destination) {
            trace.push({ operation: "rename", temporaryMode: (await stat(temporary)).mode & 0o777, temporary: JSON.parse(await readFile(temporary, "utf8")), before: await disk() });
            await rename(temporary, destination);
          },
        } });
        const observations = [];
        async function observe(operation) {
          try { observations.push({ value: await operation() }); }
          catch (error) {
            if (!(error instanceof Error) || error instanceof assert.AssertionError) throw error;
            observations.push({ error: { name: error.name, message: error.message, ...(error instanceof AggregateError ? { errors: error.errors.map((entry) => entry.message) } : {}) } });
          }
        }
        await observe(() => runtime.reload());
        if (scenario === "atomic-observer") {
          const first = runtime.update({ codexFastMode: false });
          await entered;
          const second = runtime.update({ codexStatusPercentage: "used" });
          try {
            assert.equal(writes, 1);
            assert.deepEqual((await disk()).document, initial);
            trace.push({ operation: "blocked-first-write", queuedWrites: writes, state: runtime.get(), disk: await disk() });
          } finally { releaseWrite(); }
          await observe(() => first);
          await observe(() => second);
        } else if (scenario === "delete-patch") await observe(() => runtime.update({ codexFastMode: undefined, fireworksAccountId: undefined }));
        else if (scenario === "snapshot-isolation") {
          const copy = runtime.get();
          copy.document.unknown.keep = false;
          copy.settings.selectedTargets.fireworks = "mutated";
          await observe(() => runtime.update({ codexStatusPercentage: "used" }));
        } else if (scenario === "queued-abort-recovery") {
          await observe(() => runtime.reload(AbortSignal.abort()));
          await observe(() => runtime.update({ codexStatusPercentage: "used" }));
        } else if (["abort-publication", "rollback-write-failure", "rollback-missing-conflict"].includes(scenario)) {
          await observe(() => runtime.updateSelectedTarget("fireworks", "selected", controller.signal, async () => {
            trace.push({ operation: "publication", disk: await disk(), state: runtime.get() });
            if (scenario === "abort-publication") { controller.abort(); return; }
            if (scenario === "rollback-missing-conflict") await writeFile(path, '{"external":true}');
            throw new Error("Synthetic publication rejected");
          }));
        } else if (["non-fireworks-target", "invalid-provider"].includes(scenario)) await observe(() => runtime.updateSelectedTarget(scenario === "invalid-provider" ? "bad/provider" : "deepseek", "selected"));
        else await observe(() => runtime.update({ codexFastMode: false }, controller.signal));
        await runtime.flush();
        return { observations, trace, state: runtime.get(), disk: await disk(), remainingFiles: (await readdir(directory)).sort() };
      });
  }
  fixture("settings:host-default-path", "src/settings.ts", ["usageSettingsPath", "createUsageSettingsRuntime"], 4,
    "Real PiG getAgentDir supplies the frozen default settings path, without reading Pi's separate root.", {},
    async (library) => {
      const path = library.usageSettingsPath();
      assert.equal(path, join(process.env.PIG_CODING_AGENT_DIR, "pi-usage.json"));
      assert.notEqual(path, join(process.env.PI_CODING_AGENT_DIR, "pi-usage.json"));
      const runtime = library.createUsageSettingsRuntime();
      try { return { path, state: await runtime.reload() }; }
      finally { await runtime.flush(); }
    });

  for (const item of cases.filter((entry) => entry.id.endsWith(":normal"))) {
    for (const variant of ["stored", "other-provider", "provider-origin", "model-origin", "readiness-reject", "readiness-rotate", "readiness-abort", "readiness-stale"]) addHostAuth(fixture, item.provider, variant);
  }
  for (const item of cases.filter((entry) => entry.id.endsWith(":normal") && !["openai", "github-copilot", "xai"].includes(entry.provider))) {
    for (const variant of ["null-authorization", "empty-authorization", "other-authorization", "mismatch"]) addHostAuth(fixture, item.provider, variant);
  }
  addHostAuth(fixture, "openai", "api-key");
  for (const provider of ["openai", "github-copilot", "xai"]) {
    for (const variant of ["mismatch", "null-authorization", "empty-authorization", "other-authorization", "ambiguous-authorization", "no-credential", "malformed-offer", "duplicate", "conflicting", "late-offer", "incomplete", "enterprise", "wrong-scope", "wrong-client", "candidate-other-account", "offer-only", "offer-no-match", "clone-offer"]) addHostAuth(fixture, provider, variant);
  }
  for (const provider of ["moonshotai", "moonshotai-cn"]) {
    for (const variant of ["regional-shared-sibling", "regional-stored-sibling", "regional-explicit-sibling", "regional-shared-current", "regional-sibling-only", "regional-distinct"]) addHostAuth(fixture, provider, variant);
  }
  for (const variant of ["companion", "companion-missing", "companion-api-key", "companion-account-mismatch", "companion-conflicting", "companion-readiness-reject", "companion-invalid-token"]) addHostAuth(fixture, "openai", variant);
}

function hostScenario(provider, variant, previous = {}) {
  const document = structuredClone(previous);
  const providers = variant.startsWith("companion") ? [provider, "openai-codex"] : [provider];
  const regional = variant.startsWith("regional-");
  const registrations = [];
  for (const id of providers) {
    if (oauthProviders.includes(id)) document[id] = credential(id);
    else document[id] = { type: "api_key", key: "fixture-access" };
    if (regional && !variant.includes("stored")) delete document[id];
    if (variant === "regional-sibling-only" || variant === "regional-distinct") {
      document[provider === "moonshotai" ? "moonshotai-cn" : "moonshotai"] = { type: "api_key", key: "fixture-other-region" };
      if (variant === "regional-distinct") document[id] = { type: "api_key", key: `fixture-${provider}-region` };
    }
    if (variant === "api-key") document[id] = { type: "api_key", key: "fixture-access" };
    if (variant === "no-credential" || (id === "openai-codex" && variant === "companion-missing")) delete document[id];
    if (id === "openai-codex" && variant === "companion-api-key") document[id] = { type: "api_key", key: codexAccess };
    if (id === "openai-codex" && variant === "companion-account-mismatch") document[id].accountId = "different-account";
    if (id === "openai-codex" && variant === "companion-invalid-token") document[id].access = "fixture-invalid-token";
    if (variant === "incomplete") delete document[id].refresh;
    if (variant === "enterprise") document[id].enterpriseUrl = "https://enterprise.fixture.invalid";
    if (variant === "wrong-scope" && id === "openai") document[id].scopes = ["other"];
    if (variant === "wrong-client" && id === "openai") document[id].clientId = " ";
    const config = definition(id, variant);
    if (regional && variant.includes("explicit")) config.apiKey = "fixture-region-specific";
    registrations.push({ provider: id, config });
  }
  return { document, providers, registrations, regional };
}

const authLockAttempts = 3000;
export async function writeHostAuth(path, text, options, attempts = authLockAttempts) {
  for (let attempt = 1; ; attempt++) {
    try { await mkdir(`${path}.lock`); break; }
    catch (error) { if (error.code !== "EEXIST" || attempt === attempts) throw error; }
    await delay(10);
  }
  try { await writeFile(path, text, options); }
  finally { await rmdir(`${path}.lock`); }
}
export function writeHostAuthSync(path, text, options, attempts = authLockAttempts) {
  const wait = new Int32Array(new SharedArrayBuffer(4));
  for (let attempt = 1; ; attempt++) {
    try { mkdirSync(`${path}.lock`); break; }
    catch (error) { if (error.code !== "EEXIST" || attempt === attempts) throw error; }
    Atomics.wait(wait, 0, 0, 10);
  }
  try { writeFileSync(path, text, options); }
  finally { rmdirSync(`${path}.lock`); }
}

function addHostAuth(fixture, provider, variant) {
  const input = hostScenario(provider, variant, { deepseek: { type: "api_key", key: "fixture-host-access" } });
  fixture(`auth:host:${provider}:${variant}`, "src/query.ts", ["resolveUsageAuth", "providerIsConfigured"], 3,
    "Real PiG model/provider authentication composition and frozen OAuth event reader resolve only synthetic stored/configured credentials; model view uses a host-registered model, not a substitute registry. No provider query or model prompt is sent.", { provider, variant, initialAuthDocument: input.document, registrations: input.registrations, oauthResolver: "getApiKey returns stored access; login and refresh reject", sharedMoonshotEnvironment: "fixture-shared-moonshot" },
    async (library, _directory, host) => {
      assert.ok(host?.pi && host.ctx.modelRegistry);
      const { pi, ctx, createReader } = host;
      const progress = (stage) => appendFile(join(process.env.TMPDIR, "settings-auth-progress.log"), `${provider}:${variant}:${stage}\n`);
      await progress("start");
      const path = join(process.env.PIG_CODING_AGENT_DIR, "auth.json");
      const previous = await readFile(path, "utf8");
      const trace = [];
      const document = {};
      const providers = variant.startsWith("companion") ? [provider, "openai-codex"] : [provider];
      const controller = new AbortController();
      const subscriptions = [];
      let lateOffer;
      let lateWait;
      const pending = [];
      let current = true;
      const regional = variant.startsWith("regional-");
      const refresh = async () => {
        const result = await ctx.modelRegistry.refresh({ allowNetwork: false, providers });
        assert.equal(result.aborted, false);
        assert.equal(result.errors.size, 0, [...result.errors.values()].map((error) => error.message).join(", "));
      };
      try {
        const setup = hostScenario(provider, variant, JSON.parse(previous));
        Object.assign(document, setup.document);
        for (const { provider: id, config } of setup.registrations) pi.registerProvider(id, config);
        await writeHostAuth(path, JSON.stringify(document), { mode: 0o600 });
        await progress("registered");
        await refresh();
        await progress("refreshed");
        const selected = ctx.modelRegistry.find(provider, "fixture-auth");
        assert.ok(selected);
        const foreground = provider === ctx.model.provider ? ctx.modelRegistry.getAll().find((model) => model.provider !== provider) : ctx.model;
        const view = { ...ctx, model: (regional && (variant.includes("sibling") || variant === "regional-distinct")) || variant === "other-provider" ? foreground : selected };
        subscriptions.push(pi.events.on("oauth:credential-readiness:v1", (request) => {
          if (request.session !== ctx.sessionManager || !providers.includes(request.provider)) return;
          trace.push({ event: "readiness", provider: request.provider, frozen: Object.isFrozen(request) });
          lateWait = request.waitUntil;
          request.waitUntil({ then() { throw new Error("Non-Promise readiness must be ignored"); } });
          const work = (async () => {
            if (variant === "readiness-reject" || (variant === "companion-readiness-reject" && request.provider === "openai-codex")) throw new Error("Synthetic readiness rejection");
            if (variant === "readiness-rotate") {
              document[provider] = oauthProviders.includes(provider) ? { ...credential(provider), access: provider === "openai-codex" ? codexAccess : "fixture-rotated" } : { type: "api_key", key: "fixture-rotated" };
              await writeHostAuth(path, JSON.stringify(document));
              await refresh();
              trace.push({ event: "rotated" });
            }
            if (variant === "readiness-abort") controller.abort();
            if (variant === "readiness-stale") current = false;
            trace.push({ event: "ready", provider: request.provider });
          })();
          pending.push(work.catch(() => undefined));
          request.waitUntil(work);
        }));
        subscriptions.push(pi.events.on("oauth:credential-source:v1", (request) => {
          if (request.session !== ctx.sessionManager || !providers.includes(request.provider)) return;
          trace.push({ event: "candidates", provider: request.provider, frozen: Object.isFrozen(request) });
          lateOffer = request.offer;
          const offered = credential(request.provider);
          if (["offer-only", "offer-no-match", "clone-offer"].includes(variant)) {
            delete document[provider];
            host.writeAuthSync(path, JSON.stringify(document));
            if (variant === "offer-no-match") offered.access = "fixture-other";
            request.offer(offered);
            if (variant === "clone-offer") offered.refresh = "fixture-mutated-after-offer";
          }
          if (variant === "duplicate") request.offer(offered);
          if (variant === "conflicting" || (variant === "companion-conflicting" && request.provider === "openai-codex")) request.offer({ ...offered, refresh: "fixture-other-refresh" });
          if (variant === "candidate-other-account") request.offer({ ...offered, access: "fixture-other" });
          if (variant === "malformed-offer") for (const value of [null, [], 42, { type: "api_key", key: "fixture-key" }, { ...offered, expires: Infinity }, { ...offered, refresh: "" }, { ...offered, method() {} }]) request.offer(value);
        }));
        const reader = createReader(pi);
        const configured = library.providerIsConfigured(view, provider);
        let resolved;
        let error;
        try { resolved = await library.resolveUsageAuth(view, library.adapterForProvider(provider), new Uint8Array(32), undefined, reader, undefined, { signal: controller.signal, checkCurrent: () => { if (!current) throw Object.assign(new Error("Synthetic stale auth context"), { name: "UsageStaleContextError" }); }, openaiUseCodexUsage: variant.startsWith("companion") }); }
        catch (caught) {
          if (!(caught instanceof Error) || caught instanceof assert.AssertionError) throw caught;
          error = { name: caught.name, message: caught.message };
        }
        if (variant === "late-offer") {
          const candidates = await reader(view, provider);
          const before = structuredClone(candidates);
          lateOffer?.({ ...credential(provider), refresh: "fixture-late" });
          lateWait?.(Promise.resolve());
          trace.push({ event: "late-offer", before, after: candidates });
        }
        await progress("resolved");
        const status = ctx.modelRegistry.getProviderAuthStatus(provider);
        const selectedAuth = await ctx.modelRegistry.getApiKeyAndHeaders(selected);
        const providerAuth = await ctx.modelRegistry.getProviderAuth(provider);
        return { configured, status, selected: { provider: selected.provider, id: selected.id, baseUrl: selected.baseUrl, headers: selected.headers }, selectedAuth, providerAuth, resolved, error, trace };
      } finally {
        for (const unsubscribe of subscriptions) unsubscribe();
        await Promise.all(pending);
        await writeHostAuth(path, previous);
        for (const id of providers) pi.unregisterProvider(id);
        await progress("unregistered");
        await refresh();
        await progress("done");
      }
    });
}
