/** Finite-state safety exploration of the frozen Hermes protocol; not a writer. */
import { pathToFileURL } from "node:url";

function writer(id) {
  return { id, phase: "idle", incarnation: 0, token: null, base: 0, plan: 0, version: 0, renewed: false, released: false, crashed: false, outcome: null };
}

export function initialPublication(mode = "reuse", fault = "none") {
  return { mode, fault, now: 0, clockMoves: 0, lease: null, doc: 0, exists: mode !== "missing", version: 0, rows: 0, accepted: 0, writers: [writer(0), writer(1)] };
}

function owned(s, w) {
  return s.lease !== null && s.lease.token === w.token;
}

function reclaimable(s) {
  if (s.lease === null) return true;
  const holder = s.writers[s.lease.id];
  const alive = holder.phase !== "dead";
  const known = s.fault !== "unknown-incarnation";
  const same = known && alive && holder.incarnation === s.lease.incarnation;
  const unknown = alive && !known;
  return s.now - s.lease.at >= 2 || (!same && !unknown);
}

function change(s, label, mutate) {
  const next = structuredClone(s);
  mutate(next);
  return { label, state: next };
}

function acquisition(s, w) {
  if (!reclaimable(s)) return [];
  return [change(s, `${w.id}:acquire`, (n) => {
    const a = n.writers[w.id];
    a.token = `${w.id}:${w.incarnation}`;
    a.phase = "read";
    n.lease = { id: w.id, token: a.token, incarnation: w.incarnation, at: n.now };
  })];
}

function publication(s, w) {
  if (w.phase === "read") return [change(s, `${w.id}:read-fingerprint`, (n) => {
    const a = n.writers[w.id];
    a.base = n.doc;
    a.version = n.version;
    a.plan = n.doc | (1 << (w.id + 2 * w.incarnation));
    a.phase = "check";
  })];
  if (w.phase === "check") return [change(s, `${w.id}:check-fingerprint`, (n) => {
    n.writers[w.id].phase = s.doc === w.base ? "publish" : "conflict";
  })];
  if (w.phase !== "publish") return [];
  const collision = s.mode === "missing" && s.exists;
  if (collision) return [change(s, `${w.id}:atomic-link-conflict`, (n) => { n.writers[w.id].phase = "conflict"; })];
  return [change(s, `${w.id}:${s.mode === "missing" ? "link" : "rename"}`, (n) => {
    n.doc = w.plan;
    n.exists = true;
    n.version++;
    n.writers[w.id].phase = "verify";
  })];
}

function finalization(s, w) {
  if (w.phase === "verify") return [change(s, `${w.id}:verify`, (n) => {
    n.writers[w.id].phase = s.doc === w.plan ? "reconcile" : "conflict";
  })];
  if (w.phase === "reconcile") {
    const ok = change(s, `${w.id}:reconcile-and-report`, (n) => {
      n.rows = n.doc;
      n.accepted |= 1 << (w.id + 2 * w.incarnation);
      n.writers[w.id].phase = "done";
      n.writers[w.id].outcome = "saved";
    });
    if (s.fault !== "partial") return [ok];
    return [ok, change(s, `${w.id}:reconcile-fails-report-warning`, (n) => {
      n.accepted |= 1 << (w.id + 2 * w.incarnation);
      n.writers[w.id].phase = "repair";
      n.writers[w.id].outcome = "saved-repair-warning";
    })];
  }
  if (w.phase === "repair") return [change(s, `${w.id}:repair-from-document`, (n) => {
    n.rows = n.doc;
    n.writers[w.id].phase = "done";
  })];
  return [];
}

function leaseMaintenance(s, w) {
  if (w.token === null || w.phase === "dead") return [];
  const out = [];
  if (!w.renewed && s.fault === "renewal") {
    out.push(change(s, `${w.id}:renew-${owned(s, w) ? "ok" : "old-token-rejected"}`, (n) => {
      n.writers[w.id].renewed = true;
      if (owned(s, w)) n.lease.at = n.now;
    }));
    out.push(change(s, `${w.id}:renew-io-failure`, (n) => { n.writers[w.id].renewed = true; }));
  }
  if (["done", "conflict"].includes(w.phase) && !w.released) {
    out.push(change(s, `${w.id}:release-token-checked`, (n) => {
      n.writers[w.id].released = true;
      if (owned(s, w)) n.lease = null;
    }));
    if (s.fault === "release") out.push(change(s, `${w.id}:release-fails-pending`, (n) => { n.writers[w.id].phase = "pending-release"; }));
  }
  if (w.phase === "pending-release") out.push(change(s, `${w.id}:retry-pending-release`, (n) => {
    if (owned(s, w)) n.lease = null;
    n.writers[w.id].phase = "done";
    n.writers[w.id].released = true;
  }));
  return out;
}

