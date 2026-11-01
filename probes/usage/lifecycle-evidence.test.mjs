/** Receipt mutation controls: frozen output cannot silently lose registered-handler safety or partial results. */
import assert from "node:assert/strict";
import test from "node:test";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { execFile } from "node:child_process";
import { resolve, join } from "node:path";
import { promisify } from "node:util";
import { verifyLifecycle } from "./lifecycle-evidence.mjs";

const expected = JSON.parse(await readFile(new URL("../../docs/compatibility/usage/expected.json", import.meta.url), "utf8"));
const receipt = (data, id) => data.lifecycle.find((row) => row.id === `lifecycle:${id}`);

test("registered lifecycle receipts retain the actual command/API boundary contracts", () => {
  verifyLifecycle(expected);
});

test("registered lifecycle executable preserves exact requests and overlapping all-provider response barriers", async () => {
  const scratch = resolve(".pb/probes/7");
  await mkdir(scratch, { recursive: true });
  const outer = await mkdtemp(join(scratch, "lifecycle-independent-test-"));
  const roots = Object.fromEntries(["HOME", "PIG_HOME", "PIG_CODING_AGENT_DIR", "PI_CODING_AGENT_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "TMPDIR"].map((name) => [name, join(outer, name)]));
  await Promise.all(Object.values(roots).map((root) => mkdir(root, { recursive: true })));
  const env = { ...roots, PATH: process.env.PATH, GOMAXPROCS: "2", GOFLAGS: "-p=2" };

  const result = await promisify(execFile)(process.execPath, ["probes/usage-baseline.mjs", "--lifecycle-observe"], { env, maxBuffer: 16 * 1024 * 1024 });
  await Promise.all([writeFile(join(outer, "stdout.log"), result.stdout), writeFile(join(outer, "stderr.log"), result.stderr)]);
  const root = result.stdout.match(/^Disposable evidence: (.+)$/mu)?.[1];
  assert.ok(root && root.startsWith(`${scratch}/`));
  const observed = JSON.parse(await readFile(join(root, "observed.json"), "utf8"));
  const choreography = JSON.parse(await readFile(join(root, "response-choreography.json"), "utf8"));
  const events = choreography.find((row) => row.id === "lifecycle:command:all-partial").events;
  const accountsUrl = "https://api.fireworks.ai/v1/accounts?pageSize=200";
  const subscriptionsUrl = "https://api.commandcode.ai/alpha/billing/subscriptions?orgId=fixture-org";
  const summaryUrl = "https://api.commandcode.ai/alpha/usage/summary?orgId=fixture-org";
  const observedAccounts = events.findIndex((event) => event.kind === "request-observed" && event.url === accountsUrl);
  const observedSummary = events.findIndex((event) => event.kind === "request-observed" && event.url === summaryUrl);
  const releasedSubscriptions = events.findIndex((event) => event.kind === "response-released" && event.url === subscriptionsUrl);
  const releasedAccounts = events.findIndex((event) => event.kind === "response-released" && event.url === accountsUrl);
  const summaryRequest = events[observedSummary];

  assert.match(result.stdout, /clean shutdown/u);
  assert.deepEqual(observed.requests, expected.requests.filter((row) => row.id.startsWith("lifecycle:")));
  assert.deepEqual(observed.commandObservations, expected.commandObservations);
  assert.deepEqual(receipt(observed, "command:all-partial"), receipt(expected, "command:all-partial"));
  verifyLifecycle(observed);
  assert.deepEqual(choreography.map((row) => row.id), ["lifecycle:command:all-partial"]);
  assert.ok(observedAccounts >= 0 && observedSummary >= 0 && releasedSubscriptions >= 0 && releasedAccounts >= 0);
  assert.deepEqual(summaryRequest.outstanding, [accountsUrl, summaryUrl]);
  assert.ok(releasedSubscriptions < observedSummary);
  assert.ok(observedAccounts < releasedSubscriptions);
  assert.ok(observedSummary < releasedAccounts);
  assert.equal(choreography.some((row) => row.events.some((event) => event.kind === "cancelled")), false);
});

for (const [name, mutate] of [
  ["selected target silently replaced", (data) => { receipt(data, "command:target").settings.selectedTargets.fireworks = "fixture-account"; }],
  ["cancelled response published late", (data) => { receipt(data, "api:cancel-inflight").changes.push({ providerId: "deepseek", reason: "updated" }); }],
  ["stale provider response published into successor", (data) => { receipt(data, "api:stale-model").changes.push({ providerId: "deepseek", reason: "updated" }); }],
  ["partial API report loses mandatory quota", (data) => { receipt(data, "api:query-partial").response.report.buckets = []; }],
  ["all-provider action omits an explicit configured provider", (data) => { data.requests = data.requests.filter((row) => row.id !== "lifecycle:command:all-partial" || !row.url.includes("api.commandcode.ai")); }],
  ["enabled Fast request sent at the standard tier", (data) => {
    const request = data.requests.find((row) => row.id === "lifecycle:fast:prompt-priority" && row.method === "POST");
    request.body = JSON.stringify({ ...JSON.parse(request.body), service_tier: "default" });
  }],
  ["Fast assistant cost left uncorrected", (data) => {
    const reply = data.commandObservations.find((row) => row.command === "lifecycle:fast:prompt-priority" && row.type === "message_end").message;
    reply.usage.cost.total /= 2;
  }],
  ["reset consumed after a conflicting credential appeared", (data) => {
    const credits = data.requests.find((row) => row.id === "lifecycle:reset:consume-conflict");
    data.requests.push({ ...credits, method: "POST", url: `${credits.url}/consume` });
  }],
]) {
  test(`registered lifecycle verifier rejects ${name}`, () => {
    const altered = structuredClone(expected);
    mutate(altered);
    assert.throws(() => verifyLifecycle(altered), assert.AssertionError);
  });
}

const posts = (data, id) => data.requests.filter((row) => row.id === `lifecycle:${id}` && row.method === "POST");
const redeem = (data, id) => { data.requests.push({ ...structuredClone(posts(data, "reset:retry")[0]), id: `lifecycle:${id}` }); };

for (const [name, id, mutate] of [
  ["a reset retry sent under a new redemption identity", "reset:retry", (data) => {
    const retry = posts(data, "reset:retry")[1];
    retry.body = JSON.stringify({ ...JSON.parse(retry.body), redeem_request_id: "$REDEMPTION-changed" });
  }],
  ["a reset redeemed after the user cancelled confirmation", "reset:cancel", (data) => redeem(data, "reset:cancel")],
  ["a reset redeemed from an unready credential candidate", "reset:candidates-unready", (data) => redeem(data, "reset:candidates-unready")],
  ["a reset redeemed for a mismatched account", "reset:account-mismatch", (data) => redeem(data, "reset:account-mismatch")],
  ["a disabled Fast prompt sent at the priority tier", "fast:prompt-disabled", (data) => {
    const request = posts(data, "fast:prompt-disabled")[0];
    request.body = JSON.stringify({ ...JSON.parse(request.body), service_tier: "priority" });
  }],
]) {
  test(`registered lifecycle verifier rejects ${name}`, () => {
    const altered = structuredClone(expected);

    assert.ok(receipt(expected, id));
    mutate(altered);

    assert.throws(() => verifyLifecycle(altered), assert.AssertionError);
  });
}
