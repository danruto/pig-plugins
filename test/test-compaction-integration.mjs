/** PiG-only registration/lifecycle checks with disposable, offline synthetic sessions. */
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cpSync, mkdtempSync, mkdirSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync, chmodSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";

const project = resolve(import.meta.dirname, "..");

function fixture(t) {
	const root = mkdtempSync(join(tmpdir(), "pig-compaction-integration-"));
	t.after(() => rmSync(root, { recursive: true, force: true }));
	return { root, env: {
		PATH: process.env.PATH, HOME: root, TMPDIR: root,
		PIG_HOME: join(root, "pig"), PIG_CODING_AGENT_DIR: join(root, "agent"),
		PI_CODING_AGENT_DIR: join(root, "pi-agent"), CODEX_HOME: join(root, "codex"),
		XDG_CONFIG_HOME: join(root, "config"),
	} };
}

function validate(path, fixture) {
	const result = spawnSync("pig", ["install", path, "--validate-only", "--json"], {
		cwd: fixture.root, env: fixture.env, encoding: "utf8", timeout: 60_000,
	});
	assert.equal(result.status, 0, result.stderr || result.stdout);
	const registration = JSON.parse(result.stdout);
	assert.equal(registration.valid, true, result.stdout);
	return registration;
}

function runHandlers(t, body) {
	const f = fixture(t);
	const copy = join(f.root, "package");
	mkdirSync(copy);
	cpSync(join(project, "compaction"), join(copy, "compaction"), { recursive: true });
	const entry = join(copy, "extensions/pig-openai-server-compaction.ts");
	mkdirSync(join(copy, "extensions"));
	cpSync(join(project, "extensions/pig-openai-server-compaction.ts"), entry);
	writeFileSync(join(copy, "package.json"), '{"type":"module"}');
	symlinkSync(join(project, "node_modules"), join(copy, "node_modules"));
	const generated = join(copy, "compaction/generated");
	const hashes = () => readdirSync(generated).map((name) => createHash("sha256").update(readFileSync(join(generated, name))).digest("hex"));
	const before = hashes();
	readdirSync(generated).forEach((name) => chmodSync(join(generated, name), 0o444));
	const receiptPath = join(f.root, "receipt.json");
	const harness = join(f.root, "check.mjs");
	writeFileSync(harness, `
import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import extension from ${JSON.stringify(pathToFileURL(entry).href)};
export default function () {
  const handlers = new Map();
  const providers = [];
  extension({ on: (name, handler) => handlers.set(name, handler), registerProvider: (...args) => providers.push(args) });
  const root = ${JSON.stringify(f.root)};
  const receiptPath = ${JSON.stringify(receiptPath)};
  ${body}
}
`);

	validate(harness, f);

	assert.deepEqual(hashes(), before);
	return JSON.parse(readFileSync(receiptPath, "utf8"));
}

test("public package entry loads once in PiG and npm pack includes the prepared runtime", (t) => {
	const f = fixture(t);
	const pkg = JSON.parse(readFileSync(join(project, "package.json"), "utf8"));
	const entries = pkg.pi.extensions.filter((entry) => entry.includes("compaction"));
	assert.equal(entries.length, 1);
	assert.ok(!pkg.pi.extensions.some((entry) => entry.includes("node_modules")));
	const registration = validate(project, f);
	const extensions = registration.extensions ?? [registration];
	const compactor = extensions.filter((entry) => entry.name === "pig-openai-server-compaction");
	assert.equal(compactor.length, 1, JSON.stringify(registration));
	assert.ok(compactor[0].handlers.includes("before_provider_request"));
	assert.ok(compactor[0].handlers.includes("session_before_compact"));
	const packed = spawnSync("npm", ["pack", "--dry-run", "--json", "--ignore-scripts"], {
		cwd: project, env: { ...f.env, npm_config_cache: join(f.root, "npm-cache") },
		encoding: "utf8", timeout: 30_000,
	});
	assert.equal(packed.status, 0, packed.stderr);
	const paths = JSON.parse(packed.stdout)[0].files.map((file) => file.path);
	[...entries, "compaction/payload.ts", "compaction/generated/index.ts", "compaction/generated/LICENSE.md"].forEach((path) => {
		assert.ok(paths.includes(path), `missing packaged runtime: ${path}`);
	});
});

