/** Independent monitor and traversal checks for the finite safety model. */
import assert from "node:assert/strict";
import { test } from "node:test";
import { explore, initialPublication, initialRecovery, publicationTransitions, violations } from "./model.mjs";

test("accepted updates cannot disappear from a later document, and successor tokens cannot be touched", () => {
  const before = initialPublication();
  before.doc = 1;
  before.accepted = 1;
  before.version = 1;
  before.lease = { id: 1, token: "successor", incarnation: 0, at: 2 };
  const after = structuredClone(before);
  assert.deepEqual(violations(before, { state: after, label: "0:release-token-checked" }), []);
  after.doc = 2;
  after.version = 2;
  assert.deepEqual(violations(before, { state: after, label: "0:rename" }), ["stale-plan", "lost-accepted-update"]);
  after.version = 1;
  after.doc = 1;
  after.lease = null;
  assert.deepEqual(violations(before, { state: after, label: "0:release-token-checked" }), ["old-token-modified-successor"]);
  assert.deepEqual(violations(before, { state: after, label: "0:renew-old-token-rejected" }), ["old-token-modified-successor"]);
  before.fault = "renewal";
  before.writers[0].token = "old";
  before.writers[0].phase = "done";
  const maintenance = publicationTransitions(before).filter((e) => /^(0:renew|0:release)/.test(e.label));
  assert(maintenance.length > 0);
  for (const edge of maintenance) assert.deepEqual(edge.state.lease, before.lease);
});

["read", "pending-release"].forEach((phase) => {
  test(`AC-repair-1: restart discards volatile authority from ${phase}`, () => {
    const acquired = publicationTransitions(initialPublication("reuse", "crash")).find((e) => e.label === "0:acquire");
    assert(acquired);
    const before = acquired.state;
    before.writers[0].phase = phase;
    before.doc = 1;
    before.rows = 1;
    before.accepted = 1;
    before.version = 1;

    const crashed = publicationTransitions(before).find((e) => e.label === "0:crash");
    assert(crashed);
    const restarted = publicationTransitions(crashed.state).find((e) => e.label === "0:restart-same-pid-new-incarnation");
    assert(restarted);

    assert.equal(restarted.state.writers[0].incarnation, before.writers[0].incarnation + 1);
    assert.deepEqual(
      { lease: restarted.state.lease, doc: restarted.state.doc, rows: restarted.state.rows, accepted: restarted.state.accepted },
      { lease: before.lease, doc: before.doc, rows: before.rows, accepted: before.accepted },
    );
    assert.deepEqual(
      {
        token: restarted.state.writers[0].token,
        maintenance: publicationTransitions(restarted.state).filter((e) => /^0:(renew|release|retry-pending-release)/.test(e.label)).map((e) => e.label),
      },
      { token: null, maintenance: [] },
    );
  });
});

test("AC-repair-2: recovery monitors final publication independently of readable row preservation", () => {
  const before = initialRecovery();
  before.accepted = 1;
  before.files = [1, 0];
  const after = structuredClone(before);
  after.active = 1;
  after.backup = 0;
  after.recovery = "done";
  assert.deepEqual(violations(before, { state: after, label: "A:rename-rebuilt-main" }), ["accepted-row-detached-from-current-db"]);
  after.files[0] = 0;
  assert.deepEqual(violations(before, { state: after, label: "A:rename-rebuilt-main" }), ["readable-row-destroyed", "accepted-row-detached-from-current-db"]);
  after.files[1] = 1;
  assert.deepEqual(violations(before, { state: after, label: "A:rename-rebuilt-main" }), []);

  const stale = initialRecovery();
  stale.checked = true;
  stale.lease = "B";
  stale.snapshot = 1;
  stale.files = [1, 1];
  const published = structuredClone(stale);
  published.active = 1;
  published.backup = 0;
  published.recovery = "done";
  const owned = structuredClone(stale);
  owned.lease = "A";
  const ownedAfter = structuredClone(published);
  ownedAfter.lease = "A";

  assert.deepEqual(violations(owned, { state: ownedAfter, label: "A:rename-rebuilt-main" }), []);
  assert.deepEqual(violations(stale, { state: published, label: "A:rename-rebuilt-main" }), ["stale-recovery-owner-renamed"]);
});

test("a durable save with stale search rows must warn, and repair must restore the mirror", () => {
  const before = initialPublication("reuse", "partial");
  before.doc = 1;
  before.writers[0].phase = "reconcile";
  const warning = publicationTransitions(before).find((e) => e.state.writers[0].phase === "repair");
  assert(warning);
  assert.equal(warning.state.writers[0].outcome, "saved-repair-warning");
  assert.deepEqual(violations(before, warning), []);
  const wrong = structuredClone(warning);
  wrong.state.writers[0].outcome = "saved";
  assert.deepEqual(violations(before, wrong), ["unreported-partial-success"]);
  const repaired = publicationTransitions(warning.state).find((e) => e.label === "0:repair-from-document");
  assert(repaired);
  assert.equal(repaired.state.rows, 1);
  assert.deepEqual(violations(warning.state, repaired), []);
  repaired.state.rows = 0;
  assert.deepEqual(violations(warning.state, repaired), ["repair-incomplete"]);
});

test("exploration closes a cyclic diamond and retains the shortest violating prefix", () => {
  const initial = initialPublication();
  initial.accepted = 1;
  initial.doc = 1;
  const transitions = (s) => {
    if (s.now === 2) return [];
    const next = structuredClone(s);
    next.now++;
    if (next.now === 2) next.doc = 0;
    const short = { state: next, label: "advance" };
    if (s.now === 1) return [short, { state: initial, label: "cycle" }];
    return [short, { state: { ...next, now: 2, doc: 0 }, label: "short-violation" }];
  };
  const result = explore(initial, transitions);
  assert.equal(result.states, 3);
  assert.equal(result.edges, 4);
  assert.equal(result.terminals, 1);
  assert.equal(result.exhausted, true);
  assert.deepEqual(result.counterexamples["lost-accepted-update"].map((s) => s.event), ["initial", "short-violation"]);
});
