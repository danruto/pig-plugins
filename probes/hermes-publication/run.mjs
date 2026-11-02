/** Disposable mixed-process evidence runner. Frozen-writer failures are findings, never qualification passes. */
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync, mkdirSync, mkdtempSync } from "node:fs";
import { createInterface } from "node:readline";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { runModel } from "./model.mjs";

const project = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
if (process.argv.some((arg, i) => arg === "--source" && (!process.argv[i + 1] || process.argv[i + 1].startsWith("-")))) {
  console.error("--source requires a source path operand");
  process.exit(1);
}
const sourceArg = process.argv.indexOf("--source");
const source = sourceArg < 0 ? process.env.HERMES_PUBLICATION_SOURCE : process.argv[sourceArg + 1];
const scratch = path.join(project, ".pb/probes/10");
mkdirSync(scratch, { recursive: true });
const model = runModel();
const retainedPath = path.join(project, "docs/compatibility/hermes-publication-model.json");
const retained = { notice: "GENERATED FROM CODE — DO NOT EDIT. Run node probes/hermes-publication/run.mjs --write-evidence to regenerate.", status: model.status, bound: model.bound, cases: {}, counterexamples: {} };
for (const [name, result] of Object.entries(model.cases)) {
  retained.cases[name] = { states: result.states, edges: result.edges, terminals: result.terminals, exhausted: result.exhausted, events: result.events };
  for (const [failure, trace] of Object.entries(result.counterexamples)) {
    if (!(failure in retained.counterexamples) || trace.length < retained.counterexamples[failure].trace.length) retained.counterexamples[failure] = { family: name, trace };
  }
}
const retainedJson = `${JSON.stringify(retained, null, 2)}\n`;
if (process.argv.includes("--write-evidence")) writeFileSync(retainedPath, retainedJson);
if (process.argv.includes("--check-evidence")) assert.equal(readFileSync(retainedPath, "utf8"), retainedJson, "model evidence drift; regenerate and review retained traces");
const report = { model, processes: { status: "NOT_RUN", reason: "pass --source with reconstructed pinned source and native dependencies" } };

function sourceReceipt(root) {
  const manifest = JSON.parse(readFileSync(path.join(project, "docs/compatibility/hermes-baseline.json"), "utf8"));
  const hashes = {};
  for (const [name, expected] of Object.entries(manifest.patchedSourceHashes)) {
    const actual = createHash("sha256").update(readFileSync(path.join(root, name))).digest("hex");
    assert.equal(actual, expected, `frozen source mismatch ${name}`);
    hashes[name] = actual;
  }
  execFileSync("python3", [path.join(project, "probes/capture-hermes.py"), root, "--verify-source-only"], { cwd: project, env: { ...process.env, TMPDIR: scratch }, stdio: "pipe" });
  return { commit: manifest.upstreamCommit, hashes, runtime: process.version, platform: `${process.platform}/${process.arch}` };
}

function start({ command, args, root, name }) {
  const env = { PATH: process.env.PATH, HOME: path.join(root, "home"), TMPDIR: scratch, GOMAXPROCS: "2", GOFLAGS: "-p=2", HERMES_PROBE_ROOT: root, HERMES_PACKAGE: source, PI_CODING_AGENT_DIR: path.join(root, "pi"), PIG_HOME: path.join(root, "pig"), PIG_CODING_AGENT_DIR: path.join(root, "pig-agent"), XDG_CONFIG_HOME: path.join(root, "config") };
  const child = spawn(command, args, { cwd: project, env, stdio: ["pipe", "pipe", "pipe"] });
  const log = [];
  child.stderr.on("data", (chunk) => log.push(chunk.toString()));
  const lines = createInterface({ input: child.stdout })[Symbol.asyncIterator]();
  let error;
  child.on("error", (e) => { error = e; });
  const exit = new Promise((resolve) => child.on("close", (code, signal) => resolve({ code, signal })));
  const receipt = path.join(root, `${name}-pid.json`);
  writeFileSync(receipt, JSON.stringify({ pid: child.pid, command, args, state: "running" }));
  async function next() {
    let timer;
    try {
      const line = await Promise.race([lines.next(), new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`${name} barrier timeout`)), 30000); })]);
      assert(!line.done, `${name} exited before event: ${log.join("")}`);
      return JSON.parse(line.value);
    } finally { clearTimeout(timer); }
  }
  async function stop() {
    child.stdin.end();
    const timer = setTimeout(() => child.kill("SIGKILL"), 2000);
    const status = await exit;
    clearTimeout(timer);
    writeFileSync(path.join(root, `${name}-stderr.log`), log.join(""));
    writeFileSync(receipt, JSON.stringify({ pid: child.pid, state: "exited", ...status }));
    assert(!error && status.code === 0, `${name} failed: ${error ?? JSON.stringify(status)} ${log.join("")}`);
  }
  return { child, next, stop };
}

