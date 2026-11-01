/** Integrity and real-V8 negative controls for the source-to-fixture ledger: disposable synthetic modules, the committed artifacts and the ledger check command. */
import assert from "node:assert/strict";
import test from "node:test";
import { Session } from "node:inspector";
import { Script } from "node:vm";
import { createHash } from "node:crypto";
import { cp, mkdir, mkdtemp, readFile, writeFile, rm, symlink } from "node:fs/promises";
import { execFile } from "node:child_process";
import { resolve, join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import * as branchLedgerModule from "./branch-ledger.mjs";
import { readLedger, writeLedger } from "./ledger-artifact.mjs";

const { armWitness, buildBranchLedger, branchEvidenceContract, branchLedgerIdentity, evidenceAt } = branchLedgerModule;

const sha = (text) => createHash("sha256").update(text).digest("hex");
let measurementId = 0;
async function measured(text, calls) {
  const filename = `ledger-synthetic-${measurementId++}.js`;
  const session = new Session();
  const post = (method, params = {}) => new Promise((resolve, reject) => session.post(method, params, (error, result) => error ? reject(error) : resolve(result)));
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


async function contract(text, functions) {
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, "branch-contract-test-"));
  try {
    return await branchEvidenceContract({ scripts: [{ path: "src/query.ts", text, sha256: sha(text) }],
      observations: [{ id: "synthetic", blocks: [{ script: sha(text), functions }] }] }, root);
  } finally { await rm(root, { recursive: true, force: true }); }
}

test("armWitness respects real V8 early exits, nested function counts and conditional arms", async () => {
  const text = `(function run(value) {
  if (value === 0) return "early";
  if (value === 1) { const unused = () => "never-called"; return "one"; }
  return value > 1 ? "large" : "small";
})`;
  const functions = await measured(text, [1, 2]);
  const model = (await contract(text, functions)).scripts[0];
  for (const marker of ['return "one"', '"large"']) {
    const start = text.indexOf(marker);
    assert.ok(armWitness(functions, start, start + 1, model), marker);
  }
  for (const marker of ['return "early"', '"never-called"', '"small"']) {
    const start = text.indexOf(marker);
    assert.equal(armWitness(functions, start, start + 1, model), undefined, marker);
  }
});

test("buildBranchLedger keeps every syntactic outcome pending without execution evidence", async () => {
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "ledger-census-test-"));
  try {
    const backend = join(project, "node_modules/@narumitw/pi-usage/src");
    await mkdir(backend, { recursive: true });
    const source = `function run(value) {
  if (value) value = value ? 1 : 2;
  value && value.a && value.b; value || value.b; value ?? value.c; value?.d?.e;
  for (const item of value) { value = item; }
  while (value) { break; }
  do { value = 0; } while (value);
  try { value(); } catch (error) { value = error; }
  switch (value) { case 0: break; default: break; }
}`;
    await writeFile(join(backend, "query.ts"), source);
    const provenance = { dependencies: { "node_modules/@narumitw/pi-usage": { installed: { files: { "src/query.ts": { sha256: sha(source) } } } } } };
    const ledger = await buildBranchLedger(project, project, { symbols: [], semanticFixtures: [] }, { scripts: [], observations: [] }, { results: [], requests: [] }, provenance);
    const outcomes = Object.groupBy(ledger.obligations, (item) => item.kind);
    assert.deepEqual(Object.fromEntries(Object.entries(outcomes).map(([kind, rows]) => [kind, rows.map((row) => row.outcome)])), {
      IfStatement: ["true", "false"], ConditionalExpression: ["true", "false"],
      BinaryExpression: ["rhs-evaluated", "rhs-skipped", "rhs-evaluated", "rhs-skipped", "rhs-evaluated", "rhs-skipped", "rhs-evaluated", "rhs-skipped"],
      PropertyAccessExpression: ["non-nullish", "nullish", "non-nullish", "nullish"], ForOfStatement: ["body-entered", "zero-iterations", "normal-exit"],
      WhileStatement: ["body-entered", "zero-iterations", "normal-exit"], DoStatement: ["body-entered", "normal-exit"],
      TryStatement: ["try-completed", "try-threw"], CatchClause: ["entered"], CaseClause: ["selected"], DefaultClause: ["selected"],
    });
    assert.ok(ledger.obligations.every((item) => item.status === "pending" && item.evidence.length === 0));
    assert.deepEqual(ledger.remainingGroups.flatMap((item) => item.obligations).sort(), ledger.obligations.map((item) => item.id).sort());
  } finally { await rm(project, { recursive: true, force: true }); }
});

test("buildBranchLedger ties exact arms to fixture/output identities and rejects changed or dangling evidence", async () => {
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "ledger-test-"));
  try {
    const backend = join(project, "node_modules/@narumitw/pi-usage/src");
    await mkdir(backend, { recursive: true });
    const source = `function run(value) {
  if (value === 0) return "early";
  value && value.toString && value.toString();
  return value > 1 ? "large" : "small";
}`;
    const runtime = `(() => 0)();(${source})`;
    await writeFile(join(backend, "query.ts"), source);
    const functions = await measured(runtime, [2]);
    const provenance = { dependencies: { "node_modules/@narumitw/pi-usage": { installed: { files: { "src/query.ts": { sha256: sha(source) } } } } } };
    await mkdir(join(project, "status"));
    await writeFile(join(project, "status/usage.ts"), "");
    provenance.packageInputs = { "status/usage.ts": sha("") };
    const inventory = { symbols: [], semanticFixtures: [{ id: "synthetic", sourceBranchEvidence: "branch-ledger.json#/fixtureMappings/0", inputs: { value: 2 }, evidence: ["expected.json#/results/0", "expected.json#/results/1"] }] };
    const expected = { results: [{ implementation: "source", id: "synthetic", value: "large" }, { implementation: "dist", id: "synthetic", value: "large" }], requests: [] };
    const observations = { scripts: [{ path: "src/query.ts", text: runtime, sha256: sha(runtime) }], observations: [{ id: "synthetic", blocks: [{ script: sha(runtime), functions }] }] };
    const build = () => buildBranchLedger(project, project, inventory, observations, expected, provenance);
    const ledger = await build();
    const identity = branchLedgerIdentity(ledger, observations);
    assert.ok(identity.obligations.some((item) => item.evidence.length));
    const incidental = functions.find((fn) => fn.ranges[0].startOffset === 1);
    assert.ok(incidental && incidental.ranges.length === 1);
    observations.observations[0].blocks[0].functions = functions.filter((fn) => fn !== incidental);
    const shifted = await build();
    assert.deepEqual(branchLedgerIdentity(shifted, observations), identity);
    const armEvidence = (result) => result.obligations.find((item) => item.evidence.some((entry) => entry.v8Range)).evidence[0];
    assert.notEqual(armEvidence(shifted).v8Range, armEvidence(ledger).v8Range);
    observations.observations[0].blocks[0].functions = functions;
    const badPointer = structuredClone(ledger);
    const evidence = armEvidence(badPointer);
    evidence.v8Range = evidence.v8Range.replace(/ranges\/\d+$/u, "ranges/999");
    assert.throws(() => branchLedgerIdentity(badPointer, observations), /Dangling evidence/);
    assert.equal(ledger.sourceFiles["status/usage.ts"], sha(""));
    await writeFile(join(project, "status/usage.ts"), " ");
    await assert.rejects(build(), /Changed source identity: status\/usage.ts/);
    await writeFile(join(project, "status/usage.ts"), "");
    const conditional = ledger.obligations.filter((item) => item.kind === "ConditionalExpression");
    const idsWith = (status) => ledger.obligations.filter((item) => item.status === status).map((item) => item.id).sort();
    assert.deepEqual(ledger.obligations.map((item) => [item.kind, item.input, item.outcome, item.status]), [
      ["IfStatement", "value === 0", "true", "pending"], ["IfStatement", "value === 0", "false", "observed"],
      ["BinaryExpression", "value && value.toString && value.toString()", "rhs-evaluated", "observed"], ["BinaryExpression", "value && value.toString && value.toString()", "rhs-skipped", "pending"],
      ["BinaryExpression", "value && value.toString", "rhs-evaluated", "observed"], ["BinaryExpression", "value && value.toString", "rhs-skipped", "pending"],
      ["ConditionalExpression", "value > 1", "true", "observed"], ["ConditionalExpression", "value > 1", "false", "pending"],
    ]);
    assert.deepEqual([...ledger.fixtureMappings[0].sourceBranchOutcomes].sort(), idsWith("observed"));
    assert.deepEqual(ledger.remainingGroups.flatMap((item) => item.obligations).sort(), idsWith("pending"));
    assert.deepEqual(conditional[0].evidence[0].output, ["expected.json#/results/0", "expected.json#/results/1"]);
    inventory.semanticFixtures[0].sourceBranchEvidence = "branch-ledger.json#/fixtureMappings/99";
    await assert.rejects(build(), /Dangling semantic fixture\/branch link/);
    inventory.semanticFixtures[0].sourceBranchEvidence = "branch-ledger.json#/fixtureMappings/0";
    inventory.semanticFixtures[0].evidence = ["expected.json#/results/99"];
    await assert.rejects(build(), /Dangling evidence/);
    inventory.semanticFixtures[0].evidence = ["expected.json#/results/1", "expected.json#/results/0"];
    await assert.rejects(build(), /Invented fixture\/output association/);
    inventory.semanticFixtures[0].evidence = ["expected.json#/results/0", "expected.json#/results/1"];
    observations.observations[0].id = "invented";
    await assert.rejects(build(), /Missing or invented fixture window/);
    observations.observations[0].id = "synthetic";
    observations.observations[0].blocks[0].script = "invented";
    await assert.rejects(build(), /Dangling script/);
    observations.observations[0].blocks[0].script = sha(runtime);
    observations.scripts[0].text += " ";
    await assert.rejects(build(), /Changed runtime identity/);
    observations.scripts[0].text = runtime;
    await writeFile(join(backend, "query.ts"), source + " ");
    await assert.rejects(build(), /Changed source identity/);
  } finally { await rm(project, { recursive: true, force: true }); }
});

