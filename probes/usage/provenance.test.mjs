/** Executable inspection must configure private Go telemetry before a real version command can start a sidecar. */
import assert from "node:assert/strict";
import test from "node:test";
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const run = promisify(execFile);
const project = fileURLToPath(new URL("../../", import.meta.url));

async function privateEnvironment(root, path) {
  const roots = Object.fromEntries(["HOME", "PIG_HOME", "PIG_CODING_AGENT_DIR", "PI_CODING_AGENT_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "TMPDIR"].map((name) => [name, join(root, name)]));
  await Promise.all(Object.values(roots).map((directory) => mkdir(directory, { recursive: true })));
  return { ...roots, PATH: path };
}

test("TELEMETRY-private-bootstrap: executable identity disables private Go telemetry before version inspection", async (t) => {
  const scratch = join(project, ".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const outer = await mkdtemp(join(scratch, "provenance-telemetry-test-"));
  t.after(() => rm(outer, { recursive: true }));
  const { stdout } = await run("sh", ["-c", "command -v go"], { env: { PATH: process.env.PATH } });
  const realGo = stdout.trim();
  const bin = join(outer, "bin");
  await mkdir(bin);
  const observer = join(bin, "go");
  await writeFile(observer, `#!${process.execPath}
import { spawnSync } from "node:child_process";
const realGo = ${JSON.stringify(realGo)};
const args = process.argv.slice(2);
if (args[0] === "version") {
  const mode = spawnSync(realGo, ["telemetry"], { env: process.env, encoding: "utf8" });
  if (mode.error) throw mode.error;
  if (mode.status !== 0) throw new Error(mode.stderr);
  if (mode.stdout.trim() !== "off") {
    console.error("telemetry observer: refusing real go version; private mode=" + mode.stdout.trim());
    process.exit(86);
  }
}
const result = spawnSync(realGo, args, { env: process.env, stdio: "inherit" });
if (result.error) throw result.error;
process.exit(result.status ?? 1);
`, { mode: 0o755 });
  const path = `${bin}:${process.env.PATH}`;
  const control = await privateEnvironment(join(outer, "control"), path);
  await run(observer, ["telemetry", "off"], { env: control });
  await run(observer, ["version"], { env: control });
  const env = await privateEnvironment(join(outer, "host"), path);
  const root = join(outer, "measurement");
  await mkdir(root);
  const driver = 'import { measurementIdentity } from "./probes/usage/provenance.mjs"; const identity = await measurementIdentity(process.argv[1], process.argv[2]); process.stdout.write(JSON.stringify({ identity }));';
  let result;

  await assert.doesNotReject(async () => {
    result = await run(process.execPath, ["--input-type=module", "-e", driver, project, root], { env, maxBuffer: 1024 * 1024 });
  }, "executable inspection must disable telemetry in its private Go config before real go version");

  assert.match(JSON.parse(result.stdout).identity, /\S/u);
});
