/** Soundness controls for the exact-counter replay: real executions of instrumented synthetic modules must count every outcome exactly, never count an outcome that did not run, and behave like the uninstrumented module; erasure and mapping must reject structural tampering, and mutated counting helpers must fail the controls. */
import assert from "node:assert/strict";
import test from "node:test";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { Session } from "node:inspector";
import { Script } from "node:vm";
import { createHash } from "node:crypto";
import { branchLedgerIdentity, buildBranchLedger, evidenceAt } from "./branch-ledger.mjs";
import { counterStoreSymbol, instrumentSources, verifyCounterCopy } from "./counter-replay.mjs";

const scratch = resolve(".pb/probes/7");
const sha = (text) => createHash("sha256").update(text).digest("hex");
const FILE = "src/query.ts";
let moduleId = 0;
const instrumentations = new Map();

async function load(root, text) {
  const path = join(root, `module-${moduleId++}.mjs`);
  await writeFile(path, text);
  return import(pathToFileURL(path).href);
}

async function runCase(t, source, calls, transform = (text) => text) {
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, "counter-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  if (!instrumentations.has(source)) instrumentations.set(source, await instrumentSources({ [FILE]: source }, root));
  const { manifest, instrumented } = instrumentations.get(source);
  const original = await load(root, source);
  delete globalThis[Symbol.for(counterStoreSymbol)];
  const counted = await load(root, transform(instrumented[FILE]));
  const settle = async (run, input) => {
    try { return { value: await run(input) }; } catch (error) { return { error: error instanceof Error ? `${error.name}: ${error.message}` : error }; }
  };
  for (const input of calls) assert.deepEqual(await settle(counted.run, input()), await settle(original.run, input()), "Instrumented behaviour differs");
  const counts = globalThis[Symbol.for(counterStoreSymbol)]?.counts ?? new Float64Array(manifest.counters.length);
  delete globalThis[Symbol.for(counterStoreSymbol)];
  return { manifest, instrumented, counts: [...counts] };
}