test("branch evidence excludes only uniquely matched branchless non-arms and retains every carrier fact", async () => {
  const text = `(function run(value) {
    const leaf = () => "leaf";
    const unused = (flag) => { if (flag) return "nested"; return "other"; };
    if (value) return leaf();
    return value ? () => 1 : () => 2;
  })`;
  const functions = await measured(text, [true, false]);
  const original = await contract(text, functions);
  const model = original.scripts[0];
  const records = original.observations[0].blocks[0].functions;
  const leaf = functions.find((fn) => fn.functionName === "leaf");
  assert.ok(leaf && leaf.ranges.length === 1);
  assert.deepEqual(await contract(text, functions.filter((fn) => fn !== leaf)), original);
  assert.ok(!records.includes(leaf));
  const unused = functions.find((fn) => fn.functionName === "unused");
  assert.ok(unused && unused.ranges[0].count === 0);
  const nested = text.indexOf('return "nested"');
  const hit = text.indexOf('return leaf()');
  assert.ok(armWitness(functions, hit, hit + 1, model));
  assert.equal(armWitness(functions.filter((fn) => fn !== unused), nested, nested + 1, model), undefined);
  const arrows = model.boundaries.filter((boundary) => boundary.kind === "ArrowFunction" && text.slice(boundary.start).startsWith("() => 1"));
  assert.equal(arrows.length, 1);
  assert.equal(arrows[0].exclusionOnly, false);
  const arm = functions.find((fn) => fn.ranges[0].startOffset === arrows[0].start);
  assert.ok(arm && records.includes(arm));
  for (const removed of [unused, arm, functions.find((fn) => fn.functionName === "run")]) {
    assert.notDeepEqual(await contract(text, functions.filter((fn) => fn !== removed)), original);
  }
  for (const mutate of [
    (fn) => { fn.ranges[0].count = 0; },
    (fn) => { fn.ranges[0].startOffset += 1; },
    (fn) => { fn.isBlockCoverage = false; },
  ]) {
    const changed = structuredClone(functions);
    mutate(changed.find((fn) => fn.functionName === "run"));
    assert.notDeepEqual(await contract(text, changed), original);
    assert.equal(armWitness(changed, hit, hit + 1, model), undefined);
  }
  for (const mutate of [
    (fn) => { fn.ranges[0].startOffset += 1; },
    (fn) => { fn.ranges.push({ ...fn.ranges[0], count: 0 }); },
  ]) {
    const changed = structuredClone(functions);
    mutate(changed.find((fn) => fn.functionName === "leaf"));
    const result = await contract(text, changed);
    assert.equal(result.observations[0].blocks[0].functions.some((fn) => fn.functionName === "leaf"), true);
  }
  const branched = text.replace('() => "leaf"', '(flag) => flag ? "leaf" : "miss"');
  const branchedFunctions = await measured(branched, [true]);
  const branchedContract = await contract(branched, branchedFunctions);
  assert.ok(branchedContract.observations[0].blocks[0].functions.some((fn) => fn.functionName === "leaf"));
  assert.notDeepEqual(await contract(branched, branchedFunctions.filter((fn) => fn.functionName !== "leaf")), branchedContract);
  const factoryText = '(function run(value) { const factory = () => { const nested = (flag) => flag ? "hit" : "miss"; return nested; }; if (value) return factory(); return "miss"; })';
  const factoryFunctions = await measured(factoryText, [true]);
  const factoryContract = await contract(factoryText, factoryFunctions);
  const factory = factoryFunctions.find((fn) => fn.functionName === "factory");
  assert.equal(factory.ranges.length, 1);
  assert.deepEqual(factoryContract.observations[0].blocks[0].functions.find((fn) => fn.functionName === "factory"), factory);
  assert.notDeepEqual(await contract(factoryText, factoryFunctions.filter((fn) => fn !== factory)), factoryContract);
});

async function ledgerFixture(t, source = 'function run(value) { if (value) return "hit"; return "miss"; }') {
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "ledger-independent-test-"));
  t.after(() => rm(project, { recursive: true, force: true }));
  const backend = join(project, "node_modules/@narumitw/pi-usage/src");
  await mkdir(backend, { recursive: true });
  await writeFile(join(backend, "query.ts"), source);
  const runtime = `(${source})`;
  const provenance = { dependencies: { "node_modules/@narumitw/pi-usage": { installed: { files: { "src/query.ts": { sha256: sha(source) } } } } } };
  const inventory = { symbols: [], semanticFixtures: [{ id: "synthetic", inputs: { value: true }, evidence: ["expected.json#/results/0", "expected.json#/results/1"], sourceBranchEvidence: "branch-ledger.json#/fixtureMappings/0" }] };
  const expected = { results: [{ id: "synthetic", implementation: "source", value: "hit" }, { id: "synthetic", implementation: "dist", value: "hit" }], requests: [{ id: "source/synthetic", url: "https://fixture.invalid/usage", method: "POST", headers: { authorization: "Bearer synthetic" }, body: "input", redirect: "error", redirectedRequest: false }] };
  const observations = { scripts: [{ path: "src/query.ts", sha256: sha(runtime), text: runtime }], observations: [{ id: "synthetic", blocks: [{ script: sha(runtime), functions: await measured(runtime, [true]) }] }] };
  return { project, source, runtime, provenance, inventory, expected, observations,
    build: () => buildBranchLedger(project, project, inventory, observations, expected, provenance) };
}

for (const [name, retain] of [
  ["missing", (functions) => functions.filter((fn) => fn.functionName !== "unused")],
  ["zero-count", (functions) => functions],
]) {
  test(`buildBranchLedger keeps ${name} uncalled nested owners pending despite an executed parent`, async (t) => {
    const source = 'function run(value) { const unused = (flag) => { if (flag) return "nested"; return "other"; }; if (value === "call") unused(true); if (value) return "hit"; return "miss"; }';
    const fixture = await ledgerFixture(t, source);
    fixture.observations.observations[0].blocks[0].functions = retain(fixture.observations.observations[0].blocks[0].functions);

    const ledger = await fixture.build();
    const nested = ledger.obligations.find((row) => row.start === source.indexOf("if (flag)") && row.outcome === "true");
    const outer = ledger.obligations.find((row) => row.start === source.indexOf("if (value)") && row.outcome === "true");
    fixture.observations.observations[0].blocks[0].functions = await measured(fixture.runtime, ["call"]);
    const called = await fixture.build();
    const calledNested = called.obligations.find((row) => row.id === nested.id);

    assert.equal(nested.status, "pending");
    assert.ok(nested.evidence.length === 0);
    assert.equal(outer.status, "observed");
    assert.equal(calledNested.status, "observed");
    assert.ok(calledNested.evidence.length > 0);
  });
}

test("branch evidence compares authentic V8 branchless omission without rewriting the raw audit", async () => {
  const rawPath = new URL("./fixtures/branchless-v8.json", import.meta.url);
  const bytes = await readFile(rawPath, "utf8");
  const auditHash = sha(bytes);
  const raw = JSON.parse(bytes);
  const first = raw.windows[0];
  const omitted = raw.windows[5];
  const firstFunctions = first.raw.result.find((script) => script.url.endsWith(raw.url)).functions;
  const omittedFunctions = omitted.raw.result.find((script) => script.url.endsWith(raw.url)).functions;
  const scriptIndex = omitted.raw.result.findIndex((script) => script.url.endsWith(raw.url));
  const pointer = `raw.json#/windows/5/raw/result/${scriptIndex}/functions`;

  const completeContract = await contract(raw.text, firstFunctions);
  const omittedContract = await contract(raw.text, omittedFunctions);

  assert.equal(sha(raw.text), raw.sha256);
  assert.notDeepEqual(firstFunctions, omittedFunctions);
  assert.deepEqual(first.values, omitted.values);
  assert.deepEqual(omittedContract, completeContract);
  assert.deepEqual(evidenceAt({ "raw.json": raw }, pointer), raw.windows[5].raw.result.find((script) => script.url.endsWith(raw.url)).functions);
  assert.equal(sha(await readFile(rawPath, "utf8")), auditHash);
  assert.equal(JSON.stringify(raw), JSON.stringify(JSON.parse(bytes)));
});

