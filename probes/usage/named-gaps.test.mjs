/** Owner decision controls: the committed ledger names exactly the owner-listed unreachable outcomes as pending gaps, and source-composition replay witnesses stay confined to registered backend-source windows. */
import assert from "node:assert/strict";
import test from "node:test";
import { cp, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { execFile } from "node:child_process";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { readLedger } from "./ledger-artifact.mjs";

const artifact = async (name) => {
  const url = new URL(`../../docs/compatibility/usage/${name}`, import.meta.url);
  return JSON.parse(name === "branch-ledger.json" ? await readLedger(new URL(`${url}.gz`)) : await readFile(url, "utf8"));
};
const NAMED_GAPS = "docs/compatibility/usage.md#named-gaps";

const ownerRows = [
  ["src/oauth-credential-source.ts", 45, "TryStatement", "try-threw"],
  ["src/oauth-credential-source.ts", 47, "CatchClause", "entered"],
  ["src/oauth-credential-source.ts", 77, "TryStatement", "try-threw"],
  ["src/oauth-credential-source.ts", 79, "CatchClause", "entered"],
  ["status/usage.ts", 111, "BinaryExpression", "rhs-evaluated"],
  ["status/usage.ts", 111, "BinaryExpression", "rhs-evaluated"],
  ["status/usage.ts", 111, "BinaryExpression", "rhs-skipped"],
  ["status/usage.ts", 111, "BinaryExpression", "rhs-skipped"],
  ["status/usage.ts", 113, "BinaryExpression", "rhs-evaluated"],
  ["status/usage.ts", 113, "BinaryExpression", "rhs-skipped"],
  ["status/usage.ts", 114, "IfStatement", "false"],
  ["status/usage.ts", 134, "IfStatement", "true"],
  ["status/usage.ts", 134, "BinaryExpression", "rhs-skipped"],
  ["status/usage.ts", 140, "BinaryExpression", "rhs-evaluated"],
  ["status/usage.ts", 140, "BinaryExpression", "rhs-evaluated"],
  ["status/usage.ts", 140, "BinaryExpression", "rhs-evaluated"],
  ["status/usage.ts", 140, "BinaryExpression", "rhs-evaluated"],
  ["status/usage.ts", 140, "PropertyAccessExpression", "non-nullish", "ctx.model?.provider"],
  ["status/usage.ts", 140, "PropertyAccessExpression", "nullish", "ctx.model?.provider"],
  ["status/usage.ts", 139, "PropertyAccessExpression", "nullish", "response?.report"],
  ["status/usage.ts", 140, "IfStatement", "true"],
  ["status/usage.ts", 140, "PropertyAccessExpression", "nullish", "response?.status"],
  ["status/usage.ts", 147, "IfStatement", "true"],
  ["status/usage.ts", 147, "IfStatement", "false"],
  ["status/usage.ts", 147, "BinaryExpression", "rhs-evaluated"],
  ["status/usage.ts", 147, "BinaryExpression", "rhs-skipped"],
  ["src/providers/kimi-coding.ts", 134, "PropertyAccessExpression", "nullish", "row?.reset_time"],
  ["src/providers/kimi-coding.ts", 180, "IfStatement", "true"],
  ["src/providers/kimi-coding.ts", 183, "IfStatement", "true"],
  ["src/providers/kimi-coding.ts", 183, "BinaryExpression", "rhs-skipped"],
  ["src/providers/kimi-coding.ts", 232, "ConditionalExpression", "false"],
  ["src/providers/kimi-coding.ts", 232, "BinaryExpression", "rhs-skipped"],
  ["src/codex-resets.ts", 103, "IfStatement", "true"],
];
const reportPathLines = [46, 66];
const key = (row, withInput) => [row.source, row.line, row.kind, row.outcome, withInput ? row.input : ""].join("|");

test("committed ledger names exactly the owner-decided outcomes as pending named gaps", async () => {
  const ledger = await artifact("branch-ledger.json");
  const named = ledger.obligations.filter((row) => Object.hasOwn(row, "namedGap") || /^Named gap:/u.test(row.pendingReason ?? ""));
  const reportPath = ledger.obligations.filter((row) => row.source === "status/usage.ts" && row.line >= reportPathLines[0] && row.line <= reportPathLines[1]);
  const listed = ownerRows.map(([source, line, kind, outcome, input]) => ({ source, line, kind, outcome, input }));
  const byInput = new Set(listed.filter((row) => row.input).map((row) => key(row, false)));
  const expected = [...reportPath.map((row) => key(row, false)), ...listed.map((row) => key(row, Boolean(row.input)))].sort();
  const actual = named.map((row) => key(row, byInput.has(key(row, false)))).sort();

  assert.equal(reportPath.length, 30);
  assert.deepEqual(actual, expected);
  assert.deepEqual(named.map((row) => [row.status, row.evidence.length, row.namedGap.decision, row.namedGap.nativeObligation.length > 0, /^Named gap: \S/u.test(row.pendingReason), Object.hasOwn(row, "scopeDecision")]),
    named.map(() => ["pending", 0, NAMED_GAPS, true, true, false]));
  assert.equal(new Set(named.map((row) => row.pendingReason)).size > 1, true, "Each owner-listed group records its own invariant");
});

test("source-composition replay witnesses come only from tagged backend-source blocks in registered windows", async () => {
  const [ledger, observations] = await Promise.all([artifact("branch-ledger.json"), artifact("branch-observations.json")]);
  const paths = new Map(observations.scripts.map((script) => [script.sha256, script.path]));
  const tagged = observations.observations.flatMap((window) => window.blocks.filter((block) => block.composition).map((block) => [window.id, paths.get(block.script), block.composition]));
  const witnesses = ledger.obligations.flatMap((row) => row.evidence.filter((evidence) => evidence.composition));

  assert.ok(tagged.length > 0 && witnesses.length > 0);
  assert.deepEqual(tagged.filter(([id, path, composition]) => !(id.startsWith("lifecycle:") && path.startsWith("src/") && composition === "source-replay")), []);
  assert.deepEqual(witnesses.filter((evidence) => !(evidence.implementation === "source" && evidence.composition === "source-replay" && evidence.fixtureId.startsWith("lifecycle:"))), []);
});

test("ledger check rejects a source-replay tag on an installed-dist block", async (t) => {
  const project = fileURLToPath(new URL("../../", import.meta.url));
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const root = await mkdtemp(join(scratch, "named-gaps-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  await Promise.all(["package.json", "probes", "status", "extensions", "docs/compatibility/usage"].map((path) => cp(join(project, path), join(root, path), { recursive: true })));
  await symlink(join(project, "node_modules"), join(root, "node_modules"));
  const observations = await artifact("branch-observations.json");
  const paths = new Map(observations.scripts.map((script) => [script.sha256, script.path]));
  const block = observations.observations.flatMap((window) => window.id.startsWith("lifecycle:") ? window.blocks : []).find((item) => paths.get(item.script).startsWith("dist/"));
  block.composition = "source-replay";
  await writeFile(join(root, "docs/compatibility/usage/branch-observations.json"), `${JSON.stringify(observations)}\n`);

  await assert.rejects(promisify(execFile)(process.execPath, ["probes/usage/branch-ledger.mjs", "--check"], { cwd: root, maxBuffer: 16 * 1024 * 1024 }),
    (error) => error.code === 1 && /Invalid composition block: lifecycle:/u.test(error.stderr));
});
