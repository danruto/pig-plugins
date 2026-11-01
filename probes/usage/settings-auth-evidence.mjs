/** Validate settings/auth capture receipts and derive a source-outcome evidence index; this is not whole-objective qualification. */
import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { cases } from "./cases.mjs";
import { readLedger } from "./ledger-artifact.mjs";

export function verifySettingsAuth(expected) {
  for (const implementation of ["source", "dist"]) {
    const rows = expected.consumers.filter((row) => row.implementation === implementation);
    const value = (id) => {
      const row = rows.find((entry) => entry.id === id);
      assert.ok(row, `Missing settings/auth receipt: ${implementation}/${id}`);
      assert.equal(row.outcome, "value", `Settings/auth setup failed: ${implementation}/${id}: ${row.error?.message}`);
      return row.value;
    };
    const defaults = value("settings:normalize:0");
    assert.deepEqual({ openaiUseCodexUsage: defaults.openaiUseCodexUsage, codexFastMode: defaults.codexFastMode, codexStatusResetCountdown: defaults.codexStatusResetCountdown, codexStatusPercentage: defaults.codexStatusPercentage, selectedTargets: defaults.selectedTargets },
      { openaiUseCodexUsage: false, codexFastMode: false, codexStatusResetCountdown: true, codexStatusPercentage: "remaining", selectedTargets: {} });
    assert.deepEqual(value("settings:normalize:10").selectedTargets, { fireworks: "selected" });
    for (const row of rows.filter((entry) => entry.id.startsWith("settings:extra-normalize:"))) {
      assert.equal(row.outcome, "value", row.id);
      const valid = ["valid-target-boundaries", "explicit-false", "legacy-with-other-target", "display-unknown"].some((name) => row.id.endsWith(`:${name}`));
      assert.equal(row.value !== undefined, valid, row.id);
    }
    for (const scenario of ["invalid-json", "invalid-shape", "oversize", "symlink", "directory"]) {
      const capture = value(`settings:file:${scenario}`);
      assert.equal(capture.observations[0].value.kind, "invalid");
      assert.match(capture.observations[1].error.message, /Cannot overwrite an invalid/);
      assert.deepEqual(capture.trace, []);
    }
    const atomic = value("settings:transaction:atomic-observer");
    assert.deepEqual(atomic.trace.map((entry) => entry.operation), ["write", "blocked-first-write", "rename", "write", "rename"]);
    assert.deepEqual(atomic.trace[1].disk.document, atomic.trace[0].before.document);
    assert.equal(atomic.trace[1].state.settings.codexFastMode, true);
    assert.equal(atomic.trace[3].before.document.codexFastMode, false);
    assert.equal(atomic.disk.document.codexFastMode, false);
    assert.equal(atomic.disk.document.codexStatusPercentage, "used");
    assert.deepEqual(atomic.disk.document.unknown, { keep: true });
    for (const entry of atomic.trace.filter((entry) => entry.operation === "rename")) assert.equal(entry.temporaryMode, 0o600);
    assert.equal(atomic.disk.mode, 0o600);
    const removed = value("settings:transaction:delete-patch");
    assert.deepEqual(removed.disk.document, { unknown: { keep: true } });
    assert.equal(removed.state.settings.codexFastMode, false);
    assert.deepEqual(value("settings:transaction:snapshot-isolation").state.settings.selectedTargets, { fireworks: "legacy" });
    for (const scenario of ["abort-after-write", "abort-publication"]) {
      const capture = value(`settings:transaction:${scenario}`);
      assert.equal(capture.observations[1].error.name, "AbortError");
      assert.deepEqual(capture.disk.document, capture.observations[0].value.document);
      assert.deepEqual(capture.remainingFiles, ["pi-usage.json"]);
    }
    for (const scenario of ["rollback-write-failure", "rollback-missing-conflict"]) {
      const capture = value(`settings:transaction:${scenario}`);
      assert.equal(capture.observations[1].error.name, "AggregateError");
      assert.equal(capture.observations[1].error.errors.length, 2);
      assert.deepEqual(capture.state.document, capture.disk.document);
      assert.deepEqual(capture.remainingFiles, ["pi-usage.json"]);
    }
    assert.deepEqual(value("settings:transaction:rollback-missing-conflict").disk.document, { external: true });
    assert.equal(value("settings:transaction:rollback-write-failure").disk.document.selectedTargets.fireworks, "selected");
    assert.equal(value("settings:transaction:non-fireworks-target").disk.document.fireworksAccountId, "legacy");
    assert.equal(value("settings:transaction:invalid-provider").observations[1].error.message, "Refusing to save an invalid usage target selection");
    assert.equal(value("settings:transaction:queued-abort-recovery").state.settings.codexStatusPercentage, "used");
    assert.equal(value("settings:transaction:invalid-display-file").state.kind, "invalid");
    assert.equal(value("settings:transaction:exact-size").observations[0].value.kind, "loaded");
    assert.equal(value("settings:file:queue-recovers").runtime.settings.codexStatusPercentage, "used");
    for (const scenario of ["target-rollback-loaded", "target-rollback-missing", "target-rollback-conflict"]) {
      const capture = value(`settings:file:${scenario}`);
      assert.ok(capture.observations[1].error);
      if (scenario === "target-rollback-missing") { assert.equal(capture.runtime.kind, "missing"); assert.deepEqual(capture.remainingFiles, []); }
      else if (scenario === "target-rollback-conflict") assert.deepEqual(JSON.parse(capture.file), { external: true });
      else assert.deepEqual(JSON.parse(capture.file), capture.observations[0].value.document);
    }
    assert.equal(value("settings:host-default-path").state.kind, "missing");

    const hostRows = rows.filter((row) => row.id.startsWith("auth:host:"));
    for (const row of hostRows) {
      const capture = value(row.id);
      assert.ok(capture.selectedAuth, row.id);
      assert.ok(capture.status, row.id);
      assert.ok(!JSON.stringify(capture).includes("fixture-wrong-root"), row.id);
      const variant = row.id.split(":").at(-1);
      const provider = capture.selected.provider;
      if (["stored", "other-provider", "readiness-rotate"].includes(variant)) {
        assert.ok(capture.resolved && !capture.error, row.id);
        assert.equal(capture.resolved.source, capture.providerAuth.source, row.id);
        assert.equal(capture.resolved.model.provider, provider);
        assert.equal(capture.resolved.auth.apiKey, capture.providerAuth.auth.apiKey);
        if (variant === "readiness-rotate" && provider !== "openai-codex") assert.equal(capture.resolved.auth.apiKey, "fixture-rotated");
      }
      if (variant === "provider-origin") assert.match(capture.error.message, /overridden provider credential/);
      if (variant === "model-origin") assert.match(capture.error.message, /custom provider base URL credential/);
      if (variant === "readiness-reject") assert.match(capture.error.message, /readiness failed closed/);
      if (variant === "readiness-abort") assert.equal(capture.error.name, "AbortError");
      if (variant === "readiness-stale") assert.equal(capture.error.name, "UsageStaleContextError");
      if (["offer-only", "clone-offer", "late-offer", "malformed-offer", "candidate-other-account"].includes(variant)) {
        assert.ok(capture.resolved && !capture.error, row.id);
        assert.ok(capture.resolved.secrets.includes("fixture-refresh"), row.id);
        assert.ok(!capture.resolved.secrets.includes("fixture-mutated-after-offer"), row.id);
      }
      if (["offer-no-match", "conflicting", "mismatch", "incomplete"].includes(variant) && ["openai", "github-copilot", "xai"].includes(provider)) assert.ok(capture.error && !capture.resolved, row.id);
      if (variant === "late-offer") {
        const observation = capture.trace.find((entry) => entry.event === "late-offer");
        assert.ok(observation.before.candidates.length > 0);
        assert.deepEqual(observation.after, observation.before);
      }
      if (variant === "duplicate") assert.equal(Boolean(capture.error), provider === "xai", row.id);
      if (["null-authorization", "empty-authorization", "other-authorization"].includes(variant)) {
        if (provider === "openai" || (provider === "deepseek" && variant === "other-authorization")) assert.ok(capture.error && !capture.resolved, row.id);
        else assert.ok(capture.resolved && !capture.error, row.id);
      }
      if (variant === "regional-shared-sibling" || variant === "regional-sibling-only") {
        assert.equal(capture.configured, false);
        assert.equal(capture.status.label, "MOONSHOT_API_KEY");
        assert.equal(capture.resolved, undefined);
      }
      if (variant === "regional-shared-current") assert.equal(capture.resolved.apiKey, "fixture-shared-moonshot");
      if (variant === "regional-distinct") assert.equal(capture.resolved.apiKey, `fixture-${provider}-region`);
      if (variant === "regional-stored-sibling") assert.equal(capture.resolved.apiKey, "fixture-access");
      if (variant === "regional-explicit-sibling") assert.equal(capture.resolved.apiKey, "fixture-region-specific");
    }
    assert.deepEqual(hostRows.filter((row) => row.id.endsWith(":stored")).map((row) => row.value.selected.provider).sort(), cases.filter((item) => item.id.endsWith(":normal")).map((item) => item.provider).sort());
    assert.equal(value("auth:host:openai:api-key").error.name, "UnsupportedOpenAIUsageAuthError");
    const companion = value("auth:host:openai:companion").resolved;
    assert.equal(companion.apiKey, "fixture-access");
    assert.equal(companion.openaiCompanion.headers["ChatGPT-Account-Id"], "fixture-account");
    assert.notEqual(companion.headers.Authorization, companion.openaiCompanion.headers.Authorization);
    const missing = value("auth:host:openai:companion-missing").resolved;
    assert.equal(missing.openaiUseCodexUsage, true);
    assert.equal(missing.openaiCompanion, undefined);
    for (const variant of ["companion-api-key", "companion-account-mismatch", "companion-conflicting", "companion-readiness-reject", "companion-invalid-token"]) assert.ok(value(`auth:host:openai:${variant}`).error);
  }
}