test("default extension keeps native providers and exposes compaction and replay handlers", (t) => {
	const receipt = runHandlers(t, `
  const required = ["before_provider_request", "message_end", "session_before_compact", "session_compact", "session_shutdown", "session_start"];

  assert.deepEqual([...handlers.keys()].filter((name) => required.includes(name)).sort(), required);
  assert.deepEqual(providers, []);
  writeFileSync(receiptPath, JSON.stringify({ nativeProviders: true }));
`);

	assert.deepEqual(receipt, { nativeProviders: true });
});

[
	{ provider: "openai", api: "openai-responses" },
	{ provider: "openai-codex", api: "openai-codex-responses" },
].forEach(({ provider, api }) => {
	test(`${provider} native outgoing handler replays repeated compact and resume events without changing current context`, (t) => {
		const receipt = runHandlers(t, `
  const provider = ${JSON.stringify(provider)};
  const api = ${JSON.stringify(api)};
  const model = { provider, api, id: "fixture", input: ["text"] };
  const modelKey = provider + ":" + api + ":fixture";
  const context = [
    { role: "system", content: "Current fixture system instructions" },
    { type: "message", role: "developer", content: [{ type: "input_text", text: "Current fixture developer instructions" }] },
    { type: "additional_tools", role: "developer", tools: [
      { type: "function", name: "fffind", description: "Find fixture files", namespace: "fixture.files", parameters: { type: "object", properties: { pattern: { type: "string" } }, required: ["pattern"] }, strict: null },
      { type: "function", name: "shell", description: "Fixture shell", namespace: "fixture.shell", parameters: { type: "object", properties: { command: { type: "string" } }, required: ["command"], additionalProperties: false }, strict: true },
    ] },
    { type: "configuration_update", reasoning: { effort: "high" } },
  ];
  const live = { type: "message", role: "user", content: [{ type: "input_text", text: "Continue fixture" }] };
  const payload = { model: "fixture", tools: [], instructions: "current", input: [...context, { role: "user", content: "ordinary discarded history" }] };
  const original = structuredClone(payload);
  let branch = [];
  const ctx = { cwd: root, model, hasUI: false, ui: {}, sessionManager: { getSessionId: () => "synthetic-" + provider, getBranch: () => branch } };

  assert.equal(handlers.get("before_provider_request")({ payload }, ctx), undefined);
  ["session_compact", "session_start", "session_compact", "session_start"].forEach((event, index) => {
    const remote = [
      { type: "compaction", encrypted_content: "synthetic-opaque-" + Math.floor(index / 2) },
      { type: "function_call", call_id: "fixture-call-" + index, name: "shell", arguments: '{"command":"printf fixture"}' },
      { type: "function_call_output", call_id: "fixture-call-" + index, output: "fixture" },
    ];
    branch = [{ type: "compaction", id: "fixture-compaction-" + index, details: { remoteCompaction: { version: 2, provider: "openai-responses-compaction", modelKey, replacementHistory: [
      { role: "developer", content: "stale instruction" },
      { type: "additional_tools", role: "developer", tools: [{ type: "function", name: "removed_fixture_tool", parameters: { type: "object", properties: {} }, strict: null }] },
      ...remote,
    ] } } }];
    const originalBranch = structuredClone(branch);

    handlers.get(event)({}, ctx);
    handlers.get("message_end")({ message: { role: "user", content: [{ type: "text", text: "Continue fixture" }] } }, ctx);
    const actual = handlers.get("before_provider_request")({ payload }, ctx);

    assert.deepEqual(actual, { ...payload, input: [...context, ...remote, live] });
    assert.deepEqual(payload, original);
    assert.deepEqual(branch, originalBranch);
    const actualBefore = structuredClone(actual);
    const again = handlers.get("before_provider_request")({ payload: actual }, ctx);
    assert.deepEqual(again, actual);
    assert.deepEqual(actual, actualBefore);
  });
  ctx.model = { ...model, id: "other-fixture" };

  assert.equal(handlers.get("before_provider_request")({ payload }, ctx), undefined);
  assert.deepEqual(payload, original);
  handlers.get("session_shutdown")({}, ctx);
  writeFileSync(receiptPath, JSON.stringify({ lifecycleReplays: 4, provider }));
`);

		assert.deepEqual(receipt, { lifecycleReplays: 4, provider });
	});
});
