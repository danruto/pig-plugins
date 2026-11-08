/** Real frozen PiG lifecycle acceptance through the private-namespace launcher. */
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { promisify } from "node:util";

const execute = promisify(execFile);

function events(rows, event) {
  return rows.filter((row) => row.event === event);
}

function nonemptyViews(rows) {
  return events(rows, "publish")
    .map((row) => row.detail)
    .filter((view) => view.windows?.length > 0);
}

function initialQuota(rows) {
  return nonemptyViews(rows)[0];
}

function eventIndex(rows, event) {
  const index = rows.findIndex((row) => row.event === event);
  assert.notEqual(index, -1, `missing observed ${event} event`);
  return index;
}

function afterModelBeforeConfigured(rows) {
  return rows.slice(eventIndex(rows, "model"), eventIndex(rows, "configured"));
}

function settingsSegment(rows) {
  return rows.slice(eventIndex(rows, "held"), eventIndex(rows, "configured") + 1);
}

function closeSegment(rows) {
  return rows.slice(eventIndex(rows, "closing-held"), eventIndex(rows, "server-closed"));
}

function afterJoined(rows) {
  return rows.slice(eventIndex(rows, "joined") + 1);
}

async function json(root, name) {
  return JSON.parse(await readFile(path.join(root, name), "utf8"));
}

async function launch(scenario = []) {
  const scratch = process.env.PIG_NATIVE_USAGE_SCRATCH;
  assert.ok(scratch, "set PIG_NATIVE_USAGE_SCRATCH to an objective-22 disposable root");
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
  const python = "/nix/store/q1khrhbn506imjxx7kiqcbihycawa6kl-python3-3.13.15/bin/python3";
  let result;
  try {
    result = await execute(python, ["scripts/check-native-usage.py", ...scenario], {
      env,
      timeout: 600_000,
      maxBuffer: 16 * 1024 * 1024,
    });
  } catch (error) {
    await writeFile(path.join(scratch, "launcher-failed.log"), error.stdout + error.stderr);
    throw error;
  }
  await writeFile(path.join(scratch, "launcher.log"), result.stdout + result.stderr);
  const created = (await readdir(scratch)).filter(
    (name) => !before.has(name) && name.startsWith("nativeusage-"),
  );
  assert.equal(created.length, 1, "launcher must retain exactly one fresh isolated run");
  return path.join(scratch, created[0]);
}

test("native usage real-host lifecycle", { timeout: 650_000 }, async (t) => {
  const root = await launch();
  const rows = (await readFile(path.join(root, "lifecycle.jsonl"), "utf8"))
    .trim()
    .split("\n")
    .map((line) => JSON.parse(line));

  await t.test("AC-publish: session startup feeds fresh quota in process", () => {
    const quotas = nonemptyViews(rows);

    assert.deepEqual(
      quotas.map((view) => view.windows.map((window) => window.UsedPercent)),
      [[20]],
    );
    assert.equal(quotas[0].expires, true);
    assert.equal(quotas[0].reason, "");
  });

  await t.test("AC-model: provider replacement removes prior quota while HTTPS is held", () => {
    assert.equal(initialQuota(rows).windows[0].UsedPercent, 20);
    assert.deepEqual(nonemptyViews(afterModelBeforeConfigured(rows)), []);
    assert.equal(events(rows, "held").length, 2);
  });

  await t.test("AC-settings: replacement cancels held HTTPS without stale quota", () => {
    assert.equal(initialQuota(rows).windows[0].UsedPercent, 20);
    assert.equal(events(settingsSegment(rows), "cancelled").length, 1);
    assert.deepEqual(nonemptyViews(settingsSegment(rows)), []);
    assert.equal(events(rows, "configured")[0].detail, true);
  });

  await t.test("AC-close: active HTTPS cancels and closed cached feed stays empty", () => {
    assert.equal(initialQuota(rows).windows[0].UsedPercent, 20);
    assert.equal(events(rows, "closing-held").length, 1);
    assert.equal(events(closeSegment(rows), "cancelled").length, 1);
    assert.equal(events(rows, "joined")[0].detail, true);
    assert.deepEqual(nonemptyViews(afterJoined(rows)), []);
  });

  await t.test("AC-shutdown: RPC EOF closes factory server and consumes host wait", async () => {
    const wait = await json(root, "host-wait.json");
    const process = await json(root, "host-process.json");

    assert.equal(initialQuota(rows).windows[0].UsedPercent, 20);
    assert.equal(events(rows, "server-closed")[0].detail, true);
    assert.equal(wait.exit, 0);
    assert.equal(wait.pid, process.pid);
    assert.equal(wait.waitOwner, process.waitOwner);
  });
});

function phaseSegment(rows, start, end) {
  const first = rows.findIndex((row) => row.event === "phase" && row.detail === start);
  const last = rows.findIndex((row) => row.event === "phase" && row.detail === end);
  assert.notEqual(first, -1, `missing phase ${start}`);
  assert.notEqual(last, -1, `missing phase ${end}`);
  return rows.slice(first + 1, last);
}

function cachePercentages(rows) {
  return events(rows, "cache-view").map(
    (row) => row.detail.windows?.map((window) => window.UsedPercent) ?? [],
  );
}

async function qualification() {
  const root = await launch(["--scenario", "qualification"]);
  const rows = (await readFile(path.join(root, "lifecycle.jsonl"), "utf8"))
    .trim()
    .split("\n")
    .map((line) => JSON.parse(line));
  return { root, rows };
}

