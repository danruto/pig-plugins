/** Deterministic source/dependency/executable fingerprints for disposable usage observations. */
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFile, readdir, realpath, lstat, writeFile, mkdtemp, mkdir, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import * as ts from "typescript/unstable/ast";
import { API } from "typescript/unstable/sync";
import { build } from "esbuild";
import { lifecycleEvidence } from "./lifecycle-evidence.mjs";
import { lifecycleFixtures } from "./lifecycle.mjs";
import { cases } from "./cases.mjs";
import { consumerFixtures, protocolFixtures } from "./consumers.mjs";
import { bridgeFixtures } from "./bridge.mjs";
import { settingsAuthEvidence } from "./settings-auth-evidence.mjs";
import { branchLedger } from "./branch-ledger.mjs";
import { readLedger } from "./ledger-artifact.mjs";

const generatedBy = "GENERATED FROM CODE; DO NOT EDIT. Run node probes/usage-baseline.mjs --capture";
const sha = (raw) => createHash("sha256").update(raw).digest("hex");
const json = async (path) => JSON.parse(await readFile(path, "utf8"));
const canonical = (value) => JSON.stringify(value);
const packageFiles = ["package.json", "package-lock.json", "extensions/pig-status-usage.ts", "status/usage.ts", "extensions/pig-openai-server-compaction.ts", "compaction/payload.ts", "scripts/prepare-compaction.mjs"];
export const measuredEntrypoints = { baseline: "probes/usage-baseline.mjs", transport: "probes/usage/transport.mjs", extension: "probes/usage/extension.mjs", bridge: "probes/usage/bridge", tls: "probes/usage/tls" };
const goBuildInput = (name) => name === "go.mod" || name === "go.sum" || (name.endsWith(".go") && !name.endsWith("_test.go") && !/^[._]/.test(name));

async function probeFiles(project) {
  const { metafile } = await build({ absWorkingDir: project, entryPoints: [measuredEntrypoints.baseline, measuredEntrypoints.transport, measuredEntrypoints.extension],
    bundle: true, write: false, metafile: true, platform: "node", format: "esm", packages: "external", outdir: "unused", logLevel: "silent" });
  const modules = Object.keys(metafile.inputs).filter((name) => name.startsWith("probes/"));
  const goModules = await Promise.all([measuredEntrypoints.bridge, measuredEntrypoints.tls].map(async (directory) =>
    (await readdir(join(project, directory))).filter(goBuildInput).map((name) => `${directory}/${name}`)));
  return [...modules, ...goModules.flat()].sort();
}

async function fingerprints(project, names) {
  return Object.fromEntries(await Promise.all(names.map(async (name) => [name, sha(await readFile(join(project, name)))])));
}

async function tree(root) {
  const files = {};
  async function walk(directory, prefix) {
    for (const name of (await readdir(directory)).sort()) {
      const path = join(directory, name);
      const key = prefix + name;
      const info = await lstat(path);
      if (info.isSymbolicLink()) throw new Error(`Linked executable closure member: ${key}`);
      if (info.isDirectory()) await walk(path, key + "/");
      else if (info.isFile()) files[key] = { sha256: sha(await readFile(path)), executableBits: info.mode & 0o111 };
      else throw new Error(`Non-regular executable closure member: ${key}`);
    }
  }
  await walk(root, "");
  assert.ok(Object.keys(files).length > 0);
  return { fileCount: Object.keys(files).length, sha256: sha(canonical(files)), files };
}

async function executable(name, args, root) {
  const path = await realpath(execFileSync("which", [name], { encoding: "utf8" }).trim());
  const directory = await mkdtemp(join(root, "executable-"));
  const env = { PATH: process.env.PATH, HOME: join(directory, "home"), TMPDIR: directory,
    PIG_HOME: join(directory, "pig"), PIG_CODING_AGENT_DIR: join(directory, "pig/agent"),
    PI_CODING_AGENT_DIR: join(directory, "pi-agent"), XDG_CONFIG_HOME: join(directory, "config"),
    PIG_OFFLINE: "1", PI_OFFLINE: "1", PI_SKIP_VERSION_CHECK: "1", GOTOOLCHAIN: "local",
    TZ: "UTC", LANG: "en_US.UTF-8", LC_ALL: "en_US.UTF-8" };
  try {
    for (const key of ["HOME", "PIG_HOME", "PIG_CODING_AGENT_DIR", "PI_CODING_AGENT_DIR", "XDG_CONFIG_HOME"]) await mkdir(env[key], { recursive: true });
    if (name === "go") execFileSync(path, ["telemetry", "off"], { encoding: "utf8", cwd: directory, env });
    return { path, sha256: sha(await readFile(path)), version: execFileSync(path, args, { encoding: "utf8", cwd: directory, env }).trim() };
  } finally { await rm(directory, { recursive: true, force: true }); }
}

