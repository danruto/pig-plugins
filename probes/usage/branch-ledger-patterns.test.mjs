/** Real-V8 negative controls for count rules around destructuring: a default value inside a pattern is evaluated only when its slot is undefined, so it never borrows the completion count of the statement holding the pattern. */
import assert from "node:assert/strict";
import test from "node:test";
import { Session } from "node:inspector";
import { Script } from "node:vm";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, writeFile, rm } from "node:fs/promises";
import { resolve, join } from "node:path";
import { buildBranchLedger } from "./branch-ledger.mjs";

const sha = (text) => createHash("sha256").update(text).digest("hex");
let measurementId = 0;

async function measuredCalls(text, calls) {
  const filename = `ledger-pattern-${measurementId++}.js`;
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

async function patternLedger(t, source, calls) {
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "ledger-pattern-test-"));
  t.after(() => rm(project, { recursive: true, force: true }));
  const backend = join(project, "node_modules/@narumitw/pi-usage");
  await mkdir(join(backend, "src"), { recursive: true });
  await writeFile(join(backend, "src/query.ts"), source);
  const provenance = { dependencies: { "node_modules/@narumitw/pi-usage": { installed: { files: { "src/query.ts": { sha256: sha(source) } } } } } };
  const runtime = `(${source})`;
  const fixtures = [{ id: "synthetic", inputs: { calls: calls.length }, evidence: ["expected.json#/results/0", "expected.json#/results/1"], sourceBranchEvidence: "branch-ledger.json#/fixtureMappings/0" }];
  const observations = { scripts: [{ path: "src/query.ts", text: runtime, sha256: sha(runtime) }],
    observations: [{ id: "synthetic", blocks: [{ script: sha(runtime), functions: await measuredCalls(runtime, calls) }] }] };
  const expected = { results: [{ id: "synthetic", implementation: "source" }, { id: "synthetic", implementation: "dist" }], requests: [] };
  return buildBranchLedger(project, project, { symbols: [], semanticFixtures: fixtures }, observations, expected, provenance);
}

const patternCases = [
  ["skipped || RHS in an array assignment-pattern default", "function run(x) { let a; [a = x.b || 0] = x.arr; if (a) return 1; return 2; }", "BinaryExpression", "x.b || 0", "rhs-skipped", [{ arr: [5] }], "pending"],
  ["first optional link nullish in an object assignment-pattern default", "function run(x) { let a; ({ k: a = x?.y } = x.o); if (a) return 1; return 2; }", "PropertyAccessExpression", "x?.y", "nullish", [{ o: { k: 5 } }], "pending"],
  ["skipped || RHS in a nested assignment-pattern default", "function run(x) { let a; [[a = x.b || 0]] = x.arr; if (a) return 1; return 2; }", "BinaryExpression", "x.b || 0", "rhs-skipped", [{ arr: [[5]] }], "pending"],
  ["skipped || RHS on the value side of an assignment pattern", "function run(x) { let a; [a] = x.arr || [0]; if (a) return 1; return 2; }", "BinaryExpression", "x.arr || [0]", "rhs-skipped", [{ arr: [5] }], "observed"],
];

for (const [name, source, kind, input, outcome, calls, status] of patternCases) {
  test(`count rule: ${name} is ${status}`, async (t) => {
    const ledger = await patternLedger(t, source, calls);
    const rows = ledger.obligations.filter((row) => row.kind === kind && row.input === input && row.outcome === outcome);
    assert.equal(rows.length, 1);
    assert.equal(rows[0].status, status);
    if (status === "observed") assert.ok(rows[0].evidence.every((entry) => entry.lowerBound > 0));
  });
}
