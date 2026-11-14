import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, realpathSync, rmSync, symlinkSync } from "node:fs";
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
