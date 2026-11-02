import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { appendFileSync, existsSync, mkdtempSync, mkdirSync, readFileSync, realpathSync, rmSync, statSync, symlinkSync, writeFileSync } from "node:fs";
import { createInterface } from "node:readline";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

test("PiG automatically discovers the packaged native footer", () => {
	const pkg = resolve(process.env.PIG_STATUS_PACKAGE);
	const root = mkdtempSync(join(tmpdir(), "pig-status-discovery-"));
	try {
		const agent = join(root, "agent");
		mkdirSync(join(agent, "extensions"), { recursive: true });
		symlinkSync(pkg, join(agent, "extensions", "pig-status"));
		const result = spawnSync("pig", ["status", "--json"], {
			cwd: root,
			env: { ...process.env, PIG_HOME: root, PIG_CODING_AGENT_DIR: agent },
			encoding: "utf8",
			timeout: 20_000,
		});
		assert.equal(result.status, 0, result.stderr || result.stdout);
		const status = JSON.parse(result.stdout);
		const footers = status.resources.items.filter(
			(item) => item.kind === "extensions" && item.name === "pig-status",
		);
		assert.equal(footers.length, 1, "native footer must be discovered once");
		assert.equal(footers[0].enabled, true);
		assert.equal(footers[0].path, realpathSync(join(pkg, process.env.PIG_STATUS_NATIVE ?? "extensions/pig-status")));
		const collectors = status.resources.items.filter((item) => item.kind === "extensions" && item.name === "pig-status-usage");
		assert.equal(collectors.length, 1, "status must own exactly one usage collector");
		assert.equal(collectors[0].enabled, true);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

function hermesFixture() {
	const pkg = resolve(process.env.PIG_STATUS_PACKAGE);
	const root = mkdtempSync(join(tmpdir(), "pig-hermes-startup-"));
	const agent = join(root, "native-agent");
	const shared = join(root, "shared-agent");
	const cwd = join(root, "session-project");
	for (const path of [agent, shared, cwd, join(root, "home")]) mkdirSync(path, { recursive: true });
	writeFileSync(join(shared, "hermes-memory-config.json"), "shared settings must not be read");
	writeFileSync(join(root, "release"), "");
	const env = {
		PATH: process.env.PATH, HOME: join(root, "home"),
		PIG_HOME: join(root, "pig"), PIG_CODING_AGENT_DIR: agent, PI_CODING_AGENT_DIR: shared,
		PIG_CODING_AGENT_SESSION_DIR: join(root, "pig-sessions"), PI_CODING_AGENT_SESSION_DIR: join(root, "pi-sessions"),
		XDG_CONFIG_HOME: join(root, "config"), XDG_CACHE_HOME: join(root, "cache"), XDG_DATA_HOME: join(root, "data"), XDG_STATE_HOME: join(root, "state"),
		TMPDIR: process.env.TMPDIR ?? root, GOCACHE: process.env.GOCACHE ?? join(root, "go-cache"), GOMODCACHE: process.env.GOMODCACHE ?? join(process.env.HOME, "go/pkg/mod"),
		GOMAXPROCS: "2", GOFLAGS: "-p=2", GOTELEMETRY: "off", PI_OFFLINE: "1", PI_TELEMETRY: "0",
		PROBE_REPORT: join(root, "provider.json"), PROBE_RELEASE: join(root, "release"),
	};
	const diagnosis = spawnSync("pig", ["diagnose"], { cwd, env, encoding: "utf8", timeout: 20_000 });
	assert.equal(diagnosis.status, 0, diagnosis.stderr);
	const binary = diagnosis.stdout.match(/^binary:\s+(.+)$/m)?.[1];
	assert.equal(typeof binary, "string", "PiG must identify its public runtime executable");
	return { pkg, root, agent, shared, cwd, env, binary };
}

function startHermes(fixture, input = '{"id":"extensions","type":"get_extensions"}\n{"id":"state","type":"get_state"}\n') {
	return spawnSync(fixture.binary, ["--offline", "--mode", "rpc", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files", "--no-mcp", "--no-tools", "-e", join(fixture.pkg, "extensions/pig-hermes"), "-e", join(fixture.pkg, "probes/completion-probe"), "--model", "hermes-probe/fixture"], {
		cwd: fixture.cwd, env: fixture.env, input, encoding: "utf8", timeout: 120_000, maxBuffer: 4 * 1024 * 1024,
	});
}

function sqliteCheckpoint(path) {
	const result = spawnSync("python3", ["-c", "import sqlite3,json,sys; c=sqlite3.connect('file:'+sys.argv[1]+'?mode=ro',uri=True); print(json.dumps({'integrity':c.execute('PRAGMA integrity_check').fetchone()[0], 'tokenizer':c.execute(\"SELECT value FROM extension_metadata WHERE key='fts5_tokenizer_version'\").fetchone()[0], 'tables':[r[0] for r in c.execute(\"SELECT name FROM sqlite_schema WHERE type='table' AND name IN ('memories','messages','sessions','session_files','extension_metadata') ORDER BY name\")]})); c.close()", path], { encoding: "utf8", timeout: 10_000 });
	assert.equal(result.status, 0, result.stderr);
	return JSON.parse(result.stdout);
}

const hermesSettingsLocations = [
	{ name: "native agent", select: (fixture) => join(fixture.agent, "hermes-memory-config.json") },
	{ name: "explicit config", select: (fixture) => {
		writeFileSync(join(fixture.agent, "hermes-memory-config.json"), "must not read unselected native settings");
		fixture.env.PIG_HERMES_CONFIG = join(fixture.root, "selected-config.json");
		return fixture.env.PIG_HERMES_CONFIG;
	} },
	{ name: "PiG home", select: (fixture) => {
		delete fixture.env.PIG_CODING_AGENT_DIR;
		mkdirSync(join(fixture.env.PIG_HOME, "agent"), { recursive: true });
		return join(fixture.env.PIG_HOME, "agent/hermes-memory-config.json");
	} },
	{ name: "default home", select: (fixture) => {
		delete fixture.env.PIG_CODING_AGENT_DIR;
		delete fixture.env.PIG_HOME;
		mkdirSync(join(fixture.env.HOME, ".pig/agent"), { recursive: true });
		return join(fixture.env.HOME, ".pig/agent/hermes-memory-config.json");
	} },
];

for (const location of hermesSettingsLocations) test(`AC-factory: offline native Hermes startup loads only native settings and opens the configured shared schema (${location.name})`, () => {
	const fixture = hermesFixture();
	try {
		const knowledge = join(fixture.root, "configured-knowledge");
		writeFileSync(location.select(fixture), JSON.stringify({ memoryDir: knowledge, memoryMode: "legacy-inject", memoryCharLimit: 123 }));

		const result = startHermes(fixture);
		assert.equal(result.error, undefined);
		assert.equal(result.status, 0, result.stderr || result.stdout);
		const records = result.stdout.trim().split("\n").map((line) => JSON.parse(line));
		assert.deepEqual(records.filter((record) => record.type === "extension_error" || record.type === "error"), []);
		assert.deepEqual(records.find((record) => record.id === "extensions").data.paths, [join(fixture.pkg, "extensions/pig-hermes"), join(fixture.pkg, "probes/completion-probe")]);
		assert.equal(records.find((record) => record.id === "state").data.model.provider, "hermes-probe");
		assert.deepEqual(sqliteCheckpoint(join(knowledge, "sessions.db")), { integrity: "ok", tokenizer: "trigram-v1", tables: ["extension_metadata", "memories", "messages", "session_files", "sessions"] });
		assert.equal(readFileSync(join(fixture.shared, "hermes-memory-config.json"), "utf8"), "shared settings must not be read");
	} finally {
		rmSync(fixture.root, { recursive: true, force: true });
	}
});

test("AC-factory-default: offline native Hermes missing settings initializes knowledge under the separate Pi root", () => {
	const fixture = hermesFixture();
	try {
		const result = startHermes(fixture);

		assert.equal(result.status, 0, result.stderr || result.stdout);
		assert.deepEqual(sqliteCheckpoint(join(fixture.shared, "pi-hermes-memory/sessions.db")), { integrity: "ok", tokenizer: "trigram-v1", tables: ["extension_metadata", "memories", "messages", "session_files", "sessions"] });
	} finally {
		rmSync(fixture.root, { recursive: true, force: true });
	}
});

const hermesFailureInputs = [
	{ name: "malformed settings", prepare: (fixture) => {
		fixture.env.PIG_HERMES_CONFIG = join(fixture.root, "synthetic-private-config.json");
		writeFileSync(fixture.env.PIG_HERMES_CONFIG, '{"synthetic-secret-content":');
	}, stage: /Hermes.*configuration|configuration.*Hermes/i },
	{ name: "relative override", prepare: (fixture) => {
		fixture.env.PIG_HERMES_CONFIG = "synthetic-private-config.json";
	}, stage: /Hermes.*(configuration|environment)|(configuration|environment).*Hermes/i },
	{ name: "blocked database parent", prepare: (fixture) => {
		const blocked = join(fixture.root, "synthetic-private-config");
		writeFileSync(blocked, "synthetic-secret-content");
		writeFileSync(join(fixture.agent, "hermes-memory-config.json"), JSON.stringify({ memoryDir: join(blocked, "knowledge") }));
	}, stage: /Hermes.*(path|storage|database)|(path|storage|database).*Hermes/i },
];

for (const input of hermesFailureInputs) test(`AC-factory-reject: initialization failure reports no private paths or content (${input.name})`, () => {
	const fixture = hermesFixture();
	try {
		input.prepare(fixture);

		const result = startHermes(fixture);
		assert.equal(result.error, undefined);
		const diagnostic = result.stderr + result.stdout;
		const errors = result.stdout.trim().split("\n").filter(Boolean).map((line) => JSON.parse(line)).filter((record) => record.type === "extension_error");
		assert.equal(errors.length, 1, diagnostic);
		assert.equal(errors[0].event, "session_start");
		assert.match(errors[0].error, input.stage);
		assert.doesNotMatch(diagnostic, /synthetic-private-config|synthetic-secret-content/);
	} finally {
		rmSync(fixture.root, { recursive: true, force: true });
	}
});

function startHermesLifecycle(fixture) {
	const child = spawn(fixture.binary, ["--offline", "--mode", "rpc", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files", "--no-mcp", "--no-tools", "-e", join(fixture.pkg, "extensions/pig-hermes"), "-e", join(fixture.pkg, "probes/completion-probe"), "--model", "hermes-probe/fixture"], {
		cwd: fixture.cwd, env: fixture.env, detached: true, stdio: ["pipe", "pipe", "pipe"],
	});
	if (process.env.HERMES_ACCEPTANCE_PID_FILE) appendFileSync(process.env.HERMES_ACCEPTANCE_PID_FILE, `${child.pid}\n`);
	const pending = new Map();
	const errors = [];
	let stderr = "";
	let sequence = 0;
	child.stderr.setEncoding("utf8").on("data", (text) => { stderr += text; });
	child.on("error", (error) => errors.push({ type: "process_error", error: error.message }));
	createInterface({ input: child.stdout }).on("line", (line) => {
		const record = JSON.parse(line);
		if (record.type === "extension_error" || record.type === "error") errors.push(record);
		const waiter = pending.get(record.id);
		if (waiter) {
			pending.delete(record.id);
			waiter.resolve(record);
		}
	});
	const exit = new Promise((resolve) => child.on("close", (code, signal) => {
		for (const waiter of pending.values()) waiter.reject(new Error(`PiG closed before its RPC response: ${code}/${signal}: ${stderr}`));
		pending.clear();
		resolve({ code, signal });
	}));
	function kill() {
		if (child.exitCode !== null || child.signalCode !== null) return;
		process.kill(-child.pid, "SIGKILL");
	}
	const deadline = setTimeout(kill, 120_000);
	deadline.unref();
	return {
		errors,
		request(type) {
			const id = `lifecycle-${++sequence}`;
			const response = new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
			child.stdin.write(`${JSON.stringify({ id, type })}\n`);
			return response;
		},
		async finish() {
			child.stdin.end();
			return await exit;
		},
		async dispose() {
			kill();
			await exit;
			clearTimeout(deadline);
		},
	};
}

function selectHermesKnowledge(fixture, name) {
	const database = join(fixture.root, name, "sessions.db");
	writeFileSync(join(fixture.agent, "hermes-memory-config.json"), JSON.stringify({ memoryDir: join(fixture.root, name) }));
	return database;
}

test("RULE-replacement-lifetime: offline Hermes retires the previous store when RPC replaces its session", async () => {
	const fixture = hermesFixture();
	const oldDB = selectHermesKnowledge(fixture, "first-knowledge");
	const runtime = startHermesLifecycle(fixture);
	try {
		const started = await runtime.request("get_state");
		assert.equal(started.success, true);
		assert.ok(statSync(`${oldDB}-wal`).size > 0);
		const newDB = selectHermesKnowledge(fixture, "replacement-knowledge");

		const replaced = await runtime.request("new_session");
		const replacedState = await runtime.request("get_state");

		assert.equal(replaced.success, true);
		assert.notEqual(replacedState.data.sessionId, started.data.sessionId);
		assert.ok(statSync(`${newDB}-wal`).isFile());
		assert.equal(existsSync(`${oldDB}-wal`), false);
		assert.deepEqual(sqliteCheckpoint(newDB), { integrity: "ok", tokenizer: "trigram-v1", tables: ["extension_metadata", "memories", "messages", "session_files", "sessions"] });
		assert.deepEqual(runtime.errors, []);
		await runtime.finish();
		assert.equal(existsSync(`${newDB}-wal`), false);
	} finally {
		await runtime.dispose();
		rmSync(fixture.root, { recursive: true, force: true });
	}
});

test("RULE-shutdown-lifetime: offline Hermes closes its current store on RPC EOF", async () => {
	const fixture = hermesFixture();
	const currentDB = selectHermesKnowledge(fixture, "shutdown-knowledge");
	const runtime = startHermesLifecycle(fixture);
	try {
		const started = await runtime.request("get_state");
		assert.equal(started.success, true);
		assert.ok(statSync(`${currentDB}-wal`).size > 0);

		const exit = await runtime.finish();

		assert.equal(exit.code, 0);
		assert.equal(exit.signal, null);
		assert.equal(existsSync(`${currentDB}-wal`), false);
		assert.deepEqual(sqliteCheckpoint(currentDB), { integrity: "ok", tokenizer: "trigram-v1", tables: ["extension_metadata", "memories", "messages", "session_files", "sessions"] });
		assert.deepEqual(runtime.errors, []);
	} finally {
		await runtime.dispose();
		rmSync(fixture.root, { recursive: true, force: true });
	}
});