const thrower = () => { throw new Error("fixture-throw"); };
const cases = [
  ["if/else-if chain counts each decision; a throwing condition counts neither outcome",
    "export function run(x) { if (x.a > 1) return \"a\"; else if (x.a > 0) return \"b\"; return \"c\"; }",
    [() => ({ a: 2 }), () => ({ a: 1 }), () => ({ a: 0 }), () => null],
    [["if (x.a > 1)", "IfStatement", "true", 1], ["if (x.a > 1)", "IfStatement", "false", 2], ["if (x.a > 0)", "IfStatement", "true", 1], ["if (x.a > 0)", "IfStatement", "false", 1]]],
  ["conditional counts only the evaluated arm",
    "export function run(x) { return x.flag ? x.yes() : x.no(); }",
    [() => ({ flag: 1, yes: () => 1 }), () => ({ flag: 0, no: () => 2 }), () => ({ flag: 0, no: () => 3 })],
    [["x.flag ?", "ConditionalExpression", "true", 1], ["x.flag ?", "ConditionalExpression", "false", 2]]],
  ["&&, || and ?? skip exactly when their operand decides; falsy non-nullish values do not skip ??",
    "export function run(x) { return [x.a && x.b, x.a || x.b, x.a ?? x.b]; }",
    [() => ({ a: 0, b: 1 }), () => ({ a: "", b: 1 }), () => ({ a: null, b: 1 }), () => ({ a: 2, b: 1 })],
    [["x.a && x.b", "BinaryExpression", "rhs-evaluated", 1], ["x.a && x.b", "BinaryExpression", "rhs-skipped", 3], ["x.a || x.b", "BinaryExpression", "rhs-evaluated", 3],
      ["x.a || x.b", "BinaryExpression", "rhs-skipped", 1], ["x.a ?? x.b", "BinaryExpression", "rhs-evaluated", 1], ["x.a ?? x.b", "BinaryExpression", "rhs-skipped", 3]]],
  ["a throwing left operand counts neither logical outcome",
    "export function run(x) { const v = x.a.b || x.c; return v; }",
    [() => ({}), () => ({ a: { b: 0 }, c: 1 })],
    [["x.a.b || x.c", "BinaryExpression", "rhs-evaluated", 1], ["x.a.b || x.c", "BinaryExpression", "rhs-skipped", 0]]],
  ["a short-circuited later optional link counts neither of its outcomes",
    "export function run(x) { return x?.a?.b; }",
    [() => null, () => ({ a: null }), () => ({ a: { b: 1 } }), () => undefined],
    [["x?.a?.b", "PropertyAccessExpression", "non-nullish", 2, 0], ["x?.a?.b", "PropertyAccessExpression", "nullish", 2, 0],
      ["x?.a?.b", "PropertyAccessExpression", "non-nullish", 1, 1], ["x?.a?.b", "PropertyAccessExpression", "nullish", 1, 1]]],
  ["an optional method call keeps its receiver, reads the property once and evaluates no argument when absent",
    "export function run(x) { return x.target.m?.(x.arg()); }",
    [() => { let reads = 0; return { target: { k: 5, get m() { reads += 1; return reads === 1 ? function m(v) { return this.k + v; } : undefined; } }, arg: () => 1 }; },
      () => ({ target: {}, arg: thrower }), () => ({ target: null, arg: thrower })],
    [["x.target.m?.", "CallExpression", "non-nullish", 1], ["x.target.m?.", "CallExpression", "nullish", 1]]],
  ["an optional property link followed by an optional method call",
    "export function run(x) { return x?.m?.(); }",
    [() => null, () => ({}), () => ({ k: 3, m() { return this.k; } })],
    [["x?.m?.()", "PropertyAccessExpression", "non-nullish", 2], ["x?.m?.()", "PropertyAccessExpression", "nullish", 1], ["x?.m?.()", "CallExpression", "non-nullish", 1], ["x?.m?.()", "CallExpression", "nullish", 1]]],
  ["optional element and plain optional call",
    "export function run(x) { return [x.list?.[x.i], x.fn?.(2)]; }",
    [() => ({ list: [7], i: 0, fn: (v) => v * 2 }), () => ({ i: thrower })],
    [["x.list?.", "ElementAccessExpression", "non-nullish", 1], ["x.list?.", "ElementAccessExpression", "nullish", 1], ["x.fn?.", "CallExpression", "non-nullish", 1], ["x.fn?.", "CallExpression", "nullish", 1]]],
  ["catch entry, try completion and try throw count only what ran; a return leaves the try uncompleted",
    "export function run(x) { try { if (x.ret) return 1; x.f(); } catch (error) { return 2; } return 3; }",
    [() => ({ f: () => 0 }), () => ({ f: thrower }), () => ({ ret: 1 })],
    [["try {", "TryStatement", "try-completed", 1], ["try {", "TryStatement", "try-threw", 1], ["if (x.ret)", "IfStatement", "true", 1], ["if (x.ret)", "IfStatement", "false", 2], ["catch (error)", "CatchClause", "entered", 1]]],
  ["try/finally without catch counts a throw and rethrows the same error",
    "export function run(x) { let n = 0; try { x.f(); } finally { n += 1; } return n; }",
    [() => ({ f: thrower }), () => ({ f: () => 0 })],
    [["try {", "TryStatement", "try-completed", 1], ["try {", "TryStatement", "try-threw", 1]]],
  ["for-of counts entries, zero iterations and normal exits; break, return and throw are not normal exits",
    "export function run(x) { let n = 0; for (const v of x) { if (v === \"b\") break; if (v === \"r\") return -1; if (v === \"t\") throw new Error(\"t\"); n += 1; } return n; }",
    [() => [], () => [1, 2], () => ["b"], () => ["r"], () => ["t"]],
    [["for (const v", "ForOfStatement", "body-entered", 5], ["for (const v", "ForOfStatement", "zero-iterations", 1], ["for (const v", "ForOfStatement", "normal-exit", 2],
      ["if (v === \"b\")", "IfStatement", "true", 1], ["if (v === \"b\")", "IfStatement", "false", 4], ["if (v === \"r\")", "IfStatement", "true", 1], ["if (v === \"r\")", "IfStatement", "false", 3],
      ["if (v === \"t\")", "IfStatement", "true", 1], ["if (v === \"t\")", "IfStatement", "false", 2]]],
  ["a labelled break to an outer loop ends both loops abnormally; a switch break inside a loop does not",
    "export function run(x) { let n = 0; outer: for (const a of x) { for (const b of a) { switch (b) { case 1: break; } if (b === 2) break outer; n += 1; } } return n; }",
    [() => [[1, 1]], () => [[2]]],
    [["for (const a", "ForOfStatement", "body-entered", 2], ["for (const a", "ForOfStatement", "zero-iterations", 0], ["for (const a", "ForOfStatement", "normal-exit", 1],
      ["for (const b", "ForOfStatement", "body-entered", 3], ["for (const b", "ForOfStatement", "zero-iterations", 0], ["for (const b", "ForOfStatement", "normal-exit", 1],
      ["case 1", "CaseClause", "selected", 2], ["if (b === 2)", "IfStatement", "true", 1], ["if (b === 2)", "IfStatement", "false", 2]]],
  ["while, for and do loops count exits; a throwing condition counts nothing",
    "export function run(x) { let n = 0; while (x.more(n)) n += 1; for (let i = 0; i < x.k; i += 1) n += 1; do { n += 1; } while (n < 0); return n; }",
    [() => ({ more: (n) => n < 2, k: 0 }), () => ({ more: thrower }), () => ({ more: () => false, k: 1 })],
    [["while (x.more", "WhileStatement", "body-entered", 2], ["while (x.more", "WhileStatement", "zero-iterations", 1], ["while (x.more", "WhileStatement", "normal-exit", 2],
      ["for (let i", "ForStatement", "body-entered", 1], ["for (let i", "ForStatement", "zero-iterations", 1], ["for (let i", "ForStatement", "normal-exit", 2],
      ["do {", "DoStatement", "body-entered", 2], ["do {", "DoStatement", "normal-exit", 2]]],
  ["case selection counts only the matching clause, including an empty fallthrough clause",
    "export function run(x) { switch (x) { case 1: case 2: return \"a\"; case 3: return \"b\"; } return \"none\"; }",
    [() => 2, () => 3, () => 5],
    [["case 1", "CaseClause", "selected", 0], ["case 2", "CaseClause", "selected", 1], ["case 3", "CaseClause", "selected", 1]]],
  ["async loops and try outcomes count across awaits",
    "export async function run(x) { let n = 0; for (const v of x) { try { n += await v(); } catch (error) { n -= 1; } } return n; }",
    [() => [async () => 1, async () => { throw new Error("e"); }], () => []],
    [["for (const v", "ForOfStatement", "body-entered", 2], ["for (const v", "ForOfStatement", "zero-iterations", 1], ["for (const v", "ForOfStatement", "normal-exit", 2],
      ["try {", "TryStatement", "try-completed", 1], ["try {", "TryStatement", "try-threw", 1], ["catch (error)", "CatchClause", "entered", 1]]],
];

