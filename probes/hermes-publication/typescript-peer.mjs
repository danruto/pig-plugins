/** Barrier-controlled execution of the unmodified pinned TypeScript writer on synthetic roots. */
import assert from "node:assert/strict";
import fs from "node:fs";
import fsp from "node:fs/promises";
import path from "node:path";
import { syncBuiltinESMExports } from "node:module";
import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";
import { DatabaseSync } from "node:sqlite";

const root = process.env.HERMES_PROBE_ROOT;
const source = process.env.HERMES_PACKAGE;
assert(root && source, "explicit disposable root and pinned source required");
const load = (name) => import(pathToFileURL(path.join(source, "src", name)).href);
const lines = createInterface({ input: process.stdin });
const commands = lines[Symbol.asyncIterator]();
async function barrier(event) {
  console.log(JSON.stringify({ event }));
  const next = await commands.next();
  assert(!next.done && next.value === "continue", `barrier ${event} requires continue`);
}

const { MemoryStore } = await load("store/memory-store.ts");
const { DatabaseManager } = await load("store/db.ts");
const { reconcileMarkdownMemoryScope } = await load("store/sqlite-memory-store.ts");
const { AtomicLockCoordinator } = await load("store/atomic-lock-coordinator.ts");
const memory = path.join(root, "memory");
await fsp.mkdir(memory, { recursive: true });
const manager = new DatabaseManager(memory);
await manager.waitForStartupIntegrityScan();
const store = new MemoryStore({ memoryDir: memory, memoryMode: "policy-only", memoryCharLimit: 100000, userCharLimit: 100000 });
store.setMutationObserver((target, entries) => {
  if (process.argv[2] === "partial") return "saved document; synthetic reconciliation unavailable; run /memory-sync-markdown";
  reconcileMarkdownMemoryScope(manager, entries, target);
  return null;
});

try {
  await store.loadFromDisk();
  if (process.argv[2] === "recovery") {
    const original = fs.renameSync;
    const dbPath = path.join(memory, "sessions.db");
    manager.getDb().exec("CREATE TABLE probe_corrupt(n INTEGER CHECK(n>0)); PRAGMA ignore_check_constraints=ON; INSERT INTO probe_corrupt VALUES(-1); PRAGMA ignore_check_constraints=OFF;");
    let oldRenewRejected = false;
    const recoveryKey = `recovery:${dbPath}`;
    const recoveryCoordinator = AtomicLockCoordinator.shared(path.join(memory, ".pi-hermes-locks.sqlite"));
    fs.renameSync = function (from, to) {
      if (String(from) === dbPath && String(to).includes(".corrupt-")) {
        const locks = new DatabaseSync(path.join(memory, ".pi-hermes-locks.sqlite"));
        let oldToken;
        try { oldToken = locks.prepare("SELECT token FROM locks WHERE lock_key=?").get(recoveryKey).token; }
        finally { locks.close(); }
        console.log(JSON.stringify({ event: "owner-checked-before-recovery-rename" }));
        const until = Date.now() + 30000;
        while (!fs.existsSync(path.join(root, "continue-recovery"))) {
          assert(Date.now() < until, "recovery barrier timeout");
          Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 10);
        }
        oldRenewRejected = !recoveryCoordinator.renew(recoveryKey, oldToken);
        recoveryCoordinator.release(recoveryKey, oldToken);
      }
      return original(from, to);
    };
    let result;
    try {
      result = manager.recoverFromCorruption(new Error("synthetic CHECK violation"));
    } finally {
      fs.renameSync = original;
    }
    const rows = manager.getDb().prepare("SELECT project,target,category,content,hit_count,last_hit_at FROM memories ORDER BY id").all();
    const metadata = manager.getDb().prepare("SELECT key,value FROM extension_metadata ORDER BY key").all();
    console.log(JSON.stringify({ event: "result", result, rows, metadata, oldRenewRejected, successorStillOwned: recoveryCoordinator.isCurrentOwner(recoveryKey, "go-successor") }));
  } else {
    if (process.argv[2] === "seed") {
      const result = await store.add("memory", "synthetic seed");
      assert.equal(result.success, true);
      await store.add("memory", "synthetic snapshot primer");
      console.log(JSON.stringify({ event: "seeded" }));
    } else {
      const rename = fsp.rename;
      if (process.argv[2] === "race") {
        fsp.rename = async function (from, to) {
          if (path.basename(String(from)) === "write.tmp" && path.basename(String(to)) === "MEMORY.md") await barrier("checked-before-rename");
          return rename(from, to);
        };
        syncBuiltinESMExports();
      }
      let result;
      try { result = await store.add("memory", "typescript accepted"); }
      finally {
        fsp.rename = rename;
        syncBuiltinESMExports();
      }
      const doc = await fsp.readFile(path.join(memory, "MEMORY.md"), "utf8");
      const rows = manager.getDb().prepare("SELECT project,target,category,content,hit_count,last_hit_at FROM memories ORDER BY id").all();
      const metadata = manager.getDb().prepare("SELECT key,value FROM extension_metadata ORDER BY key").all();
      const coordinator = AtomicLockCoordinator.shared(path.join(root, ".pi-hermes-locks.sqlite"));
      const key = `mutation:${path.join(memory, "MEMORY.md")}`;
      console.log(JSON.stringify({ event: "result", result, doc, rows, metadata, successorStillOwned: coordinator.isCurrentOwner(key, "go-successor") }));
      if (process.argv[2] === "partial") {
        await barrier("before-repair");
        reconcileMarkdownMemoryScope(manager, store.getMemoryEntries(), "memory", null, { force: true });
        const repaired = manager.getDb().prepare("SELECT project,target,category,content,hit_count,last_hit_at FROM memories ORDER BY id").all();
        console.log(JSON.stringify({ event: "repaired", rows: repaired }));
      }
    }
  }
} finally {
  manager.close();
  lines.close();
}