async function withPeer(launch, operation) {
  const peer = start(launch);
  let failure;
  try { return await operation(peer); }
  catch (error) { failure = error; throw error; }
  finally {
    try { await peer.stop(); }
    catch (error) {
      if (failure) throw new AggregateError([failure, error], `${launch.name} operation and cleanup failed`);
      throw error;
    }
  }
}

function tsArgs(action) {
  return ["--import", "tsx", path.join(project, "probes/hermes-publication/typescript-peer.mjs"), action];
}

async function seed(root) {
  await withPeer({ command: "node", args: tsArgs("seed"), root, name: "seed" }, async (p) => { assert.equal((await p.next()).event, "seeded"); });
}

function assertMemorySnapshot(result, expected) {
  assert.deepEqual(result.rows, expected.map((content) => ({ project: null, target: "memory", category: null, content, hit_count: 0, last_hit_at: null })));
  assert.equal(result.result.entry_count, 3);
  const entryBytes = result.doc.split("\n§\n");
  assert.deepEqual(entryBytes.map((entry) => entry.replace(/ <!-- created=\d{4}-\d{2}-\d{2}, last=\d{4}-\d{2}-\d{2} -->$/, "")), ["synthetic seed", "synthetic snapshot primer", "typescript accepted"]);
  const metadata = result.metadata.find((row) => row.key === 'mdsync:v1:["memory",null]');
  assert(metadata);
  const mirrored = entryBytes.slice(0, expected.length);
  assert.deepEqual(JSON.parse(metadata.value), { sha256: createHash("sha256").update(JSON.stringify(mirrored)).digest("hex"), entryCount: expected.length });
}

async function memoryCase(binary, action) {
  const root = mkdtempSync(path.join(scratch, `${action}-`));
  await seed(root);
  const observation = await withPeer({ command: binary, args: [], root, name: "go" }, async (go) => {
    assert.equal((await go.next()).event, "ready");
    return withPeer({ command: "node", args: tsArgs(action), root, name: "typescript" }, async (ts) => {
      if (action === "race") {
        assert.equal((await ts.next()).event, "checked-before-rename");
        go.child.stdin.write("publish\n");
        assert.equal((await go.next()).accepted, true);
        ts.child.stdin.write("continue\n");
      }
      const result = await ts.next();
      assert.equal(result.event, "result");
      assert.equal(result.result.success, true);
      assertMemorySnapshot(result, action === "partial" ? ["synthetic seed", "synthetic snapshot primer"] : ["synthetic seed", "synthetic snapshot primer", "typescript accepted"]);
      if (action === "serial") {
        go.child.stdin.write("publish\n");
        assert.equal((await go.next()).accepted, true);
      }
      go.child.stdin.write("rows\n");
      const peerRows = await go.next();
      const document = readFileSync(path.join(root, "memory/MEMORY.md"), "utf8");
      if (action === "race") {
        assert(!document.includes("go accepted"), "retained counterexample changed; review protocol before changing this probe");
        assert(!peerRows.some((row) => row.content === "go accepted"));
        assert.equal(result.successorStillOwned, true, "old TypeScript release removed successor token");
      }
      if (action === "serial") {
        assert(document.includes("go accepted") && document.includes("typescript accepted"));
        assert(peerRows.some((row) => row.content === "go accepted" && row.hit_count === 7 && row.project === null));
      }
      let repair = null;
      if (action === "partial") {
        assert(result.result.warning.includes("memory-sync-markdown"));
        assert(document.includes("typescript accepted") && !peerRows.some((row) => row.content === "typescript accepted"));
        assert.equal((await ts.next()).event, "before-repair");
        ts.child.stdin.write("continue\n");
        repair = await ts.next();
        assert.equal(repair.event, "repaired");
        assert(repair.rows.some((row) => row.content === "typescript accepted" && row.project === null));
      }
      go.child.stdin.write("release\n");
      assert.equal((await go.next()).released, true);
      return { outcome: action === "race" ? "BLOCKED_LOST_ACCEPTED_UPDATE" : action === "partial" ? "PARTIAL_SUCCESS_REPAIRED" : "SERIAL_SUCCESS", typescript: result, document, peerRows, repair };
    });
  });
  writeFileSync(path.join(root, "observation.json"), JSON.stringify(observation, null, 2));
  return { root: path.relative(project, root), ...observation };
}