function lifecycle(s, w) {
  if (!["crash", "pid-reuse", "unknown-incarnation"].includes(s.fault)) return [];
  if (!w.crashed && w.token !== null && w.phase !== "dead") return [change(s, `${w.id}:crash`, (n) => {
    n.writers[w.id].phase = "dead";
    n.writers[w.id].crashed = true;
  })];
  if (w.phase !== "dead") return [];
  return [change(s, `${w.id}:restart-same-pid-new-incarnation`, (n) => {
    const a = writer(w.id);
    a.incarnation = w.incarnation + 1;
    a.crashed = true;
    n.writers[w.id] = a;
  })];
}

export function publicationTransitions(s) {
  const out = [];
  for (const w of s.writers) {
    out.push(...(w.phase === "idle" ? acquisition(s, w) : publication(s, w)));
    out.push(...finalization(s, w), ...leaseMaintenance(s, w), ...lifecycle(s, w));
  }
  if (s.clockMoves < 2) {
    for (const now of [-2, 0, 2, 4]) {
      if (now !== s.now) out.push(change(s, `clock:${now}`, (n) => { n.now = now; n.clockMoves++; }));
    }
  }
  return out;
}

export function initialRecovery() {
  return { lease: "A", now: 0, checked: false, recovery: "snapshot", snapshot: 0, active: 0, files: [1, 0], backup: null, peer: "open", handle: 0, accepted: 1, restored: false, crashed: false };
}

export function recoveryTransitions(s) {
  const out = [];
  if (s.recovery === "snapshot") out.push(change(s, "A:copy-readable-rows", (n) => { n.snapshot = n.files[n.active]; n.recovery = "check"; }));
  if (s.recovery === "check") out.push(change(s, "A:check-recovery-owner", (n) => { n.checked = s.lease === "A"; n.recovery = n.checked ? "move" : "abort"; }));
  if (s.recovery === "move") out.push(change(s, "A:rename-main-to-backup", (n) => { n.backup = n.active; n.active = null; n.recovery = "move-wal"; }));
  if (s.recovery === "move-wal") out.push(change(s, "A:rename-wal-to-backup", (n) => { n.recovery = "move-shm"; }));
  if (s.recovery === "move-shm") out.push(change(s, "A:rename-shm-to-backup", (n) => { n.recovery = "publish"; }));
  if (s.recovery === "publish") {
    out.push(change(s, "A:rename-rebuilt-main", (n) => { n.files[1] = n.snapshot; n.active = 1; n.recovery = "done"; }));
    out.push(change(s, "A:publish-fails-restore-backup", (n) => { n.active = n.backup; n.recovery = "failed-restored"; n.restored = true; }));
    out.push(change(s, "A:publish-and-restore-fail-backup-retained", (n) => { n.active = null; n.recovery = "failed-backup-retained"; }));
  }
  if (s.peer === "open") out.push(change(s, "B:commit-on-open-handle", (n) => { n.files[n.handle] |= 2; n.accepted |= 2; n.peer = "accepted"; }));
  if (!s.crashed && ["move-wal", "move-shm", "publish"].includes(s.recovery)) out.push(change(s, "A:crash-between-file-renames", (n) => { n.crashed = true; n.recovery = "dead"; }));
  if (s.recovery === "dead") out.push(change(s, "A:restart-retains-readable-backup", (n) => { n.recovery = "failed-backup-retained"; }));
  if (s.peer === "accepted" && s.active !== null) out.push(change(s, "B:crash-and-reopen-current-path", (n) => { n.handle = n.active; n.peer = "reopened"; }));
  if (s.now === 0) out.push(change(s, "clock:stale-takeover", (n) => { n.now = 2; n.lease = "B"; }));
  return out;
}

