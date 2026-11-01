/** Source-map identity and coordinate negative controls for installed-dist attribution. */
import assert from "node:assert/strict";
import test from "node:test";
import { distSourceMap } from "./dist-attribution.mjs";

const text = "    if (ready) {}\n";
const original = "  if (ready) {}\n";
const payload = { version: 3, sources: ["../src/query.ts"], sourcesContent: [original], mappings: "AAAE", names: [] };
const parsed = { text, getLineAndCharacterOfPosition(offset) { return offset < text.length ? { line: 0, character: offset } : { line: 1, character: 0 }; } };

test("dist attribution accepts only an exact original coordinate with whitespace-only generated displacement", () => {
  const locate = distSourceMap("dist/index.ts", payload, new Map([["src/query.ts", original]]));
  assert.deepEqual(locate(parsed, 4), { source: "src/query.ts", line: 1, character: 2 });
  assert.equal(locate(parsed, 5), undefined);
  assert.equal(locate(parsed, text.length), undefined);
});

test("dist attribution rejects changed source-map content and paths outside the frozen source tree", () => {
  assert.throws(() => distSourceMap("dist/index.ts", payload, new Map([["src/query.ts", "different"]])), /Changed source-map content/u);
  assert.throws(() => distSourceMap("dist/index.ts", { ...payload, sources: ["../../other.ts"] }, new Map()), /Unexpected mapped source/u);
});

let measuredScript = 0;
async function measured(text, value = true) {
  const filename = `dist-ledger-synthetic-${measuredScript++}.js`;
  const { Session } = await import("node:inspector");
  const { Script } = await import("node:vm");
  const session = new Session();
  const post = (method, params = {}) => new Promise((resolve, reject) => session.post(method, params, (error, result) => error ? reject(error) : resolve(result)));
  session.connect();
  try {
    await post("Profiler.enable");
    await post("Profiler.startPreciseCoverage", { callCount: true, detailed: true });
    new Script(text, { filename }).runInThisContext()(value);
    const { result } = await post("Profiler.takePreciseCoverage");
    return result.find((item) => item.url === filename).functions;
  } finally {
    await post("Profiler.stopPreciseCoverage");
    session.disconnect();
  }
}

test("dist ledger witnesses collapsed exact branch ASTs but rejects ambiguous twins and altered map content", async () => {
  const { createHash } = await import("node:crypto");
  const { mkdir, mkdtemp, writeFile, rm } = await import("node:fs/promises");
  const { resolve, join } = await import("node:path");
  const { buildBranchLedger } = await import("./branch-ledger.mjs");
  const sha = (value) => createHash("sha256").update(value).digest("hex");
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "dist-ledger-test-"));
  try {
    const backend = join(project, "node_modules/@narumitw/pi-usage");
    await mkdir(join(backend, "src"), { recursive: true });
    await mkdir(join(backend, "dist"));
    const source = 'function run(value) {\n  if (value) return "yes";\n  return "no";\n}';
    const dist = `\n${source}`;
    const runtime = `(${source.replaceAll("\n", "")})`;
    const map = { version: 3, sources: ["../src/query.ts"], sourcesContent: [source], names: [], mappings: ";AAAA;AACE,aAAW" };
    const files = { "src/query.ts": source, "dist/index.ts": dist, "dist/index.ts.map": JSON.stringify(map) };
    for (const [path, text] of Object.entries(files)) await writeFile(join(backend, path), text);
    const provenance = { dependencies: { "node_modules/@narumitw/pi-usage": { installed: { files: Object.fromEntries(Object.entries(files).map(([path, text]) => [path, { sha256: sha(text) }])) } } } };
    const inventory = { symbols: [], semanticFixtures: [{ id: "lifecycle:synthetic", evidence: ["expected.json#/lifecycle/0"], sourceBranchEvidence: "branch-ledger.json#/fixtureMappings/0", inputs: { value: true } }] };
    const expected = { lifecycle: [{ id: "lifecycle:synthetic", value: "yes" }], requests: [] };
    const observations = { scripts: [{ path: "dist/index.ts", text: runtime, sha256: sha(runtime) }], observations: [{ id: "lifecycle:synthetic", blocks: [{ script: sha(runtime), functions: await measured(runtime) }] }] };
    const build = () => buildBranchLedger(project, project, inventory, observations, expected, provenance);
    const ledger = await build();
    assert.deepEqual(ledger.obligations.map((row) => [row.outcome, row.status]), [["true", "observed"], ["false", "pending"]]);
    assert.equal(ledger.obligations[0].evidence[0].runtimeMatch, "unique-complete-branch-AST");
    assert.equal(ledger.fixtureMappings[0].productionDistBranchOutcomes.length, 1);
    const ambiguous = `(function run(value) {if (value) return "yes";return "no";function unused(value) {if (value) return "yes";}})`;
    observations.scripts = [{ path: "dist/index.ts", text: ambiguous, sha256: sha(ambiguous) }];
    observations.observations[0].blocks = [{ script: sha(ambiguous), functions: await measured(ambiguous) }];
    assert.ok((await build()).obligations.every((row) => row.status === "pending"));
    const changedMap = JSON.stringify({ ...map, sourcesContent: ["different"] });
    await writeFile(join(backend, "dist/index.ts.map"), changedMap);
    await assert.rejects(build(), /Changed installed source map/u);
    provenance.dependencies["node_modules/@narumitw/pi-usage"].installed.files["dist/index.ts.map"].sha256 = sha(changedMap);
    await assert.rejects(build(), /Changed source-map content/u);
  } finally { await rm(project, { recursive: true, force: true }); }
});

