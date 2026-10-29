import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

assert(process.env.HERMES_PACKAGE, "HERMES_PACKAGE must point at the pinned Hermes source");
const project = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const root = mkdtempSync(join(tmpdir(), "pig-hermes-session-"));
try {
  const release = join(root, "release");
  writeFileSync(release, "");
  execFileSync("pig", [
    "--mode", "json", "--session-dir", join(root, "sessions"), "--no-extensions",
    "-e", join(project, "probes/completion-probe"), "--model", "hermes-probe/fixture", "session parser fixture",
  ], {
    cwd: root,
    env: { ...process.env, PIG_HOME: root, PIG_CODING_AGENT_DIR: join(root, "agent"), PROBE_REPORT: join(root, "report"), PROBE_RELEASE: release },
    timeout: 60_000,
    stdio: ["ignore", "pipe", "pipe"],
  });
  const files = readdirSync(join(root, "sessions"), { recursive: true }).filter((path) => path.endsWith(".jsonl"));
  assert.equal(files.length, 1, files);
  const { parseSessionFile } = await import(pathToFileURL(join(process.env.HERMES_PACKAGE, "src/store/session-parser.ts")));
  const parsed = parseSessionFile(join(root, "sessions", files[0]));
  assert(parsed);
  assert.equal(parsed.cwd, root);
  assert.deepEqual(parsed.messages.map(({ role, content }) => ({ role, content })), [
    { role: "user", content: "session parser fixture" },
    { role: "assistant", content: "offline completion" },
  ]);
  console.log("Hermes TypeScript parser: real PiG JSONL session accepted with exact user/assistant content");
} finally {
  rmSync(root, { recursive: true, force: true });
}