async function recoveryCase(binary) {
  const root = mkdtempSync(path.join(scratch, "recovery-"));
  await seed(root);
  const observation = await withPeer({ command: binary, args: [], root, name: "go" }, async (go) => {
    assert.equal((await go.next()).event, "ready");
    return withPeer({ command: "node", args: tsArgs("recovery"), root, name: "typescript" }, async (ts) => {
      assert.equal((await ts.next()).event, "owner-checked-before-recovery-rename");
      go.child.stdin.write("insert\n");
      assert.equal((await go.next()).accepted, true);
      go.child.stdin.write("takeover-recovery\n");
      assert.equal((await go.next()).acquired, true);
      writeFileSync(path.join(root, "continue-recovery"), "continue");
      const result = await ts.next();
      assert.equal(result.result.strategy, "rebuilt");
      assert.equal(result.oldRenewRejected, true);
      assert.equal(result.successorStillOwned, true);
      assert(!result.rows.some((row) => row.content === "go accepted"));
      go.child.stdin.write("rows\n");
      const peerRows = await go.next();
      assert(peerRows.some((row) => row.content === "go accepted" && row.hit_count === 7));
      return { outcome: "BLOCKED_OPEN_HANDLE_ACCEPTED_ROW_DETACHED", typescript: result, peerRows };
    });
  });
  writeFileSync(path.join(root, "observation.json"), JSON.stringify(observation, null, 2));
  return { root: path.relative(project, root), ...observation };
}

if (source) {
  const provenance = sourceReceipt(source);
  const binary = path.join(scratch, "publication-peer");
  execFileSync("go", ["build", "-modcacherw", "-o", binary, "./publication-peer"], { cwd: path.join(project, "probes/hermes-sqlite"), env: { ...process.env, TMPDIR: scratch, GOMAXPROCS: "2", GOFLAGS: "-p=2" }, stdio: "inherit" });
  const cases = [];
  for (const action of ["serial", "race", "partial"]) cases.push(await memoryCase(binary, action));
  cases.push(await recoveryCase(binary));
  report.processes = { status: "BLOCKED", provenance, cases };
}
writeFileSync(path.join(scratch, "publication-report.json"), JSON.stringify(report, null, 2));
console.log(JSON.stringify({ status: model.status, cases: Object.fromEntries(Object.entries(model.cases).map(([name, c]) => [name, { states: c.states, edges: c.edges, exhausted: c.exhausted, counterexamples: Object.keys(c.counterexamples) }])), processes: report.processes.status, report: ".pb/probes/10/publication-report.json" }, null, 2));
if (process.argv.includes("--qualify") && model.status === "BLOCKED") process.exitCode = 2;
