/** Public CLI observations for the frozen shared-storage safety evidence; real-process cases are opt-in. */
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { before, test } from "node:test";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const scratch = resolve(process.env.HERMES_PUBLICATION_VALIDATION_ROOT ?? join(root, ".pb/probes/10-validation/evidence/head"));
const source = process.env.HERMES_PUBLICATION_SOURCE;
const modelEnvironment = { ...process.env };
delete modelEnvironment.HERMES_PUBLICATION_SOURCE;
const processOptions = { skip: !source && "set HERMES_PUBLICATION_SOURCE to reconstructed public source" };
const families = ["crash", "missing", "partial", "pid-reuse", "recovery", "release", "renewal", "reuse", "unknown-incarnation"];
let report;
let regression;
let qualification;

function run(args, env = process.env) {
  return spawnSync(process.execPath, ["probes/hermes-publication/run.mjs", ...args], {
    cwd: root,
    env,
    encoding: "utf8",
    timeout: 180000,
    maxBuffer: 8 * 1024 * 1024,
  });
}

before(() => {
  mkdirSync(scratch, { recursive: true });
  regression = run(["--check-evidence", ...(source ? ["--source", source] : [])]);
  writeFileSync(join(scratch, "runner.stdout.json"), regression.stdout ?? "");
  writeFileSync(join(scratch, "runner.stderr"), regression.stderr ?? "");
  assert.equal(regression.status, 0, `public runner structural seam: ${regression.stderr}`);
  report = JSON.parse(readFileSync(join(root, ".pb/probes/10/publication-report.json"), "utf8"));
  writeFileSync(join(scratch, "process-report.json"), JSON.stringify(report, null, 2));
  qualification = run(["--qualify"], modelEnvironment);
  writeFileSync(join(scratch, "qualify.stdout.json"), qualification.stdout ?? "");
  writeFileSync(join(scratch, "qualify.stderr"), qualification.stderr ?? "");
});

function lastEdge(witness) {
  return [witness.at(-2).state, witness.at(-1).state];
}

function processCase(outcome) {
  const value = report.processes.cases.find((item) => item.outcome === outcome);
  assert(value, `missing real-process outcome ${outcome}`);
  return value;
}

function documentFor(value) {
  return readFileSync(join(root, value.root, "memory/MEMORY.md"), "utf8");
}

function rowsFor(value) {
  return rowsAt(join(root, value.root, "memory/sessions.db"));
}

function rowsAt(path) {
  const Database = createRequire(join(source, "package.json"))("better-sqlite3");
  const db = new Database(path, { readonly: true, fileMustExist: true });
  try {
    return db.prepare("SELECT project, target, category, content, hit_count, last_hit_at FROM memories ORDER BY id").all();
  } finally {
    db.close();
  }
}

function canonicalRows(rows) {
  return rows.map((row) => ({ ...row, last_hit_at: row.last_hit_at === null ? null : Date.parse(row.last_hit_at) }));
}

function storedMetadata(value) {
  const Database = createRequire(join(source, "package.json"))("better-sqlite3");
  const db = new Database(join(root, value.root, "memory/sessions.db"), { readonly: true, fileMustExist: true });
  try {
    return JSON.parse(db.prepare("SELECT value FROM extension_metadata WHERE key = ?").get('mdsync:v1:["memory",null]').value);
  } finally {
    db.close();
  }
}

function contents(rows) {
  return rows.map((row) => row.content).sort();
}

