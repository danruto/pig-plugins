/** Checks the lint-baseline comparer against tiny reports: matching, extra duplicate, and missing entry, and the go gate over the checked-in go baseline. */
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, test } from "node:test";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));
const script = fileURLToPath(new URL("../scripts/lint-baseline.mjs", import.meta.url));
const dir = await mkdtemp(join(tmpdir(), "lint-baseline-"));
after(() => rm(dir, { recursive: true, force: true }));

const go = {
  kind: "go",
  encode: (issues) => ({ Issues: issues.map(({ file, linter, text }) => ({ FromLinter: linter, Pos: { Filename: file }, Text: text })) })
};
const ts = {
  kind: "ts",
  encode: (diagnostics) => ({ diagnostics: diagnostics.map(({ file, linter, text }) => ({ code: linter, filename: file, message: text })) })
};
const sample = { file: "scripts/usage-reference.mjs", linter: "cyclop", text: "calculated cyclomatic complexity for function render is 16, max is 10" };

async function fixture(name, value) {
  const path = join(dir, name);
  await writeFile(path, JSON.stringify(value));
  return path;
}

function run(kind, report, baseline) {
  return spawnSync(process.execPath, [script, "--check", kind, report, baseline], { encoding: "utf8" });
}

for (const { kind, encode } of [go, ts]) {
  test(`${kind} report repeating an entry already in the baseline fails as a new issue`, async () => {
    const baseline = await fixture(`${kind}-extra-baseline.json`, [sample]);
    const report = await fixture(`${kind}-extra-report.json`, encode([sample, sample]));
    const result = run(kind, report, baseline);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /new: scripts\/usage-reference\.mjs/);
  });

  test(`${kind} report missing a baseline entry fails as a fixed issue`, async () => {
    const baseline = await fixture(`${kind}-missing-baseline.json`, [sample]);
    const report = await fixture(`${kind}-missing-report.json`, encode([]));
    const result = run(kind, report, baseline);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /fixed, remove from baseline:/);
  });

  test(`${kind} report matching its baseline passes`, async () => {
    const baseline = await fixture(`${kind}-same-baseline.json`, [sample]);
    const report = await fixture(`${kind}-same-report.json`, encode([sample]));
    const result = run(kind, report, baseline);
    assert.equal(result.status, 0);
    assert.match(result.stdout, /no issues beyond the 1-issue baseline/);
  });
}

const checkedIn = JSON.parse(await readFile(join(root, "test/go-lint-baseline.json"), "utf8"));
const outsideIssue = { file: "test/scratch-new-issue.mjs", linter: "complexity", text: "function `probe` has a complexity of 11. Maximum allowed is 10." };
const checkedInRows = [
  { name: "the go gate accepts a report built from the checked-in baseline", issues: checkedIn, status: 0, output: /go lint: no issues beyond the \d+-issue baseline/ },
  { name: "the go gate rejects an issue outside the checked-in baseline", issues: [...checkedIn, outsideIssue], status: 1, output: /new: test\/scratch-new-issue\.mjs: complexity: / },
  { name: "the go gate rejects a report missing a checked-in baseline entry", issues: checkedIn.slice(1), status: 1, output: /fixed, remove from baseline: / }
];

for (const [index, row] of checkedInRows.entries()) {
  test(row.name, async () => {
    const report = await fixture(`checked-in-${index}.json`, go.encode(row.issues));
    const result = spawnSync(process.execPath, [script, "--check", "go", report], { cwd: root, encoding: "utf8" });

    assert.equal(result.status, row.status);
    assert.match(`${result.stdout}${result.stderr}`, row.output);
  });
}
