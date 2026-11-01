/** Real-V8 controls for source/runtime branch identity: the runtime Jiti executes erases TypeScript-only syntax and redundant parentheses, so a condition matches its source only when the erased trees agree. */
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
  const filename = `ledger-identity-${measurementId++}.js`;
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

async function erasedLedger(t, source, erased, calls) {
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const project = await mkdtemp(join(scratch, "ledger-identity-test-"));
  t.after(() => rm(project, { recursive: true, force: true }));
  const backend = join(project, "node_modules/@narumitw/pi-usage");
  await mkdir(join(backend, "src"), { recursive: true });
  await writeFile(join(backend, "src/query.ts"), source);
  const provenance = { dependencies: { "node_modules/@narumitw/pi-usage": { installed: { files: { "src/query.ts": { sha256: sha(source) } } } } } };
  const runtime = `(${erased})`;
  const fixtures = [{ id: "synthetic", inputs: { calls: calls.length }, evidence: ["expected.json#/results/0", "expected.json#/results/1"], sourceBranchEvidence: "branch-ledger.json#/fixtureMappings/0" }];
  const observations = { scripts: [{ path: "src/query.ts", text: runtime, sha256: sha(runtime) }],
    observations: [{ id: "synthetic", blocks: [{ script: sha(runtime), functions: await measuredCalls(runtime, calls) }] }] };
  const expected = { results: [{ id: "synthetic", implementation: "source" }, { id: "synthetic", implementation: "dist" }], requests: [] };
  return buildBranchLedger(project, project, { symbols: [], semanticFixtures: fixtures }, observations, expected, provenance);
}

const identityCases = [
  ["an as-cast and its parentheses erased", "function run(value) { if (!Number.isSafeInteger(value) || (value as number) < 0) return 0; return value; }",
    "function run(value) { if (!Number.isSafeInteger(value) || value < 0) return 0; return value; }", "!Number.isSafeInteger(value) || (value as number) < 0", [1, -1], ["observed", "observed"]],
  ["a non-null assertion erased", "function run(x) { if (x!.y > 0) return 1; return 2; }", "function run(x) { if (x.y > 0) return 1; return 2; }", "x!.y > 0", [{ y: 1 }, { y: 0 }], ["observed", "observed"]],
  ["call type arguments erased", "function run(xs) { if (new Set<string>(xs).size > 1) return 1; return 2; }", "function run(xs) { if (new Set(xs).size > 1) return 1; return 2; }", "new Set<string>(xs).size > 1", [["a", "b"], ["a"]], ["observed", "observed"]],
  ["a satisfies clause erased", "function run(x) { if ((x satisfies number) > 1) return 1; return 2; }", "function run(x) { if (x > 1) return 1; return 2; }", "(x satisfies number) > 1", [2, 0], ["observed", "observed"]],
  ["a different erased operator", "function run(value) { if ((value as number) < 0) return 1; return 2; }", "function run(value) { if (value <= 0) return 1; return 2; }", "(value as number) < 0", [-1, 1], ["pending", "pending"]],
  ["a different erased operand", "function run(x) { if (x!.y > 0) return 1; return 2; }", "function run(x) { if (x.z > 0) return 1; return 2; }", "x!.y > 0", [{ z: 1 }, { z: 0 }], ["pending", "pending"]],
];

for (const [name, source, erased, input, calls, statuses] of identityCases) {
  test(`source identity: ${name} leaves the if ${statuses.join("/")}`, async (t) => {
    const ledger = await erasedLedger(t, source, erased, calls);
    const rows = ledger.obligations.filter((row) => row.kind === "IfStatement" && row.input === input);
    assert.deepEqual(rows.map((row) => [row.outcome, row.status]), [["true", statuses[0]], ["false", statuses[1]]]);
    for (const row of rows.filter((item) => item.status === "observed")) assert.ok(row.evidence.every((entry) => entry.v8Range || entry.lowerBound > 0));
  });
}