function sha256(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function sourceTreeHash(path, manifest) {
  const files = [...manifest.sourceFiles];
  function walk(relative) {
    for (const entry of readdirSync(join(path, relative), { withFileTypes: true })) {
      const child = `${relative}/${entry.name}`;
      if (entry.isDirectory()) walk(child);
      else files.push(child);
    }
  }
  manifest.sourceRoots.forEach(walk);
  const pairs = files.sort().map((relative) => [relative, sha256(join(path, relative))]);
  return createHash("sha256").update(JSON.stringify(pairs)).digest("hex");
}

function exitedReceipts() {
  return report.processes.cases.flatMap((value) => ["seed", "go", "typescript"].map((name) =>
    JSON.parse(readFileSync(join(root, value.root, `${name}-pid.json`), "utf8"))));
}

test("AC-model: CLI explores each bounded fault family without a reported cutoff", () => {
  const requirements = {
    reuse: [":check-fingerprint", ":rename", "clock:-2", "clock:2"],
    missing: [":link", ":atomic-link-conflict"],
    renewal: [":renew-ok", ":renew-io-failure", ":renew-old-token-rejected"],
    release: [":release-fails-pending", ":retry-pending-release"],
    partial: [":reconcile-fails-report-warning", ":repair-from-document"],
    crash: [":crash", ":restart-same-pid-new-incarnation"],
    "pid-reuse": [":crash", ":restart-same-pid-new-incarnation"],
    "unknown-incarnation": [":crash", ":restart-same-pid-new-incarnation"],
    recovery: [":check-recovery-owner", ":rename-main-to-backup", ":rename-wal-to-backup", ":rename-shm-to-backup", ":rename-rebuilt-main", ":commit-on-open-handle", ":crash-between-file-renames", ":publish-fails-restore-backup", ":publish-and-restore-fail-backup-retained"],
  };

  assert.deepEqual(Object.keys(report.model.cases).sort(), families);
  Object.entries(requirements).forEach(([name, requiredEvents]) => {
    const c = report.model.cases[name];
    assert.equal(c.exhausted, true);
    assert(c.states > 1);
    assert(c.edges >= c.states - 1);
    assert(requiredEvents.every((event) => c.events.some((actual) => actual.endsWith(event))));
  });
});

test("AC-lost-update: retained explored witness removes an already accepted addition", () => {
  const [before, after] = lastEdge(report.model.cases.reuse.counterexamples["lost-accepted-update"]);

  assert(before.accepted > 0);
  assert.notEqual(before.accepted & after.doc, before.accepted);
  assert.equal(after.accepted & before.accepted, before.accepted);
});

test("AC-stale-plan: a checked fingerprint does not fence a later stale-version publication", () => {
  const witness = report.model.cases.reuse.counterexamples["stale-plan"];
  const [before, after] = lastEdge(witness);
  const writer = before.writers[Number(witness.at(-1).event.split(":")[0])];

  assert(witness.some((step) => step.event.endsWith(":check-fingerprint")));
  assert(writer.version < before.version);
  assert.equal(after.version, before.version + 1);
  assert.equal(after.doc, writer.plan);
});

test("AC-detached-row: rebuilt-path loss is distinct from physical readable-row destruction", () => {
  const last = report.model.cases.recovery.counterexamples["accepted-row-detached-from-current-db"].at(-1).state;

  assert.equal(last.recovery, "done");
  assert.notEqual(last.files[last.active] & last.accepted, last.accepted);
  assert.equal(last.files.reduce((a, b) => a | b, 0) & last.accepted, last.accepted);
});

test("AC-blocked: evidence executes successfully while unsafe qualification refuses", () => {
  assert.equal(regression.status, 0);
  assert.equal(report.model.status, "BLOCKED");
  assert.equal(qualification.status, 2);
  assert.equal(JSON.parse(qualification.stdout).status, "BLOCKED");
});

[
  { name: "missing-operand", args: ["--check-evidence", "--source"] },
  { name: "flag-as-operand", args: ["--source", "--check-evidence"] },
].forEach(({ name, args }) => {
  test(`AC-repair-3: explicit source rejects ${name}`, () => {
    const result = run(args, modelEnvironment);
    writeFileSync(join(scratch, `${name}.stdout.json`), result.stdout ?? "");
    writeFileSync(join(scratch, `${name}.stderr`), result.stderr ?? "");

    assert.equal(result.error, undefined);
    assert.equal(result.signal, null);
    assert.notEqual(result.status, 0);
    assert.doesNotMatch(result.stderr, /ENOENT/);
  });
});

test("AC-serial: real serial control persists both additions in their shared scope", processOptions, () => {
  const serial = processCase("SERIAL_SUCCESS");
  const document = documentFor(serial);
  const rows = rowsFor(serial);

  assert.equal(serial.typescript.result.success, true);
  assert.match(document, /typescript accepted/);
  assert.match(document, /go accepted/);
  assert.deepEqual(contents(rows), ["go accepted", "synthetic seed", "synthetic snapshot primer", "typescript accepted"]);
  assert.deepEqual(canonicalRows(rows), canonicalRows(serial.peerRows));
  assert(rows.every((row) => row.project === null && row.target === "memory"));
});

test("AC-race: reported stale overwrite agrees with surviving document and search fingerprint", processOptions, () => {
  const race = processCase("BLOCKED_LOST_ACCEPTED_UPDATE");
  const document = documentFor(race);
  const rows = rowsFor(race);
  const metadata = JSON.parse(race.typescript.metadata.find((row) => row.key === 'mdsync:v1:["memory",null]').value);

  assert.equal(race.typescript.result.success, true);
  assert.match(document, /typescript accepted/);
  assert.doesNotMatch(document, /go accepted/);
  assert.deepEqual(contents(rows), ["synthetic seed", "synthetic snapshot primer", "typescript accepted"]);
  assert.deepEqual(rows, race.peerRows);
  assert.equal(race.typescript.successorStillOwned, true);
  assert.equal(metadata.entryCount, rows.length);
  assert.deepEqual(metadata, storedMetadata(race));
});

test("AC-partial: saved document warns about stale rows and forced repair persists the addition", processOptions, () => {
  const partial = processCase("PARTIAL_SUCCESS_REPAIRED");
  const document = documentFor(partial);
  const rows = rowsFor(partial);

  assert.equal(partial.typescript.result.success, true);
  assert.match(partial.typescript.result.warning, /saved document.*memory-sync-markdown/);
  assert.match(document, /typescript accepted/);
  assert.deepEqual(contents(partial.typescript.rows), ["synthetic seed", "synthetic snapshot primer"]);
  assert.deepEqual(contents(rows), ["synthetic seed", "synthetic snapshot primer", "typescript accepted"]);
  assert.deepEqual(rows, partial.repair.rows);
});

test("AC-recovery-token: rebuilt current path omits the reported readable peer row despite token protection", processOptions, () => {
  const recovery = processCase("BLOCKED_OPEN_HANDLE_ACCEPTED_ROW_DETACHED");
  const rows = rowsFor(recovery);
  const peer = recovery.peerRows.find((row) => row.content === "go accepted");

  assert.equal(recovery.typescript.result.strategy, "rebuilt");
  assert.deepEqual(rows, recovery.typescript.rows);
  assert.deepEqual(contents(rows), ["synthetic seed", "synthetic snapshot primer"]);
  assert.equal(peer.content, "go accepted");
  assert.equal(peer.project, null);
  assert.equal(peer.target, "memory");
  assert(peer.category.length > 0);
  assert(peer.hit_count > 0);
  assert(Number.isFinite(Date.parse(peer.last_hit_at)));
  assert.equal(recovery.typescript.oldRenewRejected, true);
  assert.equal(recovery.typescript.successorStillOwned, true);
  assert(recovery.typescript.result.backupPaths.every(existsSync));
  assert.deepEqual(canonicalRows(rowsAt(recovery.typescript.result.backupPaths[0])), canonicalRows(recovery.peerRows));
});

test("AC-provenance: real process dependency matches the frozen archive, patch and packaged source tree", processOptions, () => {
  const manifest = JSON.parse(readFileSync(join(root, "docs/compatibility/hermes-baseline.json"), "utf8"));
  const archive = join(root, "docs/compatibility/hermes-upstream-0.9.11.tar.gz");
  const patch = join(root, manifest.patch.path);

  assert.equal(sha256(archive), manifest.upstreamArchive.sha256);
  assert.equal(sha256(patch), manifest.patch.sha256);
  assert.equal(sourceTreeHash(source, manifest), manifest.sourceTreeSha256);
  assert.equal(report.processes.provenance.commit, manifest.upstreamCommit);
  assert.deepEqual(report.processes.provenance.hashes, manifest.patchedSourceHashes);
});

test("AC-cleanup: every real fixture child has a joined successful exit and no live PID", processOptions, () => {
  const receipts = exitedReceipts();

  receipts.forEach((receipt) => {
    assert.equal(receipt.state, "exited");
    assert.equal(receipt.code, 0);
    assert.equal(receipt.signal, null);
    assert.throws(() => process.kill(receipt.pid, 0), { code: "ESRCH" });
  });
});