for (const [name, mutate] of [
  ["semantic result", (fixture) => { fixture.expected.results[0].value = "different"; }],
  ["request body", (fixture) => { fixture.expected.requests[0].body = "changed"; }],
  ["request header", (fixture) => { fixture.expected.requests[0].headers.authorization = "Bearer changed"; }],
  ["request association", (fixture) => { fixture.expected.requests[0].id = "another-fixture"; }],
  ["fixture input", (fixture) => { fixture.inventory.semanticFixtures[0].inputs.value = false; }],
]) {
  test(`buildBranchLedger retains exact ${name} identity across normalized replays`, async (t) => {
    const fixture = await ledgerFixture(t);
    const ledger = await fixture.build();
    const identity = structuredClone(branchLedgerIdentity(ledger, fixture.observations));

    mutate(fixture);
    const changedIdentity = branchLedgerIdentity(await fixture.build(), fixture.observations);

    assert.ok(identity.obligations.some((row) => row.evidence.length > 0));
    assert.deepEqual(identity.fixtureMappings[0].requests, ["expected.json#/requests/0"]);
    assert.notDeepEqual(changedIdentity, identity);
  });
}

for (const [name, mutate] of [
  ["pending reason", (ledger) => { ledger.obligations.find((row) => row.status === "pending").pendingReason = "different pending reason"; }],
  ["pending status", (ledger) => { ledger.obligations.find((row) => row.status === "pending").status = "observed"; }],
  ["outcome association", (ledger) => { ledger.obligations.find((row) => row.evidence.length).outcome = "false"; }],
]) {
  test(`branchLedgerIdentity preserves ${name} rather than erasing it`, async (t) => {
    const fixture = await ledgerFixture(t);
    const ledger = await fixture.build();
    const identity = branchLedgerIdentity(ledger, fixture.observations);
    const altered = structuredClone(ledger);

    mutate(altered);

    assert.ok(identity.obligations.some((row) => row.evidence.length > 0));
    assert.notDeepEqual(branchLedgerIdentity(altered, fixture.observations), identity);
  });
}

test("branchLedgerIdentity rejects a witness attached to another fixture", async (t) => {
  const fixture = await ledgerFixture(t);
  const ledger = await fixture.build();
  const identity = branchLedgerIdentity(ledger, fixture.observations);
  const altered = structuredClone(ledger);

  altered.obligations.find((row) => row.evidence.length).evidence[0].fixtureId = "different";

  assert.ok(identity.obligations.some((row) => row.evidence.length > 0));
  assert.throws(() => branchLedgerIdentity(altered, fixture.observations), /Changed witness fixture/u);
});

test("branchLedgerIdentity authenticates retained positive raw pointers instead of trusting witness labels", async () => {
  const observations = JSON.parse(await readFile(new URL("../../docs/compatibility/usage/branch-observations.json", import.meta.url), "utf8"));
  const ledger = JSON.parse(await readLedger(new URL("../../docs/compatibility/usage/branch-ledger.json.gz", import.meta.url)));
  const evidence = ledger.obligations.find((row) => row.evidence.length > 1).evidence[0];
  const range = evidenceAt({ "branch-observations.json": observations }, evidence.v8Range);
  const altered = structuredClone(ledger);
  const changed = altered.obligations.find((row) => row.evidence.length > 1).evidence[0];
  const other = ledger.obligations.flatMap((row) => row.evidence).find((row) => row.v8Witness.range.count > 0 && row.v8Witness.range.startOffset !== range.startOffset);
  changed.v8Range = other.v8Range;

  const identity = branchLedgerIdentity(ledger, observations);

  assert.ok(identity.obligations.some((row) => row.evidence.length > 0));
  assert.deepEqual(range, evidence.v8Witness.range);
  assert.equal(sha(JSON.stringify(observations)), ledger.observationSha256);
  assert.throws(() => branchLedgerIdentity(altered, observations), /./u);
});

test("branchLedgerIdentity rejects a changed raw count even when the pointer still resolves", async () => {
  const observations = JSON.parse(await readFile(new URL("../../docs/compatibility/usage/branch-observations.json", import.meta.url), "utf8"));
  const ledger = JSON.parse(await readLedger(new URL("../../docs/compatibility/usage/branch-ledger.json.gz", import.meta.url)));
  const evidence = ledger.obligations.find((row) => row.evidence.length > 0).evidence[0];
  const changed = structuredClone(observations);
  const range = evidenceAt({ "branch-observations.json": changed }, evidence.v8Range);

  range.count += 1;

  assert.equal(evidenceAt({ "branch-observations.json": observations }, evidence.v8Range).count, evidence.v8Witness.range.count);
  assert.throws(() => branchLedgerIdentity(ledger, changed), /./u);
});

async function captureClassExecution(runtime) {
  const filename = `ledger-class-owner-${measurementId++}.js`;
  const session = new Session();
  const post = (method, params = {}) => new Promise((resolve, reject) => session.post(method, params, (error, result) => error ? reject(error) : resolve(result)));
  session.connect();
  try {
    await post("Profiler.enable");
    await post("Profiler.startPreciseCoverage", { callCount: true, detailed: true });
    const run = new Script(runtime, { filename }).runInThisContext();
    const output = run(true);
    const raw = await post("Profiler.takePreciseCoverage");
    return { filename, output, raw, functions: raw.result.find((script) => script.url === filename).functions };
  } finally {
    await post("Profiler.stopPreciseCoverage");
    session.disconnect();
  }
}

async function classOwnerFixture(t, definition) {
  const source = `function run(flag) {
  const events = [];
  ${definition.declaration}
  ${definition.exercise ?? ""}
  function explicit(value) { if (value) return "explicit-hit"; return "explicit-miss"; }
  events.push(explicit(flag));
  if (flag) return events;
  return ["miss"];
}`;
  const fixture = await ledgerFixture(t, source);
  const capture = await captureClassExecution(fixture.runtime);
  fixture.expected.results[0].value = capture.output;
  fixture.expected.results[1].value = capture.output;
  fixture.inventory.semanticFixtures[0].inputs = { flag: true };
  fixture.expected.requests = [];
  return { fixture, capture };
}

function classOwnerRecords(fixture, definition, functions, markers) {
  const start = fixture.runtime.indexOf(definition.declaration);
  const end = start + definition.declaration.length;
  return functions.filter((fn) => fn.ranges[0].startOffset >= start && fn.ranges[0].endOffset <= end
    && markers.some((marker) => fn.ranges[0].startOffset <= fixture.runtime.indexOf(marker) && fn.ranges[0].endOffset > fixture.runtime.indexOf(marker)));
}

async function retainClassCapture(t, fixture, capture, functions, ledger, witnesses = []) {
  const root = resolve(".pb/probes/7/class-owner-independent/captures");
  await mkdir(root, { recursive: true });
  await writeFile(join(root, `${process.pid}-${t.name.replace(/[^a-zA-Z0-9-]+/gu, "-")}.json`), JSON.stringify({
    source: fixture.source, runtime: fixture.runtime, runtimeSha256: sha(fixture.runtime),
    filename: capture.filename, output: capture.output, raw: capture.raw, functions,
    obligations: ledger.obligations, witnesses,
  }, null, 2) + "\n");
}

const classOwnerCases = [
  { name: "implicit-public", declaration: 'class Subject { value = flag ? "field-hit" : "field-miss"; }', conditions: ['flag ? "field-hit" : "field-miss"'], output: ["explicit-hit"] },
  { name: "explicit-public", declaration: 'class Subject { constructor() {} value = flag ? "field-hit" : "field-miss"; }', conditions: ['flag ? "field-hit" : "field-miss"'], output: ["explicit-hit"] },
  { name: "implicit-private-multiple", declaration: 'class Subject {\n value = flag ? "field-hit" : "field-miss";\n #hidden = flag ? "private-hit" : "private-miss";\n }', conditions: ['flag ? "field-hit" : "field-miss"', 'flag ? "private-hit" : "private-miss"'], output: ["explicit-hit"] },
  { name: "explicit-private-multiple", declaration: 'class Subject {\n constructor() {}\n value = flag ? "field-hit" : "field-miss";\n #hidden = flag ? "private-hit" : "private-miss";\n }', conditions: ['flag ? "field-hit" : "field-miss"', 'flag ? "private-hit" : "private-miss"'], output: ["explicit-hit"] },
  { name: "static-field", declaration: 'class Subject { static value = flag ? "static-hit" : "static-miss"; }', conditions: ['flag ? "static-hit" : "static-miss"'], output: ["explicit-hit"] },
  { name: "static-block", declaration: 'class Subject { static { events.push(flag ? "block-hit" : "block-miss"); } }', conditions: ['flag ? "block-hit" : "block-miss"'], output: ["block-hit", "explicit-hit"] },
  { name: "mixed-static", declaration: 'class Subject {\n static value = flag ? "static-hit" : "static-miss";\n static #hidden = flag ? "private-hit" : "private-miss";\n static { events.push(flag ? "block-hit" : "block-miss"); }\n }', conditions: ['flag ? "static-hit" : "static-miss"', 'flag ? "private-hit" : "private-miss"', 'flag ? "block-hit" : "block-miss"'], output: ["block-hit", "explicit-hit"] },
];

