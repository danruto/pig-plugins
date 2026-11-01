/** Public consumer captures against the frozen implementation; inputs are synthetic and files stay in the disposable host root. */
import { mkdir, readFile, readdir, rename, lstat, readlink, symlink, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { cases, now } from "./cases.mjs";
import { addSettingsAuthFixtures } from "./settings-auth.mjs";
import { codexAccess } from "./lifecycle.mjs";

const fixtures = [];
function fixture(id, source, symbols, checkpoint, behavior, input, run) {
  fixtures.push({ id, source, symbols, checkpoint, behavior, input: JSON.parse(JSON.stringify(input)), run });
}
const model = {
  provider: "openai-codex", id: "gpt-5.5", api: "openai-codex-responses", baseUrl: "https://chatgpt.com/backend-api/codex",
  cost: { input: 2, output: 10, cacheRead: 0.2, cacheWrite: 1 },
};
for (const [id, candidate] of [
  ["missing", undefined], ["other-provider", { ...model, provider: "openai" }],
  ["custom-origin", { ...model, baseUrl: "https://fixture.invalid/codex" }],
  ["invalid-origin", { ...model, baseUrl: ":bad:" }], ["wrong-api", { ...model, api: "openai-responses" }], ["official", model],
]) {
  for (const enabled of [false, true]) {
    for (const capability of [{ kind: "supported" }, { kind: "unsupported" }, { kind: "unknown", reason: "Synthetic catalogue unavailable" }]) {
      const input = { model: candidate, enabled, capability, payload: { service_tier: "existing", marker: "preserved" } };
      fixture(`fast:${id}:${enabled}:${capability.kind}`, "src/codex-fast.ts", ["codexFastAvailability", "codexFastRequestTier", "rewriteCodexFastPayload"], 4,
        "Official model, saved toggle and discovered capability determine availability and the next request tier; unrelated payload fields survive.", input,
        async (library) => ({ availability: library.codexFastAvailability(candidate, enabled, capability), effective: library.codexFastIsEffective(candidate, enabled, capability), tier: library.codexFastRequestTier(candidate, enabled, capability), payload: library.rewriteCodexFastPayload(input.payload, candidate, enabled, capability) }));
    }
  }
}
for (const payload of [null, [], "payload"]) {
  fixture(`fast:invalid-payload:${JSON.stringify(payload)}`, "src/codex-fast.ts", ["rewriteCodexFastPayload"], 4,
    "Non-record payloads are not rewritten.", { payload }, async (library) => library.rewriteCodexFastPayload(payload, model, true, { kind: "supported" }));
}
for (const id of ["gpt-5.5", "gpt-5.4"]) {
  for (const variant of ["matching", "already-corrected", "other-model", "other-provider", "user", "incomplete-usage", "disabled", "unsupported"]) {
    const input = { model: { ...model, id }, message: { role: "assistant", provider: model.provider, model: id, content: [], usage: { input: 100, output: 20, cacheRead: 30, cacheWrite: 5, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } } }, enabled: true, capability: { kind: "supported" } };
    if (variant === "other-model") input.message.model = "another";
    if (variant === "other-provider") input.message.provider = "another";
    if (variant === "user") input.message.role = "user";
    if (variant === "incomplete-usage") delete input.message.usage.input;
    if (variant === "disabled") input.enabled = false;
    if (variant === "unsupported") input.capability.kind = "unsupported";
    fixture(`fast:cost:${id}:${variant}`, "src/codex-fast.ts", ["correctCodexFastMessageCost"], 4,
      "Cost correction requires a matching complete assistant usage record and effective Fast; correction is idempotent and uses the model-specific multiplier.", input,
      async (library) => {
        const corrected = library.correctCodexFastMessageCost(input.message, input.model, input.enabled, input.capability);
        return variant === "already-corrected" ? library.correctCodexFastMessageCost(corrected, input.model, input.enabled, input.capability) : { corrected, original: input.message };
      });
  }
}
for (const input of [
  {}, { unknown: "preserved", codexStatusPercentage: "used", codexFastMode: true, openaiUseCodexUsage: true, codexStatusResetCountdown: false },
  null, [], { openaiUseCodexUsage: "true" }, { codexFastMode: 1 }, { codexStatusResetCountdown: null }, { codexStatusPercentage: "invalid" },
  { fireworksAccountId: "fixture-account" }, { fireworksAccountId: "bad/account" },
  { fireworksAccountId: "legacy", selectedTargets: { fireworks: "selected" } },
  { selectedTargets: [] }, { selectedTargets: { "bad/id": "target" } }, { selectedTargets: { fireworks: "bad\nvalue" } },
  { statuslineDisplay: null }, { statuslineDisplay: [] }, { statuslineDisplay: { template: 1 } }, { statuslineDisplay: { template: "{bad}" } },
  { statuslineDisplay: { window: "{{value}" } }, { statuslineDisplay: { countdown: "{value}\n{reset}" } },
  { statuslineDisplay: { template: "{windows} [{provider}]", separator: " | ", window: "{value}" } },
]) {
  fixture(`settings:normalize:${fixtures.filter((item) => item.id.startsWith("settings:normalize:")).length}`, "src/settings.ts", ["normalizeUsageSettings"], 4,
    "Settings defaults, validation, legacy Fireworks migration and display-template validation preserve the frozen accepted/rejected shapes.", input,
    async (library) => library.normalizeUsageSettings(input));
}
const validTarget = { id: "fixture-a", label: "Label", description: "Description" };
for (const [id, input] of [
  ["valid", [validTarget]], ["empty", []], ["not-array", {}], ["too-many", Array.from({ length: 1001 }, () => validTarget)],
  ["null", [null]], ["array-choice", [[]]], ["empty-id", [{ ...validTarget, id: "" }]],
  ["long-id", [{ ...validTarget, id: "a".repeat(257) }]], ["control-id", [{ ...validTarget, id: "a\u007f" }]],
  ["invalid-label", [{ ...validTarget, label: 1 }]], ["empty-label", [{ ...validTarget, label: "\u001b[31m" }]],
  ["invalid-description", [{ ...validTarget, description: 1 }]], ["duplicate-id", [validTarget, validTarget]],
  ["sanitized", [{ ...validTarget, label: "\u001b[31mLabel\nText", description: "\u001b]0;ignored\u0007Description" }]],
  ["duplicate-label", [{ id: "a", label: "Same" }, { id: "b", label: "Same" }, { id: "c", label: "Same · b" }, { id: "d", label: "Same" }]],
]) {
  fixture(`targets:${id}`, "src/usage-targets.ts", ["normalizeUsageTargets", "createUsageTargetSelectOptions"], 3,
    "Target discovery validates bounded unique identifiers, sanitizes labels and keeps display-option selection unambiguous.", input,
    async (library) => {
      const normalized = library.normalizeUsageTargets(input);
      const select = library.createUsageTargetSelectOptions(input);
      return { normalized, options: select.options, selected: select.options.map((option) => select.targetIdFor(option)), unknownSelection: select.targetIdFor("not offered") };
    });
}
for (const [id, input] of [
  ["zero", { available_count: 0 }], ["generic", { available_count: "2" }],
  ["negative", { available_count: -1 }], ["fractional", { available_count: 1.5 }], ["blank-count", { available_count: " " }],
  ["invalid-credits", { available_count: 2, credits: {} }],
  ["sorted-filtered", { available_count: 2, credits: [null, [], { status: "used" }, { status: "available", reset_type: "other" },
    { id: "later", status: "available", reset_type: "codex_rate_limits", expires_at: new Date(now + 7200000).toISOString(), title: "\u001b[31mLater", description: "Description\ntext" },
    { id: "earlier", status: "available", reset_type: "codex_rate_limits", expires_at: new Date(now + 3600000).toISOString() },
    { id: "unlimited", status: "available", reset_type: "codex_rate_limits", expires_at: null }] }],
  ...["", 42, "a".repeat(1025)].map((id, index) => [`invalid-id-${index}`, { available_count: 1, credits: [{ id, status: "available", reset_type: "codex_rate_limits" }] }]),
  ...[42, "invalid"].map((expires_at, index) => [`invalid-expiry-${index}`, { available_count: 1, credits: [{ id: "fixture", status: "available", reset_type: "codex_rate_limits", expires_at }] }]),
  ["bounded-options", { available_count: 100, credits: Array.from({ length: 40 }, (_, index) => ({ id: `fixture-${index}`, status: "available", reset_type: "codex_rate_limits" })) }],
]) {
  fixture(`resets:${id}`, "src/codex-resets.ts", ["normalizeCodexResetCreditsPayload"], 4,
    "Reset availability validates counts, filters status/type, sorts expiration, bounds choices and falls back to a generic option; no redemption request is made.", input,
    async (library) => library.normalizeCodexResetCreditsPayload(input));
}
const resetAccess = codexAccess("fixture-account");
const resetCredential = { type: "oauth", access: resetAccess, refresh: "fixture-refresh", expires: now + 3600000, accountId: "fixture-account" };
for (const [variant, setup] of [
  ["matched", {}],
  ["no-model", { model: null }],
  ["not-codex", { model: { ...model, provider: "openai" } }],
  ["readiness-denied", { ready: false }],
  ["no-readiness", { ready: null }],
  ["model-changed", { changeModel: true }],
  ["no-auth", { auth: null }],
  ["discovery-denied", { discoveryOk: false }],
  ["basic-api-key", { authorization: "Basic synthetic" }],
  ["no-access", { authorization: "Basic synthetic", apiKey: null }],
  ["opaque-access", { access: "fixture-opaque-access" }],
  ["undecodable-access", { access: "fixture.!.signature" }],
  ["no-account-claim", { access: `fixture.${Buffer.from("{}").toString("base64url")}.signature` }],
  ["non-oauth-candidates", { candidates: [null, { type: "api_key", key: "fixture-key" }] }],
  ["other-access", { candidates: [{ ...resetCredential, access: codexAccess("fixture-other-account") }] }],
  ["incomplete-candidate", { candidates: [{ ...resetCredential, refresh: "" }] }],
  ["throwing-candidate", { candidates: ["throwing-getter", resetCredential] }],
  ["standalone-empty", { candidates: [], offeredCount: 0 }],
  ["standalone-fallback-mismatch", { reader: null, stored: { ...resetCredential, access: codexAccess("fixture-other-account") } }],
  ["standalone-fallback-match", { reader: null, stored: resetCredential }],
]) {
  fixture(`resets:auth:${variant}`, "src/codex-resets.ts", ["resolveCodexResetAuth"], 4,
    "Reset authentication requires the official Codex model, readiness, an unchanged model, resolved Bearer access with an account claim, successful credential discovery and exactly one matching complete OAuth candidate; synthetic registry and credential sources only, no reset request.", { variant, setup },
    async (library) => {
      const access = setup.access ?? resetAccess;
      const selected = setup.model === undefined ? { ...model, id: "fixture" } : setup.model;
      const ctx = { model: selected ?? undefined, modelRegistry: {
        getAvailable: () => selected ? [selected] : [], getAll: () => selected ? [selected] : [], getProvider: () => ({ baseUrl: model.baseUrl }),
        getApiKeyAndHeaders: async () => {
          if (setup.changeModel) ctx.model = { ...selected, id: "fixture-other" };
          return setup.auth === null ? undefined : { ok: true, ...(setup.apiKey === null ? {} : { apiKey: access }), headers: { Authorization: setup.authorization ?? `Bearer ${access}` } };
        },
        getProviderAuth: async () => setup.auth === null ? undefined : { source: "OAuth", auth: { ...(setup.apiKey === null ? {} : { apiKey: access }) } },
      } };
      const offered = (setup.candidates ?? [{ ...resetCredential, access }]).map((candidate) => candidate === "throwing-getter"
        ? Object.defineProperty({}, "type", { enumerable: true, get() { throw new Error("Synthetic candidate getter failure"); } }) : candidate);
      const reader = async () => ({ ok: setup.discoveryOk ?? true, candidates: offered, offeredCount: setup.offeredCount ?? offered.length });
      if (setup.ready !== null) reader.waitUntilReady = async () => setup.ready ?? true;
      try {
        return { resolved: await library.resolveCodexResetAuth(ctx, new Uint8Array(32), () => setup.stored, setup.reader === null ? undefined : reader) };
      } catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}
for (const item of cases.filter((item) => item.id.endsWith(":normal"))) {
  for (const variant of ["official", "custom-origin", "provider-override", "resolved-proxy", "selected-proxy", "readiness-denied", "cancelled"]) {
    const input = { provider: item.provider, baseUrl: item.baseUrl, variant };
    fixture(`auth:${item.provider}:${variant}`, "src/query.ts", ["resolveUsageAuth"], 3,
      "Synthetic registry boundary records official-origin validation order, credential-source readiness and cancellation before normalization; this is not evidence of real host credential discovery.", input,
      async (library) => {
        const trace = [];
        const selected = { provider: item.provider, id: "fixture", baseUrl: variant === "custom-origin" ? "https://fixture.invalid/v1" : item.baseUrl };
        const controller = new AbortController();
        if (variant === "cancelled") controller.abort();
        const registry = {
          getAvailable: () => [selected], getAll: () => [selected],
          getProvider: () => { trace.push("getProvider"); return { baseUrl: variant === "provider-override" ? "https://fixture.invalid/v1" : item.baseUrl }; },
          getApiKeyAndHeaders: async () => { trace.push("getApiKeyAndHeaders"); return { ok: true, apiKey: "fixture-access", headers: { Authorization: "Bearer fixture-access" }, ...(variant === "selected-proxy" ? { baseUrl: "https://fixture.invalid/v1" } : {}) }; },
          getProviderAuth: async () => { trace.push("getProviderAuth"); return { source: "fixture", auth: { apiKey: "fixture-access", ...(variant === "resolved-proxy" ? { baseUrl: "https://fixture.invalid/v1" } : {}) } }; },
        };
        const candidates = async () => { trace.push("candidates"); return { ok: true, candidates: [], offeredCount: 0 }; };
        candidates.waitUntilReady = async () => { trace.push("waitUntilReady"); return variant !== "readiness-denied"; };
        try {
          const resolved = await library.resolveUsageAuth({ model: selected, modelRegistry: registry }, library.adapterForProvider(item.provider), new Uint8Array(32), () => undefined, candidates, undefined, { signal: controller.signal });
          return { trace, resolved };
        } catch (error) {
          if (!(error instanceof Error)) throw error;
          return { trace, error: { name: error.name, message: error.message } };
        }
      });
  }
}

for (const provider of ["openai", "github-copilot", "xai"]) {
  const baseUrl = cases.find((item) => item.provider === provider).baseUrl;
  for (const variant of ["match", "mismatch", "missing-refresh", "invalid-expiry", "conflicting", "duplicate", "discovery-denied", "null-authorization", "empty-authorization", "other-authorization", "ambiguous-authorization", "enterprise"]) {
    const credential = { type: "oauth", access: "fixture-access", refresh: "fixture-refresh", expires: now + 3600000,
      ...(provider === "openai" ? { clientId: "fixture-client", scopes: ["chatgpt.tokens.use.direct"] } : {}) };
    if (variant === "mismatch") credential.access = "another-access";
    if (variant === "missing-refresh") delete credential.refresh;
    if (variant === "invalid-expiry") credential.expires = "invalid";
    if (variant === "enterprise") credential.enterpriseUrl = "https://enterprise.fixture.invalid";
    const offered = [credential];
    if (variant === "conflicting") offered.push({ ...credential, refresh: "another-refresh" });
    if (variant === "duplicate") offered.push({ ...credential });
    const headers = { Authorization: variant === "null-authorization" ? null : variant === "empty-authorization" ? "" : variant === "other-authorization" ? "Basic synthetic" : "Bearer fixture-access" };
    if (variant === "ambiguous-authorization") headers.authorization = "Bearer another-access";
    const input = { provider, baseUrl, headers, candidates: offered, discoveryOk: variant !== "discovery-denied" };
    fixture(`auth:oauth:${provider}:${variant}`, "src/query.ts", ["resolveUsageAuth"], 3,
      "Fresh resolved OAuth authentication must match complete candidate identity; conflicting/enterprise/override variants record provider-specific rejection and deduplication, not host credential discovery.", input,
      async (library) => {
        const selected = { provider, id: "fixture", baseUrl };
        const trace = [];
        const registry = {
          getAvailable: () => [selected], getAll: () => [selected], getProvider: () => ({ baseUrl }),
          getApiKeyAndHeaders: async () => { trace.push("selected"); return { ok: true, apiKey: "fixture-access", headers }; },
          getProviderAuth: async () => { trace.push("provider"); return { source: "OAuth", auth: { apiKey: "fixture-access" } }; },
        };
        const candidates = async () => ({ ok: input.discoveryOk, candidates: offered, offeredCount: offered.length });
        try {
          return { resolved: await library.resolveUsageAuth({ model: selected, modelRegistry: registry }, library.adapterForProvider(provider), new Uint8Array(32), () => undefined, candidates), trace };
        } catch (error) {
          if (!(error instanceof Error)) throw error;
          return { trace, error: { name: error.name, message: error.message } };
        }
      });
  }
}
for (const provider of ["moonshotai", "moonshotai-cn"]) {
  for (const selectedProvider of [provider, "deepseek"]) {
    for (const status of [undefined, { configured: true }, { configured: false, source: "OAuth" }, { configured: true, source: "environment", label: "MOONSHOT_API_KEY" }, { configured: true, source: "environment", label: "REGION_SPECIFIC_KEY" }, { configured: true, source: "OAuth" }]) {
      const input = { provider, selectedProvider, status };
      fixture(`auth:regional:${provider}:${selectedProvider}:${fixtures.filter((item) => item.id.startsWith("auth:regional:")).length}`, "src/query.ts", ["providerIsConfigured"], 3,
        "Regional sibling discovery rejects shared MOONSHOT_API_KEY and unknown sources while current-provider resolution and explicit regional sources retain their own policy.", input,
        async (library) => library.providerIsConfigured({ model: { provider: selectedProvider }, modelRegistry: {
          getProviderAuthStatus: () => { if (!status) throw new Error("Synthetic status unavailable"); return status; },
          getAvailable: () => [{ provider, id: "fixture" }], getAll: () => [],
        } }, provider));
    }
  }
}

const settingsScenarios = ["missing", "loaded", "invalid-json", "invalid-shape", "oversize", "symlink", "directory", "update", "ordered-updates", "invalid-patch", "write-failure", "rename-failure", "concurrent-change", "queue-recovers", "target-commit", "target-rollback-loaded", "target-rollback-missing", "target-rollback-conflict", "invalid-target", "aborted"];
for (const scenario of settingsScenarios) {
  fixture(`settings:file:${scenario}`, "src/settings.ts", ["loadUsageSettings", "createUsageSettingsRuntime"], 4,
    "Disposable file transaction records settings reads, private atomic saves, unknown-field preservation, ordered updates, refusal to overwrite and publication rollback.", { scenario, document: { unknown: { keep: true }, fireworksAccountId: "legacy" } },
    async (library, directory) => {
      await mkdir(directory, { recursive: true });
      const path = join(directory, "pi-usage.json");
      const initial = JSON.stringify({ unknown: { keep: true }, fireworksAccountId: "legacy" });
      if (scenario === "directory") await mkdir(path);
      else if (scenario === "symlink") { await writeFile(join(directory, "target"), initial); await symlink("target", path); }
      else if (!["missing", "target-rollback-missing"].includes(scenario)) {
        await writeFile(path, scenario === "invalid-json" ? "{" : scenario === "invalid-shape" ? "[]" : scenario === "oversize" ? " ".repeat(65537) : initial, { mode: 0o600 });
      }
      const trace = [];
      const operations = {
        async writeFile(temporary, data, options) {
          trace.push({ operation: "writeFile", mode: options.mode, flag: options.flag });
          if (scenario === "write-failure" || (scenario === "queue-recovers" && trace.length === 1)) throw new Error("Synthetic write failure");
          await writeFile(temporary, data, options);
          if (scenario === "concurrent-change") await writeFile(path, '{"external":true}');
        },
        async rename(temporary, destination) {
          trace.push({ operation: "rename" });
          if (scenario === "rename-failure") throw new Error("Synthetic rename failure");
          await rename(temporary, destination);
        },
      };
      const runtime = library.createUsageSettingsRuntime({ path, operations });
      const observations = [];
      async function observe(action) {
        try { observations.push({ outcome: "value", value: await action() }); }
        catch (error) {
          if (!(error instanceof Error)) throw error;
          observations.push({ outcome: "error", error: { name: error.name, message: error.message, ...(error instanceof AggregateError ? { errors: error.errors.map((item) => item.message) } : {}) } });
        }
      }
      await observe(() => runtime.reload());
      if (["missing", "loaded", "invalid-json", "invalid-shape", "oversize", "symlink", "directory", "update", "write-failure", "rename-failure", "concurrent-change"].includes(scenario)) {
        await observe(() => runtime.update({ codexFastMode: true }));
      } else if (scenario === "ordered-updates") {
        await observe(() => Promise.all([runtime.update({ codexFastMode: true }), runtime.update({ codexStatusPercentage: "used" })]));
      } else if (scenario === "queue-recovers") {
        await observe(() => runtime.update({ codexFastMode: true }));
        await observe(() => runtime.update({ codexStatusPercentage: "used" }));
      } else if (scenario === "invalid-patch") {
        await observe(() => runtime.update({ codexFastMode: "invalid" }));
      } else if (scenario === "aborted") {
        await observe(() => runtime.update({ codexFastMode: true }, AbortSignal.abort()));
      } else {
        await observe(() => runtime.updateSelectedTarget("fireworks", scenario === "invalid-target" ? "bad\nvalue" : "fixture-selected", undefined, async () => {
          if (scenario === "target-rollback-conflict") await writeFile(path, '{"external":true}');
          if (scenario.startsWith("target-rollback")) throw new Error("Synthetic publication changed");
        }));
      }
      await runtime.flush();
      const info = await lstat(path).catch((error) => { if (error.code === "ENOENT") return undefined; throw error; });
      const file = info?.isFile() ? await readFile(path, "utf8") : undefined;
      const linkTarget = info?.isSymbolicLink() ? await readlink(path) : undefined;
      return { observations, trace, runtime: runtime.get(), file, linkTarget, mode: info?.isFile() ? info.mode & 0o777 : undefined, remainingFiles: (await readdir(directory)).sort() };
    });
}
fixture("cache:ownership-expiry-eviction", "src/core.ts", ["UsageCache"], 3,
  "Account-keyed cache uses TTL at the boundary, bounded FIFO replacement, provider invalidation and coalesced notifications.", { ttlMs: 100, maxEntries: 2, time: now },
  async (library) => {
    const changes = [];
    const cache = new library.UsageCache(100, 2, (providerId, reason) => changes.push({ providerId, reason }));
    const report = { providerId: "deepseek", marker: "synthetic" };
    cache.set("deepseek", "account-a", report, now);
    const otherAccount = cache.get("deepseek", "account-b", now);
    const beforeExpiry = cache.getEntry("deepseek", "account-a", now + 99);
    const atExpiry = cache.getEntry("deepseek", "account-a", now + 100);
    cache.set("deepseek", "account-a", report, now + 101);
    cache.set("openai", "account-b", report, now + 101);
    cache.set("xai", "account-c", report, now + 101);
    const evicted = cache.get("deepseek", "account-a", now + 101);
    cache.clearProvider("openai");
    cache.delete("xai", "account-c");
    cache.clear();
    return { otherAccount, beforeExpiry, atExpiry, evicted, size: cache.size, changes };
  });
for (const [id, ttl, limit] of [["zero-ttl", 0, 2], ["invalid-ttl", Infinity, 2], ["zero-limit", 100, 0], ["fractional-limit", 100, 1.5]]) {
  fixture(`cache:${id}`, "src/core.ts", ["UsageCache"], 3, "Invalid cache lifetime/capacity are rejected before use.", { ttl: Number.isFinite(ttl) ? ttl : String(ttl), limit }, async (library) => new library.UsageCache(ttl, limit));
}
for (const input of ["\u001b[31mRed\u001b[0m\nText", "\u001b]0;title\u0007Text", "\u009b31mText\u009dtitle\u001b\\end", "\u001b]unterminated", "long".repeat(60)]) {
  fixture(`display:sanitize:${fixtures.filter((item) => item.id.startsWith("display:")).length}`, "src/core.ts", ["sanitizeDisplayText", "redactUsageError"], 5,
    "Terminal controls are removed, whitespace collapsed, length bounded and synthetic credential values redacted.", { input, maxChars: 20 },
    async (library) => ({ display: library.sanitizeDisplayText(input, 20), error: library.redactUsageError(`${input} fixture-access Bearer synthetic-token {"refresh_token":"synthetic-refresh"}`, ["fixture-access"]) }));
}
for (const variant of ["partial-failure", "cancel-before", "cancel-during", "empty", "invalid-limit"]) {
  const input = { items: variant === "empty" ? [] : [0, 1, 2, 3], limit: variant === "invalid-limit" ? 0 : 2, variant };
  fixture(`concurrency:${variant}`, "src/core.ts", ["runWithConcurrency"], 3,
    "Bounded workers retain input ordering and partial results; caller cancellation rejects rather than publishing an ordinary empty/partial success.", input,
    async (library) => {
      const controller = new AbortController();
      if (variant === "cancel-before") controller.abort();
      let active = 0;
      let peak = 0;
      const trace = [];
      try {
        const result = await library.runWithConcurrency(input.items, input.limit, async (item) => {
          active += 1;
          peak = Math.max(peak, active);
          trace.push({ start: item });
          try {
            await new Promise((resolve) => setImmediate(resolve));
            if (variant === "cancel-during" && item === 0) controller.abort();
            if (item === 1) throw new Error("Synthetic worker failure");
            return item * 2;
          } finally {
            active -= 1;
            trace.push({ end: item });
          }
        }, controller.signal);
        return { trace, peak, result: result.map((item) => item.status === "fulfilled" ? item : { status: item.status, reason: item.reason.message }) };
      } catch (error) {
        if (!(error instanceof Error)) throw error;
        await new Promise((resolve) => setImmediate(resolve));
        return { trace, peak, error: { name: error.name, message: error.message } };
      }
    });
}
for (const variant of ["resolved", "rejected", "preaborted", "aborted", "timeout"]) {
  fixture(`deadline:${variant}`, "src/core.ts", ["awaitWithDeadline"], 3,
    "Owned deadline/caller cancellation and backend rejection remain distinct from a successful result; timers are released after settlement.", { variant, timeoutMs: variant === "timeout" ? 1 : 1000 },
    async (library) => {
      const controller = new AbortController();
      const operation = variant === "resolved" ? Promise.resolve("synthetic-result") : variant === "rejected" ? Promise.reject(new Error("Synthetic backend rejection")) : new Promise(() => {});
      if (variant === "preaborted") controller.abort();
      const result = library.awaitWithDeadline(operation, controller.signal, variant === "timeout" ? 1 : 1000, "synthetic operation");
      if (variant === "aborted") controller.abort();
      return await result;
    });
}
fixture("cache:reentrant-observer-failure", "src/core.ts", ["UsageCache"], 3,
  "Synchronous observer reentry coalesces provider/reason notifications; observer failure cannot prevent the cache mutation or later notifications.", { ttlMs: 100, maxEntries: 2 },
  async (library) => {
    const trace = [];
    const cache = new library.UsageCache(100, 2, (provider, reason) => {
      trace.push({ provider, reason });
      cache.set(provider, "a", { marker: "reentrant" }, now);
      if (provider === "deepseek") throw new Error("Synthetic observer failure");
    });
    cache.set("deepseek", "a", { marker: "initial" }, now);
    cache.set("openai", "a", { marker: "initial" }, now);
    return { trace, deepseek: cache.get("deepseek", "a", now), openai: cache.get("openai", "a", now), size: cache.size };
  });

const at = (seconds) => now / 1000 + seconds;
const codexGroups = {
  rate_limit: { primary_window: { used_percent: 23, limit_window_seconds: 18_000, reset_at: at(3_600) }, secondary_window: { used_percent: 40, limit_window_seconds: 604_800, reset_at: at(183_600) } },
  additional_rate_limits: [
    { metered_feature: "gpt_5_3_codex_spark", limit_name: "GPT-5.3-Codex-Spark", rate_limit: { primary_window: { used_percent: 10, limit_window_seconds: 5_400, reset_at: at(86_400) } } },
    { metered_feature: "codex_max", limit_name: "Codex Max", rate_limit: { primary_window: { used_percent: 105, limit_window_seconds: 1_209_600, reset_at: at(90) }, secondary_window: { used_percent: -5, limit_window_seconds: 172_800 } } },
  ],
  credits: { has_credits: true, unlimited: true },
};
const codexModels = [
  ["no-model", undefined], ["other-provider", { provider: "openai", id: "gpt-5.5-codex-max" }],
  ["key-match", { provider: "openai-codex", id: "gpt-5.3-codex-spark", name: "GPT-5.3 Codex Spark" }],
  ["variant-token", { provider: "openai-codex", id: "gpt-5.5-codex-max" }], ["no-group", { provider: "openai-codex", id: "gpt-5.5" }],
];
const customDisplay = { template: "[{provider}] {windows}", window: "{window}={value}", countdown: "{window}={value} in {reset}", separator: " | " };
const statuslines = (library, report, model, time) => ({
  remaining: library.formatUsageStatusline(report, model, time),
  used: library.formatUsageStatusline(report, model, time, true, "used"),
  noCountdown: library.formatUsageStatusline(report, model, time, false),
  custom: library.formatUsageStatusline(report, model, time, true, "remaining", customDisplay),
});
for (const [name, model] of codexModels) {
  fixture(`format:codex-group:${name}`, "src/format.ts", ["formatUsageStatusline", "formatUsageReport"], 5,
    "A normalized multi-group Codex report selects the statusline group from the selected model's provider, exact keys and Codex variant token, then renders remaining/used/countdown-off and custom window templates.",
    { payload: codexGroups, model, time: now },
    async (library) => {
      const report = library.normalizeCodexBackendPayload(codexGroups, now);
      return { statuslines: statuslines(library, report, model, now), current: library.formatUsageReport(report, "current") };
    });
}
for (const [name, offset] of [["days-hours", -1_800], ["exact-days", -10_800], ["days-minutes", -95_400], ["hours-minutes", 4_500], ["minutes", -900], ["expired", -200_000], ["non-finite-now", Number.NaN]]) {
  fixture(`format:codex-countdown:${name}`, "src/format.ts", ["formatUsageStatusline"], 5,
    "Codex reset countdowns render day/hour/minute combinations from the caller's clock, clamp elapsed resets and omit countdowns for a non-finite clock.",
    { payload: codexGroups, timeOffsetSeconds: offset },
    async (library) => {
      const report = library.normalizeCodexBackendPayload(codexGroups, now);
      const model = { provider: "openai-codex", id: "gpt-5.5" };
      return statuslines(library, report, model, now - offset * 1000);
    });
}
for (const [name, payload] of [
  ["credits-none", { credits: { has_credits: false } }], ["credits-available", { credits: { has_credits: true } }],
  ["credits-balance", { credits: { has_credits: true, balance: "3" } }], ["credits-unlimited", { credits: { has_credits: true, unlimited: true } }],
  ["reset-count-only", { rate_limit_reset_credits: { available_count: 1 } }],
  ["additional-only", { additional_rate_limits: [{ limit_name: "Codex_Mini Window", rate_limit: { primary_window: { used_percent: 50 } } }] }],
  ["primary-without-reset", { rate_limit: { primary_window: { used_percent: 60 }, secondary_window: { used_percent: 70, limit_window_seconds: 0 } }, plan_type: "plus" }],
]) {
  fixture(`format:codex-fallback:${name}`, "src/format.ts", ["formatUsageStatusline", "formatUsageReport"], 5,
    "Codex reports without a selectable quota window fall back to credit status; reports without a codex group use the first group and fallback window labels.",
    { payload },
    async (library) => {
      const report = library.normalizeCodexBackendPayload(payload, now);
      return { statuslines: Object.fromEntries(codexModels.map(([id, model]) => [id, library.formatUsageStatusline(report, model, now)])), current: library.formatUsageReport(report, "configured") };
    });
}
const miniMaxRow = (model_name, remaining) => ({
  model_name, start_time: now - 14_400_000, end_time: now + 3_600_000, weekly_start_time: now - 601_200_000, weekly_end_time: now + 3_600_000,
  current_interval_total_count: 100, current_interval_usage_count: remaining, current_interval_remaining_percent: remaining,
  current_weekly_total_count: 1000, current_weekly_usage_count: remaining * 10, current_weekly_remaining_percent: remaining,
});
for (const [name, rows] of [
  ["wildcards", [miniMaxRow("MiniMax-M*", 70), miniMaxRow("Speech-*-HD", 60), miniMaxRow("*-Lightning", 50), miniMaxRow("General", 40)]],
  ["no-general", [miniMaxRow("MiniMax-M*", 70), miniMaxRow("Speech-*-HD", 60)]],
  ["single", [miniMaxRow("MiniMax-M*", 70)]],
]) {
  for (const [modelName, model] of [
    ["no-model", undefined], ["other-provider", { provider: "openai", id: "minimax-m2" }], ["prefix-wildcard", { provider: "minimax", id: "MiniMax-M2.5" }],
    ["middle-wildcard", { provider: "minimax", id: "speech-02-hd" }], ["middle-wildcard-miss", { provider: "minimax", id: "speech-02-turbo" }],
    ["leading-wildcard", { provider: "minimax", id: "fixture", name: "MiniMax-M2 Lightning" }], ["exact", { provider: "minimax", id: "general" }],
    ["symbols-only", { provider: "minimax", id: "--", name: "**" }],
  ]) {
    fixture(`format:minimax-group:${name}:${modelName}`, "src/format.ts", ["formatUsageStatusline"], 5,
      "MiniMax Token Plan statuslines select one quota group by exact key, wildcard pattern or general fallback, and hide the chip for another provider's model.",
      { rows, model },
      async (library) => {
        const report = library.normalizeMiniMaxUsagePayload("minimax", "token-plan", { base_resp: { status_code: 0 }, model_remains: rows }, now);
        return statuslines(library, report, model, now);
      });
  }
}
fixture("format:provider-states", "src/format.ts", ["formatProviderStates"], 5,
  "Provider state lists render ready reports, target-selection prompts and each unavailable status with current/configured labels.",
  { states: ["ready-current", "selection-required-configured", "auth-unavailable-current", "unsupported-configured", "query-failed-configured"] },
  async (library) => {
    const report = library.normalizeCodexBackendPayload(codexGroups, now);
    return library.formatProviderStates([
      { providerId: "openai-codex", providerName: "OpenAI Codex", displayState: "current", status: "ready", report },
      { providerId: "fireworks", providerName: "Fireworks", displayState: "configured", status: "selection-required", singularLabel: "account", choices: [] },
      { providerId: "xai", providerName: "xAI", displayState: "current", status: "auth-unavailable", message: "Synthetic auth unavailable" },
      { providerId: "fixture", providerName: "Fixture", displayState: "configured", status: "unsupported", message: "Synthetic unsupported" },
      { providerId: "deepseek", providerName: "DeepSeek", displayState: "configured", status: "query-failed", message: "Synthetic failure" },
    ]);
  });
const edgeReport = (providerId, providerName, buckets, metrics = []) => ({ providerId, providerName, source: `${providerId}-fixture`, semantics: { kind: "api-key", label: "Synthetic edge report" }, buckets, metrics, fetchedAt: now });
for (const [name, report] of [
  ["xai-limit-only", edgeReport("xai", "xAI", [{ id: "included-allowance", label: "Included allowance", unit: "usd", limit: 25 }, { id: "on-demand", label: "On-demand", unit: "usd" }])],
  ["xai-unavailable", edgeReport("xai", "xAI", [{ id: "included-allowance", label: "Included allowance", unit: "usd" }, { id: "on-demand", label: "On-demand", unit: "usd", used: 1.5, limit: 10 }])],
  ["zai-partial-counts", edgeReport("zai", "Z.AI", [{ id: "used-only", label: "Used only", unit: "count", used: 3 }, { id: "remaining-only", label: "Remaining only", unit: "count", remaining: 4 },
    { id: "empty", label: "Empty", unit: "count" }, { id: "used-limit", label: "Used and limit", unit: "count", used: 2, limit: 5 }])],
  ["zai-empty", edgeReport("zai", "Z.AI", [])],
  ["openrouter-cap-only", edgeReport("openrouter", "OpenRouter", [{ id: "key-limit", label: "Key limit", unit: "usd" }])],
  ["openrouter-remaining-only", edgeReport("openrouter", "OpenRouter", [{ id: "key-limit", label: "Key limit", unit: "usd", remaining: 5 }])],
  ["deepseek-no-metrics", edgeReport("deepseek", "DeepSeek", [])],
  ["generic-unavailable", edgeReport("openai", "OpenAI", [{ id: "plan", label: "Plan", unit: "count" }])],
]) {
  fixture(`format:report-edge:${name}`, "src/format.ts", ["formatUsageReport"], 5,
    "Provider report formatting of partial normalized reports: limit-only, used-only, remaining-only and empty buckets, absent periods and resets, and no metrics.", { report },
    async (library) => ({ current: library.formatUsageReport(report, "current"), configured: library.formatUsageReport(report, "configured") }));
}
const shapeReport = (providerId, source, buckets, metrics = []) => ({ ...edgeReport(providerId, providerId, buckets, metrics), source });
const unknownReset = 1e20;
for (const [name, report, model] of [
  ["companion-unavailable", shapeReport("openai", "openai-chatgpt-companion", [{ id: "plan-weekly", label: "Weekly", groupId: "chatgpt-plan", unit: "percent", windowMinutes: 10080 },
    { id: "app-images", label: "Images", groupId: "chatgpt-app", unit: "percent" }])],
  ["codex-ungrouped", shapeReport("openai-codex", "codex-fixture", [{ id: "codex", label: "5h", unit: "percent", used: 40 },
    { id: "spark:primary", label: "5h", groupId: "spark-pro", unit: "percent", remaining: 30 }, { id: "spark:secondary", label: "Weekly", groupId: "spark-pro", unit: "percent", windowMinutes: 10080 }]),
    { provider: "openai-codex", id: "--gpt-5-codex-spark--" }],
  ["codex-unkeyed-group", shapeReport("openai-codex", "codex-fixture", [{ id: "a", label: "5h", groupId: "!!!", unit: "percent", remaining: 20 },
    { id: "b", label: "5h", groupId: "Mini Plan", groupLabel: "Mini Plan", unit: "percent" }], [{ id: "credits", label: "Credits", value: "available" }]),
    { provider: "openai-codex", id: "gpt-5-codex-mini" }],
  ["deepseek-unavailable", shapeReport("deepseek", "deepseek-fixture", [])],
  ["deepseek-no-totals", shapeReport("deepseek", "deepseek-fixture", [], [{ id: "api-availability", label: "API availability", value: "available" }])],
  ["vercel-no-balance", shapeReport("vercel-ai-gateway", "vercel-fixture", [])],
  ["copilot-no-quota", shapeReport("github-copilot", "copilot-fixture", [])],
  ["copilot-zero-limit", shapeReport("github-copilot", "copilot-fixture", [{ id: "premium-requests", label: "Premium requests", unit: "count", limit: 0, remaining: 0 }])],
  ["opencode-partial", shapeReport("opencode-go", "opencode-fixture", [{ id: "rolling", label: "Rolling", unit: "count" },
    { id: "monthly", label: "Monthly", unit: "count", used: 5, resetsAt: unknownReset }, { id: "weekly", label: "Weekly", unit: "percent", used: 20, remaining: 80 }])],
  ["opencode-unused", shapeReport("opencode-go", "opencode-fixture", [{ id: "rolling", label: "Rolling", unit: "count" }])],
  ["command-code-unavailable", shapeReport("command-code", "command-code-fixture", [{ id: "five-hour", label: "5h", unit: "usd" }])],
  ["kimi-partial", shapeReport("kimi-coding", "kimi-fixture", [{ id: "weekly", label: "Weekly", unit: "percent" }, { id: "monthly", label: "Monthly", unit: "count", remaining: 3 }],
    [{ id: "booster-balance", label: "Balance", value: "n/a" }, { id: "booster-monthly-used", label: "Used", value: 3 }, { id: "booster-monthly-limit", label: "Limit", value: 9, currency: "CNY" }])],
  ["moonshot-no-balance", shapeReport("moonshotai", "moonshot-fixture", [])],
  ["moonshot-no-currency", shapeReport("moonshotai", "moonshot-fixture", [], [{ id: "available-balance", label: "Available", value: 3 }])],
  ["minimax-ungrouped", shapeReport("minimax", "minimax-fixture", [{ id: "a:primary", label: "Text", unit: "percent", remaining: 40 }, { id: "a:weekly", label: "Text weekly", unit: "count", remaining: 2 }])],
  ["minimax-grouped-unavailable", shapeReport("minimax", "minimax-fixture", [{ id: "g:primary", label: "Speech", groupId: "speech", unit: "count", remaining: 2 }])],
  ["minimax-balance-unavailable", shapeReport("minimax", "minimax-account-balance", [])],
  ["zai-no-limits", shapeReport("zai", "zai-fixture", [{ id: "five-hour", label: "5h", unit: "count", remaining: 4 }])],
]) {
  fixture(`format:shape:${name}`, "src/format.ts", ["formatUsageReport", "formatUsageStatusline"], 5,
    "Report and statusline formatting of normalized reports with missing groups, labels, quotas, totals, currencies, limits and resets, unknown reset dates and unnormalizable keys.", { report, model },
    async (library) => ({ current: library.formatUsageReport(report, "current"), configured: library.formatUsageReport(report, "configured"), statuslines: statuslines(library, report, model, now) }));
}
for (const [name, display] of [["malformed", { window: "{value" }], ["unsupported-placeholder", { countdown: "{provider}" }], ["control", { separator: "\n" }], ["non-object", "template"]]) {
  fixture(`format:display-invalid:${name}`, "src/format.ts", ["formatUsageStatusline"], 5,
    "An invalid statusline display configuration fails before any template is rendered.", { display },
    async (library) => library.formatUsageStatusline(library.normalizeCodexBackendPayload(codexGroups, now), undefined, now, true, "remaining", display));
}

export const protocolFixtures = [
  { event: "usage:capabilities:v1", options: {} },
  { event: "usage:query:v1", options: { refresh: false } },
  { event: "usage:query:v1", options: { refresh: true } },
  { event: "usage:query:v1", options: { refresh: true, cancel: true } },
  { event: "usage:query:v1", options: { refresh: false, foreignSession: true } },
  { event: "usage:query:v1", options: { refresh: false, ownSession: true } },
  ...cases.filter((item) => item.id.endsWith(":normal")).flatMap((item) => [
    { event: "usage:capabilities:v1", options: { providerId: item.provider } },
    { event: "usage:query:v1", options: { providerId: item.provider, refresh: false } },
  ]),
  { event: "usage:capabilities:v1", options: { providerId: "fixture-unsupported" } },
  { event: "usage:query:v1", options: { providerId: "fixture-unsupported", refresh: true } },
  { event: "usage:query:v1", options: { refresh: true, preaborted: true } },
  { event: "usage:query:v1", options: { refresh: true, offerReturn: false } },
  { event: "usage:query:v1", options: { refresh: true, offerThrows: true } },
  { event: "usage:query:v1", options: { providerId: "invalid/id", expectNoOffer: true } },
  { event: "usage:query:v1", options: { refresh: "invalid", expectNoOffer: true } },
  { event: "usage:query:v1", options: { invalidSignal: true, expectNoOffer: true } },
].map((item, index) => ({ id: `protocol:${index}`, ...item }));

const kimiWindow = (duration, timeUnit, detail, name) => ({ window: { duration, timeUnit }, ...(detail === undefined ? {} : { detail }), ...(name === undefined ? {} : { name }) });
const kimiReset = (resetTime) => ({ limit: "10", used: "1", resetTime });
const kimiWallet = (change) => {
  const wallet = { balance: { type: "BOOSTER", amount: 1_000_000_000, amountLeft: 500_000_000 }, monthlyUsed: { priceInCents: 100, currency: "USD" },
    monthlyChargeLimit: { priceInCents: 2_000, currency: "USD" }, monthlyChargeLimitEnabled: true };
  change(wallet);
  return { usages: { limit_month_total: { used_ratio: 0.5 } }, boosterWallet: wallet };
};
for (const [name, payload] of [
  ["not-object", null],
  ["no-limits", { limits: [] }],
  ["limits-not-array", { usage: { limit: "10" }, limits: {} }],
  ["usages-not-object", { usage: { limit: "10" }, usages: "fixture" }],
  ["window-shapes", { limits: [
    null, { window: "fixture" }, kimiWindow(0, "TIME_UNIT_HOUR"), kimiWindow(3, "TIME_UNIT_SECOND"), kimiWindow(2 ** 52, "TIME_UNIT_WEEK"),
    kimiWindow(30, "TIME_UNIT_MINUTE", { limit: "3" }, 5), kimiWindow(90, "TIME_UNIT_MINUTE", { limit: "4" }, "\u0007"),
    kimiWindow(1, "TIME_UNIT_DAY", { limit: "5", remaining: "2" }, "Fixture daily"), kimiWindow(3, "TIME_UNIT_DAY", { limit: "6" }),
    kimiWindow(2, "TIME_UNIT_WEEK", { limit: "7" }), kimiWindow(1, "TIME_UNIT_WEEK", { limit: "8", used: "8" }), kimiWindow(4, "TIME_UNIT_HOUR", { limit: "9" }),
  ] }],
  ["row-rejections", { limits: [
    kimiWindow(2, "TIME_UNIT_HOUR", { limit: "0" }), kimiWindow(4, "TIME_UNIT_HOUR", { limit: "5", used: "bad" }), kimiWindow(6, "TIME_UNIT_HOUR", { limit: "5", remaining: -1 }),
    kimiWindow(8, "TIME_UNIT_HOUR", {}), kimiWindow(10, "TIME_UNIT_HOUR", "fixture"), kimiWindow(12, "TIME_UNIT_HOUR", { limit: "5" }), kimiWindow(12, "TIME_UNIT_HOUR", { limit: "6" }),
  ], usages: { limit_month_total: { used_ratio: 0.1 } } }],
  ["ratio-windows", { limits: [kimiWindow(5, "TIME_UNIT_HOUR", { limit: "10" })], usages: {
    limit_5h: { used_ratio: 0.2 }, limit_week: { used_ratio: 2 }, limit_month_total: 5 } }],
  ["ratio-bounds", { usages: { limit_5h: { used_ratio: -0.1 }, limit_week: { used_ratio: true }, limit_month_total: { used_ratio: "0.5" } } }],
  ["reset-times", { limits: [
    kimiWindow(1, "TIME_UNIT_HOUR", kimiReset("fixture")), kimiWindow(2, "TIME_UNIT_HOUR", kimiReset("2030-13-01T00:00:00Z")),
    kimiWindow(3, "TIME_UNIT_HOUR", kimiReset("2030-00-01T00:00:00Z")), kimiWindow(4, "TIME_UNIT_HOUR", kimiReset("2030-02-00T00:00:00Z")),
    kimiWindow(5, "TIME_UNIT_HOUR", kimiReset("2030-02-30T00:00:00Z")), kimiWindow(6, "TIME_UNIT_HOUR", kimiReset("2030-02-01T24:00:00Z")),
    kimiWindow(7, "TIME_UNIT_HOUR", kimiReset("2030-02-01T00:60:00Z")), kimiWindow(8, "TIME_UNIT_HOUR", kimiReset("2030-02-01T00:00:60Z")),
    kimiWindow(9, "TIME_UNIT_HOUR", kimiReset("2030-02-01T00:00:00+24:00")), kimiWindow(10, "TIME_UNIT_HOUR", kimiReset("2030-02-01T00:00:00+05:60")),
    kimiWindow(11, "TIME_UNIT_HOUR", kimiReset("2030-02-01T00:00:00.250+05:30")), kimiWindow(13, "TIME_UNIT_HOUR", kimiReset("1969-12-31T23:59:59Z")),
  ] }],
  ["wallet-no-amount", kimiWallet((wallet) => { delete wallet.balance.amount; })],
  ["wallet-no-left", kimiWallet((wallet) => { delete wallet.balance.amountLeft; wallet.monthlyChargeLimitEnabled = false; })],
  ["wallet-sub-cent", kimiWallet((wallet) => { wallet.balance.amountLeft = 1; delete wallet.monthlyUsed; delete wallet.monthlyChargeLimitEnabled; })],
  ["wallet-mixed-currency", kimiWallet((wallet) => { wallet.monthlyUsed.currency = "EUR"; })],
  ["wallet-invalid-money", kimiWallet((wallet) => { wallet.monthlyUsed = { priceInCents: "x", currency: "USD" }; wallet.monthlyChargeLimit = { priceInCents: 1, currency: 5 }; })],
  ["wallet-invalid-currency", kimiWallet((wallet) => { wallet.monthlyUsed = null; wallet.monthlyChargeLimit = { priceInCents: 1, currency: "US1" }; })],
  ["wallet-no-limit", kimiWallet((wallet) => { delete wallet.monthlyChargeLimit; })],
]) {
  fixture(`kimi:${name}`, "src/providers/kimi-coding.ts", ["normalizeKimiCodingUsagePayload"], 2,
    "Kimi Coding plan rows, proto windows, ratio fallbacks, reset timestamps and booster wallets keep valid windows, reject malformed or duplicate ones and never invent counts or currencies.", { payload },
    async (library) => {
      try { return { report: library.normalizeKimiCodingUsagePayload(payload, now) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}

const zaiLimit = (type, unit, fields) => ({ type, ...(unit === undefined ? {} : { unit }), ...fields });
for (const [name, payload, plan] of [
  ["error", { success: false, code: 1000 }],
  ["limits-not-array", { success: true, data: { level: "Synthetic plan", limits: {} } }],
  ["no-limits", { success: true, data: { limits: [] } }],
  ["limit-shapes", { success: true, data: { level: 5, limits: [
    null, zaiLimit("CREDIT_LIMIT", 3, { number: 2, percentage: 150 }), zaiLimit("TOKENS_LIMIT", 3, {}), zaiLimit("TOKENS_LIMIT", 3, { percentage: 10 }),
    zaiLimit("OTHER", 3, { percentage: 10 }), zaiLimit("OTHER", 6, { percentage: 10 }), zaiLimit("TOKENS_LIMIT", 6, { percentage: 40, number: 2 }),
    zaiLimit("TIME_LIMIT", undefined, { currentValue: 1 }), zaiLimit("TIME_LIMIT", undefined, { currentValue: 1, usage: 5, usageDetails: [] }),
    zaiLimit("TIME_LIMIT", undefined, { currentValue: 1, usage: 5, usageDetails: [null, { modelCode: 5, usage: 1 }, { modelCode: "fixture" }, { modelCode: "\u0007", usage: 2 }, { modelCode: "!!!", usage: 1 }] }),
  ] } }],
  ["plan-renewal", { success: true, data: { limits: [zaiLimit("CREDIT_LIMIT", 3, { percentage: 1 })] } }, { name: "Synthetic plan", renewsAt: "2030-03-18" }],
]) {
  fixture(`zai:quota:${name}`, "src/providers/zai.ts", ["normalizeZaiQuotaPayload"], 2,
    "Z.AI quota limits keep 5-hour, weekly and MCP windows with payload-driven lengths, skip malformed rows and details, and fail closed on errors or no displayable window.", { payload, plan },
    async (library) => {
      try { return { report: library.normalizeZaiQuotaPayload("zai", "Z.AI", payload, now, plan) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}
for (const [name, payload] of [
  ["error", { success: false, code: 1001 }],
  ["no-products", { success: true, data: [] }],
  ["no-state-metadata", { success: true, data: [null, { productName: 5 }, { productName: "Synthetic first" }, { productName: "Synthetic second", nextRenewTime: 1_900_000_000_000 }] }],
  ["numeric-state", { success: true, data: [{ productName: "Synthetic old", status: "EXPIRED", inCurrentPeriod: 0, nextRenewTime: 0 }, { productName: "Synthetic current", inCurrentPeriod: 1 }] }],
  ["status-only", { success: true, data: [{ productName: "Synthetic valid", status: "valid", inCurrentPeriod: "yes", nextRenewTime: "renews soon" }] }],
  ["inactive-only", { success: true, data: [{ productName: "Synthetic inactive", status: 7, inCurrentPeriod: false }, { productName: "Synthetic expired", status: "EXPIRED" }] }],
]) {
  fixture(`zai:subscription:${name}`, "src/providers/zai.ts", ["normalizeZaiSubscriptionPayload"], 2,
    "Z.AI subscriptions prefer the current valid product, then a current or valid product with missing state, and fall back to the quota level when only inactive products exist.", { payload },
    async (library) => ({ plan: library.normalizeZaiSubscriptionPayload(payload) ?? null }));
}

for (const [name, payload, tier] of [
  ["config-not-object", { config: [] }, null],
  ["legacy-usd", { config: { monthlyLimit: { val: 5_000 }, billingPeriodStart: "2030-03-01T00:00:00Z", billingPeriodEnd: "2030-04-01T00:00:00Z", prepaidBalance: {} } }, "\u0007"],
  ["used-only", { config: { used: { val: 125 }, onDemandUsed: { val: 10 }, currentPeriod: null } }, "Synthetic"],
  ["limit-and-cap-only", { config: { monthlyLimit: { val: 100 }, onDemandCap: { val: 200 }, currentPeriod: { type: "USAGE_PERIOD_TYPE_MONTHLY", start: "2030-03-01T00:00:00Z" } } }, undefined],
  ["period-only", { config: { currentPeriod: { type: "USAGE_PERIOD_TYPE_BILLING_CYCLE", end: "2030-04-01T00:00:00Z" } } }, undefined],
  ["period-start-only", { config: { currentPeriod: { start: "2030-03-01T00:00:00Z" } } }, undefined],
  ["empty-config", { config: { currentPeriod: {} } }, undefined],
  ["period-not-object", { config: { currentPeriod: "weekly" } }, undefined],
  ["usd-not-wrapper", { config: { onDemandCap: 5 } }, undefined],
  ["usd-unsafe", { config: { onDemandCap: { val: 1.5 } } }, undefined],
  ["usd-not-number", { config: { prepaidBalance: { val: "5" } } }, undefined],
  ["percent-out-of-range", { config: { creditUsagePercent: 101 } }, undefined],
  ["percent-negative", { config: { creditUsagePercent: -1 } }, undefined],
  ["percent-not-number", { config: { creditUsagePercent: "5" } }, undefined],
  ["timestamp-not-string", { config: { billingPeriodStart: 5 } }, undefined],
  ["timestamp-too-long", { config: { billingPeriodEnd: "2".repeat(81) } }, undefined],
  ["timestamp-invalid", { config: { currentPeriod: { end: "fixture" } } }, undefined],
  ["type-not-string", { config: { currentPeriod: { type: 5 } } }, undefined],
  ["type-too-long", { config: { currentPeriod: { type: "T".repeat(81) } } }, undefined],
  ["tier-not-string", { config: null }, 5],
  ["tier-too-long", {}, "t".repeat(161)],
]) {
  fixture(`xai:billing:${name}`, "src/providers/xai.ts", ["normalizeXaiBillingPayload"], 2,
    "xAI consumer billing keeps percent, USD allowance, on-demand and prepaid fields with their period, rejects malformed wrappers, percentages, timestamps and strings, and notes when nothing is displayable.", { payload, tier },
    async (library) => {
      try { return { report: library.normalizeXaiBillingPayload(payload, tier, now) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}

const commandCodeAccount = { user: { userName: "synthetic" }, org: { id: "fixture-org" } };
for (const [name, bundle] of [
  ["no-account-name", { account: { user: { userName: "\u0007" } } }],
  ["display-name", { account: { user: { name: "Synthetic" } }, credits: { credits: { monthlyCredits: 0 }, windowLimits: { limited: false } }, usage: { totalCost: 0, totalTokens: 5 } }],
  ["window-shapes", { account: commandCodeAccount, credits: { windowLimits: {
    fiveHour: { used: 1, cap: 0 }, weekly: { used: 2, cap: 10, resetAt: " 1900000000 " } } }, subscription: { data: { planId: "team_enterprise-plus", currentPeriodEnd: " " } } }],
  ["window-resets", { account: commandCodeAccount, credits: { windowLimits: { fiveHour: { used: 1, cap: 5, resetAt: -1 }, weekly: { used: 1, cap: 5, resetAt: "soon" } } },
    subscription: { data: { planId: "individual-", status: "trialing", currentPeriodEnd: "fixture" } } }],
  ["window-missing-used", { account: commandCodeAccount, credits: { windowLimits: { fiveHour: { cap: 5 }, weekly: "fixture" } }, usage: { totalCost: 3, totalTokensIn: 4 } }],
  ["credits-only", { account: commandCodeAccount, credits: { credits: { purchasedCredits: 4, freeCredits: 0 } }, subscription: { data: { planId: 5, status: "\u0007" } } }],
  ["plan-unknown-prefix", { account: commandCodeAccount, subscription: { data: { planId: "individual_custom-tier" } }, usage: { totalTokensOut: 2 } }],
  ["plan-symbols", { account: commandCodeAccount, subscription: { data: { planId: "---" } } }],
  ["nothing-displayable", { account: commandCodeAccount, credits: { credits: {} }, subscription: {}, usage: {} }],
]) {
  fixture(`command-code:normalize:${name}`, "src/providers/command-code.ts", ["normalizeCommandCodeUsagePayload"], 2,
    "Command Code reports keep rolling USD windows, the billing-period credit pool, plan, request and token metrics, note missing sections and reject a report with no safe account name or displayable data.", { bundle },
    async (library) => {
      try { return { report: library.normalizeCommandCodeUsagePayload(bundle, now) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}
for (const [name, account] of [["no-org", {}], ["org-not-object", { org: "fixture" }], ["invalid-id", { org: { id: "bad id" } }], ["numeric-id", { org: { id: 5 } }]]) {
  fixture(`command-code:org:${name}`, "src/providers/command-code.ts", ["commandCodeOrgId"], 2,
    "Only a bounded identifier from the account's organization becomes the Command Code org query parameter.", { account },
    async (library) => ({ orgId: library.commandCodeOrgId(account) ?? null }));
}

for (const [name, payload] of [
  ["not-array", { accounts: {} }], ["row-not-object", { accounts: [null] }], ["name-missing", { accounts: [{}] }],
  ["unsafe-name", { accounts: [{ name: "accounts/bad/id" }] }], ["unsafe-id", { accounts: [{ name: "accounts/-bad" }] }],
  ["repeated", { accounts: [{ name: "accounts/fixture" }, { name: "accounts/fixture" }] }], ["empty", { accounts: [] }],
]) {
  fixture(`fireworks:accounts:${name}`, "src/providers/fireworks.ts", ["normalizeFireworksAccountsPayload"], 2,
    "Fireworks account listings accept only unique safe accounts/<id> resource names and reject malformed rows.", { payload },
    async (library) => {
      try { return { accounts: library.normalizeFireworksAccountsPayload(payload) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}
const fireworksMoney = (totalCost, series = "SERVERLESS") => ({ lineItems: [{ series, totalCost }] });
for (const [name, payload, accountId] of [
  ["unsafe-account", {}, "bad/id"], ["line-items-not-array", { lineItems: {} }], ["no-line-items", {}],
  ["line-item-not-object", { lineItems: [5] }], ["money-not-object", fireworksMoney("1")], ["currency-invalid", fireworksMoney({ currencyCode: "usd", units: "1" })],
  ["currency-missing", fireworksMoney({ units: "1" })], ["units-not-integer", fireworksMoney({ currencyCode: "USD", units: "1.5" })],
  ["units-too-long", fireworksMoney({ currencyCode: "USD", units: "1".repeat(21) })], ["units-out-of-range", fireworksMoney({ currencyCode: "USD", units: "9223372036854775808" })],
  ["nanos-out-of-range", fireworksMoney({ currencyCode: "USD", nanos: 1_000_000_000 })], ["mixed-signs", fireworksMoney({ currencyCode: "USD", units: "1", nanos: -1 })],
  ["negative-mixed-signs", fireworksMoney({ currencyCode: "USD", units: "-1", nanos: 1 })], ["series-invalid", fireworksMoney({ currencyCode: "USD", units: 1 }, 5)],
  ["series-kinds", { lineItems: [
    { series: "DEDICATED_DEPLOYMENT", totalCost: { currencyCode: "USD", units: 2 } }, { series: null, totalCost: { currencyCode: "USD", nanos: -500_000_000 } },
    { series: "FUTURE", totalCost: { currencyCode: "EUR", units: "-3", nanos: -250_000_000 } }, { totalCost: { currencyCode: "EUR" } },
  ] }],
]) {
  fixture(`fireworks:billing:${name}`, "src/providers/fireworks.ts", ["normalizeFireworksBillingSummaryPayload"], 2,
    "Fireworks rated spend sums exact proto3 Money per currency and series, defaults absent components to zero and rejects unsafe accounts, malformed money and invalid series.", { payload, accountId },
    async (library) => {
      try { return { report: library.normalizeFireworksBillingSummaryPayload(payload, accountId ?? "fixture-account", now) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}

const miniMaxPlan = (rows) => ({ base_resp: { status_code: 0 }, model_remains: rows });
const miniMaxChanged = (change) => { const row = miniMaxRow("MiniMax-M*", 70); change(row); return miniMaxPlan([row]); };
for (const [name, kind, payload] of [
  ["rows-not-array", "token-plan", { base_resp: { status_code: 0 }, model_remains: {} }],
  ["no-rows", "token-plan", miniMaxPlan([])],
  ["row-not-object", "token-plan", miniMaxPlan([null])],
  ["not-success", "token-plan", { base_resp: { status_code: 1 }, model_remains: [miniMaxRow("MiniMax-M*", 70)] }],
  ["status-unsupported", "token-plan", miniMaxChanged((row) => { row.current_interval_status = 4; })],
  ["status-unlimited", "token-plan", miniMaxChanged((row) => { row.current_weekly_status = 3; row.weekly_boost_permille = 500; })],
  ["reversed", "token-plan", miniMaxChanged((row) => { row.end_time = row.start_time - 1; })],
  ["zero-total-count", "token-plan", miniMaxChanged((row) => { row.current_interval_total_count = 0; })],
  ["zero-total-no-percent", "token-plan", miniMaxChanged((row) => { row.current_interval_total_count = 0; row.current_interval_usage_count = 0; delete row.current_interval_remaining_percent; })],
  ["zero-total-percent", "token-plan", miniMaxChanged((row) => { row.current_interval_total_count = 0; row.current_interval_usage_count = 0; })],
  ["count-exceeds-total", "token-plan", miniMaxChanged((row) => { row.current_interval_usage_count = 101; })],
  ["percent-null", "token-plan", miniMaxChanged((row) => { row.current_interval_remaining_percent = null; })],
  ["percent-out-of-range", "token-plan", miniMaxChanged((row) => { row.current_interval_remaining_percent = 101; })],
  ["percent-not-number", "token-plan", miniMaxChanged((row) => { row.current_interval_remaining_percent = "70"; })],
  ["percent-as-used", "token-plan", miniMaxChanged((row) => { row.current_interval_remaining_percent = 30; })],
  ["boost-unreasonable", "token-plan", miniMaxChanged((row) => { row.weekly_boost_permille = 20_000; })],
  ["count-negative", "token-plan", miniMaxChanged((row) => { row.current_interval_usage_count = -1; })],
  ["timestamp-zero", "token-plan", miniMaxChanged((row) => { row.start_time = 0; })],
  ["name-not-string", "token-plan", miniMaxPlan([{ ...miniMaxRow("MiniMax-M*", 70), model_name: 5 }])],
  ["name-fallback-and-duplicates", "token-plan", miniMaxPlan([miniMaxRow("\u0007", 70), miniMaxRow("!!!", 60), miniMaxRow("General", 50), miniMaxRow("general", 40)])],
  ["balance-not-string", "account-balance", { base_resp: { status_code: 0 }, available_amount: 5, cash_balance: "-1", voucher_balance: "0", credit_balance: "0", owed_amount: "0" }],
  ["balance-negative", "account-balance", { base_resp: { status_code: 0 }, available_amount: "-1", cash_balance: "0", voucher_balance: "0", credit_balance: "0", owed_amount: "0" }],
  ["balance-too-long", "account-balance", { base_resp: { status_code: 0 }, available_amount: "1".repeat(65), cash_balance: "0", voucher_balance: "0", credit_balance: "0", owed_amount: "0" }],
  ["balance-not-decimal", "account-balance", { base_resp: { status_code: 0 }, available_amount: "1e3", cash_balance: "0", voucher_balance: "0", credit_balance: "0", owed_amount: "0" }],
]) {
  fixture(`minimax:normalize:${name}`, "src/providers/minimax.ts", ["normalizeMiniMaxUsagePayload"], 2,
    "MiniMax Token Plan rows keep rolling and weekly windows with unlimited, percent-only and count quotas, interpret an ambiguous percent by distance, and reject unsuccessful, malformed or inconsistent rows and balance amounts.", { kind, payload },
    async (library) => {
      try { return { report: library.normalizeMiniMaxUsagePayload("minimax", kind, payload, now) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}

const deepSeekRow = (currency, change = {}) => ({ currency, total_balance: "1.00", granted_balance: "0.00", topped_up_balance: "1.00", ...change });
for (const [name, payload] of [
  ["availability-not-boolean", { is_available: "true", balance_infos: [deepSeekRow("USD")] }],
  ["infos-not-array", { is_available: true, balance_infos: {} }],
  ["infos-empty", { is_available: true, balance_infos: [] }],
  ["row-not-object", { is_available: true, balance_infos: [[]] }],
  ["currency-unsupported", { is_available: true, balance_infos: [deepSeekRow("EUR")] }],
  ["currency-repeated", { is_available: true, balance_infos: [deepSeekRow("USD"), deepSeekRow("USD")] }],
  ["amount-not-string", { is_available: true, balance_infos: [deepSeekRow("USD", { granted_balance: 0 })] }],
  ["amount-too-long", { is_available: true, balance_infos: [deepSeekRow("USD", { total_balance: "1".repeat(65) })] }],
  ["amount-not-decimal", { is_available: true, balance_infos: [deepSeekRow("USD", { topped_up_balance: "01" })] }],
  ["unavailable-cny-only", { is_available: false, balance_infos: [deepSeekRow("CNY", { total_balance: "-1.5" })] }],
]) {
  fixture(`deepseek:normalize:${name}`, "src/providers/deepseek.ts", ["normalizeDeepSeekBalancePayload"], 2,
    "DeepSeek balances keep exact decimal strings per supported currency in a fixed order and reject a non-boolean availability, missing rows, unsupported or repeated currencies and invalid amounts.", { payload },
    async (library) => {
      try { return { report: library.normalizeDeepSeekBalancePayload(payload, now) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}
for (const [name, payload] of [
  ["usage-not-object", { usage: [] }],
  ["no-windows", { usage: {} }],
  ["window-shapes", { usage: { rolling: null, weekly: { status: 5, percent: 10 }, monthly: { status: "\u0007" } } }],
  ["percent-and-reset-shapes", { usage: { rolling: { status: "ok", percent: -1 }, weekly: { status: "ok", percent: "10" }, monthly: { status: "rate-limited", percent: 120, resetsAt: "fixture" } } }],
  ["reset-blank", { usage: { rolling: { status: "ok", percent: 10, resetsAt: " " } } }],
]) {
  fixture(`opencode-zen:normalize:${name}`, "src/providers/opencode-zen.ts", ["normalizeOpenCodeZenPayload"], 2,
    "OpenCode Zen keeps ok and rate-limited windows with a valid percent, notes windows with another or unknown status and fails closed when nothing is displayable.", { payload },
    async (library) => {
      try { return { report: library.normalizeOpenCodeZenPayload(payload, now) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}
for (const [name, payload] of [
  ["additional-shapes", { additional_rate_limits: [null, {}, { limit_name: "\u0007" }, { metered_feature: "fixture-feature", rate_limit: "fixture" },
    { limit_name: "Fixture limit", rate_limit: { primary_window: "fixture" } }, { metered_feature: "fixture-other", limit_name: 5, rate_limit: { primary_window: { used_percent: 5 } } }],
    rate_limit: { primary_window: { used_percent: "fixture" }, secondary_window: { used_percent: " " } }, credits: { has_credits: true, balance: "fixture" } }],
  ["rate-limit-not-object", { rate_limit: "fixture" }],
  ["plan-sanitized-empty", { credits: { has_credits: false }, plan_type: "\u0007" }],
]) {
  fixture(`codex:normalize:${name}`, "src/providers/codex.ts", ["normalizeCodexBackendPayload"], 2,
    "Codex usage keeps primary and optional additional rate-limit groups, skips malformed optional groups without hiding primary data, and rejects a malformed primary rate limit.", { payload },
    async (library) => {
      try { return { report: library.normalizeCodexBackendPayload(payload, now) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}
for (const [name, payload] of [
  ["not-object", "fixture"], ["nested-code", { error: { code: 1001 } }], ["nested-without-code", { error: {}, code: 1210 }],
  ["unknown-code", { code: "9999" }], ["code-zero-success-false", { success: false, code: 0 }], ["invalid-code", { code: "12345" }],
]) {
  fixture(`zai:error:${name}`, "src/providers/zai.ts", ["normalizeZaiQuotaPayload"], 2,
    "Z.AI payload errors map documented codes to fixed messages, prefer explicit nested codes, never interpolate provider text and fall back to a generic message.", { payload },
    async (library) => {
      try { return { report: library.normalizeZaiQuotaPayload("zai", "Z.AI", payload, now) }; }
      catch (error) {
        if (!(error instanceof Error)) throw error;
        return { error: { name: error.name, message: error.message } };
      }
    });
}

addSettingsAuthFixtures(fixture);

export const consumerFixtures = fixtures;
export async function captureConsumers(library, implementation, root, observe = (_id, operation) => operation(), host) {
  const results = [];
  for (const item of fixtures) {
    try {
      const value = await observe(item.id, () => item.run(library, join(root, `consumer-${implementation}`, item.id.replaceAll(":", "-")), host));
      results.push({ implementation, id: item.id, outcome: "value", value });
    } catch (error) {
      if (!(error instanceof Error) || error.name === "AssertionError") throw error;
      results.push({ implementation, id: item.id, outcome: "error", error: { name: error.name, message: error.message } });
    }
  }
  return JSON.parse(JSON.stringify(results).replaceAll(join(root, `consumer-${implementation}`), "$CONSUMER_ROOT").replaceAll(root, "$HOST_ROOT"));
}