export async function settingsAuthEvidence(project, inventory, capture) {
  const retained = join(project, "docs/compatibility/usage");
  const expected = JSON.parse(await readFile(join(retained, "expected.json"), "utf8"));
  verifySettingsAuth(expected);
  const ledger = JSON.parse(await readLedger(join(retained, "branch-ledger.json.gz")));
  const family = (id) => id.startsWith("settings:") || id.startsWith("auth:");
  const fixtures = inventory.semanticFixtures.filter((item) => family(item.id)).map((item) => {
    const index = ledger.fixtureMappings.findIndex((entry) => entry.fixtureId === item.id);
    assert.ok(index >= 0, item.id);
    const mapping = ledger.fixtureMappings[index];
    assert.deepEqual(item.evidence, mapping.output);
    return { id: item.id, inputs: item.inputs, expected: item.evidence, sourceBranchEvidence: item.sourceBranchEvidence,
      hostResolution: item.id.startsWith("auth:host:") ? "Real PiG registry with host-registered model view and offline synthetic OAuth resolver; not production selected-model lifecycle" : "Not established by this fixture",
      sourceBranchOutcomes: mapping.sourceBranchOutcomes };
  });
  const sourceIds = new Set(fixtures.flatMap((item) => item.sourceBranchOutcomes));
  const queryText = await readFile(join(project, "node_modules/@narumitw/pi-usage/src/query.ts"), "utf8");
  const nestedAuthRanges = [["export async function resolveUsageAuth(", "export async function queryProviderUsage("], ["function candidateModels(", "export async function fetchProviderJson("]]
    .map(([start, end]) => [queryText.indexOf(start), queryText.indexOf(end)]);
  assert.ok(nestedAuthRanges.every(([start, end]) => start >= 0 && end > start));
  const relevant = ledger.obligations.filter((item) => ["src/settings.ts", "src/oauth-credential-source.ts", "src/auth-resolution-guard.ts", "src/openai-companion-auth.ts"].includes(item.source)
    || sourceIds.has(item.id)
    || (item.source === "src/query.ts" && (nestedAuthRanges.some(([start, end]) => item.start >= start && item.start < end) || ["resolveUsageAuth", "providerIsConfigured", "moonshotProviderAuthIsAllowed", "moonshotProviderAuthSourceIsAllowed", "isMoonshotSiblingProvider", "candidateModels", "resolveXaiUsageAuth", "resolveGitHubCopilotUsageAuth", "authorizationFrom", "bearerToken", "asObject", "isPublicGitHubDomain", "hasOfficialOrigin", "hasOfficialUrlOrigin", "headerValue"].includes(item.symbol)))
    || (item.source === "src/providers/openai-chatgpt.ts" && item.symbol !== "query"));
  const result = { generatedBy: "GENERATED FROM CODE; DO NOT EDIT. Run node probes/usage-baseline.mjs --capture", baselineCommit: ledger.baselineCommit,
    qualification: "Bounded settings/auth observations verified; not complete source-outcome or whole-objective acceptance",
    fixtures,
    sourceOutcomes: relevant.map((item) => ({ id: item.id, source: item.source, symbol: item.symbol, line: item.line, input: item.input, outcome: item.outcome,
      sourceSha256: item.sourceSha256, symbolSha256: item.symbolSha256, branchSha256: item.branchSha256,
      status: sourceIds.has(item.id) ? "observed in settings/auth fixtures" : "pending settings/auth witness",
      evidence: item.evidence.filter((entry) => family(entry.fixtureId)), ...(sourceIds.has(item.id) ? {} : { pendingReason: item.pendingReason ?? "Observed only outside settings/auth fixtures" }) })),
    laterBatches: ["Registered /usage settings UI and transactional settings races; registered menu/current/other/all/target workflows are captured in lifecycle.json", "Cache/backoff/request-boundary revalidation and stale-publication interleavings under actual session/model/settings replacement beyond the lifecycle.json slice", "Fast and reset outcomes still pending in lifecycle.json; registered catalogue discovery, request-tier and message-cost hooks, reset confirmation/retry/redemption and credential-candidate failures are captured there", "Remaining adapter and owned normalization/footer evidence", "Observation rules beyond the branch-ledger count rules and dist attribution; pending syntax is not exempt"],
    limitations: ["OAuth getApiKey is an explicitly registered offline fixture resolver returning the synthetic stored access, not built-in network token exchange", "Host model view comes from the real registry but does not change the production composition's selected DeepSeek model", "Readiness abort/stale inputs exercise auth continuation guards, not an actual session replacement", "Missing implicit source arms and unmatched transformations stay pending in branch-ledger.json", "Frozen Copilot/xAI null/empty/non-Bearer model overrides can fall back to provider OAuth; capture is not safety approval", "No numerical quota or independent same-account identity is inferred from native OpenAI/companion authentication"] };
  const path = join(retained, "settings-auth.json");
  if (capture) await writeFile(path, JSON.stringify(result, null, 2) + "\n");
  else assert.deepEqual(result, JSON.parse(await readFile(path, "utf8")), "Settings/auth evidence drift");
  console.log(`usage settings/auth: ${fixtures.length} fixture mappings; ${result.sourceOutcomes.filter((item) => item.status.startsWith("observed")).length} witnessed family outcomes; ${result.sourceOutcomes.filter((item) => item.status.startsWith("pending")).length} pending family outcomes`);
}