test("dist ledger grants an if-false count witness through a unique branch mapping and keeps it pending for an ambiguous twin", async (t) => {
  const { createHash } = await import("node:crypto");
  const { mkdir, mkdtemp, writeFile, rm } = await import("node:fs/promises");
  const { resolve, join } = await import("node:path");
  const { buildBranchLedger } = await import("./branch-ledger.mjs");
  const sha = (value) => createHash("sha256").update(value).digest("hex");
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "dist-count-test-"));
  t.after(() => rm(project, { recursive: true, force: true }));
  const backend = join(project, "node_modules/@narumitw/pi-usage");
  await mkdir(join(backend, "src"), { recursive: true });
  await mkdir(join(backend, "dist"));
  const source = 'function run(value) {\n  if (value) return "yes";\n  return "no";\n}';
  const map = { version: 3, sources: ["../src/query.ts"], sourcesContent: [source], names: [], mappings: ";AAAA;AACE,aAAW" };
  const files = { "src/query.ts": source, "dist/index.ts": `\n${source}`, "dist/index.ts.map": JSON.stringify(map) };
  await Promise.all(Object.entries(files).map(([path, text]) => writeFile(join(backend, path), text)));
  const provenance = { dependencies: { "node_modules/@narumitw/pi-usage": { installed: { files: Object.fromEntries(Object.entries(files).map(([path, text]) => [path, { sha256: sha(text) }])) } } } };
  const inventory = { symbols: [], semanticFixtures: [{ id: "lifecycle:synthetic", evidence: ["expected.json#/lifecycle/0"], sourceBranchEvidence: "branch-ledger.json#/fixtureMappings/0", inputs: { value: false } }] };
  const expected = { lifecycle: [{ id: "lifecycle:synthetic", value: "no" }], requests: [] };
  const ledgerFor = async (runtime) => buildBranchLedger(project, project, inventory, {
    scripts: [{ path: "dist/index.ts", text: runtime, sha256: sha(runtime) }],
    observations: [{ id: "lifecycle:synthetic", blocks: [{ script: sha(runtime), functions: await measured(runtime, false) }] }],
  }, expected, provenance);

  const unique = await ledgerFor(`(${source.replaceAll("\n", "")})`);
  const ambiguous = await ledgerFor('(function run(value) {if (value) return "yes";return "no";function unused(value) {if (value) return "yes";}})');

  assert.deepEqual(unique.obligations.map((row) => [row.outcome, row.status]), [["true", "pending"], ["false", "observed"]]);
  assert.ok(unique.obligations[1].evidence[0].lowerBound > 0);
  assert.deepEqual(ambiguous.obligations.map((row) => [row.outcome, row.status, row.evidence.length]), [["true", "pending", 0], ["false", "pending", 0]]);
});