for (const definition of classOwnerCases) {
  for (const [mode, mutate] of [
    ["missing", (functions, owners) => structuredClone(functions.filter((fn) => !owners.includes(fn)))],
    ["zero-count", (functions, owners) => functions.map((fn) => ({ ...fn, ranges: fn.ranges.map((range) => ({ ...range, count: owners.includes(fn) ? 0 : range.count })) }))],
    ["unknown-interval", (functions, owners) => functions.map((fn) => ({ ...fn, ranges: fn.ranges.map((range, index) => ({ ...range, startOffset: range.startOffset + Number(owners.includes(fn) && index === 0) })) }))],
  ]) {
    test(`CLASS-owner: buildBranchLedger keeps ${definition.name} arms pending with ${mode} initializer ownership`, async (t) => {
      const { fixture, capture } = await classOwnerFixture(t, definition);
      const owners = classOwnerRecords(fixture, definition, capture.functions, definition.conditions);
      const functions = mutate(capture.functions, owners);
      fixture.observations.observations[0].blocks[0].functions = functions;

      const ledger = await fixture.build();
      const model = (await contract(fixture.runtime, functions)).scripts[0];
      const rows = ledger.obligations.filter((row) => row.kind === "ConditionalExpression");
      const outer = fixture.runtime.indexOf("return events");
      const explicit = fixture.runtime.indexOf('return "explicit-hit"');
      const witnesses = definition.conditions.map((marker) => {
        const hit = fixture.runtime.indexOf(marker) + marker.indexOf("?") + 2;
        const miss = fixture.runtime.indexOf(marker) + marker.indexOf(":") + 2;
        return [armWitness(functions, hit, hit + 1, model), armWitness(functions, miss, miss + 1, model)];
      });
      await retainClassCapture(t, fixture, capture, functions, ledger, witnesses);

      assert.deepEqual(capture.output, definition.output);
      assert.ok(owners.length > 0);
      assert.deepEqual(ledger.obligations.filter((row) => row.kind === "IfStatement").map((row) => [row.outcome, row.status]), [["true", "observed"], ["false", "pending"], ["true", "observed"], ["false", "pending"]]);
      assert.ok(armWitness(functions, outer, outer + 1, model));
      assert.ok(armWitness(functions, explicit, explicit + 1, model));
      assert.deepEqual(rows.map((row) => [row.start, row.outcome, row.status, row.evidence.length]), definition.conditions.flatMap((marker) => [[fixture.source.indexOf(marker), "true", "pending", 0], [fixture.source.indexOf(marker), "false", "pending", 0]]));
      assert.deepEqual(witnesses, definition.conditions.map(() => [undefined, undefined]));
    });
  }
}

for (const definition of [
  { name: "instance-computed-key", declaration: 'class Subject { [events.push(flag ? "key-hit" : "key-miss")] = "initializer-value"; }', condition: 'flag ? "key-hit" : "key-miss"', hit: '"key-hit"', miss: '"key-miss"', output: ["key-hit", "explicit-hit"] },
  { name: "static-computed-key", declaration: 'class Subject { static [events.push(flag ? "key-hit" : "key-miss")] = "initializer-value"; }', condition: 'flag ? "key-hit" : "key-miss"', hit: '"key-hit"', miss: '"key-miss"', output: ["key-hit", "explicit-hit"] },
  { name: "heritage", declaration: 'class Subject extends (events.push(flag ? "heritage-hit" : "heritage-miss"), Object) { value = "initializer-value"; }', condition: 'flag ? "heritage-hit" : "heritage-miss"', hit: '"heritage-hit"', miss: '"heritage-miss"', output: ["heritage-hit", "explicit-hit"] },
]) {
  test(`CLASS-eager: buildBranchLedger preserves ${definition.name} evidence with the initializer record missing`, async (t) => {
    const { fixture, capture } = await classOwnerFixture(t, definition);
    const owners = classOwnerRecords(fixture, definition, capture.functions, ['"initializer-value"']);
    const functions = structuredClone(capture.functions.filter((fn) => !owners.includes(fn)));
    fixture.observations.observations[0].blocks[0].functions = functions;

    const ledger = await fixture.build();
    const model = (await contract(fixture.runtime, functions)).scripts[0];
    const rows = ledger.obligations.filter((row) => row.kind === "ConditionalExpression" && row.start === fixture.source.indexOf(definition.condition));
    const hit = fixture.runtime.indexOf(definition.hit);
    const miss = fixture.runtime.indexOf(definition.miss);
    await retainClassCapture(t, fixture, capture, functions, ledger);

    assert.deepEqual(capture.output, definition.output);
    assert.ok(owners.length > 0);
    assert.deepEqual(ledger.obligations.filter((row) => row.kind === "IfStatement").map((row) => [row.outcome, row.status]), [["true", "observed"], ["false", "pending"], ["true", "observed"], ["false", "pending"]]);
    assert.deepEqual(rows.map((row) => [row.outcome, row.status]), [["true", "observed"], ["false", "pending"]]);
    assert.ok(armWitness(functions, hit, hit + 1, model));
    assert.equal(armWitness(functions, miss, miss + 1, model), undefined);
  });
}

test("CLASS-instantiated: buildBranchLedger preserves explicit constructor evidence without inventing untaken field evidence", async (t) => {
  const definition = {
    declaration: 'class Subject { value = flag ? events.push("field-hit") : events.push("field-miss"); #hidden = flag ? events.push("private-hit") : events.push("private-miss"); constructor() { if (flag) events.push("constructor-hit"); } }',
    exercise: "new Subject();",
  };
  const { fixture, capture } = await classOwnerFixture(t, definition);
  const functions = capture.functions;
  fixture.observations.observations[0].blocks[0].functions = functions;

  const ledger = await fixture.build();
  const rows = ledger.obligations.filter((row) => row.kind === "ConditionalExpression");
  await retainClassCapture(t, fixture, capture, functions, ledger);

  assert.deepEqual(capture.output, ["field-hit", "private-hit", "constructor-hit", "explicit-hit"]);
  assert.deepEqual(ledger.obligations.filter((row) => row.kind === "IfStatement").map((row) => [row.outcome, row.status]), [["true", "observed"], ["false", "pending"], ["true", "observed"], ["false", "pending"], ["true", "observed"], ["false", "pending"]]);
  assert.deepEqual(rows.filter((row) => row.outcome === "false").map((row) => [row.status, row.evidence.length]), [["pending", 0], ["pending", 0]]);
});

for (const definition of [
  { name: "method", declaration: 'class Subject { [flag ? "m1" : "m2"]() { return "method"; } }', exercise: "events.push(new Subject().m1());", condition: 'flag ? "m1" : "m2"', hit: '"m1"', miss: '"m2"', output: ["method", "explicit-hit"] },
  { name: "getter", declaration: 'class Subject { get [flag ? "g1" : "g2"]() { return "getter"; } }', exercise: "events.push(new Subject().g1);", condition: 'flag ? "g1" : "g2"', hit: '"g1"', miss: '"g2"', output: ["getter", "explicit-hit"] },
  { name: "object-method", declaration: 'const subject = { [flag ? "o1" : "o2"]() { return "object"; } };', exercise: "events.push(subject.o1());", condition: 'flag ? "o1" : "o2"', hit: '"o1"', miss: '"o2"', output: ["object", "explicit-hit"] },
]) {
  test(`CLASS-key: buildBranchLedger keeps ${definition.name} computed-key arms pending instead of inheriting the method count`, async (t) => {
    const { fixture, capture } = await classOwnerFixture(t, definition);
    const functions = capture.functions;
    fixture.observations.observations[0].blocks[0].functions = functions;

    const ledger = await fixture.build();
    const model = (await contract(fixture.runtime, functions)).scripts[0];
    const rows = ledger.obligations.filter((row) => row.kind === "ConditionalExpression" && row.start === fixture.source.indexOf(definition.condition));
    const hit = fixture.runtime.indexOf(definition.hit);
    const miss = fixture.runtime.indexOf(definition.miss);
    await retainClassCapture(t, fixture, capture, functions, ledger);

    assert.deepEqual(capture.output, definition.output);
    assert.deepEqual(rows.map((row) => [row.outcome, row.status]), [["true", "pending"], ["false", "pending"]]);
    assert.equal(armWitness(functions, hit, hit + 1, model), undefined);
    assert.equal(armWitness(functions, miss, miss + 1, model), undefined);
  });
}

test("CLASS-explicit: buildBranchLedger keeps an explicit function inside a field initializer eligible", async (t) => {
  const definition = { declaration: 'class Subject { run = () => flag ? "arrow-hit" : "arrow-miss"; }', exercise: "events.push(new Subject().run());" };
  const { fixture, capture } = await classOwnerFixture(t, definition);
  const functions = capture.functions;
  fixture.observations.observations[0].blocks[0].functions = functions;

  const ledger = await fixture.build();
  const model = (await contract(fixture.runtime, functions)).scripts[0];
  const rows = ledger.obligations.filter((row) => row.kind === "ConditionalExpression" && row.start === fixture.source.indexOf('flag ? "arrow-hit" : "arrow-miss"'));
  const hit = fixture.runtime.indexOf('"arrow-hit"');
  const miss = fixture.runtime.indexOf('"arrow-miss"');
  await retainClassCapture(t, fixture, capture, functions, ledger);

  assert.deepEqual(capture.output, ["arrow-hit", "explicit-hit"]);
  assert.deepEqual(rows.map((row) => [row.outcome, row.status]), [["true", "observed"], ["false", "pending"]]);
  assert.ok(armWitness(functions, hit, hit + 1, model));
  assert.equal(armWitness(functions, miss, miss + 1, model), undefined);
});

