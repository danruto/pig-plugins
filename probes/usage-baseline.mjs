/** Capture/check the locked usage backend in disposable PiG roots with loopback-only provider IO. */
import { createHash } from "node:crypto";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { createServer as createTlsServer } from "node:https";
import { zstdDecompressSync } from "node:zlib";
import { once } from "node:events";
import { loadavg } from "node:os";
import { spawn, execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { appendFile, mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { performance } from "node:perf_hooks";
import { setTimeout as delay } from "node:timers/promises";
import { branchEvidenceContract } from "./usage/branch-ledger.mjs";
import { verifyLifecycle } from "./usage/lifecycle-evidence.mjs";
import { assertPlannedResponsesUsed, captureLifecycle, codexBearers, lifecycleFixtures, plannedLifecycleResponse, portableModelRequestBody, portableRedemptionBody, serverSentEvents } from "./usage/lifecycle.mjs";
import { createRequestChoreography, createResponseBarrier } from "./usage/response-barrier.mjs";
import { cases, now } from "./usage/cases.mjs";
import { provenance, measurementIdentity, measuredEntrypoints } from "./usage/provenance.mjs";
import { verifySettingsAuth } from "./usage/settings-auth-evidence.mjs";
import { consumerFixtures } from "./usage/consumers.mjs";
import { verifyBridge } from "./usage/bridge.mjs";
import { counterComposition, instrumentSources, pinnedBackendSources, writeCounterCopy } from "./usage/counter-replay.mjs";

const project = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const retained = join(project, "docs/compatibility/usage");
const scratch = join(project, ".pb/probes/7");
const mode = process.argv[2];
const sourceReplay = process.argv[3] === "--source-composition";
const counterReplay = process.argv[3] === "--counter-composition";
if (!["--check", "--capture", "--measure", "--observe", "--lifecycle-observe"].includes(mode) || process.argv.length > (sourceReplay || counterReplay ? 4 : 3)
  || (sourceReplay && mode !== "--lifecycle-observe") || (counterReplay && (mode !== "--observe" || !process.env.USAGE_FIXTURE_COUNTER_COPY))) {
  throw new Error("Use --check, --capture, --measure, --observe, --lifecycle-observe, --lifecycle-observe --source-composition or (internally) --observe --counter-composition");
}
const disposableOutput = mode === "--lifecycle-observe" || counterReplay;
await mkdir(scratch, { recursive: true });
const root = await mkdtemp(join(scratch, "usage-"));
const tls = join(root, "tls");
await mkdir(tls);
execFileSync("go", ["run", ".", tls], { cwd: join(project, measuredEntrypoints.tls), env: { ...process.env, GOCACHE: join(scratch, "go-cache"), GOFLAGS: "-p=2", GOTOOLCHAIN: "local" }, stdio: ["ignore", "ignore", "inherit"] });
const requests = [];
const routingErrors = [];
const byId = new Map(cases.map((item) => [item.id, item]));
const choreographies = new Map(lifecycleFixtures.filter((fixture) => fixture.responseChoreography).map((fixture) => [fixture.id, createRequestChoreography(fixture.responseChoreography)]));
const statusGates = new Map(lifecycleFixtures.flatMap((fixture) => Object.entries(fixture.statusGates ?? {}).map(([route, status]) =>
  [`${fixture.id} ${route}`, { fixtureId: fixture.id, status: new RegExp(status, "u"), barrier: createResponseBarrier() }])));
const activeRoutes = new Set();
const plannedOrdinals = new Map();
const server = createServer((request, response) => {
  const task = route(request, response);
  activeRoutes.add(task);
  task.then(() => activeRoutes.delete(task), (error) => {
    routingErrors.push(error.message);
    activeRoutes.delete(task);
    response.destroy(error);
  });
});
async function heldResponse(response, holder, hold) {
  const controller = new AbortController();
  const abort = () => { if (!response.writableEnded) controller.abort(new Error(`Fixture client disconnected during ${holder}`)); };
  response.once("close", abort);
  try { await hold(controller.signal); } finally { response.removeListener("close", abort); }
}
async function route(request, response) {
  try {
    const id = request.headers["x-usage-fixture-case"];
    const original = request.headers["x-usage-fixture-url"];
    if (typeof id !== "string" || typeof original !== "string") throw new Error("Missing fixture request identity");
    const url = new URL(original);
    const lifecycleProvider = id.startsWith("lifecycle:") ? url.hostname === "api.deepseek.com" ? "deepseek" : url.hostname === "openrouter.ai" ? "openrouter" : url.hostname === "api.fireworks.ai" ? "fireworks" : url.hostname === "api.commandcode.ai" ? "command-code" : undefined : undefined;
    const lifecycleVariant = lifecycleProvider === "fireworks" ? "multiple-targets" : lifecycleProvider === "command-code" ? "optional-failure" : "normal";
    const route = url.pathname.split("/").at(-1);
    const plannedKey = `${id} ${route}`;
    const plannedOrdinal = plannedOrdinals.get(plannedKey) ?? 0;
    const planned = plannedLifecycleResponse(id, route, plannedOrdinal);
    if (planned) plannedOrdinals.set(plannedKey, plannedOrdinal + 1);
    const item = planned ? undefined : lifecycleProvider ? byId.get(`${lifecycleProvider}:${lifecycleVariant}`) : id === "host" || id.startsWith("protocol/") ? byId.get("deepseek:normal") : byId.get(id.substring(id.indexOf("/") + 1));
    if (!planned && !item) throw new Error(`Unknown fixture ${id}: ${url.pathname}`);
    const authorization = request.headers.authorization;
    if (![...(planned ? codexBearers : []), "Bearer fixture-access", "Bearer sk-api-fixture-access", "Bearer fixture-host-access", "Bearer fixture-companion", "fixture-access"].includes(authorization)) {
      throw new Error("Unexpected credential at local endpoint");
    }
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const body = Buffer.concat(chunks).toString("utf8");
    requests.push({ id, url: url.href, method: request.method, redirect: request.headers["x-usage-fixture-redirect"],
      headers: Object.fromEntries(["authorization", "chatgpt-account-id", "x-userid", "x-xai-token-auth", "x-grok-client-version", "x-grok-client-mode", "x-github-api-version", "user-agent"].filter((key) => request.headers[key] !== undefined).map((key) => [key, request.headers[key]])),
      body: planned && route === "consume" ? portableRedemptionBody(body) : body, redirectedRequest: request.url === "/redirect" });
    const choreography = choreographies.get(id);
    if (choreography) {
      choreography.observeRequest(url.href);
      await heldResponse(response, "response choreography", (signal) => choreography.releaseResponse(url.href, signal));
    }
    const gate = planned && statusGates.get(plannedKey);
    if (gate) await heldResponse(response, "a status gate", (signal) => gate.barrier.hold(signal));
    response.setHeader("Content-Type", "application/json");
    if (planned) {
      if (planned.fixtureStatus) response.writeHead(planned.fixtureStatus).end(JSON.stringify(planned.body));
      else response.end(JSON.stringify(planned));
      return;
    }
    if (item.response === "redirect" && request.url !== "/redirect") {
      response.writeHead(302, { Location: "/redirect" }).end();
      return;
    }
    if ((id === "lifecycle:command:current-failure" || id === "lifecycle:api:query-failure") && lifecycleProvider === "deepseek" || id === "lifecycle:command:other-failure" && lifecycleProvider === "openrouter" || id === "lifecycle:command:target-failure" && lifecycleProvider === "fireworks" || id === "lifecycle:command:all-partial" && lifecycleProvider === "openrouter" || item.response === "http-error" || item.failRoutes?.includes(route)) {
      response.writeHead(503).end(JSON.stringify({ message: `${id.startsWith("lifecycle:") ? authorization.replace(/^Bearer /u, "") : "fixture-access"} fixture-refresh \u001b[31m` }));
      return;
    }
    if (item.response === "invalid-json") { response.end("not-json"); return; }
    if (item.response === "oversize") { response.end(JSON.stringify({ padding: "x".repeat(65536) })); return; }
    if (item.response === "malformed") { response.end("{}"); return; }
    let payload = item.routes[route];
    if (item.pages && url.searchParams.has("pageToken")) payload = item.pages[0];
    if (payload === undefined) throw new Error(`Unmapped fixture route ${id}: ${url.pathname}`);
    response.end(JSON.stringify(payload));
  } catch (error) {
    routingErrors.push(error.message);
    response.writeHead(500).end(JSON.stringify({ error: "fixture routing failure" }));
  }
}
let modelFixture;
const modelOrdinals = new Map();
const modelHeaders = ["authorization", "chatgpt-account-id", "openai-beta", "originator", "accept", "content-type", "content-encoding", "upgrade"];
const modelRequest = (request, fields) => ({ id: modelFixture.id, url: `https://chatgpt.com${request.url}`, method: request.method, transport: "pig-host-proxy",
  headers: Object.fromEntries(modelHeaders.filter((key) => request.headers[key] !== undefined).map((key) => [key, request.headers[key]])), ...fields });
function plannedModelFixture(request) {
  if (!modelFixture?.modelResponses) throw new Error(`Unplanned host model request: ${request.method} ${request.url}`);
  if (request.url !== "/backend-api/codex/responses") throw new Error(`Unmapped host model route: ${request.url}`);
  if (!codexBearers.includes(request.headers.authorization)) throw new Error("Unexpected credential at local model endpoint");
}
async function routeModel(request, response) {
  try {
    plannedModelFixture(request);
    if (request.method !== "POST") throw new Error(`Unexpected host model method: ${request.method}`);
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const encoding = request.headers["content-encoding"];
    if (encoding !== undefined && encoding !== "zstd") throw new Error(`Unexpected host model encoding: ${encoding}`);
    const raw = Buffer.concat(chunks);
    const body = (encoding === "zstd" ? zstdDecompressSync(raw) : raw).toString("utf8");
    const ordinal = modelOrdinals.get(modelFixture.id) ?? 0;
    const planned = modelFixture.modelResponses[ordinal];
    if (!planned) throw new Error(`Unplanned host model response: ${modelFixture.id} ${ordinal}`);
    modelOrdinals.set(modelFixture.id, ordinal + 1);
    requests.push(modelRequest(request, { body: portableModelRequestBody(body, root) }));
    response.writeHead(200, { "Content-Type": "text/event-stream" }).end(serverSentEvents(planned));
  } catch (error) {
    routingErrors.push(error.message);
    response.writeHead(500).end(JSON.stringify({ error: "fixture routing failure" }));
  }
}
const modelServer = createTlsServer({ key: await readFile(join(tls, "server.key")), cert: await readFile(join(tls, "server.pem")) }, (request, response) => {
  const task = routeModel(request, response);
  activeRoutes.add(task);
  task.finally(() => activeRoutes.delete(task));
});
modelServer.on("upgrade", (request, socket) => {
  try {
    plannedModelFixture(request);
    requests.push(modelRequest(request, { response: 426 }));
  } catch (error) { routingErrors.push(error.message); }
  socket.end("HTTP/1.1 426 Upgrade Required\r\nContent-Length: 0\r\nConnection: close\r\n\r\n");
});
server.on("connect", (request, socket) => {
  if (request.url !== "chatgpt.com:443") {
    routingErrors.push(`Refused host proxy tunnel: ${request.url}`);
    socket.destroy();
    return;
  }
  socket.write("HTTP/1.1 200 Connection Established\r\n\r\n");
  modelServer.emit("connection", socket);
});
let child;
let succeeded = false;
let interrupted = false;
function stop() {
  interrupted = true;
  child?.stdin.end();
  child?.kill("SIGTERM");
  replay?.kill("SIGTERM");
  for (const choreography of choreographies.values()) choreography.cancel(new Error("Usage observation interrupted"));
  for (const gate of statusGates.values()) gate.barrier.cancel(new Error("Usage observation interrupted"));
  server.closeAllConnections();
}
process.once("SIGINT", stop);
process.once("SIGTERM", stop);
const packageArguments = ["--mode", "rpc", "--no-session", "--offline", "--no-extensions", "--no-mcp", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-tools", "--model", "deepseek/fixture", "-e", project];
const productionUsageMember = "extensions/pig-status-usage.ts";
function swapImports(text, swaps, name) {
  let swapped = text;
  for (const [from, to] of swaps) {
    const specifier = `from ${JSON.stringify(from)};`;
    assert.equal(swapped.split(specifier).length, 2, `${name} must import ${from} exactly once`);
    swapped = swapped.replace(specifier, `from ${JSON.stringify(to)};`);
  }
  return swapped;
}
async function sourceCompositionMembers(backendEntry) {
  const composed = swapImports(await readFile(join(project, productionUsageMember), "utf8"),
    [["@narumitw/pi-usage/dist/index.ts", backendEntry], ["../status/usage.ts", join(project, "status/usage.ts")]], "The production usage member");
  const path = join(root, "pig-status-usage-source.ts");
  await writeFile(path, composed);
  const members = JSON.parse(await readFile(join(project, "package.json"), "utf8")).pi.extensions;
  assert.equal(members.filter((member) => member === productionUsageMember).length, 1);
  return members.flatMap((member) => ["-e", member === productionUsageMember ? path : join(project, member)]);
}
let replay;
const counterCopy = process.env.USAGE_FIXTURE_COUNTER_COPY;
async function counterExtension() {
  const extensionSource = join(project, measuredEntrypoints.extension);
  const text = await readFile(extensionSource, "utf8");
  const relative = [...text.matchAll(/from "(\.\/[^"]+)";/gu)].map((match) => [match[1], join(dirname(extensionSource), match[1])]);
  const backend = [...text.matchAll(/from "(\.\.\/\.\.\/node_modules\/@narumitw\/pi-usage\/(src|dist)\/[^"]+)";/gu)]
    .map((match) => [match[1], match[2] === "src" ? join(counterCopy, "source", match[1].slice(match[1].indexOf("/src/") + 1)) : join(dirname(extensionSource), match[1])]);
  assert.equal(backend.filter(([, to]) => to.startsWith(counterCopy)).length, 3, "The observation extension must import the backend source three times");
  const path = join(root, "counter-extension.mjs");
  await writeFile(path, swapImports(text, [...relative, ...backend], "The observation extension"));
  return path;
}
async function lifecycleOnly(...options) {
  return replayRun(["--lifecycle-observe", ...options], process.env);
}
async function replayRun(options, env) {
  replay = spawn(process.execPath, [fileURLToPath(import.meta.url), ...options], { cwd: project, env, stdio: ["ignore", "pipe", "pipe"] });
  await appendFile(join(scratch, "pids"), `${replay.pid}\n`);
  let stdout = "";
  let stderr = "";
  replay.stdout.on("data", (chunk) => { stdout += chunk; });
  replay.stderr.on("data", (chunk) => { stderr += chunk; });
  const exit = await once(replay, "close");
  replay = undefined;
  assert.deepEqual(exit, [0, null], `Replay ${options.join(" ")} failed: ${stderr}`);
  const replayRoot = /^Disposable evidence: (.+)$/mu.exec(stdout)?.[1];
  assert.ok(replayRoot?.startsWith(`${scratch}/usage-`), "Replay left no disposable evidence");
  const rawBranches = await readFile(join(replayRoot, "lifecycle-branches.json"), "utf8");
  const raw = options.includes("--counter-composition") ? "raw-counters" : "raw-branches";
  await mkdir(join(scratch, raw), { recursive: true });
  await writeFile(join(scratch, raw, `${createHash("sha256").update(rawBranches).digest("hex")}.json`), rawBranches, { mode: 0o600 });
  const run = { observed: JSON.parse(await readFile(join(replayRoot, "observed.json"), "utf8")), branches: JSON.parse(rawBranches) };
  await rm(replayRoot, { recursive: true, force: true });
  return run;
}
async function replayCounterComposition(observed, branchObservations) {
  const sources = await pinnedBackendSources(project, JSON.parse(await readFile(join(retained, "provenance.json"), "utf8")));
  const { manifest, instrumented } = await instrumentSources(sources, root);
  const copy = join(root, "counter");
  for (const member of ["composition", "source"]) await writeCounterCopy(join(copy, member), instrumented);
  await writeFile(join(copy, "manifest.json"), JSON.stringify(manifest));
  const run = await replayRun(["--observe", "--counter-composition"], { ...process.env, USAGE_FIXTURE_COUNTER_COPY: copy });
  await writeFile(join(scratch, "raw-counters", `manifest-${manifest.sha256}.json`), JSON.stringify(manifest), { mode: 0o600 });
  const { selectedMembers: _replayMembers, ...replayed } = run.observed;
  const { selectedMembers: _members, ...expected } = observed;
  assert.deepEqual(replayed, expected, "Counter replay behaviour drift: requests, RPC UI, receipts or results differ from the uninstrumented run");
  assert.deepEqual(run.branches.observations.map((window) => window.id), branchObservations.observations.map((window) => window.id), "Counter replay window drift");
  return { ...branchObservations, counterManifest: manifest,
    observations: branchObservations.observations.map((window, index) => {
      const counters = run.branches.observations[index].counters;
      assert.equal(counters?.composition, counterComposition, `Counter replay window without counters: ${window.id}`);
      return counters.counts.length ? { ...window, counters } : window;
    }) };
}
async function replaySourceComposition(observed, branchObservations) {
  const lifecycleRequests = observed.requests.filter((request) => request.id.startsWith("lifecycle:"));
  const dist = await lifecycleOnly();
  const source = await lifecycleOnly("--source-composition");
  for (const run of [dist, source]) {
    assert.deepEqual(run.observed.requests, lifecycleRequests, "Lifecycle-only replay request drift");
    assert.deepEqual(run.observed.commandObservations, observed.commandObservations, "Lifecycle-only replay UI drift");
  }
  assert.deepEqual(source.observed.lifecycle, dist.observed.lifecycle, "Source-composition replay receipt drift");
  assert.deepEqual(source.observed.bridgeHostErrors, dist.observed.bridgeHostErrors, "Source-composition replay host error drift");
  const replayedScripts = new Map(source.branches.scripts.map((script) => [script.sha256, script]));
  const windows = new Map(source.branches.observations.map((window) => [window.id, window]));
  assert.deepEqual([...windows.keys()], branchObservations.observations.filter((window) => window.id.startsWith("lifecycle:")).map((window) => window.id), "Source-composition replay window drift");
  const scripts = new Map(branchObservations.scripts.map((script) => [script.sha256, script]));
  const observations = branchObservations.observations.map((window) => {
    const blocks = (windows.get(window.id)?.blocks ?? []).filter((block) => replayedScripts.get(block.script).path.startsWith("src/"));
    for (const block of blocks) scripts.set(block.script, replayedScripts.get(block.script));
    return blocks.length ? { ...window, blocks: [...window.blocks, ...blocks.map((block) => ({ ...block, composition: "source-replay" }))] } : window;
  });
  return { ...branchObservations, scripts: [...scripts.values()].sort((a, b) => a.path.localeCompare(b.path) || a.sha256.localeCompare(b.sha256)), observations };
}
async function prepareHost(hostRoot, port) {
  const env = {
    PATH: process.env.PATH, HOME: join(hostRoot, "home"), TMPDIR: hostRoot, PIG_HOME: join(hostRoot, "pig"),
    PIG_CODING_AGENT_DIR: join(hostRoot, "pig/agent"), PI_CODING_AGENT_DIR: join(hostRoot, "pi-agent"),
    XDG_CONFIG_HOME: join(hostRoot, "config"), PIG_OFFLINE: "1", PI_OFFLINE: "1", PI_SKIP_VERSION_CHECK: "1",
    GOMAXPROCS: "2", GOFLAGS: "-p=2", GOTOOLCHAIN: "local", TZ: "UTC", LANG: "en_US.UTF-8", LC_ALL: "en_US.UTF-8",
    GOPATH: execFileSync("go", ["env", "GOPATH"], { encoding: "utf8" }).trim(),
    GOCACHE: join(scratch, "go-cache"), MOONSHOT_API_KEY: "fixture-shared-moonshot",
    USAGE_FIXTURE_ENDPOINT: `http://127.0.0.1:${port}`, USAGE_FIXTURE_NOW: String(now),
    HTTPS_PROXY: `http://127.0.0.1:${port}`, HTTP_PROXY: `http://127.0.0.1:${port}`, NO_PROXY: "", SSL_CERT_FILE: join(tls, "authority.pem"),
    USAGE_FIXTURE_OUTPUT: join(hostRoot, "observed.json"), USAGE_FIXTURE_CANDIDATE_ARM: join(hostRoot, "candidate-arm"), USAGE_FIXTURE_BRANCH_OUTPUT: join(hostRoot, "branch-observations.json"),
    ...(counterReplay ? { USAGE_FIXTURE_COUNTER_MANIFEST: await counterManifestIdentity() } : {}),
    NODE_OPTIONS: `--import=${join(project, measuredEntrypoints.transport)}`,
  };
  await mkdir(env.PIG_CODING_AGENT_DIR, { recursive: true });
  await mkdir(env.HOME, { recursive: true });
  await mkdir(env.PI_CODING_AGENT_DIR, { recursive: true });
  await writeFile(join(env.PI_CODING_AGENT_DIR, "auth.json"), JSON.stringify(Object.fromEntries(cases.filter((item) => item.id.endsWith(":normal")).map((item) => [item.provider, { type: "api_key", key: "fixture-wrong-root" }]))), { mode: 0o600 });
  await writeFile(join(env.PI_CODING_AGENT_DIR, "pi-usage.json"), "[]", { mode: 0o600 });
  await writeFile(join(env.PIG_CODING_AGENT_DIR, "auth.json"), JSON.stringify({ deepseek: { type: "api_key", key: "fixture-host-access" } }), { mode: 0o600 });
  await writeFile(join(env.PIG_CODING_AGENT_DIR, "models.json"), JSON.stringify({ providers: { deepseek: { baseUrl: "https://api.deepseek.com/v1", api: "openai-completions", models: [{ id: "fixture", name: "Synthetic fixture", contextWindow: 128000, maxTokens: 1024 }] } } }), { mode: 0o600 });
  return env;
}
async function counterManifestIdentity() {
  const manifest = JSON.parse(await readFile(join(counterCopy, "manifest.json"), "utf8"));
  return `${manifest.sha256}:${manifest.counters.length}`;
}
async function residentTreeKiB(pid, owned) {
  const state = await readFile(`/proc/${pid}/status`, "utf8");
  const rss = Number(/^VmRSS:\s+(\d+) kB$/m.exec(state)?.[1]);
  if (!Number.isFinite(rss)) throw new Error("Missing process RSS");
  owned.push(pid);
  await appendFile(join(scratch, "pids"), `${pid}\n`);
  const children = new Set();
  for (const task of await readdir(`/proc/${pid}/task`)) {
    const raw = await readFile(`/proc/${pid}/task/${task}/children`, "utf8");
    for (const descendant of raw.trim().split(/\s+/).filter(Boolean).map(Number)) children.add(descendant);
  }
  let total = rss;
  for (const descendant of children) total += await residentTreeKiB(descendant, owned);
  return total;
}
async function shutdownOwned(host, closed, owned, stderr) {
  const shutdownAt = performance.now();
  host.stdin.end();
  const exit = await Promise.race([closed, new Promise((_, reject) => { const timer = setTimeout(() => reject(new Error("PiG shutdown timed out")), 15000); timer.unref(); })]);
  assert.deepEqual(exit, [0, null], stderr);
  const parentShutdownMs = performance.now() - shutdownAt;
  for (const pid of owned) {
    const deadline = performance.now() + 3000;
    while (true) {
      try { await stat(`/proc/${pid}`); }
      catch (error) { if (error.code === "ENOENT") break; throw error; }
      assert.ok(performance.now() < deadline, `Owned process ${pid} survived shutdown`);
      await delay(20);
    }
  }
  return { parentShutdownMs, shutdownMs: performance.now() - shutdownAt };
}
const measurementSampleCount = 3;
async function measurePackage(port) {
  const samples = [];
  let composition;
  for (let index = 0; index < measurementSampleCount; index += 1) {
    const hostRoot = join(root, `measure-${index}`);
    const env = await prepareHost(hostRoot, port);
    const requestsBefore = requests.length;
    const loadAverage = loadavg();
    const startedAt = performance.now();
    child = spawn("pig", packageArguments, { cwd: hostRoot, env, stdio: ["pipe", "pipe", "pipe"] });
    await appendFile(join(scratch, "pids"), `${child.pid}\n`);
    const closed = once(child, "close");
    let stderr = "";
    let pending = "";
    const waiters = new Map();
    const errors = [];
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("close", () => { for (const waiter of waiters.values()) waiter({ success: false, error: "PiG exited during a pending RPC request" }); });
    child.stdout.on("data", (chunk) => {
      pending += chunk;
      while (pending.includes("\n")) {
        const boundary = pending.indexOf("\n");
        const line = pending.slice(0, boundary);
        pending = pending.slice(boundary + 1);
        if (!line) continue;
        const event = JSON.parse(line);
        if (event.type === "extension_error") errors.push(event);
        if (event.type === "extension_ui_request" && ["select", "confirm", "input", "editor"].includes(event.method)) {
          child.stdin.write(JSON.stringify({ type: "extension_ui_response", id: event.id, cancelled: true }) + "\n");
        }
        waiters.get(event.id)?.(event);
      }
    });
    const rpc = async (id, type, fields = {}) => {
      let timeout;
      try {
        const result = new Promise((resolve) => { waiters.set(id, resolve); });
        child.stdin.write(JSON.stringify({ id, type, ...fields }) + "\n");
        const response = await Promise.race([result, new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error(`RPC ${id} timed out: ${stderr}`)), 120_000); })]);
        assert.equal(response.success, true, JSON.stringify(response) + stderr);
        return response.data;
      } finally { clearTimeout(timeout); waiters.delete(id); }
    };
    await rpc("commands", "get_commands");
    const startupMs = performance.now() - startedAt;
    const loaded = await rpc("extensions", "get_extensions");
    composition = loaded.paths.map((path) => path.replace(project, "$PACKAGE"));
    await rpc("usage", "prompt", { message: "/usage" });
    assert.ok(requests.slice(requestsBefore).some((request) => request.id === "host" && request.url === "https://api.deepseek.com/user/balance"), "Package sample made no current-provider usage request");
    const owned = [];
    const residentKiB = await residentTreeKiB(child.pid, owned);
    const { parentShutdownMs, shutdownMs } = await shutdownOwned(child, closed, owned, stderr);
    assert.deepEqual(errors, [], stderr);
    samples.push({ startupMs, residentKiB, shutdownMs, parentShutdownMs, processCount: owned.length, loadAverage });
  }
  assert.deepEqual(routingErrors, []);
  const identity = await measurementIdentity(project, join(root, "measure-0"));
  const previous = await retainedMeasurements();
  const measurements = { formatVersion: 2, platform: process.platform, arch: process.arch, probeSha256: identity, identityProtocol: measurementIdentityProtocol,
    package: { measurementClass: "Normal-optimization, non-profiling package measurements retained for final native comparison",
      composition, protocol: "Cold disposable PiG root per sample with shared build cache; selected package only (no observation extension or inspector); loopback transport preload; RPC get_commands readiness; one /usage current-provider refresh with its menu cancelled; aggregate host/descendant RSS; stdin EOF shutdown and all observed PIDs gone. No model prompt. loadAverage is the host's 1/5/15-minute load at launch.",
      samples },
    ...(matchedMeasurements(previous, identity) ? { instrumented: previous.instrumented } : {}) };
  await writeFile(join(retained, "measurements.json"), JSON.stringify(measurements, null, 2) + "\n");
  console.log(`usage-provider package measurements: ${samples.length} matched samples`);
  for (const sample of samples) console.log(JSON.stringify(sample));
}
const measurementIdentityProtocol = "SHA-256 of the probe files the measured runs execute (the module import closure of the baseline, transport preload and observation extension, plus the Go peer and TLS helper build inputs; test files excluded), selected-package files, production dependency trees, generated compaction/native footer inputs and PiG/Node/Go executable identities; not just the orchestration file.";
async function retainedMeasurements() {
  try { return JSON.parse(await readFile(join(retained, "measurements.json"), "utf8")); }
  catch (error) { if (error.code !== "ENOENT") throw error; }
  return undefined;
}
function matchedMeasurements(previous, identity) {
  return previous?.formatVersion === 2 && previous.probeSha256 === identity && previous.platform === process.platform && previous.arch === process.arch;
}
try {
server.listen(0, "127.0.0.1");
await once(server, "listening");
const address = server.address();
if (!address || typeof address === "string") throw new Error("Missing loopback listener");
if (mode === "--measure") {
  await measurePackage(address.port);
  succeeded = true;
} else {
  const env = await prepareHost(root, address.port);
  const composition = sourceReplay ? await sourceCompositionMembers(join(project, "node_modules/@narumitw/pi-usage/src/index.ts"))
    : counterReplay ? await sourceCompositionMembers(join(counterCopy, "composition", "src/index.ts")) : undefined;
  const command = [...(composition ? [...packageArguments.slice(0, -2), ...composition] : packageArguments), "-e", join(project, measuredEntrypoints.bridge), "-e", counterReplay ? await counterExtension() : join(project, measuredEntrypoints.extension)];
  const startedAt = performance.now();
  child = spawn("pig", command, { cwd: root, env, stdio: ["pipe", "pipe", "pipe"] });
  await appendFile(join(scratch, "pids"), `${child.pid}\n`);
  const closed = once(child, "close");
  let stderr = "";
  let pending = "";
  const waiters = new Map();
  child.on("close", () => { for (const waiter of waiters.values()) waiter({ success: false, error: "PiG exited during a pending RPC request" }); });
  const transcript = [];
  let activeCommand;
  let statusCompletion;
  let awaitedStatus;
  let chooseSettings = false;
  let lifecycleFixture;
  let actions = [];
  let agentCompletion;
  const commandObservations = [];
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  child.stdout.on("data", (chunk) => {
    pending += chunk;
    while (pending.includes("\n")) {
      const boundary = pending.indexOf("\n");
      const line = pending.slice(0, boundary);
      pending = pending.slice(boundary + 1);
      if (!line) continue;
      const event = JSON.parse(line);
      transcript.push(event);
      if (event.type === "message_end" && event.message?.role === "assistant" && activeCommand?.startsWith("lifecycle:")) {
        const { role, api, provider, model, content, usage, stopReason, responseId, diagnostics } = event.message;
        commandObservations.push({ command: activeCommand, type: event.type, message: { role, api, provider, model, content, usage, stopReason, responseId,
          ...(diagnostics ? { diagnostics: diagnostics.map(({ timestamp: _timestamp, ...diagnostic }) => diagnostic) } : {}) } });
      }
      if (event.type === "agent_end") agentCompletion?.();
      if (event.type === "extension_ui_request" && event.method === "setStatus") {
        if (activeCommand?.startsWith("lifecycle:")) {
          const { id: _id, ...portable } = event;
          commandObservations.push({ command: activeCommand, ...portable });
        }
        if (event.statusKey === "usage" && awaitedStatus?.test(event.statusText)) statusCompletion?.();
        for (const gate of statusGates.values()) {
          if (event.statusKey === "usage" && gate.fixtureId === lifecycleFixture?.id && gate.status.test(event.statusText)) gate.barrier.release();
        }
      }
      if (event.type === "extension_ui_request" && ["notify", "select", "confirm", "input", "editor"].includes(event.method)) {
        const { id: _id, ...portable } = event;
        commandObservations.push({ command: activeCommand ?? "startup", ...JSON.parse(JSON.stringify(portable).replaceAll(root, "$ROOT")) });
        if (event.method !== "notify") {
          let selection;
          if (lifecycleFixture) {
            const action = actions.shift();
            if (action !== undefined && action !== null) {
              const matches = event.options?.filter((item) => item === action || item.includes(action)) ?? [];
              if (matches.length !== 1) routingErrors.push(`Missing/ambiguous lifecycle action ${action}: ${JSON.stringify(event)}`);
              selection = matches[0];
              if (lifecycleFixture.armCandidates?.after === action) writeFileSync(env.USAGE_FIXTURE_CANDIDATE_ARM, lifecycleFixture.armCandidates.mode, { mode: 0o600 });
            }
          } else selection = chooseSettings && event.method === "select" ? event.options.find((item) => /settings/i.test(item)) : undefined;
          chooseSettings = false;
          child.stdin.write(JSON.stringify({ type: "extension_ui_response", id: event.id, ...(selection ? { value: selection } : { cancelled: true }) }) + "\n");
        }
      }
      waiters.get(event.id)?.(event);
    }
  });
  child.on("error", (error) => { for (const waiter of waiters.values()) waiter({ success: false, error: error.message }); });
  async function rpc(id, type, fields = {}) {
    let timeout;
    try {
      const result = new Promise((resolve) => { waiters.set(id, resolve); });
      child.stdin.write(JSON.stringify({ id, type, ...fields }) + "\n");
      const response = await Promise.race([result, new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error(`RPC ${id} timed out: ${stderr}`)), id === "observe" ? 300_000 : 120_000); })]);
      assert.equal(response.success, true, JSON.stringify(response) + stderr);
      return response.data;
    } finally { clearTimeout(timeout); waiters.delete(id); }
  }
  const commands = await rpc("commands", "get_commands");
  const startupMs = performance.now() - startedAt;
  const loaded = await rpc("extensions", "get_extensions");
  await rpc("observe", "prompt", { message: mode === "--lifecycle-observe" ? "/fixture-observe lifecycle-only" : "/fixture-observe" });
  for (const [index, message] of ["/usage invalid", "/fast invalid", "/fast", "/usage", "/usage"].entries()) {
    activeCommand = `${index}:${message}`;
    chooseSettings = index === 4;
    await rpc(`command-${index}`, "prompt", { message });
  }
  activeCommand = undefined;
  await captureLifecycle(rpc, (fixture) => {
    if (lifecycleFixture) assert.deepEqual(actions, [], `Unused lifecycle actions: ${lifecycleFixture.id}`);
    lifecycleFixture = fixture;
    modelFixture = fixture;
    activeCommand = fixture?.id;
    actions = [...(fixture?.actions ?? [])];
  }, async (selectModel, status) => {
    let timer;
    try {
      awaitedStatus = status;
      const completed = new Promise((resolve, reject) => {
        statusCompletion = resolve;
        timer = setTimeout(() => reject(new Error("Successor usage status did not complete")), 15000);
      });
      await selectModel();
      await completed;
    } finally { clearTimeout(timer); statusCompletion = awaitedStatus = undefined; }
  }, async (prompt) => {
    let timer;
    try {
      const completed = new Promise((resolve, reject) => {
        agentCompletion = resolve;
        timer = setTimeout(() => reject(new Error("Synthetic model prompt did not complete")), 30000);
      });
      await prompt();
      await completed;
    } finally { clearTimeout(timer); agentCompletion = undefined; }
  });
  const observation = JSON.parse(await readFile(env.USAGE_FIXTURE_OUTPUT, "utf8"));
  const rawBranches = await readFile(env.USAGE_FIXTURE_BRANCH_OUTPUT, "utf8");
  const rawBranchesSha256 = createHash("sha256").update(rawBranches).digest("hex");
  const rawDirectory = join(scratch, "raw-branches");
  await mkdir(rawDirectory, { recursive: true });
  await writeFile(join(rawDirectory, `${rawBranchesSha256}.json`), rawBranches, { mode: 0o600 });
  const ownedPids = [];
  const residentKiB = await residentTreeKiB(child.pid, ownedPids);
  const { parentShutdownMs, shutdownMs } = await shutdownOwned(child, closed, ownedPids, stderr);
  for (const choreography of choreographies.values()) choreography.finish();
  assert.deepEqual(routingErrors, []);
  assertPlannedResponsesUsed(plannedOrdinals, modelOrdinals);
  assert.equal(transcript.filter((event) => event.type === "extension_error").length, 0, stderr);
  const observed = {
    formatVersion: 1, commandNames: commands.commands.map((item) => item.name).sort(),
    selectedMembers: loaded.paths.map((path) => path.replace(project, "$PACKAGE")),
    ...observation, commandObservations,
    bridgeHostErrors: stderr.split("\n").filter((line) => line.startsWith("Event handler error (")),
    requests: requests.filter((request) => request.id !== "host"),
  };
  await writeFile(join(root, "actual-observation.json"), JSON.stringify(observed, null, 2) + "\n", { mode: 0o600 });
  for (const item of (mode === "--lifecycle-observe" ? [] : cases)) {
    const dist = observed.results.find((result) => result.implementation === "dist" && result.id === item.id);
    const source = observed.results.find((result) => result.implementation === "source" && result.id === item.id);
    assert.deepEqual({ ...dist, implementation: "matched" }, { ...source, implementation: "matched" }, `Source/dist behavior drift: ${item.id}`);
  }
  for (const item of (mode === "--lifecycle-observe" ? [] : consumerFixtures)) {
    const dist = observed.consumers.find((result) => result.implementation === "dist" && result.id === item.id);
    const source = observed.consumers.find((result) => result.implementation === "source" && result.id === item.id);
    assert.ok(dist && source, `Missing consumer capture: ${item.id}`);
    assert.deepEqual({ ...dist, implementation: "matched" }, { ...source, implementation: "matched" }, `Source/dist consumer drift: ${item.id}`);
  }
  if (mode !== "--lifecycle-observe") {
    verifySettingsAuth(observed);
    verifyBridge(observed);
  }
  verifyLifecycle(observed);
  const identity = await measurementIdentity(project, root);
  const previous = await retainedMeasurements();
  const matched = matchedMeasurements(previous, identity);
  const sample = { startupMs, residentKiB, shutdownMs, parentShutdownMs, rawBranchesSha256, processCount: ownedPids.length };
  const measurements = { formatVersion: 2, platform: process.platform, arch: process.arch, probeSha256: identity, identityProtocol: measurementIdentityProtocol,
    ...(matched ? { package: previous.package } : {}),
    instrumented: { measurementClass: "Normal-optimization inspector-instrumented observation-run measurements; not comparable with package samples",
      composition: observed.selectedMembers,
      protocol: "Cold disposable PiG root with shared build cache; complete package plus identical probe; RPC get_commands readiness; aggregate host/descendant RSS after fixture run; stdin EOF shutdown and all observed PIDs gone. Includes synthetic Codex model prompts answered by the loopback proxy endpoint.",
      samples: [...(matched && mode === "--capture" ? previous.instrumented?.samples ?? [] : []), sample].slice(-measurementSampleCount) } };
  await writeFile(join(root, "rpc.json"), JSON.stringify(transcript, null, 2));
  await writeFile(join(root, "stderr.txt"), stderr);
  const branchObservations = disposableOutput ? rawBranches : JSON.stringify(await replayCounterComposition(observed, await replaySourceComposition(observed, JSON.parse(rawBranches)))) + "\n";
  const raw = JSON.stringify(observed, null, 2) + "\n";
  if (mode === "--capture") {
    await writeFile(join(retained, "branch-observations.json"), branchObservations);
    await writeFile(join(retained, "expected.json"), raw);
    await writeFile(join(retained, "measurements.json"), JSON.stringify(measurements, null, 2) + "\n");
  } else if (mode === "--check") {
    const currentContract = await branchEvidenceContract(JSON.parse(branchObservations), root);
    const retainedContract = await branchEvidenceContract(JSON.parse(await readFile(join(retained, "branch-observations.json"), "utf8")), root);
    assert.deepEqual(currentContract, retainedContract, "V8 branch-evidence contract drift");
    assert.deepEqual(observed, JSON.parse(await readFile(join(retained, "expected.json"), "utf8")));
    assert.ok(matched, "Retained measurements predate the current measurement identity; run --measure then --capture");
    assert.equal(previous.package?.samples.length, measurementSampleCount, "Retained package measurements are incomplete; run --measure");
    assert.ok(previous.instrumented?.samples.length > 0, "Retained instrumented measurements are missing; run --capture");
  } else {
    const output = disposableOutput ? root : scratch;
    await writeFile(join(output, "observed.json"), raw);
    await writeFile(join(output, "lifecycle-branches.json"), branchObservations);
    await writeFile(join(output, "measurements.json"), JSON.stringify(measurements, null, 2) + "\n");
    if (disposableOutput) console.log(`Disposable evidence: ${root}`);
  }
  assert.equal(interrupted, false, "Usage observation interrupted");
  if (!["--observe", "--lifecycle-observe"].includes(mode)) await provenance(project, root, mode === "--capture", mode === "--check" ? JSON.parse(branchObservations) : undefined);
  succeeded = true;
  console.log(`usage-provider baseline: ${observed.results.length / 2} provider + ${observed.consumers.length / 2} consumer cases x source/dist; ${observation.lifecycle.length} registered lifecycle cases; ${requests.length} loopback requests; ${observation.protocol.length} real-PiG API observations; clean shutdown`);
  console.log(JSON.stringify(sample));
}
} catch (error) {
  console.error(`Disposable evidence: ${root}`);
  throw error;
} finally {
  if (child && child.exitCode === null && child.signalCode === null) {
    child.stdin.end();
    child.kill("SIGTERM");
    await once(child, "close");
  }
  process.removeListener("SIGINT", stop);
  process.removeListener("SIGTERM", stop);
  for (const choreography of choreographies.values()) choreography.cancel(new Error("Usage fixture teardown"));
  for (const gate of statusGates.values()) gate.barrier.cancel(new Error("Usage fixture teardown"));
  server.closeAllConnections();
  modelServer.closeAllConnections();
  await Promise.allSettled([...activeRoutes]);
  await new Promise((resolve) => server.close(resolve));
  await writeFile(join(root, "response-choreography.json"), JSON.stringify([...choreographies].map(([id, choreography]) => ({ id, events: choreography.events })), null, 2) + "\n");
  if (succeeded && !["--observe", "--lifecycle-observe"].includes(mode)) await rm(root, { recursive: true, force: true });
}