function owningCheckpoint(name, symbol) {
  if (name === "src/query.ts" && ["query", "queryProviderUsage", "fetchProviderJson", "readBoundedResponse", "queryMiniMaxUsage", "queryMoonshotBalance", "queryZaiUsage", "fetchZaiPlan", "fetchCommandCodeOptional"].includes(symbol)) return 2;
  if (name === "src/settings.ts" || name === "src/usage-settings-ui.ts" || name.startsWith("src/codex-")) return 4;
  if (name === "src/format.ts" || name === "src/statusline-display.ts" || name === "src/usage-helpers.ts") return 5;
  if (name.startsWith("src/providers/")) return 2;
  return 3;
}

const providerSymbols = {
  baseten: ["baseten", "normalizeBasetenBillingUsagePayload"],
  openai: ["openai-chatgpt", "query"],
  "openai-codex": ["codex", "normalizeCodexBackendPayload"],
  deepseek: ["deepseek", "normalizeDeepSeekBalancePayload"],
  "github-copilot": ["github-copilot", "normalizeGitHubCopilotUsagePayload"],
  openrouter: ["openrouter", "normalizeOpenRouterKeyPayload"],
  "vercel-ai-gateway": ["vercel-ai-gateway", "normalizeVercelAIGatewayCreditsPayload"],
  "command-code": ["command-code", "normalizeCommandCodeUsagePayload"],
  fireworks: ["fireworks", "normalizeFireworksBillingSummaryPayload"],
  "opencode-go": ["opencode-zen", "normalizeOpenCodeZenPayload"],
  "kimi-coding": ["kimi-coding", "normalizeKimiCodingUsagePayload"],
  minimax: ["minimax", "normalizeMiniMaxUsagePayload"],
  "minimax-cn": ["minimax", "normalizeMiniMaxUsagePayload"],
  moonshotai: ["moonshot", "normalizeMoonshotBalancePayload"],
  "moonshotai-cn": ["moonshot", "normalizeMoonshotBalancePayload"],
  zai: ["zai", "normalizeZaiQuotaPayload"],
  "zai-coding-cn": ["zai", "normalizeZaiQuotaPayload"],
  xai: ["xai", "normalizeXaiBillingPayload"],
};
const providerBehaviors = {
  baseten: "Organization Model APIs total, credits and subtotal remain USD spend, not quota.",
  openai: "Native authentication-only reports make no request; companion usage is opt-in and uses separate credentials.",
  "openai-codex": "Primary/secondary quota windows, reset seconds, credits and additional metered model groups stay distinct.",
  deepseek: "Multiple currency balances preserve fractional precision and grant/top-up components.",
  "github-copilot": "Token-based entitlement/remaining becomes account allowance with the provider reset date.",
  openrouter: "Key cap/remaining, daily/weekly/monthly spend and free-tier policy remain distinct.",
  "vercel-ai-gateway": "Exact-string credits and lifetime spend normalize separately.",
  "command-code": "Mandatory organization identity precedes optional credits, subscription and period spend; request query boundaries are retained.",
  fireworks: "Explicit account discovery and billing summary retain money nanos, currencies and series without aggregating accounts.",
  "opencode-go": "Rolling, weekly and monthly statuses retain known/unknown/rate-limited quota semantics.",
  "kimi-coding": "Count-based quota precedence, ratio fallback, fixed-point wallet and cent-denominated spend remain distinct.",
  minimax: "Coding-plan interval and weekly remaining-count/percentage interpretation uses the global endpoint.",
  "minimax-cn": "Coding-plan interval and weekly remaining-count/percentage interpretation uses the China endpoint.",
  moonshotai: "Global account cash/voucher/available balances retain currency and negative cash.",
  "moonshotai-cn": "China account cash/voucher/available balances use the regional endpoint.",
  zai: "Global credit/token/tool quota and optional subscription identity retain provider units and reset timestamps.",
  "zai-coding-cn": "China credit/token/tool quota and optional subscription identity use regional endpoints.",
  xai: "Canonical user lookup precedes billing with x-userid; menu-only quota and cent-denominated values retain period semantics.",
};
const variants = {
  malformed: "An object without required sections exercises the provider's malformed-response result.",
  "http-error": "HTTP 503 exercises bounded error-body handling and synthetic credential redaction.",
  redirect: "A same-origin 302 records reject-before-follow versus follow-then-reject, including credential-bearing second requests.",
  "guard-reject": "The first auth-boundary guard rejects; record which adapters actually invoke that guard and whether requests still occur.",
  companion: "A separately authorized companion app provides quota; native OpenAI credentials are not forwarded.",
  "companion-pagination": "A paginated app response without a matching complete app fails closed.",
  "missing-companion": "Explicit companion opt-in without companion auth retains authentication-only status and companion-login guidance, not numerical quota.",
  empty: "A valid empty billing payload records unknown/absent values rather than assumed zero.",
  "optional-malformed": "An invalid additional model window does not erase mandatory Codex quota.",
  "legacy-overage": "Legacy allowance with negative remaining and overage uses its frozen quota interpretation.",
  free: "Legacy free monthly chat allowance uses its separate payload shape.",
  unlimited: "Provider-specific unlimited flags do not invent finite remaining quota.",
  uncapped: "Null key cap and free-tier flag do not invent a finite spend limit.",
  balance: "sk-api credential shape selects account balance rather than coding-plan remains.",
  "legacy-count": "Interval count 23 and remaining percentage 77 exercise the frozen legacy count interpretation alongside unchanged weekly values.",
  contradictory: "Conflicting count and remaining-percentage fields retain the frozen precedence/failure behavior.",
  "optional-failure": "Optional subscription/summary endpoint failures retain mandatory collected data.",
  "business-error": "A successful HTTP response carrying a provider business error preserves the frozen failure mapping.",
  "guard-changed": "An auth guard fails after initial requests; subsequent requests and publication are captured.",
  "multiple-targets": "Multiple discovered accounts require explicit selection before billing.",
  "remembered-target": "A remembered offered account selects only that account's summary.",
  "missing-target": "A remembered account absent from discovery cannot silently fall back to another account.",
  pagination: "Discovery follows the next page to the explicitly selected account.",
  "ratio-fallback": "Missing count sections allow fractional ratio quota fallback.",
  "malformed-count-precedence": "Malformed count sections prevent a ratio field from silently replacing count-based quota.",
  legacy: "Legacy monthly cap and used-cent fields normalize without modern period configuration.",
  null: "Absent subscription configuration remains unknown rather than zero quota.",
  oversize: "A success body over 64 KiB fails bounded response reading.",
  "invalid-json": "Invalid JSON remains a parse failure, not an empty report.",
  abort: "A pre-aborted request preserves cancellation and makes no provider request.",
};

