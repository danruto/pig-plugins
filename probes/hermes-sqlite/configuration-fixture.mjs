/** Native-checkpoint cross-engine inputs and assertions; the historical fixture remains closure-pinned. */
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const source = process.env.HERMES_PACKAGE;
const root = process.env.PI_CODING_AGENT_DIR;
assert(source && root, "HERMES_PACKAGE and disposable PI_CODING_AGENT_DIR are required");
const directory = join(resolve(root), "store");
mkdirSync(directory, { recursive: true });
const { DatabaseManager } = await import(pathToFileURL(join(source, "src/store/db.ts")).href);
const manager = new DatabaseManager(directory);

try {
  const db = manager.getDb();
  if (process.argv[2] === "checkpoint-seed") {
    db.exec("INSERT INTO memories(target,content,created,last_referenced,category,hit_count,last_hit_at) VALUES ('memory','東京日本語 checkpoint','2026-01-01','2026-01-02','insight',7,'2026-01-03'); INSERT INTO extension_metadata(key,value) VALUES ('checkpoint','{\"synthetic\":true}')");
    console.log(JSON.stringify({ seeded: true }));
  } else {
    assert.equal(process.argv[2], "checkpoint-read");
    assert.deepEqual(db.prepare("SELECT content,category,hit_count FROM memories ORDER BY id").all(), [
      { content: "東京日本語 checkpoint", category: "insight", hit_count: 7 },
      { content: "native checkpoint", category: "preference", hit_count: 2 },
    ]);
    assert.equal(db.prepare("SELECT value FROM extension_metadata WHERE key='checkpoint'").get().value, '{"synthetic":true}');
    assert.equal(db.prepare("SELECT content FROM memory_fts WHERE memory_fts MATCH '\"checkpoint\"' AND content LIKE 'native%'").get().content, "native checkpoint");
    assert.equal(Object.values(db.prepare("PRAGMA integrity_check").get())[0], "ok");
    console.log(JSON.stringify({ readable: 2, integrity: "ok" }));
  }
} finally {
  await manager.waitForStartupIntegrityScan();
  manager.close();
}
