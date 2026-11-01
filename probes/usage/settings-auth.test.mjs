/** Runnable owner checks that fixture auth writes take and release the host's auth lock. */
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { once } from "node:events";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { writeHostAuth, writeHostAuthSync } from "./settings-auth.mjs";

async function lockedAuth(t) {
  const root = await mkdtemp(join(tmpdir(), "settings-auth-lock-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const path = join(root, "auth.json");
  await writeFile(path, '{"before":true}');
  await mkdir(`${path}.lock`);
  return { path, seen: join(root, "seen-at-release") };
}

function releaseFromOtherProcess(path, seen) {
  const holder = spawn(process.execPath, ["-e", `
    const { readFileSync, rmdirSync, writeFileSync } = require("node:fs");
    setTimeout(() => { writeFileSync(${JSON.stringify(seen)}, readFileSync(${JSON.stringify(path)})); rmdirSync(${JSON.stringify(path + ".lock")}); }, 200);`], { stdio: "inherit" });
  return once(holder, "exit");
}

for (const [name, write] of [["async", writeHostAuth], ["sync", writeHostAuthSync]]) {
  test(`${name} auth write waits for a lock held by another process and releases its own`, async (t) => {
    const { path, seen } = await lockedAuth(t);
    const released = releaseFromOtherProcess(path, seen);
    await write(path, '{"after":true}');
    assert.deepEqual(await released, [0, null]);
    assert.equal(await readFile(seen, "utf8"), '{"before":true}');
    assert.equal(await readFile(path, "utf8"), '{"after":true}');
    assert.equal(existsSync(`${path}.lock`), false);
  });

  test(`${name} auth write gives up on a lock that is never released and leaves it in place`, async (t) => {
    const { path } = await lockedAuth(t);
    await assert.rejects(async () => write(path, '{"after":true}', undefined, 3), { code: "EEXIST" });
    assert.equal(await readFile(path, "utf8"), '{"before":true}');
    assert.equal(existsSync(`${path}.lock`), true);
  });
}
