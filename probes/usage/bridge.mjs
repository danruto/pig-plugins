/** Probe-only Go/Node usage-protocol boundary captures through the real PiG event bus; inputs are synthetic and no provider request is admitted. */
import assert from "node:assert/strict";
import { now } from "./cases.mjs";

const probeKey = "usageBridgeProbe";

export const bridgeFixtures = [
  { id: "protocol:go-bridge:node-query", direction: "node-to-go", event: "usage:query:v1", request: { providerId: "deepseek", refresh: false } },
  { id: "protocol:go-bridge:node-query-without-session", direction: "node-to-go", event: "usage:query:v1", request: { providerId: "deepseek", refresh: false }, omitSession: true },
  { id: "protocol:go-bridge:node-capabilities", direction: "node-to-go", event: "usage:capabilities:v1", request: { providerId: "deepseek" } },
  { id: "protocol:go-bridge:node-changed", direction: "node-to-go", event: "usage:changed:v1", request: { providerId: "fixture-bridge", changedAt: now, reason: "invalidated" } },
  { id: "protocol:go-bridge:go-query", direction: "go-to-node", event: "usage:query:v1", request: { providerId: "deepseek", refresh: true } },
  { id: "protocol:go-bridge:go-capabilities", direction: "go-to-node", event: "usage:capabilities:v1", request: { providerId: "deepseek" } },
];

function within(subject, timeoutMs) {
  let timer;
  return Promise.race([subject, new Promise((resolve) => { timer = setTimeout(() => resolve({ fixtureTimeout: true }), timeoutMs); })])
    .finally(() => clearTimeout(timer));
}

export function registerBridge(pi, scope) {
  const pending = new Map();
  let sequence = 0;
  let emitting;
  const arrive = (kind, id, value) => {
    const entry = pending.get(`${kind}:${id}`);
    if (entry) entry({ sequence: ++sequence, duringEmit: emitting === id, value });
  };
  pi.events.on("usage-bridge:received", (value) => arrive("received", value?.[probeKey], value));
  pi.events.on("usage-bridge:emitted", (value) => arrive("emitted", value?.[probeKey], value));
  for (const state of ["armed", "disarmed"]) pi.events.on(`usage-bridge:${state}`, () => arrive(state, "go", undefined));
  for (const channel of ["usage:query:v1", "usage:capabilities:v1"]) {
    pi.events.on(channel, (value) => {
      if (typeof value?.[probeKey] !== "string") return;
      arrive("node", value[probeKey], {
        channel, respondType: typeof value.respond, signalIsAbortSignal: value.signal instanceof AbortSignal,
        sessionIsComposition: value.session === scope().session, sessionIdMatchesComposition: value.sessionId === scope().sessionId,
        fields: Object.keys(value).sort(),
      });
    });
  }
  const expect = (kind, id) => new Promise((resolve) => pending.set(`${kind}:${id}`, resolve));
  async function toggle(command, state) {
    const settled = within(expect(state, "go"), 5000);
    pi.events.emit(`usage-bridge:${command}`, null);
    const outcome = await settled;
    pending.delete(`${state}:go`);
    if (outcome.fixtureTimeout) throw new Error(`Go bridge probe did not acknowledge ${command}`);
  }
  return async function captureBridge(observe) {
    const rows = [];
    await toggle("arm", "armed");
    try {
      for (const fixture of bridgeFixtures) {
        rows.push(await observe(fixture.id, async () => {
          const base = { id: fixture.id, direction: fixture.direction, event: fixture.event, request: fixture.request };
          try {
            if (fixture.direction === "node-to-go") {
              const goView = expect("received", fixture.id);
              const controller = new AbortController();
              const offers = [];
              const { session, sessionId } = scope();
              const request = { [probeKey]: fixture.id, ...(fixture.omitSession ? {} : { session }), sessionId, ...fixture.request };
              if (fixture.event !== "usage:changed:v1") {
                request.signal = controller.signal;
                request.respond = (value) => { offers.push(value instanceof Promise ? "promise" : typeof value); return false; };
              }
              let emitError = null;
              emitting = fixture.id;
              try { pi.events.emit(fixture.event, request); }
              catch (error) { if (!(error instanceof Error)) throw error; emitError = { name: error.name, message: error.message }; }
              finally { emitting = undefined; }
              controller.abort();
              const go = await within(goView, 5000);
              return { ...base, emitError, nodeOffers: offers, goReceivedDuringEmit: go.duringEmit ?? false, go: go.value ?? go };
            }
            const nodeView = expect("node", fixture.id);
            const ack = expect("emitted", fixture.id);
            pi.events.emit("usage-bridge:request", { [probeKey]: fixture.id, channel: fixture.event, ...fixture.request });
            const [goEmit, node] = await Promise.all([within(ack, 5000), within(nodeView, 5000)]);
            return { ...base, goEmit: goEmit.value ?? goEmit, node: node.value ?? node,
              nodeListenerBeforeGoEmitReturned: node.sequence !== undefined && goEmit.sequence !== undefined && node.sequence < goEmit.sequence };
          } finally {
            for (const kind of ["received", "emitted", "node"]) pending.delete(`${kind}:${fixture.id}`);
          }
        }));
      }
    } finally {
      await toggle("disarm", "disarmed");
    }
    return rows;
  };
}

export function verifyBridge(observed) {
  const row = (id) => {
    const found = observed.protocol.filter((item) => item.id === `protocol:go-bridge:${id}`);
    assert.equal(found.length, 1, `Missing Go bridge capture: ${id}`);
    return found[0];
  };
  for (const id of ["node-query", "node-capabilities", "node-changed"]) {
    assert.deepEqual(row(id).go, { fixtureTimeout: true }, `Go received a session-bearing usage event: ${id}`);
    assert.equal(row(id).emitError, null);
  }
  for (const id of ["node-query", "node-capabilities"]) assert.deepEqual(row(id).nodeOffers, ["promise"]);
  assert.equal(observed.bridgeHostErrors.filter((line) => /^Event handler error \(usage:(query|capabilities|changed):v1\): Converting circular structure to JSON$/u.test(line)).length, 3);
  const withoutSession = row("node-query-without-session");
  assert.equal(withoutSession.goReceivedDuringEmit, true);
  assert.deepEqual(withoutSession.nodeOffers, []);
  assert.equal(withoutSession.go.view.fields.respond, undefined, "Go received a callback");
  assert.deepEqual(withoutSession.go.view.signalKeys, []);
  assert.equal(withoutSession.go.view.sessionIdMatchesHost, true);
  for (const id of ["go-query", "go-capabilities"]) {
    const { node, goEmit, nodeListenerBeforeGoEmitReturned } = row(id);
    assert.equal(goEmit.error, null);
    assert.equal(nodeListenerBeforeGoEmitReturned, true);
    assert.deepEqual([node.respondType, node.signalIsAbortSignal, node.sessionIsComposition, node.sessionIdMatchesComposition], ["undefined", false, false, true]);
  }
}