function actualCounts(source, result) {
  return result.manifest.counters.map((id, index) => {
    const [, start, kind, , outcomeName] = id.split(":");
    return [Number(start), kind, outcomeName, result.counts[index]];
  }).sort((a, b) => a[0] - b[0] || a[1].localeCompare(b[1]) || a[2].localeCompare(b[2]));
}

function expectedCounts(source, rows) {
  return rows.map(([marker, kind, outcomeName, count, nested = 0]) => {
    const start = source.indexOf(marker);
    assert.ok(start >= 0 && source.indexOf(marker, start + 1) < 0, `Ambiguous marker: ${marker}`);
    return [start, kind, outcomeName, count, nested];
  });
}

function sameCounts(source, result, rows) {
  const actual = actualCounts(source, result);
  const expected = expectedCounts(source, rows);
  const groups = new Map();
  for (const row of actual) groups.set(row.slice(0, 3).join("|"), [...groups.get(row.slice(0, 3).join("|")) ?? [], row[3]]);
  const wanted = new Map();
  for (const [start, kind, outcomeName, count, nested] of expected) {
    const key = [start, kind, outcomeName].join("|");
    const list = wanted.get(key) ?? [];
    list[nested] = count;
    wanted.set(key, list);
  }
  return [Object.fromEntries([...groups].sort()), Object.fromEntries([...wanted].sort())];
}

for (const [name, source, calls, rows] of cases) {
  test(`exact counters: ${name}`, async (t) => {
    const result = await runCase(t, source, calls);
    const [actual, expected] = sameCounts(source, result, rows);
    assert.deepEqual(actual, expected);
  });
}

const mutants = [
  ["condition outcomes swapped", (text) => text.replace("function __ucB(value, whenTrue, whenFalse) { __ucHit(value ? whenTrue : whenFalse)", "function __ucB(value, whenTrue, whenFalse) { __ucHit(value ? whenFalse : whenTrue)")],
  ["?? treated like ||", (text) => text.replace("value !== null && value !== undefined) __ucHit(skipped)", "Boolean(value)) __ucHit(skipped)")],
  ["break flag ignored", (text) => text.replace("function __ucExit(entered, broke, normal, zero) { if (broke) return;", "function __ucExit(entered, broke, normal, zero) {")],
  ["absent link counted as present", (text) => text.replace("{ __ucHit(absent); return undefined; } __ucHit(present); return rest(value); }", "{ __ucHit(present); return undefined; } __ucHit(present); return rest(value); }")],
  ["case counted on every comparison", (text) => text.replace("if (value === discriminant) __ucHit(index)", "__ucHit(index)")],
  ["try completion counted on entry", (text) => text.replace("function __ucDone(index) { __ucHit(index); }", "function __ucDone(index) { __ucHit(index); __ucHit(index); }")],
];