export function violations(before, edge) {
  const after = edge.state;
  const failures = [];
  if ("doc" in after) {
    if (after.version > before.version && before.writers[Number(edge.label[0])].version !== before.version) failures.push("stale-plan");
    if ((after.accepted & after.doc) !== after.accepted) failures.push("lost-accepted-update");
    for (const w of after.writers) {
      const prior = before.writers[w.id];
      if (prior.phase === "reconcile" && ["done", "repair"].includes(w.phase) && after.rows !== after.doc && w.outcome !== "saved-repair-warning") failures.push("unreported-partial-success");
      if (prior.phase === "repair" && w.phase === "done" && after.rows !== after.doc) failures.push("repair-incomplete");
    }
    if (edge.label.includes("old-token") && JSON.stringify(before.lease) !== JSON.stringify(after.lease)) failures.push("old-token-modified-successor");
    if (edge.label.includes("release") && before.lease !== null) {
      const id = Number(edge.label[0]);
      if (!owned(before, before.writers[id]) && JSON.stringify(before.lease) !== JSON.stringify(after.lease)) failures.push("old-token-modified-successor");
    }
  } else {
    const readable = after.files.reduce((a, b) => a | b, 0);
    if ((readable & after.accepted) !== after.accepted) failures.push("readable-row-destroyed");
    if (after.recovery === "done") {
      const current = after.active === null ? 0 : after.files[after.active];
      if ((current & after.accepted) !== after.accepted) failures.push("accepted-row-detached-from-current-db");
    }
    if (["A:rename-main-to-backup", "A:rename-wal-to-backup", "A:rename-shm-to-backup", "A:rename-rebuilt-main", "A:publish-fails-restore-backup"].includes(edge.label) && before.lease !== "A") failures.push("stale-recovery-owner-renamed");
  }
  return failures;
}

export function explore(initial, transitions) {
  const nodes = [{ state: initial, parent: -1, label: "initial" }];
  const seen = new Set([JSON.stringify(initial)]);
  const counterexamples = {};
  const events = new Set();
  let edges = 0;
  let terminals = 0;
  function trace(index) {
    const steps = [];
    for (let i = index; i >= 0; i = nodes[i].parent) steps.push({ event: nodes[i].label, state: nodes[i].state });
    return steps.reverse();
  }
  for (let i = 0; i < nodes.length; i++) {
    const choices = transitions(nodes[i].state);
    if (choices.length === 0) terminals++;
    for (const edge of choices) {
      edges++;
      events.add(edge.label);
      for (const failure of violations(nodes[i].state, edge)) {
        if (!(failure in counterexamples)) counterexamples[failure] = [...trace(i), { event: edge.label, state: edge.state }];
      }
      const key = JSON.stringify(edge.state);
      if (seen.has(key)) continue;
      seen.add(key);
      nodes.push({ state: edge.state, parent: i, label: edge.label });
    }
  }
  return { states: nodes.length, edges, terminals, exhausted: true, events: [...events].sort(), counterexamples };
}

export function runModel() {
  const cases = {};
  for (const mode of ["reuse", "missing"]) {
    cases[mode] = explore(initialPublication(mode), publicationTransitions);
  }
  for (const fault of ["renewal", "release", "partial", "crash", "pid-reuse", "unknown-incarnation"]) {
    cases[fault] = explore(initialPublication("reuse", fault), publicationTransitions);
  }
  cases.recovery = explore(initialRecovery(), recoveryTransitions);
  const blocked = Object.values(cases).some((c) => Object.keys(c.counterexamples).length > 0);
  return { status: blocked ? "BLOCKED" : "NO_COUNTEREXAMPLE_IN_BOUND", bound: { writers: 2, operationsPerIncarnation: 1, restartPerWriter: 1, clockMoves: 2, clocks: [-2, 0, 2, 4], staleAt: 2, maintenanceAttempts: 1, faultFamiliesExploredSeparately: true }, cases };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const result = runModel();
  console.log(JSON.stringify(result, null, 2));
  if (process.argv.includes("--qualify") && result.status === "BLOCKED") process.exitCode = 2;
}