test("branch evidence treats a script block holding only excluded branchless functions like an omitted block", async () => {
  const branched = `(function run(value) { if (value) return "one"; return "other"; })`;
  const branchless = `(function helper() { return "leaf"; })`;
  const runFunctions = await measured(branched, [true]);
  const helperFunctions = (await measured(branchless, [undefined])).filter((fn) => fn.functionName === "helper");
  assert.equal(helperFunctions.length, 1);
  assert.ok(helperFunctions[0].ranges[0].count > 0);
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, "branch-contract-test-"));
  try {
    const scripts = [{ path: "src/query.ts", text: branched, sha256: sha(branched) }, { path: "src/core.ts", text: branchless, sha256: sha(branchless) }];
    const runBlock = { script: sha(branched), functions: runFunctions };
    const helperBlock = { script: sha(branchless), functions: helperFunctions };
    const recorded = await branchEvidenceContract({ scripts, observations: [{ id: "synthetic", blocks: [helperBlock, runBlock] }] }, root);
    const omitted = await branchEvidenceContract({ scripts, observations: [{ id: "synthetic", blocks: [runBlock] }] }, root);
    assert.deepEqual(recorded, omitted);
    const branchedOmitted = await branchEvidenceContract({ scripts, observations: [{ id: "synthetic", blocks: [helperBlock] }] }, root);
    assert.notDeepEqual(branchedOmitted, recorded);
  } finally { await rm(root, { recursive: true, force: true }); }
});

async function measuredCalls(text, calls) {
  const filename = `ledger-count-${measurementId++}.js`;
  const session = new Session();
  const post = (method, params = {}) => new Promise((resolve, reject) => session.post(method, params, (error, result) => error ? reject(error) : resolve(result)));
  session.connect();
  try {
    await post("Profiler.enable");
    await post("Profiler.startPreciseCoverage", { callCount: true, detailed: true });
    const run = new Script(text, { filename }).runInThisContext();
    for (const value of calls) {
      try { run(value); } catch (error) { if (!(error instanceof TypeError) && error?.message !== "fixture-throw") throw error; }
    }
    const { result } = await post("Profiler.takePreciseCoverage");
    return result.find((item) => item.url === filename).functions;
  } finally {
    await post("Profiler.stopPreciseCoverage");
    session.disconnect();
  }
}

async function countLedger(t, files, calls, runtimeFile = "src/query.ts") {
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "ledger-count-test-"));
  t.after(() => rm(project, { recursive: true, force: true }));
  const backend = join(project, "node_modules/@narumitw/pi-usage");
  await mkdir(join(backend, "src"), { recursive: true });
  for (const [path, text] of Object.entries(files)) await writeFile(join(backend, path), text);
  const provenance = { dependencies: { "node_modules/@narumitw/pi-usage": { installed: { files: Object.fromEntries(Object.entries(files).map(([path, text]) => [path, { sha256: sha(text) }])) } } } };
  const runtime = `(${files[runtimeFile]})`;
  const fixtures = calls ? [{ id: "synthetic", inputs: { calls: calls.length }, evidence: ["expected.json#/results/0", "expected.json#/results/1"], sourceBranchEvidence: "branch-ledger.json#/fixtureMappings/0" }] : [];
  const observations = { scripts: calls ? [{ path: runtimeFile, text: runtime, sha256: sha(runtime) }] : [],
    observations: calls ? [{ id: "synthetic", blocks: [{ script: sha(runtime), functions: await measuredCalls(runtime, calls) }] }] : [] };
  const expected = { results: [{ id: "synthetic", implementation: "source" }, { id: "synthetic", implementation: "dist" }], requests: [] };
  const ledger = await buildBranchLedger(project, project, { symbols: [], semanticFixtures: fixtures }, observations, expected, provenance);
  return { ledger, observations };
}