async function inventory(project, source, root) {
  const files = Object.keys(source.files).filter((name) => name.startsWith("src/") && name.endsWith(".ts"));
  const config = join(root, "inventory-tsconfig.json");
  await writeFile(config, JSON.stringify({ compilerOptions: { noResolve: true }, files: files.map((name) => join(project, "node_modules/@narumitw/pi-usage", name)) }));
  const api = new API({ cwd: root });
  let snapshot;
  const branches = [];
  const symbols = [];
  try {
  snapshot = api.updateSnapshot({ openProjects: [config] });
  const program = snapshot.getProject(config).program;
  for (const name of files) {
    const path = join(project, "node_modules/@narumitw/pi-usage", name);
    const parsed = program.getSourceFile(path);
    assert.ok(parsed, `Missing source inventory AST: ${name}`);
    function visit(node, parentSymbol) {
      const symbol = (ts.isFunctionDeclaration(node) || ts.isMethodDeclaration(node) || ts.isClassDeclaration(node)) && node.name ? node.name.getText(parsed) : parentSymbol;
      const location = parsed.getLineAndCharacterOfPosition(node.getStart(parsed));
      const evidence = { source: name, line: location.line + 1, symbol: symbol ?? "<module>", sourceSha256: source.files[name].sha256, checkpoint: owningCheckpoint(name, symbol) };
      if (((ts.isFunctionDeclaration(node) || ts.isMethodDeclaration(node)) && node.body) || ts.isClassDeclaration(node)) {
        symbols.push({ ...evidence, symbolSha256: sha(node.getText(parsed)), fixtureMapping: "not established by syntactic inventory" });
      }
      if (ts.isIfStatement(node) || ts.isConditionalExpression(node) || ts.isCaseClause(node) || ts.isCatchClause(node)) {
        const input = ts.isIfStatement(node) || ts.isConditionalExpression(node) ? node.expression ?? node.condition : ts.isCaseClause(node) ? node.expression : undefined;
        branches.push({ ...evidence, kind: ts.SyntaxKind[node.kind], branchSha256: sha(node.getText(parsed)),
          input: input?.getText(parsed) ?? "exception",
          expected: "Source branch only; no executable outcome inferred", evidence: [], coverage: "unmapped" });
      }
      node.forEachChild((child) => visit(child, symbol));
    }
    visit(parsed);
  }
  } finally { snapshot?.dispose(); api.close(); }
  const expected = await json(join(project, "docs/compatibility/usage/expected.json"));
  function reference(file, symbol) {
    const found = symbols.filter((item) => item.source === file && item.symbol === symbol);
    assert.equal(found.length, 1, `Ambiguous or absent semantic source symbol: ${file}:${symbol}`);
    const { fixtureMapping: _mapping, ...result } = found[0];
    return result;
  }
  const semanticFixtures = cases.map((item) => {
    const observation = expected.results.find((result) => result.implementation === "source" && result.id === item.id);
    assert.ok(observation, `Missing provider evidence: ${item.id}`);
    const variant = item.id.slice(item.provider.length + 1);
    const [file, symbol] = providerSymbols[item.provider];
    const sourceSymbols = [reference("src/query.ts", "queryProviderUsage")];
    if (observation.outcome === "report") sourceSymbols.push(reference(`src/providers/${file}.ts`, symbol));
    if (observation.trace.some((event) => event.kind === "request")) sourceSymbols.push(reference("src/query.ts", "fetchProviderJson"));
    assert.ok(variant === "normal" || variants[variant], `Missing semantic case description: ${item.id}`);
    return { id: item.id, capturedAtCheckpoint: 1, owningCheckpoint: 2, coverage: "observed scenario; not complete symbol/branch coverage",
      behavior: `${providerBehaviors[item.provider]} ${variant === "normal" ? "Baseline complete synthetic payload." : variants[variant]}`,
      sourceSymbols, inputs: item, expected: observation,
      expectedSideEffects: expected.requests.filter((request) => request.id === `source/${item.id}`),
      evidence: [`expected.json#/results/${expected.results.indexOf(observation)}`, `expected.json#/results/${expected.results.findIndex((result) => result.implementation === "dist" && result.id === item.id)}`] };
  });
  for (const item of consumerFixtures) {
    const observation = expected.consumers.find((result) => result.implementation === "source" && result.id === item.id);
    assert.ok(observation, `Missing consumer evidence: ${item.id}`);
    semanticFixtures.push({ id: item.id, capturedAtCheckpoint: 1, owningCheckpoint: item.checkpoint,
      coverage: "observed scenario; not complete symbol/branch coverage", behavior: item.behavior,
      sourceSymbols: item.symbols.map((symbol) => reference(item.source, symbol)), inputs: item.input,
      expected: observation, expectedSideEffects: observation.value?.trace ?? [],
      evidence: [`expected.json#/consumers/${expected.consumers.indexOf(observation)}`, `expected.json#/consumers/${expected.consumers.findIndex((result) => result.implementation === "dist" && result.id === item.id)}`] });
  }
  for (const item of cases) {
    const observation = expected.results.find((result) => result.implementation === "source" && result.id === item.id);
    if (observation.outcome !== "report") continue;
    semanticFixtures.push({ id: `presentation:${item.id}`, capturedAtCheckpoint: 1, owningCheckpoint: 5,
      coverage: "observed scenario; not complete symbol/branch coverage",
      behavior: "Captured normalized report feeds current/configured terminal formatting and selected-model statusline with remaining/used, countdown off and later reset time. This does not exercise the Go footer render loop.",
      sourceSymbols: [reference("src/format.ts", "formatUsageReport"), reference("src/format.ts", "formatUsageStatusline")],
      inputs: { report: observation.report, model: { provider: item.provider, id: "synthetic", baseUrl: item.baseUrl }, currentTime: observation.report.capturedAt,
        modes: ["current", "configured", "remaining", "used", "countdown-off", "time-plus-604800000ms"] },
      expected: observation.presentation, expectedSideEffects: [],
      evidence: [`expected.json#/results/${expected.results.indexOf(observation)}/presentation`, `expected.json#/results/${expected.results.findIndex((result) => result.implementation === "dist" && result.id === item.id)}/presentation`] });
  }
  for (const item of protocolFixtures) {
    const observation = expected.protocol.find((result) => result.id === item.id);
    assert.ok(observation, `Missing real-PiG protocol capture: ${item.id}`);
    semanticFixtures.push({ id: item.id, capturedAtCheckpoint: 1, owningCheckpoint: 3,
      coverage: "real Node composition session reference; public API seam characterization retained as ADR 0004 decision evidence, not an in-scope obligation",
      behavior: "The selected production composition receives a real event-bus request: capabilities provider matrix, cache-only query, explicit refresh, cancellation, rejected/throwing callback offers or invalid request shape. Independent session-reference negative controls receive no offer.",
      sourceSymbols: [reference("src/usage-api.ts", "registerUsageApi")], inputs: item,
      expected: observation, expectedSideEffects: expected.requests.filter((request) => request.id === `protocol/${item.id}`),
      evidence: [`expected.json#/protocol/${expected.protocol.indexOf(observation)}`] });
  }
  for (const item of bridgeFixtures) {
    const index = expected.protocol.findIndex((row) => row.id === item.id);
    assert.ok(index >= 0, `Missing Go bridge capture: ${item.id}`);
    semanticFixtures.push({ id: item.id, capturedAtCheckpoint: 1, owningCheckpoint: 3,
      coverage: "real PiG Go/Node event-bus boundary with a probe-only Go peer; ADR 0004 decision evidence, not a checkpoint gate",
      behavior: item.direction === "node-to-go"
        ? "A Node sender emits the public usage event to a Go listener. Session-bearing events fail host JSON relay before the Go realm; without a session, the callback is dropped and AbortSignal arrives as an empty object."
        : "A Go sender emits the closest JSON request it can express. The Node production composition sees no callback, no AbortSignal and no composition session reference, so it makes no offer.",
      sourceSymbols: [reference("src/usage-api.ts", "registerUsageApi")], inputs: item,
      expected: expected.protocol[index], expectedSideEffects: expected.requests.filter((request) => request.id === `protocol/${item.id}`),
      evidence: [`expected.json#/protocol/${index}`] });
  }
  for (const item of lifecycleFixtures) {
    const index = expected.lifecycle.findIndex((row) => row.id === item.id);
    assert.ok(index >= 0, `Missing registered lifecycle capture: ${item.id}`);
    const codex = /^lifecycle:(codex|reset|fast):/u.test(item.id);
    semanticFixtures.push({ id: item.id, capturedAtCheckpoint: 1, owningCheckpoint: codex ? 4 : 3,
      coverage: "actual registered production-dist Node handlers in disposable PiG, replayed against a source composition (the production usage member with only its backend import switched to the shipped source) whose requests, UI and receipts must match, and in an exact-counter replay against a probe-only instrumented copy of the backend source whose requests, UI, receipts and results must equal the uninstrumented run; not Go bridge qualification",
      behavior: codex
        ? "Real RPC selection of a synthetic offline-OAuth official OpenAI Codex model, then /usage reset discovery, confirmation, redemption, retry and outcome screens or /usage and /fast Fast toggles over planned loopback chatgpt.com responses; no model prompt, payload hook or live redemption."
        : "Explicit RPC /usage menu actions, model selection or structured API event invocation; synthetic local request/failure boundaries, failure backoff, target persistence and held-response model/cancellation interleavings.",
      sourceSymbols: codex
        ? [reference("src/usage.ts", "usageExtension"), reference("src/codex-resets.ts", "resolveCodexResetAuth"), reference("src/codex-resets.ts", "consumeCodexResetCredit"), reference("src/codex-fast-runtime.ts", "registerCodexFastMode"), reference("src/codex-fast-catalog.ts", "createCodexFastCatalog")]
        : [reference("src/usage.ts", "usageExtension"), reference("src/usage-api.ts", "registerUsageApi")], inputs: item,
      expected: expected.lifecycle[index], expectedSideEffects: expected.requests.filter((request) => request.id === item.id),
      evidence: [`expected.json#/lifecycle/${index}`] });
  }
  for (const [index, fixture] of semanticFixtures.entries()) fixture.sourceBranchEvidence = `branch-ledger.json#/fixtureMappings/${index}`;
  assert.equal(new Set(semanticFixtures.map((item) => item.id)).size, semanticFixtures.length);
  return { generatedBy, qualification: "BLOCKED: semantic scenario ledger is partial; syntactic nodes remain unmapped",
    capturedAtCheckpoint: 1, checkpointMeaning: "owningCheckpoint names the native implementation owner, not permission to defer its baseline capture",
    branchLedger: "branch-ledger.json: exact source-arm witnesses and grouped pending outcome obligations; join fixtureMappings by semantic fixture id",
    semanticFixtures,
    extractedBranchKinds: ["IfStatement", "ConditionalExpression", "CaseClause", "CatchClause"],
    limitations: ["Semantic scenario ledger does not establish complete branch-to-fixture mapping", "Loop, short-circuit and optional-chain paths are not enumerated", "Anonymous function ownership is inherited from its enclosing named symbol"],
    symbols, branches,
    providerFixtures: [...new Set(cases.map((item) => item.provider))].map((providerId) => ({ providerId, capturedAtCheckpoint: 1, owningCheckpoint: 2,
      input: "probes/usage/cases.mjs", expected: "docs/compatibility/usage/expected.json",
      evidence: cases.filter((item) => item.provider === providerId).map((item) => item.id),
      scope: "adapter queries and source/dist comparison only; host auth and complete branch mapping not proven" })),
    consumers: [
      { files: ["src/query.ts", "src/oauth-credential-source.ts", "src/auth-resolution-guard.ts", "src/openai-companion-auth.ts"], owningCheckpoint: 3, scope: "host effective auth, readiness, origin and credential identity", evidence: ["real-PiG registry auth:host:* matrix: stored and host-registered model auth for all 18 providers, readiness rotation/rejection/abort/stale guards, selected/provider origins and explicit Authorization overrides, actual OAuth event offers and stored fallback, native OpenAI/Copilot/xAI identity restrictions, opt-in companion account checks and regional Moonshot isolation; synthetic registry fixtures retained as separate evidence"], coverage: "real host resolution at the standalone public-function boundary; production selected-model lifecycle and request/publication revalidation remain pending" },
      { files: ["src/settings.ts", "src/usage-settings-ui.ts"], owningCheckpoint: 4, scope: "settings validation/defaults/migration/private ordered saves/rollback", evidence: ["settings:normalize:*", "settings:file:*", "settings:extra-normalize:*", "settings:transaction:*", "settings:host-default-path", "settings-auth.json"], coverage: "standalone defaults/validation/migration/private ordered atomic save/conditional rollback verified; registered settings UI and lifecycle interleavings remain pending" },
      { files: ["src/usage.ts", "src/usage-targets.ts", "src/core.ts"], owningCheckpoints: [3, 4], scope: "commands, explicit other/all/target actions, cache/backoff/concurrency and lifecycle", evidence: ["real package registration; lifecycle.json: actual registered RPC current/other/all/target actions, production Node query/capabilities/invalidation, held-response cancellation/model-selection and successor-publication receipts; EOF shutdown", "targets:*", "cache:*", "concurrency:*", "deadline:*"], coverage: "bounded registered command/API slice added; settings UI, timers, complete cache/backoff/reload/replacement/shutdown paths and exact pending outcomes in lifecycle.json remain" },
      { files: ["src/codex-fast.ts", "src/codex-fast-catalog.ts", "src/codex-fast-runtime.ts"], owningCheckpoint: 4, scope: "Fast discovery/fallback/request tier/cost correction/persistence", evidence: ["fast:* public-function fixtures; complete-package unsupported/invalid-argument RPC commands", "lifecycle:codex:select and lifecycle:fast:* registered model-selection discovery, /fast and /usage toggles, unknown/unsupported/failed and 15 model-directory shape captures over planned loopback responses", "lifecycle:fast:prompt-* synthetic model prompts through the PiG host's own Codex request path, answered by the loopback proxy endpoint: enabled, already-priced, unknown-capability and disabled request tiers and assistant cost correction", "lifecycle:fast:unknown-account and lifecycle:fast:unknown-status: with Fast enabled, an account switch warms the usage cache, then the failing model-directory response is held until the (Fast unknown) status publishes, so the catalogue's status refreshes run in a fixed order"], coverage: "partial; registered catalogue discovery, toggles, the Fast-unknown status decoration, request-tier fallback and message-cost hooks captured; exact pending outcomes in lifecycle.json remain" },
      { files: ["src/codex-resets.ts"], owningCheckpoint: 4, scope: "fresh identity/confirmation/stable redemption/retry/outcome", evidence: ["resets:* availability normalization", "lifecycle:reset:* registered /usage discovery, confirmation, loopback redemption, same-identity retry, cancel, fallback, zero/disabled, invalid outcomes and stored-account mismatch"], coverage: "partial; registered confirmation/retry/outcome plus readiness and conflicting-candidate failures armed after the menu loads captured; candidate discovery and readiness-emit failures are named gaps (docs/compatibility/usage.md#named-gaps): lifecycle:reset:candidates-source-throws and lifecycle:reset:candidates-readiness-throws show PiG logs a throwing credential-source or credential-readiness listener's error and discovery proceeds, so those fail-closed branches stay pending for the native Go seam; mid-operation model/account changes unexercised" },
      { files: ["src/usage-api.ts", "src/usage-api-types.ts"], owningCheckpoint: null, scope: "Out of scope (docs/adr/0004-no-public-usage-event-api.md): the public usage:*:v1 event API is not ported; the native collector feeds the footer in process", evidence: ["Node consumer using observed composition session handle; own/foreign session rejection; source reset-unit conversion", "protocol:go-bridge:* real PiG Go/Node event-bus captures"], coverage: "Decision evidence only: the Go/Node event bus does not carry the callback, AbortSignal or session reference. branch-ledger.json marks API-module outcomes, usage:*:v1 listener outcomes and optional calls through the registerUsageApi handle out-of-scope with their reason and retained evidence; they are excluded from the pending count" },
      { files: ["src/format.ts", "src/statusline-display.ts", "src/usage-helpers.ts", "src/index.ts", "src/types.ts"], owningCheckpoint: 5, scope: "report/status display and public helpers/types", evidence: ["presentation:* on all successful provider scenarios; display:* control/redaction captures; settings template validation", "format:* normalized Codex/MiniMax statusline group selection, countdown units, credit fallbacks, custom/invalid display templates and provider state lists"], coverage: "partial" },
      { files: ["extensions/pig-status-usage.ts", "status/usage.ts", "extensions/pig-status"], owningCheckpoint: 5, scope: "selected composition, cache-only footer and normalized summary consumer", evidence: ["real selected package registration and startup/shutdown; existing package tests"], coverage: "partial" },
    ],
  };
}

