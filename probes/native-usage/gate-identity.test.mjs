/** The native usage gate accepts only the reviewed host and SDK, and fails closed otherwise. */
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { appendFile, chmod, copyFile, cp, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { promisify } from "node:util";

const execute = promisify(execFile);
const gate = path.resolve(import.meta.dirname, "../../scripts/check-native-usage.py");
const host = process.env.PIG_NATIVE_USAGE_HOST;
const sdk = process.env.PIG_NATIVE_USAGE_SDK;
const python = process.env.PIG_NATIVE_USAGE_PYTHON ?? "python3";
const skip = host && sdk ? false : "set PIG_NATIVE_USAGE_HOST and PIG_NATIVE_USAGE_SDK";

function gateEnvironment(hostPath, sdkPath) {
  const env = { ...process.env };
  delete env.PIG_NATIVE_USAGE_HOST;
  delete env.PIG_NATIVE_USAGE_SDK;
  if (hostPath !== undefined) {
    env.PIG_NATIVE_USAGE_HOST = hostPath;
  }
  if (sdkPath !== undefined) {
    env.PIG_NATIVE_USAGE_SDK = sdkPath;
  }
  return env;
}

async function gateExit(hostPath, sdkPath) {
  const env = gateEnvironment(hostPath, sdkPath);
  try {
    await execute(python, [gate], { env, timeout: 120_000, maxBuffer: 16 * 1024 * 1024 });
  } catch (error) {
    return { code: error.code, output: `${error.stdout}${error.stderr}` };
  }
  return { code: 0, output: "" };
}

test("refuses to run without a pinned host", { skip }, async () => {
  const outcome = await gateExit(undefined, sdk);

  assert.equal(outcome.code, 2, outcome.output);
});

test(
  "refuses to run without a pinned SDK tree",
  { skip: host ? false : "set PIG_NATIVE_USAGE_HOST to the reviewed host" },
  async () => {
    const outcome = await gateExit(host, undefined);

    assert.equal(outcome.code, 2, outcome.output);
  },
);

test("rejects a host whose bytes differ from the reviewed host", { skip }, async (t) => {
  const dir = await mkdtemp(path.join(tmpdir(), "native-usage-host-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const tampered = path.join(dir, "pig");
  await copyFile(host, tampered);
  await chmod(tampered, 0o755);
  await appendFile(tampered, "\n");

  const outcome = await gateExit(tampered, sdk);

  assert.equal(outcome.code, 2, outcome.output);
});

test("rejects an SDK tree whose digest differs from the reviewed tree", { skip }, async (t) => {
  const dir = await mkdtemp(path.join(tmpdir(), "native-usage-sdk-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const tampered = path.join(dir, "sdk");
  await cp(sdk, tampered, { recursive: true });
  await writeFile(path.join(tampered, "unreviewed.go"), "package sdk\n");

  const outcome = await gateExit(host, tampered);

  assert.equal(outcome.code, 2, outcome.output);
});
