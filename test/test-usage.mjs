import assert from "node:assert/strict";
import { setTimeout as delay } from "node:timers/promises";
import { test } from "node:test";
import usageStatus from "../status/usage.ts";

function fixture(t, provider = "openai-codex") {
  const handlers = new Map(), listeners = new Map(), publications = [];
  const pi = {
    events: {
      on(name, handler) { listeners.set(name, handler); },
      emit(name, data) {
        if (name === "pig-status:usage") publications.push(data);
        listeners.get(name)?.(data);
      },
    },
    on(name, handler) { handlers.set(name, handler); },
  };
  usageStatus(pi);
  const entries = [{ type: "message", message: { role: "assistant", usage: {
    input: 100, output: 25, cacheRead: 900, cacheWrite: 300, cost: { total: 0.216 },
  } } }];
  let sessionId = "session-a";
  const ctx = {
    model: { provider, id: "fixture" },
    sessionManager: { getSessionId: () => sessionId, getEntries: () => entries, getBranch: () => assert.fail("unexpected fallback") },
  };
  t.after(() => handlers.get("session_shutdown")({}, ctx));
  return {
    pi, ctx, entries, publications,
    event: (name, data = {}) => handlers.get(name)(data, ctx),
    send: (name, data) => pi.events.emit(name, data),
    latest: () => publications.at(-1),
    session: (id) => { sessionId = id; },
  };
}

function response(ctx, buckets, expiresAt = Date.now() + 60_000) {
  return { status: "ready", expiresAt, report: { providerId: ctx.model.provider, buckets } };
}

test("uses the versioned backend API and publishes cache accounting with normalized quota", async (t) => {
  const f = fixture(t);
  f.pi.events.on("usage:query:v1", (request) => {
    assert.equal(request.session, f.ctx.sessionManager);
    assert.equal(request.sessionId, f.ctx.sessionManager.getSessionId());
    assert.equal(request.refresh, false);
    assert.equal(request.respond(response(f.ctx, [
      { label: "5h", unit: "percent", used: 39 },
      { label: "Week", unit: "count", remaining: 87, limit: 100 },
      { label: "balance only", unit: "usd", remaining: 7 },
    ])), true);
  });
  f.event("session_start");
  await delay(0);
  assert.deepEqual(f.latest(), {
    input: 100, output: 25, cacheRead: 900, cacheWrite: 300, cost: 0.216,
    subscription: false,
    windows: [{ label: "5h", usedPercent: 39 }, { label: "Week", usedPercent: 13 }],
  });
  f.entries.splice(0);
  f.session("session-b");
  f.event("session_start");
  assert.equal(f.latest().input, 0);
  assert.deepEqual(f.latest().windows, []);
});

test("ignores late backend responses and foreign change events after a model switch", async (t) => {
  const f = fixture(t);
  let resolve;
  f.pi.events.on("usage:query:v1", (request) => request.respond(new Promise((done) => { resolve = done; })));
  f.event("session_start");
  f.ctx.model = { provider: "claude-bridge", id: "fixture" };
  f.event("model_select");
  resolve(response({ model: { provider: "openai-codex" } }, [{ label: "old", unit: "percent", used: 99 }]));
  await delay(0);
  assert.deepEqual(f.latest().windows, []);
  f.send("usage:changed:v1", { session: {}, sessionId: "session-a", providerId: "openai-codex", reason: "updated" });
  assert.deepEqual(f.latest().windows, []);
});

test("quota becomes unknown at its reset without another model turn", async (t) => {
  const f = fixture(t);
  f.pi.events.on("usage:query:v1", (request) => request.respond(response(f.ctx, [
    { label: "5h", unit: "percent", used: 39, resetsAt: Date.now() + 30 },
  ])));
  f.event("session_start");
  await delay(0);
  assert.equal(f.latest().windows.length, 1);
  await delay(70);
  assert.deepEqual(f.latest().windows, []);
});

test("backend invalidation clears quota and shutdown prevents pending updates", async (t) => {
  const f = fixture(t);
  let current = response(f.ctx, [{ label: "5h", unit: "percent", used: 40 }]);
  f.pi.events.on("usage:query:v1", (request) => request.respond(current));
  f.event("session_start");
  await delay(0);
  assert.equal(f.latest().windows[0].usedPercent, 40);
  current = { status: "unavailable", reason: "cache-miss" };
  f.send("usage:changed:v1", { session: f.ctx.sessionManager, sessionId: "session-a", providerId: f.ctx.model.provider });
  await delay(0);
  assert.deepEqual(f.latest().windows, []);
  const before = f.publications.length;
  f.event("session_shutdown");
  f.send("usage:changed:v1", { session: f.ctx.sessionManager, sessionId: "session-a", providerId: f.ctx.model.provider });
  await delay(0);
  assert.equal(f.publications.length, before);
});
