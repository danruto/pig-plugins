/** Compares a golangci-lint JSON report with the checked-in baseline of pre-existing issues. */
import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";

const [mode, report] = process.argv.slice(2);
assert.ok((mode === "--check" || mode === "--write") && report && process.argv.length === 4, "Usage: go-lint-baseline.mjs --check|--write <golangci-lint.json>");
const destination = new URL("../test/go-lint-baseline.json", import.meta.url);
const { Issues } = JSON.parse(await readFile(report, "utf8"));
assert.ok(Issues === null || Array.isArray(Issues), "golangci-lint report has no Issues list");
const key = (issue) => `${issue.file}\0${issue.linter}\0${issue.text}`;
const issues = (Issues ?? [])
  .map(({ FromLinter, Pos, Text }) => ({ file: Pos.Filename, linter: FromLinter, text: Text }))
  .sort((a, b) => key(a).localeCompare(key(b)));
if (mode === "--write") {
  await writeFile(destination, `${JSON.stringify(issues, null, 2)}\n`);
  console.log(`go lint baseline: ${issues.length} issues recorded`);
} else {
  const remaining = new Map();
  for (const issue of JSON.parse(await readFile(destination, "utf8"))) remaining.set(key(issue), (remaining.get(key(issue)) ?? 0) + 1);
  const added = issues.filter((issue) => {
    const count = remaining.get(key(issue)) ?? 0;
    remaining.set(key(issue), count - 1);
    return count <= 0;
  });
  const fixed = [...remaining].filter(([, count]) => count > 0).map(([entry]) => entry);
  for (const issue of added) console.error(`new: ${issue.file}: ${issue.linter}: ${issue.text}`);
  for (const entry of fixed) console.error(`fixed, remove from baseline: ${entry.replaceAll("\0", ": ")}`);
  assert.ok(added.length === 0 && fixed.length === 0, "Go lint differs from test/go-lint-baseline.json; fix new issues, then regenerate with npm run lint:go-baseline");
  console.log(`go lint: no issues beyond the ${issues.length}-issue baseline`);
}
