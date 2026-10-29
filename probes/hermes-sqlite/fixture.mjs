import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const packageRoot = process.env.HERMES_PACKAGE;
const root = process.env.PI_CODING_AGENT_DIR;
assert(packageRoot && root, "HERMES_PACKAGE and a disposable PI_CODING_AGENT_DIR are required");
const store = join(resolve(root), "store");
mkdirSync(store, { recursive: true });
const load = (path) => import(pathToFileURL(join(packageRoot, "src", path)).href);
const { DatabaseManager } = await load("store/db.ts");
const manager = new DatabaseManager(store);

try {
  if (process.argv[2] === "seed") {
    const { addMemory } = await load("store/sqlite-memory-store.ts");
    addMemory(manager, "東京日本語 compatibility fixture", "memory");
    const row = manager.getDb().prepare("SELECT sqlite_version() AS version").get();
    console.log(JSON.stringify(row));
  } else if (process.argv[2] === "read") {
    const rows = manager.getDb().prepare("SELECT content FROM memories ORDER BY id").all();
    assert.deepEqual(rows.map((row) => row.content), ["東京日本語 compatibility fixture", "written by Go"]);
    const check = manager.getDb().prepare("PRAGMA integrity_check").get();
    assert.equal(Object.values(check)[0], "ok");
    console.log(JSON.stringify({ readable: rows.length, integrity: "ok" }));
  } else {
    const { AtomicLockCoordinator } = await load("store/atomic-lock-coordinator.ts");
    const coordinator = AtomicLockCoordinator.shared(join(root, ".pi-hermes-locks.sqlite"));
    const lease = coordinator.tryAcquire("mutation:fixture", { staleMs: 300_000 });
    console.log(JSON.stringify({ acquired: lease !== null }));
    if (process.argv[2] === "hold" && lease) {
      process.stdin.resume();
      await new Promise((done) => process.stdin.on("end", done));
    }
    lease?.release();
  }
} finally {
  await manager.waitForStartupIntegrityScan();
  manager.close();
}
