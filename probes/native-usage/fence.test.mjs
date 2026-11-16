/** Session fencing and fail-closed credential offers observed through the real PiG host gate. */
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { promisify } from "node:util";

const execute = promisify(execFile);
const gate = path.resolve(import.meta.dirname, "../../scripts/check-native-usage.py");

function events(rows, event) {
  return rows.filter((row) => row.event === event);
}

function nonemptyViews(rows) {
  return events(rows, "publish")
    .map((row) => row.detail)
    .filter((view) => view.windows?.length > 0);
}

function eventIndex(rows, event) {
  const index = rows.findIndex((row) => row.event === event);
  assert.notEqual(index, -1, `missing observed ${event} event`);
  return index;
}

function phaseIndex(rows, name) {
  const index = rows.findIndex((row) => row.event === "phase" && row.detail === name);
  assert.notEqual(index, -1, `missing phase ${name}`);
  return index;
}

function phaseSegment(rows, start, end) {
  return rows.slice(phaseIndex(rows, start) + 1, phaseIndex(rows, end));
}

function fenceSpan(rows) {
  return rows.slice(phaseIndex(rows, "fence-session") + 1);
}

function successorPublish(rows) {
  const held = eventIndex(rows, "fence-held");
  const index = rows.findIndex(
    (row, position) =>
      position > held &&
      row.event === "publish" &&
      row.detail.windows?.some((window) => window.UsedPercent === 70),
  );
  assert.notEqual(index, -1, "successor session must publish a 70 percent window after the fence");
  return index;
}

function requestedHosts(rows) {
  return [...new Set(events(rows, "request").map((row) => row.detail))].sort();
}

async function launch() {
  const scratch = process.env.PIG_NATIVE_USAGE_SCRATCH;
  assert.ok(scratch, "set PIG_NATIVE_USAGE_SCRATCH to a disposable evidence root");
  await mkdir(scratch, { recursive: true });
  const before = new Set(await readdir(scratch));
  const keys = [
    "PATH",
    "TMPDIR",
    "GOMAXPROCS",
    "GOFLAGS",
    "PIG_NATIVE_USAGE_HOST",
    "PIG_NATIVE_USAGE_SDK",
    "PIG_NATIVE_USAGE_STRACE",
    "PIG_NATIVE_USAGE_SCRATCH",
  ];
  const env = Object.fromEntries(
    keys.filter((key) => process.env[key] !== undefined).map((key) => [key, process.env[key]]),
  );
  const python = process.env.PIG_NATIVE_USAGE_PYTHON ?? "python3";
  let outcome;
  try {
    const result = await execute(python, [gate, "--scenario", "qualification"], {
      env,
      timeout: 960_000,
      maxBuffer: 16 * 1024 * 1024,
    });
    outcome = { code: 0, output: `${result.stdout}${result.stderr}` };
  } catch (error) {
    outcome = { code: error.code, output: `${error.stdout ?? ""}${error.stderr ?? ""}` };
  }
  await writeFile(path.join(scratch, "launcher.log"), outcome.output);
  const created = (await readdir(scratch)).filter(
    (name) => !before.has(name) && name.startsWith("nativeusage-"),
  );
  assert.equal(
    created.length,
    1,
    `launcher must retain exactly one fresh isolated run (exit ${outcome.code}): ${outcome.output.slice(-500)}`,
  );
  return { root: path.join(scratch, created[0]), outcome };
}

async function qualification() {
  const { root, outcome } = await launch();
  const raw = await readFile(path.join(root, "lifecycle.jsonl"), "utf8").catch(() => "");
  assert.notEqual(
    raw,
    "",
    `launcher retained no lifecycle receipts: exit ${outcome.code}: ${outcome.output.slice(-1500)}`,
  );
  const rows = raw
    .trim()
    .split("\n")
    .map((line) => JSON.parse(line));
  return { rows, outcome };
}

test("native usage session fence", { timeout: 990_000 }, async (t) => {
  const proof = qualification();

  await t.test("fence-session: the held readiness ends with no release and the successor publishes", async () => {
    const { rows } = await proof;
    const segment = fenceSpan(rows);
    const held = eventIndex(segment, "fence-held");
    const successor = successorPublish(segment);

    assert.deepEqual(
      events(segment, "fence-held").map((row) => row.detail),
      [{ provider: "deepseek" }],
    );
    assert.ok(successor > held, "the successor session must publish after the held readiness");
    assert.deepEqual(
      events(segment.slice(held + 1, successor), "fence-released"),
      [],
      "a superseded owner must not release the held readiness before the successor publishes",
    );
  });

  await t.test("fence-session: the superseded owner publishes no quota and sends no request", async () => {
    const { rows } = await proof;
    const segment = fenceSpan(rows);
    const window = segment.slice(eventIndex(segment, "fence-held") + 1, successorPublish(segment));

    assert.deepEqual(nonemptyViews(window), []);
    assert.deepEqual(requestedHosts(window), ["openrouter.ai"]);
  });

  await t.test("fence-offers: credential offers are refused whole for an OAuth provider", async () => {
    const { rows } = await proof;
    const segment = phaseSegment(rows, "fence-offers", "fence-recovered");

    assert.deepEqual(
      events(segment, "offers-refused").map((row) => row.detail),
      [{ provider: "openrouter", refused: true, values: 0 }],
    );
  });

  await t.test("fence-recovered: the successor session republishes fresh quota", async () => {
    const { rows } = await proof;
    const recovered = rows.slice(
      eventIndex(rows, "fence-held") + 1,
      phaseIndex(rows, "fence-recovered"),
    );

    assert.deepEqual(
      nonemptyViews(recovered).map((view) => view.windows.map((window) => window.UsedPercent)),
      [[70]],
    );
  });

  await t.test("fence-gate: the qualification scenario completes", async () => {
    const { outcome } = await proof;

    assert.equal(
      outcome.code,
      0,
      `gate exit ${outcome.code}: ${outcome.output.slice(-1500)}`,
    );
  });
});