const thrower = () => { throw new Error("fixture-throw"); };
const quiet = () => undefined;
const iterateOnce = (n) => n < 1;
const never = () => false;
const throwingIterable = { [Symbol.iterator]() { throw new TypeError("fixture iterator"); } };
const countCases = [
  ["if false after an exiting guard", "function run(x) { if (x) return 1; return 2; }", "IfStatement", "x", "false", [[[0], "observed"], [[1, 1], "pending"], [[1, 0], "observed"]]],
  ["if false after a completing then arm", "function run(x) { let n = 0; if (x) { n++; } return n; }", "IfStatement", "x", "false", [[[1, 0], "observed"], [[1, 1], "pending"]]],
  ["if false when the condition throws before deciding", "function run(x) { if (x.y) return 1; return 2; }", "IfStatement", "x.y", "false", [[[null, { y: 1 }], "pending"], [[{ y: 0 }], "observed"]]],
  ["if false as the last block statement", "function run(x) { let n = 0; n++; if (x) { n++; } }", "IfStatement", "x", "false", [[[0], "pending"]]],
  ["if false before a dropped empty statement", "function run(x) { if (x.y) return 1; ; }", "IfStatement", "x.y", "false", [[[null, null], "pending"], [[{ y: 0 }], "pending"]]],
  ["if false before a hoisted function declaration", "function run(x) { if (x.y) return 1; function unused() {} }", "IfStatement", "x.y", "false", [[[null], "pending"]]],
  ["if false before an uninitialized var", "function run(x) { if (x.y) return 1; var later; }", "IfStatement", "x.y", "false", [[[{ y: 0 }], "pending"]]],
  ["final else-if false with exiting arms", "function run(x) { if (x === 1) return 1; else if (x === 2) return 2; return 3; }", "IfStatement", "x === 2", "false", [[[1, 2], "pending"], [[3], "observed"]]],
  ["final else-if false with completing arms", "function run(x) { let n = 0; if (x === 1) { n = 1; } else if (x === 2) { n = 2; } return n; }", "IfStatement", "x === 2", "false", [[[1, 2], "pending"], [[1, 3], "observed"]]],
  ["if false in an unbraced loop body", "function run(xs) { let n = 0; for (const a of xs) if (a) n++; return n; }", "IfStatement", "a", "false", [[[[0]], "pending"]]],
  ["if false beside a continue arm", "function run(xs) { let n = 0; for (const a of xs) { if (a) continue; n++; } return n; }", "IfStatement", "a", "false", [[[[1, 1]], "pending"], [[[1, 0]], "observed"]]],
  ["for-of normal exit beside a break", "function run(xs) { for (const a of xs) { if (a) break; } return 1; }", "ForOfStatement", "xs", "normal-exit", [[[[1]], "pending"], [[[0]], "observed"], [[[1], [1], [0]], "observed"]]],
  ["for-of exit through return", "function run(xs) { for (const a of xs) { if (a) return 2; } return 1; }", "ForOfStatement", "xs", "normal-exit", [[[[1]], "pending"], [[[0]], "observed"]]],
  ["for-of exit with a nested switch break", "function run(xs) { for (const a of xs) { switch (a) { case 1: break; } } return 1; }", "ForOfStatement", "xs", "normal-exit", [[[[1]], "observed"]]],
  ["for-of exit as the last block statement", "function run(xs) { for (const a of xs) { a; } }", "ForOfStatement", "xs", "normal-exit", [[[[1]], "pending"]]],
  ["for-of zero iterations", "function run(xs) { let n = 0; for (const a of xs) { n += a; } return n; }", "ForOfStatement", "xs", "zero-iterations", [[[[1]], "pending"], [[[]], "observed"], [[[1], []], "observed"], [[[1, 1], []], "pending"]]],
  ["while normal exit", "function run(x) { let n = 0; while (x > n) { n++; } return n; }", "WhileStatement", "x > n", "normal-exit", [[[2], "observed"], [[null], "observed"]]],
  ["try completed beside an exiting catch", "function run(f) { try { f(); } catch (error) { return 0; } return 1; }", "TryStatement", "TryStatement", "try-completed", [[[quiet], "observed"], [[thrower], "pending"]]],
  ["try completed beside a completing catch", "function run(f) { let n = 0; try { f(); } catch (error) { n = 1; } return n; }", "TryStatement", "TryStatement", "try-completed", [[[thrower], "pending"], [[quiet], "observed"]]],
  ["try threw into a catch", "function run(f) { let n = 0; try { f(); } catch (error) { n = 1; } return n; }", "TryStatement", "TryStatement", "try-threw", [[[thrower], "observed"], [[quiet], "pending"]]],
  ["try threw through finally only", "function run(f) { try { f(); } finally { f = null; } return 1; }", "TryStatement", "TryStatement", "try-threw", [[[thrower], "pending"]]],
  ["skipped && RHS in an if condition", "function run(x) { if (x && x.y) return 1; return 2; }", "BinaryExpression", "x && x.y", "rhs-skipped", [[[0], "observed"], [[{ y: 0 }], "pending"], [[{ y: 1 }], "pending"]]],
  ["skipped && RHS when the RHS throws", "function run(x) { if (x && x.y.z) return 1; return 2; }", "BinaryExpression", "x && x.y.z", "rhs-skipped", [[[{}], "pending"]]],
  ["skipped && RHS in a return", "function run(x) { return x && x.y; }", "BinaryExpression", "x && x.y", "rhs-skipped", [[[0], "pending"]]],
  ["skipped || RHS in a conditional", "function run(x) { return x || 0 ? 1 : 2; }", "BinaryExpression", "x || 0", "rhs-skipped", [[[1], "observed"], [[0], "pending"]]],
  ["skipped RHS on a left spine", "function run(x) { if (x.a && x.b && x.c) return 1; return 2; }", "BinaryExpression", "x.a && x.b", "rhs-skipped", [[[{ a: 0 }], "observed"], [[{ a: 1, b: 0 }], "pending"]]],
  ["skipped RHS of a right operand", "function run(x) { if (x.a || x.b && x.c) return 1; return 2; }", "BinaryExpression", "x.b && x.c", "rhs-skipped", [[[{ a: 0, b: 0 }], "observed"], [[{ a: 1 }], "pending"], [[{ a: 0, b: 1, c: 0 }], "pending"]]],
  ["skipped RHS of a conjunction's right operand", "function run(x) { if (x.a && (x.b || x.c)) return 1; return 2; }", "BinaryExpression", "x.b || x.c", "rhs-skipped", [[[{ a: 1, b: 1 }], "observed"], [[{ a: 1, b: 0, c: 0 }], "pending"], [[{ a: 0 }], "pending"]]],
  ["skipped ?? RHS in a declaration before a later if", "function run(x) { const v = x.a ?? 0; let n = 0; if (v) n++; return n; }", "BinaryExpression", "x.a ?? 0", "rhs-skipped", [[[{ a: 1 }], "observed"], [[{ a: null }], "pending"], [[{ a: 1 }, { a: null }], "observed"]]],
  ["skipped ?? RHS when the declaration throws", "function run(x) { const v = x.a.b ?? 0; if (v) return 1; return 2; }", "BinaryExpression", "x.a.b ?? 0", "rhs-skipped", [[[{}], "pending"], [[{ a: { b: 1 } }], "observed"]]],
  ["skipped ?? RHS in a declaration without a later if", "function run(x) { const v = x.a ?? 0; return v; }", "BinaryExpression", "x.a ?? 0", "rhs-skipped", [[[{ a: 1 }], "pending"]]],
  ["skipped || RHS before a later loop", "function run(x) { const v = x.a || 0; let i = 0; while (i < 3) { i++; } return v; }", "BinaryExpression", "x.a || 0", "rhs-skipped", [[[{ a: null }], "pending"], [[{ a: 1 }], "pending"]]],
  ["skipped || RHS in a call argument", "function run(x) { const v = String(x || 0); if (v) return 1; return 2; }", "BinaryExpression", "x || 0", "rhs-skipped", [[[1], "observed"], [[0], "pending"]]],
  ["skipped || RHS in an optional call argument", "function run(x) { const v = x?.f(x.a || 0); if (v) return 1; return 2; }", "BinaryExpression", "x.a || 0", "rhs-skipped", [[[null], "pending"], [[{ a: 1, f: String }], "pending"]]],
  ["skipped || RHS in an untaken conditional arm", "function run(x) { const v = x.a ? x.b || 0 : 1; if (v) return 1; return 2; }", "BinaryExpression", "x.b || 0", "rhs-skipped", [[[{ a: 0 }], "pending"], [[{ a: 1, b: 1 }], "pending"]]],
  ["skipped RHS under a negated conjunction", "function run(x) { if (!(x.a && (x.b || x.c))) return 1; return 2; }", "BinaryExpression", "x.b || x.c", "rhs-skipped", [[[{ a: 1, b: 1 }], "observed"], [[{ a: 0 }], "pending"]]],
  ["skipped RHS inside a conjunction's left operand", "function run(x) { if ((x.a || x.b && x.d) && x.c) return 1; return 2; }", "BinaryExpression", "x.b && x.d", "rhs-skipped", [[[{ a: 1, c: 0 }], "pending"]]],
  ["if false before an uninitialized var and a later if", "function run(x) { if (x.y) return 1; var later; if (x.z.w) return 2; return 3; }", "IfStatement", "x.y", "false", [[[{ y: 0, z: { w: 1 } }], "observed"], [[{ y: 0 }], "pending"], [[{ y: 1 }], "pending"]]],
  ["skipped RHS in a negated while condition", "function run(x) { let n = 0; while (!(n >= 3 || !x)) { n++; } return n; }", "BinaryExpression", "n >= 3 || !x", "rhs-skipped", [[[1], "observed"], [[0], "pending"]]],
  ["optional link non-nullish", "function run(x) { return x?.y; }", "PropertyAccessExpression", "x?.y", "non-nullish", [[[null], "pending"], [[{ y: 1 }], "observed"]]],
  ["second optional link non-nullish", "function run(x) { return x?.a?.b; }", "PropertyAccessExpression", "x?.a?.b", "non-nullish", [[[{}], "pending"], [[{ a: {} }], "observed"]]],
  ["first optional link nullish in an if condition", "function run(x) { if (x?.y) return 1; return 2; }", "PropertyAccessExpression", "x?.y", "nullish", [[[null], "observed"], [[{ y: 0 }], "pending"]]],
  ["first optional link nullish in a return", "function run(x) { return x?.y; }", "PropertyAccessExpression", "x?.y", "nullish", [[[null], "pending"]]],
  ["first optional link nullish in a declaration before a later if", "function run(x) { const v = x?.y; if (v) return 1; return 2; }", "PropertyAccessExpression", "x?.y", "nullish", [[[null], "observed"], [[{ y: 1 }], "pending"]]],
  ["second optional link nullish", "function run(x) { if (x?.a?.b) return 1; return 2; }", "PropertyAccessExpression", "x?.a?.b", "nullish", [[[{}], "pending"]]],
  ["if false before a case label that another case jumps to", "function run(x) { let n = 0; switch (x.k) { case 1: if (x.a) { n++; } case 2: n++; } return n; }", "IfStatement", "x.a", "false", [[[{ k: 1, a: 1 }, { k: 2 }], "pending"]]],
  ["if false whose then arm ends in a nested if", "function run(x) { let n = 0; if (x.a) { if (x.b) { n = 1; } } return n; }", "IfStatement", "x.a", "false", [[[{ a: 1, b: 1 }, { a: 1, b: 0 }], "pending"], [[{ a: 0 }], "observed"]]],
  ["if false whose then arm ends in a switch", "function run(x) { let n = 0; if (x.a) { switch (x.k) { case 1: n = 1; } } return n; }", "IfStatement", "x.a", "false", [[[{ a: 1, k: 1 }, { a: 1, k: 2 }], "pending"], [[{ a: 0 }], "observed"]]],
  ["if false as the last try statement before a completing catch", "function run(x) { let n = 0; try { x.f(); if (x.a) { n++; } } catch (error) { n = 2; } return n; }", "IfStatement", "x.a", "false", [[[{ f: quiet, a: 1 }, { f: thrower }], "pending"]]],
  ["try completed beside a catch that ends in a nested if", "function run(x) { let n = 0; try { x.f(); } catch (error) { if (x.g) { n = 1; } } return n; }", "TryStatement", "TryStatement", "try-completed", [[[{ f: thrower, g: 1 }, { f: thrower, g: 0 }], "pending"], [[{ f: quiet }], "observed"]]],
  ["for-of normal exit when an inner loop breaks the outer label", "function run(xs) { outer: for (const a of xs) { for (const b of a) { if (b) break outer; } } return 1; }", "ForOfStatement", "xs", "normal-exit", [[[[[1]]], "pending"]]],
  ["while zero iterations when one condition throws and another call iterates", "function run(x) { let n = 0; while (x.f(n)) { n++; } return n; }", "WhileStatement", "x.f(n)", "zero-iterations", [[[{ f: thrower }, { f: iterateOnce }], "pending"], [[{ f: never }], "observed"]]],
  ["for-of zero iterations when one iterable throws and another iterates", "function run(x) { let n = 0; for (const a of x.it) { n++; } return n; }", "ForOfStatement", "x.it", "zero-iterations", [[[{ it: throwingIterable }, { it: [1] }], "pending"], [[{ it: [] }], "observed"]]],
  ["skipped && RHS when the LHS throws in an if condition", "function run(x) { if (x.a.b && x.c) return 1; return 2; }", "BinaryExpression", "x.a.b && x.c", "rhs-skipped", [[[{}], "pending"], [[{ a: { b: 0 } }], "observed"]]],
  ["skipped || RHS when one LHS throws and another call evaluates the RHS", "function run(x) { const v = x.a.b || x.c; if (v) return 1; return 2; }", "BinaryExpression", "x.a.b || x.c", "rhs-skipped", [[[{}, { a: { b: 0 }, c: 1 }], "pending"]]],
  ["skipped || RHS in a try whose catch reaches the later if", "function run(x) { let v; try { v = x.f() || 0; } catch (error) { v = 1; } if (v) return 1; return 2; }", "BinaryExpression", "x.f() || 0", "rhs-skipped", [[[{ f: thrower }], "pending"]]],
  ["first optional link nullish when its base throws", "function run(x) { const v = x.a.b?.c; if (v) return 1; return 2; }", "PropertyAccessExpression", "x.a.b?.c", "nullish", [[[{}, { a: { b: { c: 1 } } }], "pending"], [[{ a: {} }], "observed"]]],
];

