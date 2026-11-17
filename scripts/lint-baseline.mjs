/** Compares a linter JSON report with the checked-in baseline of pre-existing issues. */
import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";

const kinds = {
  go: {
    baseline: "../test/go-lint-baseline.json",
    regenerate: "npm run lint:go-baseline",
    parse: ({ Issues }) => {
      assert.ok(Issues === null || Array.isArray(Issues), "golangci-lint report has no Issues list");
      return (Issues ?? []).map(({ FromLinter, Pos, Text }) => ({ file: Pos.Filename, linter: FromLinter, text: Text }));
    }
  },
  ts: {
    baseline: "../test/ts-lint-baseline.json",
    regenerate: "npm run lint:ts-baseline",
    parse: ({ diagnostics }) => {
      assert.ok(Array.isArray(diagnostics), "oxlint report has no diagnostics list");
      return diagnostics.map(({ code, filename, message }) => ({ file: filename, linter: code, text: message }));
    }
  }
};

const key = (issue) => `${issue.file}\0${issue.linter}\0${issue.text}`;

async function readJson(path) {
  try {
    return JSON.parse(await readFile(path, "utf8"));
  } catch (error) {
    throw new Error(`${path} is not a readable JSON report`, { cause: error });
  }
}

async function record(destination, label, issues) {
  await writeFile(destination, `${JSON.stringify(issues, null, 2)}\n`);
  console.log(`${label} lint baseline: ${issues.length} issues recorded`);
}

async function compare(destination, label, regenerate, issues) {
  const remaining = new Map();
  for (const issue of await readJson(destination)) remaining.set(key(issue), (remaining.get(key(issue)) ?? 0) + 1);
  const added = issues.filter((issue) => {
    const count = remaining.get(key(issue)) ?? 0;
    remaining.set(key(issue), count - 1);
    return count <= 0;
  });
  const fixed = [...remaining].filter(([, count]) => count > 0).map(([entry]) => entry);
  for (const issue of added) console.error(`new: ${issue.file}: ${issue.linter}: ${issue.text}`);
  for (const entry of fixed) console.error(`fixed, remove from baseline: ${entry.replaceAll("\0", ": ")}`);
  assert.ok(added.length === 0 && fixed.length === 0, `${label} lint differs from its baseline; fix new issues, then regenerate with ${regenerate}`);
  console.log(`${label} lint: no issues beyond the ${issues.length}-issue baseline`);
}

const [mode, kind, report, override] = process.argv.slice(2);
const lint = kinds[kind];
assert.ok((mode === "--check" || mode === "--write") && lint && report && process.argv.length <= 6, "Usage: lint-baseline.mjs --check|--write go|ts <report.json> [baseline.json]");
const destination = override ?? new URL(lint.baseline, import.meta.url);
const issues = lint.parse(await readJson(report)).sort((a, b) => key(a).localeCompare(key(b)));
if (mode === "--write") await record(destination, kind, issues);
else await compare(destination, kind, lint.regenerate, issues);