test("native usage owned refresh qualification", { timeout: 650_000 }, async (t) => {
  const proof = qualification();

  await t.test("AC-cache-auth: cache hits reacquire effective account identity", async () => {
    const { rows } = await proof;
    const segment = phaseSegment(rows, "cache-auth", "late-same-id");

    assert.deepEqual(cachePercentages(segment), [[20], [70], [70]]);
    assert.equal(events(segment, "request").length, 2);
  });

  for (const [kind, end] of [
    ["same-id", "late-auth"],
    ["auth", "late-settings"],
    ["settings", "target-cache"],
  ]) {
    await t.test(
      `AC-fence-${kind}: late successful body cannot enter successor cache`,
      async () => {
        const { rows } = await proof;
        const segment = phaseSegment(rows, `late-${kind}`, end);

        assert.equal(initialQuota(rows).windows[0].UsedPercent, 20);
        assert.equal(events(segment, "late-body").length, 1);
        assert.deepEqual(
          events(segment, "cache-view").map((row) => ({
            name: row.detail.name,
            used: row.detail.windows?.map((window) => window.UsedPercent) ?? [],
          })),
          [
            { name: `stale-${kind}`, used: [] },
            { name: `successor-${kind}`, used: [70] },
          ],
        );
      },
    );
  }

  await t.test(
    "AC-cache-target: fresh discovery refuses a removed cached billing target",
    async () => {
      const { rows } = await proof;
      const initial = phaseSegment(rows, "target-cache", "target-invalid");
      const invalid = phaseSegment(rows, "target-invalid", "timer-start");

      assert.equal(events(initial, "billing").length, 1);
      assert.deepEqual(events(invalid, "billing"), []);
      assert.equal(events(invalid, "cache-view")[0].detail.reason, "selection-required");
      assert.equal(events(invalid, "discovery").length, 1);
    },
  );

  await t.test(
    "AC-timer: real expiry and automatic backoff recover without a user trigger",
    async () => {
      const { rows } = await proof;
      const timer = phaseSegment(rows, "timer-start", "timer-recovered");
      const cached = phaseSegment(rows, "timer-cached", "timer-recovered");
      const failures = events(cached, "failure-response");
      const first = nonemptyViews(timer)[0];
      const initial = events(timer, "publish").find((row) => row.detail.windows?.length > 0);
      const recovered = events(cached, "publish").find(
        (row) => row.detail.windows?.[0]?.UsedPercent === 70,
      );
      const expired = cached.slice(
        eventIndex(cached, "failure-response"),
        cached.indexOf(recovered),
      );

      assert.equal(first.windows[0].UsedPercent, 20);
      assert.equal(failures.length, 2);
      assert.ok(failures[0].time - initial.time >= 299_000);
      assert.ok(failures[1].time - failures[0].time >= 29_000);
      assert.ok(failures[1].time - failures[0].time < 45_000);
      assert.ok(recovered.time - failures[1].time >= 29_000);
      assert.ok(recovered.time - failures[1].time < 45_000);
      assert.deepEqual(nonemptyViews(expired), []);
      assert.equal(recovered.detail.windows[0].UsedPercent, 70);
    },
  );

  await t.test(
    "AC-session-active: session replacement rejects old contexts and cancels HTTPS",
    async () => {
      const { rows } = await proof;
      const session = phaseSegment(rows, "session-active", "session-recovered");
      const trigger = rows.find((row) => row.event === "phase" && row.detail === "session-active");

      assert.equal(initialQuota(rows).windows[0].UsedPercent, 20);
      assert.ok(events(session, "cancelled")[0].time - trigger.time < 3000);
      assert.equal(events(session, "stale-context-rejected")[0].detail, true);
      assert.equal(nonemptyViews(session)[0].windows[0].UsedPercent, 70);
    },
  );

  await t.test(
    "AC-diagnostics: background failures emit sanitized host notifications",
    async () => {
      const { root, rows } = await proof;
      const diagnostics = await readFile(path.join(root, "host.stdout.jsonl"), "utf8");
      const queryFailures = diagnostics
        .trim()
        .split("\n")
        .map((line) => JSON.parse(line))
        .filter(
          (row) =>
            row.type === "extension_ui_request" &&
            row.method === "notify" &&
            row.message.includes("query-failed"),
        );

      assert.equal(initialQuota(rows).windows[0].UsedPercent, 20);
      assert.equal(queryFailures.length, 2);
      assert.doesNotMatch(diagnostics, /fixture-private-canary|synthetic-lifecycle/);
    },
  );

  await t.test(
    "AC-reload-active: reload cancels admitted HTTPS before successor publication",
    async () => {
      const { rows } = await proof;
      const reload = phaseSegment(rows, "reload-active", "reload-recovered");
      const trigger = rows.find((row) => row.event === "phase" && row.detail === "reload-active");

      assert.equal(initialQuota(rows).windows[0].UsedPercent, 20);
      assert.ok(events(reload, "cancelled")[0].time - trigger.time < 3000);
      assert.equal(events(reload, "server-closed")[0].detail, true);
      assert.equal(nonemptyViews(reload)[0].windows[0].UsedPercent, 70);
    },
  );

  await t.test(
    "AC-shutdown-active: EOF promptly cancels HTTPS and consumes the host wait",
    async () => {
      const { root, rows } = await proof;
      const trigger = rows.find((row) => row.event === "phase" && row.detail === "eof-active");
      const shutdown = rows.slice(rows.indexOf(trigger) + 1);
      const wait = await json(root, "host-wait.json");
      const process = await json(root, "host-process.json");

      assert.equal(initialQuota(rows).windows[0].UsedPercent, 20);
      assert.ok(events(shutdown, "cancelled")[0].time - trigger.time < 3000);
      assert.equal(events(shutdown, "server-closed")[0].detail, true);
      assert.equal(wait.exit, 0);
      assert.equal(wait.pid, process.pid);
      assert.equal(wait.waitOwner, process.waitOwner);
    },
  );
});
