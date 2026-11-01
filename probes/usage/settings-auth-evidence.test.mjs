/** Negative controls for independently verified settings/auth capture receipts. */
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { verifySettingsAuth } from "./settings-auth-evidence.mjs";

const expected = JSON.parse(await readFile(new URL("../../docs/compatibility/usage/expected.json", import.meta.url), "utf8"));

test("settings/auth receipts satisfy the frozen behavior checks", () => {
  verifySettingsAuth(expected);
});

for (const [name, id, mutate] of [
  ["setup errors cannot become passing captures", "auth:host:openai:stored", (row) => { row.outcome = "error"; row.error = { message: "setup failed" }; }],
  ["readiness must resolve the rotated host credential", "auth:host:deepseek:readiness-rotate", (row) => { row.value.resolved.auth.apiKey = "stale"; }],
  ["private temporary mode is checked before replacement", "settings:transaction:atomic-observer", (row) => { row.value.trace[2].temporaryMode = 0o644; }],
  ["queue ordering cannot be replaced by concurrent writes", "settings:transaction:atomic-observer", (row) => { [row.value.trace[2], row.value.trace[3]] = [row.value.trace[3], row.value.trace[2]]; }],
  ["failed rollback must reload the surviving disk document", "settings:transaction:rollback-missing-conflict", (row) => { row.value.state.document = {}; }],
  ["shared regional credentials cannot configure a sibling", "auth:host:moonshotai-cn:regional-shared-sibling", (row) => { row.value.configured = true; }],
  ["companion auth cannot reuse the native credential", "auth:host:openai:companion", (row) => { row.value.resolved.openaiCompanion.headers.Authorization = row.value.resolved.headers.Authorization; }],
]) {
  test(`settings/auth receipt guard: ${name}`, () => {
    const changed = structuredClone(expected);
    const row = changed.consumers.find((entry) => entry.id === id && entry.implementation === "source");
    assert.ok(row);
    mutate(row);
    assert.throws(() => verifySettingsAuth(changed), assert.AssertionError);
  });
}