for (const [name, source, kind, input, outcome, runs] of countCases) {
  for (const [calls, status] of runs) {
    test(`count rule: ${name} is ${status} for ${calls.length} call(s)`, async (t) => {
      const { ledger } = await countLedger(t, { "src/query.ts": source }, calls);
      const rows = ledger.obligations.filter((row) => row.kind === kind && row.input === input && row.outcome === outcome);
      assert.equal(rows.length, 1);
      assert.equal(rows[0].status, status);
      if (status === "observed") assert.ok(rows[0].evidence.every((entry) => entry.v8Range || entry.lowerBound > 0));
    });
  }
}

test("count witness replays by identity and rejects changed raw counts or bounds", async (t) => {
  const { ledger, observations } = await countLedger(t, { "src/query.ts": "function run(x) { let n = 0; if (x) { n++; } return n; }" }, [1, 0, 0]);
  const row = ledger.obligations.find((item) => item.kind === "IfStatement" && item.outcome === "false");
  const evidence = row.evidence[0];
  assert.equal(evidence.countRule, "if-false-continuation");
  assert.equal(evidence.lowerBound, 2);
  assert.deepEqual(evidence.terms.map((term) => [term.role, term.sign, term.v8Witness.range.count]), [["continuation", 1, 3], ["then-normal-completion", -1, 1]]);
  const identity = branchLedgerIdentity(ledger, observations);
  assert.deepEqual(identity.obligations.find((item) => item.id === row.id).evidence[0].terms.map((term) => Object.hasOwn(term, "v8Range")), [false, false]);
  const changedBound = structuredClone(ledger);
  changedBound.obligations.find((item) => item.id === row.id).evidence[0].lowerBound = 3;
  assert.throws(() => branchLedgerIdentity(changedBound, observations), /Changed count lower bound/u);
  const changedCount = structuredClone(observations);
  const thenTerm = evidence.terms[1];
  const raw = evidenceAt({ "branch-observations.json": changedCount }, thenTerm.v8Range);
  raw.count = 3;
  const recount = { ...structuredClone(ledger), obligations: [structuredClone(row)] };
  const recountTerm = recount.obligations[0].evidence[0].terms[1];
  recountTerm.v8Witness.range.count = 3;
  recount.observationSha256 = sha(JSON.stringify(changedCount));
  assert.throws(() => branchLedgerIdentity(recount, changedCount), /Changed count lower bound/u);
  const flipped = structuredClone(ledger);
  flipped.obligations.find((item) => item.id === row.id).evidence[0].terms[1].sign = 2;
  assert.throws(() => branchLedgerIdentity(flipped, observations), /Invalid count term sign/u);
});

const ifFalse = (ledger) => ledger.obligations.find((item) => item.kind === "IfStatement" && item.outcome === "false");
const rawAt = (observations, pointer) => evidenceAt({ "branch-observations.json": observations }, pointer);
const rawFunctionAt = (observations, pointer) => rawAt(observations, pointer.replace(/\/ranges\/\d+$/u, ""));
const reseal = (ledger, observations) => { ledger.observationSha256 = sha(JSON.stringify(observations)); };
const rejectsAuthentically = (error) => error instanceof Error && !(error instanceof TypeError) && !(error instanceof ReferenceError);

for (const [name, tamper] of [
  ["a continuation counter zeroed in both the raw trace and the ledger", (ledger, observations, evidence) => {
    const [continuation, then] = evidence.terms;
    rawAt(observations, continuation.v8Range).count = 0;
    continuation.v8Witness.range.count = 0;
    evidence.lowerBound = -then.v8Witness.range.count;
    reseal(ledger, observations);
  }],
  ["a subtracted term moved onto another function's counter with an equal count", (ledger, observations, evidence) => {
    const then = evidence.terms.find((term) => term.sign < 0);
    const block = then.v8Range.replace(/\/functions\/\d+\/ranges\/\d+$/u, "/functions");
    const index = rawAt(observations, block).findIndex((fn) => fn.functionName !== then.v8Witness.functionName && fn.ranges[0].count === then.v8Witness.range.count);
    const moved = rawAt(observations, `${block}/${index}`);
    then.v8Range = `${block}/${index}/ranges/0`;
    then.v8Witness = { functionName: moved.functionName, functionStart: moved.ranges[0].startOffset, functionEnd: moved.ranges[0].endOffset, isBlockCoverage: moved.isBlockCoverage, range: { ...moved.ranges[0] } };
  }],
  ["counters re-marked as non-block coverage in both the raw trace and the ledger", (ledger, observations, evidence) => {
    rawFunctionAt(observations, evidence.terms[0].v8Range).isBlockCoverage = false;
    for (const term of evidence.terms) term.v8Witness.isBlockCoverage = false;
    reseal(ledger, observations);
  }],
]) {
  test(`count witness authentication rejects ${name}`, async (t) => {
    const { ledger, observations } = await countLedger(t, { "src/query.ts": "function run(x) { let n = 0; if (x) { n++; } return n; }" }, [1, 0, 0]);
    const altered = structuredClone(ledger);
    const changedObservations = structuredClone(observations);
    const evidence = ifFalse(altered).evidence[0];

    assert.equal(ifFalse(ledger).status, "observed");
    assert.ok(evidence.terms.length > 1);
    tamper(altered, changedObservations, evidence);

    assert.doesNotThrow(() => branchLedgerIdentity(ledger, observations));
    assert.throws(() => branchLedgerIdentity(altered, changedObservations), rejectsAuthentically);
  });
}

test("public usage API seams leave the pending count with a recorded ADR 0004 reason", async (t) => {
  const api = "export function registerUsageApi(pi) { pi.events.on(\"usage:query:v1\", (request) => { if (request.respond) request.respond(1); }); return { changed(id) { if (id) pi.events.emit(\"usage:changed:v1\", id); } }; }";
  const usage = "export function usageExtension(pi) { let usageApi; const notify = (id) => usageApi?.changed(id); usageApi = registerUsageApi(pi); return (x) => { if (x) notify(x); return x?.y; }; }";
  const footer = "export function usageStatus(pi) { pi.events.on(\"usage:changed:v1\", (event) => { if (event?.session) refresh(); }); return (ctx) => { if (ctx.model) refresh(); }; }";
  const { ledger } = await countLedger(t, { "src/usage-api.ts": api, "src/usage.ts": usage, "src/core.ts": footer });
  const outOfScope = ledger.obligations.filter((row) => row.status === "out-of-scope");
  assert.deepEqual([...new Set(outOfScope.map((row) => `${row.source}:${row.input}`))].sort(), ["src/core.ts:event?.session", "src/core.ts:event?.session", "src/usage-api.ts:id", "src/usage-api.ts:request.respond", "src/usage.ts:usageApi?.changed"].filter((item, index, all) => all.indexOf(item) === index));
  assert.ok(outOfScope.every((row) => row.scopeDecision.decision === "docs/adr/0004-no-public-usage-event-api.md" && row.scopeDecision.reason.length > 0 && !row.pendingReason));
  assert.deepEqual(ledger.obligations.filter((row) => row.status === "pending").map((row) => `${row.source}:${row.input}:${row.outcome}`).sort(),
    ["src/core.ts:ctx.model:false", "src/core.ts:ctx.model:true", "src/usage.ts:x:false", "src/usage.ts:x:true", "src/usage.ts:x?.y:non-nullish", "src/usage.ts:x?.y:nullish"]);
  assert.deepEqual(ledger.remainingGroups.flatMap((group) => group.obligations).sort(), ledger.obligations.filter((row) => row.status === "pending").map((row) => row.id).sort());
});

const sourceRoots = { src: "../../node_modules/@narumitw/pi-usage/", status: "../../" };
const committedArtifact = async (name) => {
  const url = new URL(`../../docs/compatibility/usage/${name}`, import.meta.url);
  return JSON.parse(name === "branch-ledger.json" ? await readLedger(new URL(`${url}.gz`)) : await readFile(url, "utf8"));
};
let committedLedger;
const committed = () => committedLedger ??= Promise.all(["branch-ledger.json", "branch-observations.json", "lifecycle.json"].map(committedArtifact))
  .then(([ledger, observations, lifecycle]) => ({ ledger, observations, lifecycle }));
const ADR_0004 = "docs/adr/0004-no-public-usage-event-api.md";

