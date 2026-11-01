/** Observe V8 block ranges in unchanged PiG/Jiti scripts: the pinned backend, and in registered windows its dist and the package's footer consumer; never rewrite upstream code or instrument expressions. In an exact-counter replay (USAGE_FIXTURE_COUNTER_MANIFEST set), each window also records the positive per-window deltas of the disposable instrumented copy's counters. */
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { Session } from "node:inspector";
import { writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const session = new Session();
const scripts = new Map();
const observations = [];
let active;
let counterBaseline;
const counterManifest = process.env.USAGE_FIXTURE_COUNTER_MANIFEST;
function counterStore() {
  const [manifestSha256, total] = counterManifest.split(":");
  const store = globalThis[Symbol.for("usage-fixture-counters")] ??= { manifestSha256, counts: new Float64Array(Number(total)) };
  assert.equal(store.manifestSha256, manifestSha256, "Counter replay manifest mismatch");
  assert.equal(store.counts.length, Number(total), "Counter replay size mismatch");
  return store.counts;
}
const footer = fileURLToPath(new URL("../../status/usage.ts", import.meta.url));
const sha = (text) => createHash("sha256").update(text).digest("hex");
const post = (method, params = {}) => new Promise((resolve, reject) => session.post(method, params, (error, result) => error ? reject(error) : resolve(result)));
session.connect();
await post("Debugger.enable");
await post("Profiler.enable");
await post("Profiler.startPreciseCoverage", { callCount: true, detailed: true });

export async function beginBranchObservation(id) {
  assert.equal(active, undefined, "Overlapping branch observation windows");
  await post("Profiler.takePreciseCoverage");
  if (counterManifest) counterBaseline = Float64Array.from(counterStore());
  active = id;
}

export async function endBranchObservation() {
  assert.ok(active, "No active branch observation");
  const { result } = await post("Profiler.takePreciseCoverage");
  const blocks = [];
  for (const script of result) {
    const marker = "/node_modules/@narumitw/pi-usage/";
    const location = script.url.startsWith("file:") ? fileURLToPath(script.url) : script.url;
    const path = location === footer ? "status/usage.ts" : script.url.includes(marker) ? script.url.slice(script.url.indexOf(marker) + marker.length) : undefined;
    if (!path || (!path.startsWith("src/") && !path.startsWith("dist/") && path !== "status/usage.ts")) continue;
    if (!path.startsWith("src/") && !/^(protocol|lifecycle):/u.test(active)) continue;
    const executing = script.functions.filter((fn) => fn.ranges[0].count > 0);
    const functions = script.functions.filter((fn) => executing.some((parent) => parent.ranges[0].startOffset <= fn.ranges[0].startOffset && parent.ranges[0].endOffset >= fn.ranges[0].endOffset))
      .sort((a, b) => a.ranges[0].startOffset - b.ranges[0].startOffset);
    if (!functions.length) continue;
    const { scriptSource } = await post("Debugger.getScriptSource", { scriptId: script.scriptId });
    const identity = sha(scriptSource);
    if (!scripts.has(identity)) scripts.set(identity, { path, sha256: identity, text: scriptSource });
    blocks.push({ script: identity, functions });
  }
  blocks.sort((a, b) => a.script.localeCompare(b.script));
  const counters = [];
  if (counterManifest) {
    const counts = counterStore();
    for (let index = 0; index < counts.length; index += 1) {
      const delta = counts[index] - counterBaseline[index];
      assert.ok(Number.isSafeInteger(delta) && delta >= 0, "Counter went backwards");
      if (delta > 0) counters.push([index, delta]);
    }
  }
  observations.push({ id: active, blocks, ...(counterManifest ? { counters: { composition: "counter-replay", counts: counters } } : {}) });
  active = undefined;
}

export async function observeBranches(id, operation) {
  await beginBranchObservation(id);
  try { return await operation(); }
  finally { await endBranchObservation(); }
}

export async function saveBranchObservations(path) {
  assert.equal(active, undefined);
  await post("Profiler.stopPreciseCoverage");
  await post("Profiler.disable");
  await post("Debugger.disable");
  session.disconnect();
  await writeFile(path, JSON.stringify({ formatVersion: 1, scripts: [...scripts.values()].sort((a, b) => a.path.localeCompare(b.path)), observations }) + "\n", { mode: 0o600 });
}
