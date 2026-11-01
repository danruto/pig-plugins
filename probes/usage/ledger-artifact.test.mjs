import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { readLedger, writeLedger } from "./ledger-artifact.mjs";

async function scratch(t) {
  const root = await mkdtemp(join(tmpdir(), "usage-ledger-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  return root;
}

test("ledger capture preserves exact bytes and produces deterministic gzip", async (t) => {
  const root = await scratch(t);
  const path = join(root, "ledger.json.gz");
  const bytes = Buffer.from('{\n  "label": "quota ☃",\n  "unknown": null\n}\n');
  await writeLedger(path, bytes);
  const first = await readFile(path);
  assert.deepEqual(await readLedger(path), bytes);
  await writeLedger(path, bytes);
  assert.deepEqual(await readFile(path), first);
});

test("ledger reads reject corrupt compressed evidence", async (t) => {
  const root = await scratch(t);
  const path = join(root, "ledger.json.gz");
  await writeFile(path, "not gzip");
  await assert.rejects(readLedger(path), { code: "Z_DATA_ERROR" });
});

test("committed ledger fits GitHub and retains its original receipt hash", async () => {
  const root = new URL("../../docs/compatibility/usage/", import.meta.url);
  const path = new URL("branch-ledger.json.gz", root);
  const provenance = JSON.parse(await readFile(new URL("provenance.json", root), "utf8"));
  assert.ok((await stat(path)).size < 100 * 1024 * 1024);
  const bytes = await readLedger(path);
  assert.equal(createHash("sha256").update(bytes).digest("hex"), provenance.branchLedgerSha256);
  assert.ok(JSON.parse(bytes).obligations.length > 0);
  await assert.rejects(stat(new URL("branch-ledger.json", root)), { code: "ENOENT" });
});