const FOOTER = "status/usage.ts";
const committedSources = async (ledger) => new Map(await Promise.all(Object.keys(ledger.sourceFiles).map(async (path) => [path, await readFile(new URL(`${sourceRoots[path.split("/")[0]]}${path}`, import.meta.url), "utf8")])));
const insideUsageListener = (texts) => (row) => /["']usage:[\w-]+:v1["']\s*,\s*$/u.test(texts.get(row.source).slice(0, row.symbolStart));

test("committed ledger takes exactly the public usage API seam out of the pending count, each with an ADR 0004 reason", async () => {
  const { ledger } = await committed();
  const texts = await committedSources(ledger);
  const handles = new Set([...texts.values()].flatMap((text) => [...text.matchAll(/(\w+)\s*=\s*registerUsageApi\(/gu)].map((match) => match[1])));
  const insideListener = (row) => row.source !== FOOTER && insideUsageListener(texts)(row);
  const throughHandle = (row) => row.kind === "PropertyAccessExpression" && handles.has(row.input.split("?.")[0]);
  const publicSeam = ledger.obligations.filter((row) => row.source === "src/usage-api.ts" || insideListener(row) || throughHandle(row)).map((row) => row.id).sort();
  const outOfScope = ledger.obligations.filter((row) => row.status === "out-of-scope");
  const pending = ledger.obligations.filter((row) => row.status === "pending").map((row) => row.id).sort();

  assert.deepEqual(Object.fromEntries([...texts].map(([path, text]) => [path, sha(text)])), ledger.sourceFiles);
  assert.ok(handles.size > 0);
  assert.deepEqual(outOfScope.map((row) => row.id).sort(), publicSeam);
  assert.deepEqual(outOfScope.map((row) => [row.scopeDecision.decision, row.scopeDecision.reason.length > 0, Object.hasOwn(row, "pendingReason")]),
    outOfScope.map(() => [ADR_0004, true, false]));
  assert.equal(ledger.outOfScope.decision, ADR_0004);
  assert.deepEqual(ledger.remainingGroups.flatMap((group) => group.obligations).sort(), pending);
});

test("committed ledger keeps the footer's usage:changed:v1 session/model fence in scope as observed or pending outcomes", async () => {
  const { ledger } = await committed();
  const texts = await committedSources(ledger);
  const fence = ledger.obligations.filter((row) => row.source === FOOTER && insideUsageListener(texts)(row));

  assert.ok(fence.length > 0);
  assert.deepEqual(fence.map((row) => [row.id, ["observed", "pending"].includes(row.status)]), fence.map((row) => [row.id, true]));
});

const NAMED_GAPS = "docs/compatibility/usage.md#named-gaps";
const CREDENTIAL_SOURCE = "src/oauth-credential-source.ts";
const soleEmitChannel = (block) => /^try\s*\{\s*(?:await\s+)?[\w.]+\.emit\(\s*([^,)\s]+)[^;]*\);?\s*\}\s*$/u.exec(block)?.[1];
const credentialSourceChannels = (text) => ['"oauth:credential-source:v1"', "'oauth:credential-source:v1'", ...[...text.matchAll(/\bconst\s+(\w+)\s*=\s*["']oauth:credential-source:v1["']/gu)].map((match) => match[1])];

test("committed ledger names exactly the credential-source listener-error outcomes as pending named gaps", async () => {
  const { ledger } = await committed();
  const text = (await committedSources(ledger)).get(CREDENTIAL_SOURCE);
  const catches = ledger.obligations.filter((row) => row.source === CREDENTIAL_SOURCE && row.kind === "CatchClause" && row.outcome === "entered");
  const tryBlock = (row) => text.slice(row.start, catches.find((clause) => clause.start > row.start && clause.start < row.end)?.start ?? row.start);
  const throws = ledger.obligations.filter((row) => row.source === CREDENTIAL_SOURCE && row.kind === "TryStatement" && row.outcome === "try-threw" && credentialSourceChannels(text).includes(soleEmitChannel(tryBlock(row))));
  const unreachable = [...throws, ...catches.filter((clause) => throws.some((row) => clause.start > row.start && clause.start < row.end))];
  const named = ledger.obligations.filter((row) => Object.hasOwn(row, "namedGap") || /^Named gap:/u.test(row.pendingReason ?? ""));
  const usageDoc = await readFile(new URL("../../docs/compatibility/usage.md", import.meta.url), "utf8");

  assert.deepEqual(unreachable.map((row) => row.outcome).sort(), ["entered", "try-threw"]);
  assert.deepEqual(named.map((row) => row.id).sort(), unreachable.map((row) => row.id).sort());
  assert.deepEqual(named.map((row) => [row.status, /^Named gap: .*listener/u.test(row.pendingReason), row.namedGap.decision, row.namedGap.nativeObligation.length > 0, Object.hasOwn(row, "scopeDecision")]),
    named.map(() => ["pending", true, NAMED_GAPS, true, false]));
  assert.deepEqual([ledger.namedGaps.decision, ledger.namedGaps.rule.length > 0], [NAMED_GAPS, true]);
  assert.match(usageDoc, /^## Named gaps$/mu);
});

for (const [name, mutate] of [
  ["named-gap record", (row) => { row.namedGap.nativeObligation = "different obligation"; }],
  ["named-gap reason", (row) => { row.pendingReason = "Named gap: different reason"; }],
  ["pending status", (row) => { row.status = "observed"; }],
]) {
  test(`branchLedgerIdentity preserves a named-gap row's ${name} rather than erasing it`, async () => {
    const { ledger, observations } = await committed();
    const altered = { ...ledger, obligations: [...ledger.obligations] };
    const index = ledger.obligations.findIndex((row) => Object.hasOwn(row, "namedGap"));
    altered.obligations[index] = structuredClone(ledger.obligations[index]);

    assert.ok(index >= 0);
    mutate(altered.obligations[index]);

    assert.notDeepEqual(branchLedgerIdentity(altered, observations), branchLedgerIdentity(ledger, observations));
  });
}

test("registered lifecycle source outcomes mark ADR 0004 scope exactly where the ledger does", async () => {
  const { ledger, lifecycle } = await committed();
  const status = new Map(ledger.obligations.map((row) => [row.id, row.status]));
  const scoped = lifecycle.sourceOutcomes.filter((row) => row.status === "out of scope (ADR 0004)").map((row) => row.id);
  const ledgerScoped = lifecycle.sourceOutcomes.filter((row) => status.get(row.id) === "out-of-scope").map((row) => row.id);

  assert.deepEqual(scoped, ledgerScoped);
  assert.deepEqual([...new Set(lifecycle.sourceOutcomes.filter((row) => row.source === "src/usage-api.ts").map((row) => row.status))], ["out of scope (ADR 0004)"]);
});

for (const [name, mutate] of [
  ["scope decision reason", (row) => { row.scopeDecision.reason = "different reason"; }],
  ["scope status", (row) => { row.status = "pending"; }],
  ["retained evidence", (row) => { row.evidence = []; }],
]) {
  test(`branchLedgerIdentity preserves an out-of-scope row's ${name} rather than erasing it`, async () => {
    const { ledger, observations } = await committed();
    const altered = { ...ledger, obligations: [...ledger.obligations] };
    const index = ledger.obligations.findIndex((row) => row.status === "out-of-scope" && row.evidence.length > 0);
    altered.obligations[index] = structuredClone(ledger.obligations[index]);

    assert.ok(index >= 0);
    mutate(altered.obligations[index]);

    assert.notDeepEqual(branchLedgerIdentity(altered, observations), branchLedgerIdentity(ledger, observations));
  });
}

async function ledgerCheckCopy(t) {
  const project = fileURLToPath(new URL("../../", import.meta.url));
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, "ledger-check-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await Promise.all(["package.json", "probes", "status", "extensions", "docs/compatibility/usage"].map((path) => cp(join(project, path), join(root, path), { recursive: true })));
  await symlink(join(project, "node_modules"), join(root, "node_modules"));
  const ledgerPath = join(root, "docs/compatibility/usage/branch-ledger.json.gz");
  return { ledgerPath, check: () => promisify(execFile)(process.execPath, ["probes/usage/branch-ledger.mjs", "--check"], { cwd: root, maxBuffer: 16 * 1024 * 1024 }) };
}

const failsClosed = (error) => error.code === 1 && /AssertionError/u.test(error.stderr);
const sumTerms = (terms) => terms.reduce((sum, term) => sum + term.sign * term.v8Witness.range.count, 0);
const observedCountEvidence = (ledger) => ledger.obligations.filter((row) => row.status === "observed").flatMap((row) => row.evidence).filter((entry) => entry.countRule);
const functionEntry = (pointer) => pointer.replace(/\/ranges\/\d+$/u, "/ranges/0");
const enclosedPositiveTerm = (observations) => (term) => term.sign > 0 && term.v8Range !== functionEntry(term.v8Range) && rawAt(observations, functionEntry(term.v8Range)).count > term.v8Witness.range.count;

for (const [name, forge] of [
  ["its subtracted terms dropped and its bound raised to match", (ledger) => {
    const evidence = observedCountEvidence(ledger).find((entry) => entry.terms.some((term) => term.sign < 0 && term.v8Witness.range.count > 0));
    evidence.terms = evidence.terms.filter((term) => term.sign > 0);
    evidence.lowerBound = sumTerms(evidence.terms);
    return evidence;
  }],
  ["a positive term moved to its enclosing function-entry counter", (ledger, observations) => {
    const evidence = observedCountEvidence(ledger).find((entry) => entry.terms.some(enclosedPositiveTerm(observations)));
    const term = evidence.terms.find(enclosedPositiveTerm(observations));
    term.v8Range = functionEntry(term.v8Range);
    term.v8Witness.range = { ...rawAt(observations, term.v8Range) };
    evidence.lowerBound = sumTerms(evidence.terms);
    return evidence;
  }],
]) {
  test(`ledger check command rejects a committed count witness with ${name}`, async (t) => {
    const { ledgerPath, check } = await ledgerCheckCopy(t);
    const { ledger, observations } = await committed();
    const forged = structuredClone(ledger);
    await writeLedger(ledgerPath, `${JSON.stringify(ledger, null, 2)}\n`);
    const { stdout } = await check();

    assert.ok(observedCountEvidence(ledger).length > 0);
    assert.ok(forge(forged, observations).lowerBound > 0);
    assert.match(stdout, /usage source ledger: \d+ observed outcomes/u);
    await writeLedger(ledgerPath, `${JSON.stringify(forged, null, 2)}\n`);

    await assert.rejects(check(), failsClosed);
  });
}