test("every mutated counting helper fails at least one exact-counter control", async (t) => {
  for (const [name, mutate] of mutants) {
    let caught = 0;
    for (const [, source, calls, rows] of cases) {
      const result = await runCase(t, source, calls, (text) => {
        const mutated = mutate(text);
        assert.notEqual(mutated, text, `Mutant ${name} did not apply`);
        return mutated;
      });
      const [actual, expected] = sameCounts(source, result, rows);
      try { assert.deepEqual(actual, expected); } catch { caught += 1; }
    }
    assert.ok(caught > 0, `Mutant survived: ${name}`);
  }
});

const tampering = [
  ["swapped condition indices", (text) => text.replace(/__ucB\(([^,]+), (\d+), (\d+)\)/u, (_, value, first, second) => `__ucB(${value}, ${second}, ${first})`), /does not map to exactly one obligation|Counter map differs/u],
  ["a removed break flag", (text) => text.replace(/\{ (__ucK\d+) = 1; break; \}/u, "break;"), /does not set its flag/u],
  ["a counter moved onto another operand", (text) => text.replace("__ucL(x.a, 0,", "__ucL(x.a, 1,"), /Logical counter outside its operand/u],
  ["a stray counter statement", (text) => text.replace("let n = 0;", "let n = 0;__ucDone(0);"), /unexpected position|carries trivia/u],
  ["a changed source character", (text) => text.replace("n += 1", "n += 2"), /Erasure does not reproduce|Changed counter copy/u],
];

test("erasure and mapping reject structurally tampered counter copies", async (t) => {
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, "counter-tamper-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const source = "export function run(x) { let n = 0; for (const v of x.list) { if (v) break; n += 1; } return [x.a && x.b, n]; }";
  const { manifest, instrumented } = await instrumentSources({ [FILE]: source }, root);
  await verifyCounterCopy({ [FILE]: source }, instrumented, manifest, root);
  for (const [name, tamper, error] of tampering) {
    const changed = tamper(instrumented[FILE]);
    assert.notEqual(changed, instrumented[FILE], `Tamper ${name} did not apply`);
    const recorded = { ...manifest, instrumentedFiles: { [FILE]: sha(changed) } };
    await assert.rejects(verifyCounterCopy({ [FILE]: source }, { [FILE]: changed }, name === "a changed source character" ? manifest : recorded, root), error, name);
  }
  await assert.rejects(verifyCounterCopy({ [FILE]: source }, instrumented, { ...manifest, counters: manifest.counters.toReversed() }, root), /Changed counter manifest/u);
});

async function measuredCalls(text, calls) {
  const filename = `counter-ledger-${moduleId++}.js`;
  const session = new Session();
  const post = (method, params = {}) => new Promise((done, fail) => session.post(method, params, (error, result) => error ? fail(error) : done(result)));
  session.connect();
  try {
    await post("Profiler.enable");
    await post("Profiler.startPreciseCoverage", { callCount: true, detailed: true });
    const run = new Script(text, { filename }).runInThisContext();
    for (const value of calls) run(value);
    const { result } = await post("Profiler.takePreciseCoverage");
    return result.find((item) => item.url === filename).functions;
  } finally {
    await post("Profiler.stopPreciseCoverage");
    session.disconnect();
  }
}

async function counterLedger(t, source, calls, adjust = () => {}) {
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "counter-ledger-test-"));
  t.after(() => rm(project, { recursive: true, force: true }));
  await mkdir(join(project, "node_modules/@narumitw/pi-usage/src"), { recursive: true });
  await writeFile(join(project, "node_modules/@narumitw/pi-usage", FILE), source);
  const provenance = { dependencies: { "node_modules/@narumitw/pi-usage": { installed: { files: { [FILE]: { sha256: sha(source) } } } } } };
  const runtime = `(${source})`;
  const { manifest, instrumented } = await instrumentSources({ [FILE]: source }, project);
  delete globalThis[Symbol.for(counterStoreSymbol)];
  const counted = new Script(`${instrumented[FILE]}\nrun;`).runInThisContext();
  for (const value of calls) counted(value);
  const counts = [...globalThis[Symbol.for(counterStoreSymbol)].counts].flatMap((count, index) => count > 0 ? [[index, count]] : []);
  delete globalThis[Symbol.for(counterStoreSymbol)];
  const observations = { scripts: [{ path: FILE, text: runtime, sha256: sha(runtime) }], counterManifest: manifest,
    observations: [{ id: "synthetic", blocks: [{ script: sha(runtime), functions: await measuredCalls(runtime, calls) }], counters: { composition: "counter-replay", counts } }] };
  adjust(observations);
  const fixtures = [{ id: "synthetic", inputs: { calls: calls.length }, evidence: ["expected.json#/results/0", "expected.json#/results/1"], sourceBranchEvidence: "branch-ledger.json#/fixtureMappings/0" }];
  const expected = { results: [{ id: "synthetic", implementation: "source" }, { id: "synthetic", implementation: "dist" }], requests: [] };
  const ledger = await buildBranchLedger(project, project, { symbols: [], semanticFixtures: fixtures }, observations, expected, provenance);
  return { ledger, observations, manifest };
}