async function esbuildLedger({ canonicalUndefined = "void 0", namespaceLoader = "jitiImport" } = {}) {
  const { createHash } = await import("node:crypto");
  const { mkdir, mkdtemp, writeFile, rm } = await import("node:fs/promises");
  const { resolve, join } = await import("node:path");
  const { Session } = await import("node:inspector");
  const { Script } = await import("node:vm");
  const { transformSync } = await import("esbuild");
  const { buildBranchLedger } = await import("./branch-ledger.mjs");
  const sha = (value) => createHash("sha256").update(value).digest("hex");
  const source = `import { ready } from "./guard";
export function run(value, other) {
  const fallback = value ? 1 : undefined;
  const chain = value || other || fallback;
  const dropped = value || (other && fallback);
  if (ready(value)) return value?.size ?? chain;
  try {
    return dropped.size.value;
  } catch (error) {
    return error;
  }
}
`;
  const built = transformSync(source, { loader: "ts", format: "esm", sourcemap: "external", sourcefile: "../src/query.ts" });
  const dist = built.code.replace("void 0", canonicalUndefined);
  const body = dist.slice(dist.indexOf("\n") + 1, dist.indexOf("export {")).replace("ready(value)", "(0, _guard.ready)(value)");
  const runtime = `(async function (${namespaceLoader}) {var _guard = await ${namespaceLoader}("./guard");\n${body} return run; })`;
  const session = new Session();
  const post = (method, params = {}) => new Promise((resolve, reject) => session.post(method, params, (error, result) => error ? reject(error) : resolve(result)));
  session.connect();
  let functions;
  try {
    await post("Profiler.enable");
    await post("Profiler.startPreciseCoverage", { callCount: true, detailed: true });
    const filename = `dist-anchor-synthetic-${measuredScript++}.js`;
    const run = await new Script(runtime, { filename }).runInThisContext()(async () => ({ ready: (value) => value !== 0 }));
    run(0, 0); run(new Map(), 0); run(0, 1);
    functions = (await post("Profiler.takePreciseCoverage")).result.find((item) => item.url === filename).functions;
  } finally {
    await post("Profiler.stopPreciseCoverage");
    session.disconnect();
  }
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "dist-anchor-test-"));
  try {
    const backend = join(project, "node_modules/@narumitw/pi-usage");
    await mkdir(join(backend, "src"), { recursive: true });
    await mkdir(join(backend, "dist"));
    const files = { "src/query.ts": source, "dist/index.ts": dist, "dist/index.ts.map": built.map };
    for (const [path, text] of Object.entries(files)) await writeFile(join(backend, path), text);
    const provenance = { dependencies: { "node_modules/@narumitw/pi-usage": { installed: { files: Object.fromEntries(Object.entries(files).map(([path, text]) => [path, { sha256: sha(text) }])) } } } };
    const inventory = { symbols: [], semanticFixtures: [{ id: "lifecycle:synthetic", evidence: ["expected.json#/lifecycle/0"], sourceBranchEvidence: "branch-ledger.json#/fixtureMappings/0", inputs: {} }] };
    const observations = { scripts: [{ path: "dist/index.ts", text: runtime, sha256: sha(runtime) }], observations: [{ id: "lifecycle:synthetic", blocks: [{ script: sha(runtime), functions }] }] };
    const ledger = await buildBranchLedger(project, project, inventory, observations, { lifecycle: [{ id: "lifecycle:synthetic" }], requests: [] }, provenance);
    return ledger.obligations.map((row) => [row.line, row.input, row.outcome, row.status]);
  } finally { await rm(project, { recursive: true, force: true }); }
}

const esbuildOutcomes = [
  [3, "value", "true", "observed"], [3, "value", "false", "observed"],
  [4, "value || other || fallback", "rhs-evaluated", "observed"], [4, "value || other || fallback", "rhs-skipped", "observed"],
  [4, "value || other", "rhs-evaluated", "observed"], [4, "value || other", "rhs-skipped", "observed"],
  [5, "value || (other && fallback)", "rhs-evaluated", "observed"], [5, "value || (other && fallback)", "rhs-skipped", "observed"],
  [5, "other && fallback", "rhs-evaluated", "observed"], [5, "other && fallback", "rhs-skipped", "pending"],
  [6, "ready(value)", "true", "observed"], [6, "ready(value)", "false", "observed"],
  [6, "value?.size ?? chain", "rhs-evaluated", "pending"], [6, "value?.size ?? chain", "rhs-skipped", "pending"],
  [6, "value?.size", "non-nullish", "observed"], [6, "value?.size", "nullish", "pending"],
  [7, "TryStatement", "try-completed", "pending"], [7, "TryStatement", "try-threw", "observed"],
  [9, "exception", "entered", "observed"],
];

test("dist ledger follows esbuild canonical arms, shared-start chains, unmapped catch keywords and Jiti import bindings", async () => {
  assert.deepEqual(await esbuildLedger(), esbuildOutcomes);
});

test("dist ledger fails closed when a canonical arm changes kind or a namespace is not a Jiti import", async () => {
  const swap = (changed) => esbuildOutcomes.map((row) => changed.some(([line, outcome]) => row[0] === line && row[2] === outcome) ? [...row.slice(0, 3), "pending"] : row);
  assert.deepEqual(await esbuildLedger({ canonicalUndefined: "(null)" }), swap([[3, "false"]]));
  assert.deepEqual(await esbuildLedger({ namespaceLoader: "loadModule" }), swap([[6, "true"], [6, "false"]]));
});