export async function measurementIdentity(project, root) {
  const lock = await json(join(project, "package-lock.json"));
  const production = Object.entries(lock.packages).filter(([path, pinned]) => path && pinned.dev !== true);
  return sha(canonical({
    files: await fingerprints(project, [...await probeFiles(project), ...packageFiles]),
    production: Object.fromEntries(await Promise.all(production.map(async ([name]) => [name, await tree(join(project, name))]))),
    footer: await tree(join(project, "extensions/pig-status")),
    compaction: await tree(join(project, "compaction/generated")),
    executables: Object.fromEntries(await Promise.all([["pig", ["--version"]], ["node", ["--version"]], ["go", ["version"]]].map(async ([name, args]) => [name, await executable(name, args, root)]))),
  }));
}

export async function provenance(project, root, capture, replay) {
  const retained = join(project, "docs/compatibility/usage");
  const lock = await json(join(project, "package-lock.json"));
  const dependencies = {};
  for (const [path, pinned] of Object.entries(lock.packages).sort(([a], [b]) => a.localeCompare(b))) {
    if (!path || !await lstat(join(project, path)).catch((error) => { if (error.code === "ENOENT" && pinned.optional) return undefined; throw error; })) continue;
    const metadata = await json(join(project, path, "package.json"));
    assert.equal(metadata.version, pinned.version);
    const edges = {};
    for (const [name, requested] of Object.entries({ ...metadata.dependencies, ...metadata.optionalDependencies, ...metadata.peerDependencies }).sort(([a], [b]) => a.localeCompare(b))) {
      let parent = resolve(project, path);
      let target;
      while (parent.startsWith(project)) {
        const candidate = join(parent, "node_modules", name);
        if (await lstat(join(candidate, "package.json")).catch((error) => { if (error.code === "ENOENT") return undefined; throw error; })) { target = candidate.slice(project.length + 1); break; }
        parent = resolve(parent, "..");
      }
      if (target) edges[name] = { requested, target };
      else if (name.startsWith("@earendil-works/pi-")) edges[name] = { requested, suppliedBy: "fingerprinted PiG Node runtime" };
      else if (metadata.optionalDependencies?.[name] || metadata.peerDependenciesMeta?.[name]?.optional) edges[name] = { requested, absentOptional: true };
      else throw new Error(`Unresolved executable dependency ${path}: ${name}`);
    }
    dependencies[path] = { name: metadata.name, version: metadata.version, production: pinned.dev !== true,
      resolved: pinned.resolved, integrity: pinned.integrity, edges, installed: await tree(join(project, path)) };
  }
  const source = dependencies["node_modules/@narumitw/pi-usage"].installed;
  const runtimeNames = (await readdir(join(root, "pig/cache/ext"))).filter((name) => name.startsWith("node-runtime-"));
  assert.equal(runtimeNames.length, 1);
  const sdkNames = (await readdir(join(root, "pig/state/pigsdk"))).filter((name) => /^sdk-[a-f0-9]+$/.test(name));
  assert.equal(sdkNames.length, 1);
  const cellRoot = join(root, "pig/cache/cells/node");
  const cellNames = (await readdir(cellRoot)).filter((name) => /^[a-f0-9]{64}$/.test(name));
  assert.equal(cellNames.length, 1);
  const cellManifest = JSON.parse((await readFile(join(cellRoot, cellNames[0], "manifest.json"), "utf8")).replaceAll(project, "$PACKAGE"));
  const packageMetadata = await json(join(project, "package.json"));
  const result = { generatedBy, formatVersion: 1,
    lockSha256: sha(await readFile(join(project, "package-lock.json"))),
    selectedPackageSha256: sha(await readFile(join(project, "package.json"))), selectedMembers: packageMetadata.pi.extensions,
    observation: { providerIO: "synthetic credentials, fixed clock, loopback HTTP substitution at fetch boundary", productionActivation: false, locale: { TZ: "UTC", LANG: "en_US.UTF-8", LC_ALL: "en_US.UTF-8" },
      fixtures: await fingerprints(project, await probeFiles(project)) },
    executables: Object.fromEntries(await Promise.all([["pig", ["--version"]], ["node", ["--version"]], ["go", ["version"]], ["tar", ["--version"]]].map(async ([name, args]) => [name, await executable(name, args, root)]))),
    dependencies, hostNodeRuntime: { identity: runtimeNames[0], ...await tree(join(root, "pig/cache/ext", runtimeNames[0], "runtime")) },
    executingNodeCell: { manifest: cellManifest, runtime: await tree(join(cellRoot, cellNames[0], "runtime")) },
    sdk: { identity: sdkNames[0], ...await tree(join(root, "pig/state/pigsdk", sdkNames[0])) },
    nativeFooterInputs: await tree(join(project, "extensions/pig-status")),
    compactionGeneratedInputs: await tree(join(project, "compaction/generated")),
    observationSha256: sha(await readFile(join(retained, "expected.json"))),
    measurementsSha256: sha(await readFile(join(retained, "measurements.json"))),
    packageInputs: Object.fromEntries(await Promise.all(["extensions/pig-status-usage.ts", "status/usage.ts", "extensions/pig-openai-server-compaction.ts", "compaction/payload.ts", "scripts/prepare-compaction.mjs"].map(async (name) => [name, sha(await readFile(join(project, name)))]))),
  };
  const inventoryResult = await inventory(project, source, root);
  await branchLedger(project, root, inventoryResult, result, capture, replay);
  await settingsAuthEvidence(project, inventoryResult, capture);
  await lifecycleEvidence(project, capture);
  result.lifecycleSha256 = sha(await readFile(join(retained, "lifecycle.json")));
  result.settingsAuthSha256 = sha(await readFile(join(retained, "settings-auth.json")));
  result.branchObservationsSha256 = sha(await readFile(join(retained, "branch-observations.json")));
  result.branchLedgerSha256 = sha(await readLedger(join(retained, "branch-ledger.json.gz")));
  const archive = join(retained, "production.tar.gz");
  if (capture) {
    const paths = Object.entries(dependencies).filter(([, value]) => value.production).map(([name]) => name);
    execFileSync("tar", ["--sort=name", "--mtime=@0", "--owner=0", "--group=0", "--numeric-owner", "-czf", archive, ...paths], { cwd: project });
  }
  result.productionArchiveSha256 = sha(await readFile(archive));
  for (const [name, value] of [["provenance.json", result], ["inventory.json", inventoryResult]]) {
    const path = join(retained, name);
    if (capture) await writeFile(path, JSON.stringify(value, null, 2) + "\n");
    else assert.deepEqual(value, await json(path), `${name} drift`);
  }
  console.log(`usage-provider provenance: ${Object.values(dependencies).filter((item) => item.production).length} production packages, ${Object.keys(dependencies).length} installed packages, ${inventoryResult.semanticFixtures.length} semantic scenario rows (partial), ${inventoryResult.branches.length} syntactic branches (not coverage)`);
}