const row = (ledger, kind, input, outcomeName) => {
  const rows = ledger.obligations.filter((item) => item.kind === kind && item.input === input && item.outcome === outcomeName);
  assert.equal(rows.length, 1);
  return rows[0];
};
const RETURN_NULLISH = "function run(x) { return x.a ?? 1; }";

test("counter witnesses observe only armless outcomes that V8 does not witness, tagged and tied to their retained count", async (t) => {
  const { ledger, observations } = await counterLedger(t, RETURN_NULLISH, [{ a: 2 }, { a: 3 }]);
  const skipped = row(ledger, "BinaryExpression", "x.a ?? 1", "rhs-skipped");
  const evaluated = row(ledger, "BinaryExpression", "x.a ?? 1", "rhs-evaluated");
  assert.deepEqual([skipped.status, skipped.evidence.map((item) => [item.composition, item.implementation, item.count, evidenceAt({ "branch-observations.json": observations }, item.counterRange)[1]])],
    ["observed", [["counter-replay", "source", 2, 2]]]);
  assert.deepEqual([evaluated.status, evaluated.evidence], ["pending", []]);
  assert.match(evaluated.pendingReason, /^No positive uniquely source-matched arm-entry witness/u);
  assert.deepEqual(ledger.fixtureMappings[0].sourceBranchOutcomes, [skipped.id]);
  const identity = branchLedgerIdentity(ledger, observations);
  assert.equal(identity.obligations.find((item) => item.id === skipped.id).evidence[0].counterRange, undefined);
  const changed = structuredClone(ledger);
  changed.obligations.find((item) => item.id === skipped.id).evidence[0].count = 3;
  assert.throws(() => branchLedgerIdentity(changed, observations), /Changed counter witness/u);
});

test("an outcome the counter replay never counted names the zero count as its pending reason", async (t) => {
  const { ledger } = await counterLedger(t, RETURN_NULLISH, [{ a: null }]);
  const skipped = row(ledger, "BinaryExpression", "x.a ?? 1", "rhs-skipped");
  assert.equal(skipped.status, "pending");
  assert.match(skipped.pendingReason, /exact-counter replay counted this outcome zero times/u);
});

test("a positive counter on an armed outcome does not replace the V8 arm witness", async (t) => {
  const { ledger } = await counterLedger(t, RETURN_NULLISH, [{ a: 2 }], (observations) => {
    const index = observations.counterManifest.counters.findIndex((id) => id.endsWith(":rhs-evaluated"));
    observations.observations[0].counters.counts = [...observations.observations[0].counters.counts, [index, 1]].sort((a, b) => a[0] - b[0]);
  });
  assert.deepEqual([row(ledger, "BinaryExpression", "x.a ?? 1", "rhs-evaluated").status, row(ledger, "BinaryExpression", "x.a ?? 1", "rhs-evaluated").evidence], ["pending", []]);
});

test("the ledger fails closed on untagged counter windows, foreign manifests and counters that V8 contradicts", async (t) => {
  await assert.rejects(counterLedger(t, RETURN_NULLISH, [{ a: 2 }], (observations) => { observations.observations[0].counters.composition = "source-replay"; }), /Invalid counter window/u);
  await assert.rejects(counterLedger(t, RETURN_NULLISH, [{ a: 2 }], (observations) => { observations.counterManifest = { ...observations.counterManifest, counters: observations.counterManifest.counters.toReversed() }; }),
    /Counter manifest differs from the instrumentation of the pinned source/u);
  await assert.rejects(counterLedger(t, RETURN_NULLISH, [{ a: 2 }], (observations) => { delete observations.counterManifest; }), /Counter window without a counter manifest/u);
  await assert.rejects(counterLedger(t, "function run(x) { if (x) return 1; return 2; }", [1], (observations) => { observations.observations[0].counters.counts = []; }),
    /Counter replay misses a V8-witnessed outcome/u);
});
