/** The staged native usage adapter compiles against the reviewed PiG SDK. */
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { copyFile, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import test from "node:test";
import { promisify } from "node:util";

const execute = promisify(execFile);
const adapter = path.resolve(import.meta.dirname, "../../extensions/pig-status/nativeusage");
const sdk = process.env.PIG_NATIVE_USAGE_SDK;
const skip = sdk ? false : "set PIG_NATIVE_USAGE_SDK to the output of `pig reload --sdk-path`";
const toolchain = { ...process.env, GOMAXPROCS: "2", GOFLAGS: "-p=2", GOTOOLCHAIN: "local" };

async function go(args) {
  try {
    const { stdout, stderr } = await execute("go", args, {
      cwd: adapter,
      env: toolchain,
      timeout: 300_000,
      maxBuffer: 32 * 1024 * 1024,
    });
    return { code: 0, output: `${stdout}${stderr}` };
  } catch (error) {
    return { code: error.code, output: `${error.stdout}${error.stderr}` };
  }
}

test("builds the staged adapter against the reviewed SDK", { skip }, async (t) => {
  const dir = await mkdtemp(path.join(tmpdir(), "native-usage-modfile-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const modfile = path.join(dir, "nativeusage.mod");
  await copyFile(path.join(adapter, "go.mod"), modfile);

  const edit = await go([
    "mod",
    "edit",
    `-modfile=${modfile}`,
    `-replace=github.com/MichaelKinsy/PiG/extensions/sdk=${sdk}`,
    "-replace=github.com/danruto/pig-plugins/extensions/pig-status=..",
  ]);
  assert.equal(edit.code, 0, edit.output);

  const build = await go(["build", `-modfile=${modfile}`, "./..."]);

  assert.deepEqual(build, { code: 0, output: "" });
});
