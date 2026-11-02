# Frozen Hermes publication model

## Release decision

**BLOCKED. Do not qualify native publication or select a native backend.** Keep
[ADR 0002](../adr/0002-frozen-shared-storage.md) and the
[publication-safety gate](../designs/shared-storage.md#publication-safety-gate).
A passing regression command means that the expected blocking findings remain
reproducible, not that the protocol is safe. A coordinated TypeScript/Go fencing
protocol, revised baseline and rollout require a separate owner decision.

The executable seam is `npm run test:hermes-publication`. It runs the model's
independent monitor/traversal checks and the independent CLI/persisted-store
suite, which explores the finite model and compares the
[generated retained traces](hermes-publication-model.json). `npm test` invokes
that same seam and builds/vets the synthetic Go peer. With
`HERMES_PUBLICATION_SOURCE` pointing to reconstructed source, the CLI suite also
executes the mixed-process probes once per maintained invocation; its separate
qualification-refusal child is model-only. Without source, six process-dependent
cases explicitly skip while model and CLI checks still run. No extension,
production Hermes store or deployment selection is added.

GOMAXPROCS=2 GOFLAGS=-p=2 npm test → focused feature and safety checks pass on disposable synthetic roots.

That line is a feature/regression check, **not publication qualification**.
`node probes/hermes-publication/run.mjs --qualify` returns exit status 2 while any
modeled invariant fails. The default regression command returns zero only after
its requested checks execute successfully; it reports `BLOCKED` separately.
An explicit `--source` requires a path operand, not another option. Without that
option the runner uses `HERMES_PUBLICATION_SOURCE` when set.

## Bounded model and scope

`probes/hermes-publication/model.mjs` uses breadth-first traversal and exact
serialized-state deduplication. Every enabled transition of every reachable state
is explored to a fixed point, including successors of bad states. There is no
random scheduler, depth cutoff, state-count cutoff or pruning at the first
counterexample. Each invariant retains its shortest failing prefix. The retained
JSON records state/edge/terminal counts and enabled event coverage per family;
the scratch report retains every family's counterexample. The regenerated
artifact contains **509,497 states / 1,308,792 edges** across nine families.
Correcting restart authority expands the crash family from 24,182 states /
77,474 edges to **172,986 states / 438,894 edges**: a new incarnation starts
idle and can acquire a fresh lease and attempt its own operation, rather than
finishing/releasing its predecessor's operation. Other family counts and the
four retained shortest counterexample prefixes are unchanged. The crash and
known PID-reuse families now share lifecycle semantics; their labels retain
the separate fault-family coverage, not distinct physical restart mechanisms.

The publication families have two writers, one canonical document/scope, one
shared lease row, distinct tokens per incarnation, document version plus a
collision-free content fingerprint abstraction, search rows, accepted update
identities and explicit result states. Each writer can attempt one add per
incarnation. The clock has four values (-2, 0, 2, 4), at most two changes and stale
threshold 2. Negative changes represent wall-clock rollback; positive changes
cross the lease threshold even for a live holder. Pausing is arbitrary scheduler
interleaving, including immediately after a successful fingerprint check.

The reuse family models the real recent-recovery-snapshot plain-rename branch.
The missing family isolates competing create-only operations and their atomic
no-replace hard-link primitive; it deliberately does not transition into a
later existing-document operation. The displaced-document fallback's
move/check/link/recheck/rollback algorithm is described below but is **not**
abstracted as an atomic compare-and-publish or claimed verified by this model.
The unsafe reuse branch alone suffices to block the frozen writer.

Additional reuse families enable, separately:

- one renewal attempt per writer, with success, token mismatch or I/O failure;
- a failed release after the baseline's bounded attempts and a token-checked
  pending retry, possibly after takeover;
- warned partial reconciliation and a forced repair from the document;
- one crash per writer at any owned phase, with durable state retained and
  volatile work, lease token and pending maintenance discarded on restart;
  the new incarnation starts idle and must acquire a fresh token before work;
- PID reuse on restart with changed incarnation, and unknown incarnation. A
  live but unknown incarnation prevents liveness-based stealing, not time-based
  stealing. A known mismatch permits reclaiming a nonstale old incarnation.

These are exhaustive **within each listed family**, not the Cartesian product of
all fault types, arbitrary retry counts or all upstream paths. Tokens and update
bits distinguish the restarted writer's new operation from its first one.
Lease transaction actions are indivisible because SQLite actually provides the
transaction. Fingerprint checks and publication are different transitions.
Memory's mutation lock does not perform an ownership check before its rename;
the model does not invent one.

The recovery family has a recovery writer and an independently writing peer,
readable seed rows, a copied replacement image, an open-handle file identity,
accepted rows and the current-path identity. Ownership check, main-file rename,
WAL rename, SHM rename and replacement publication are separate transitions.
It includes stale takeover between any of those operations, a peer commit
before/after the copy, crash/restart between renames, failed swap, successful
restoration and failed restoration retaining the backup. WAL/SHM payloads are
abstracted as belonging to the old file generation; their individual moves are
modeled, not SQLite page-level crash replay. Restart retains a readable backup;
it is not a claim that the baseline automatically restores every interrupted
file-set move.

Monitors assert no accepted addition disappears, a stale planned version cannot
replace a newer published version, old tokens cannot renew/release a successor,
readable recovery rows survive, accepted rows remain reachable through the
successfully rebuilt current database, and partial saves warn and can repair.
A separate stale-recovery-owner monitor covers main/WAL/SHM backup renames,
rebuilt-main publication and successful backup restoration, even without a
new peer row. It does not conflate ownership with physical row preservation or
accepted-row reachability.
The independent unit checks deliberately feed bad transitions to the monitors
and check cyclic graph closure, preventing an always-green monitor from being
the sole oracle. Passing token/repair invariants do not excuse publication loss.

## Actual boundaries and lock order

Source locations refer to the pinned 0.9.11 commit
`71cf5ab7d909318a383bf52bb3ad3cf83a9efc03` plus the async-integrity patch. They
are not native implementation locations.

| Path | Lock order and actual durable boundaries |
| --- | --- |
| Add, replace, remove | `memory-store.ts:303,410,495,556,883` enter the canonical Markdown lease (`markdown-mutation-lock.ts:15`). Lease acquisition linearizes at coordinator `BEGIN IMMEDIATE`/`COMMIT` (`atomic-lock-coordinator.ts:147-187`); the transaction is then closed while logical lease ownership continues. After disk read/scan/planning, `saveToDisk:940-1054` creates a same-directory temporary directory/file and writes the candidate. For a missing document, `link(tmp,target):958` is the no-replace publication point. For a recent recovery snapshot, `rename(tmp,target):967` is the replacement point, **not** the preceding fingerprint read. Otherwise `rename(target,recovery):978` first displaces the old inode, followed by displaced fingerprint checks and `link(tmp,target):992`; there is no single transaction covering them. Post-publication rereads precede the returned result. |
| Memory rollback/artifact maintenance | `memory-store.ts:1055-1276` restores with a no-replace link, or renames a published file to a conflict path, checks inode identity and attempts restoration. Temporary links/directories are removed. Recovery, conflict-local and retired snapshots have separate writes, links, renames and unlink/prune operations; retiring a snapshot writes/syncs a new snapshot then renames it before unlinking recovery. These artifacts are not a distributed compare-and-swap. Filesystem publication of Markdown and SQLite reconciliation are not one atomic operation. |
| Search reconciliation and external edits | `finalizeTargetMutation:847-880` reloads disk, then invokes the mutation observer while the Markdown lease is held. `sqlite-memory-store.ts:501-633` upserts scoped rows, deletes orphans and updates/deletes the scope fingerprint in a SQLite transaction when the driver supplies one; that transaction's commit is the search publication point. A pretransaction fingerprint read or saved Markdown is not that point. Forced sync can repair a scope; external editors do not acquire a Hermes lease. A corruption retry can acquire the recovery lease while the mutation lease remains logically held. |
| Consolidation | `auto-consolidate.ts:92-140` acquires the operation-specific consolidation lease first, renews every 10 seconds and releases in cleanup. Direct/child mutations subsequently use the Markdown lease and may reconcile/recover as above: consolidation → mutation → recovery. The model request/child lifetime holds a logical lease, **not an open SQLite transaction**. The heartbeat ignores the boolean renewal result and catches renewal errors; lease loss is not revocation of a pending filesystem operation. There is no single consolidation transaction or publication point spanning multiple memory mutations. |
| Database recovery | `db.ts:255-268,494-552` closes this manager's handle and acquires the canonical recovery lease, checks health/circuit, then copies readable rows and rebuilds FTS into a DELETE-journal temporary database (`:674-711`). Target row inserts and index/schema changes are durable database writes; successful target checks/close do not freeze the source. `moveDatabaseFilesToBackup:921-934` checks ownership once, then separately renames main, `-wal`, `-shm`. `swapRebuiltDatabase:910-919` renames the rebuilt main into the current path. That rename is the current-path publication point, not the owner check or copy. Existing peer handles are neither revoked nor joined. Verification reopens/checks the new database before reporting rebuilt. |
| Recovery failure/rollback | `db.ts:578-662,910-966` writes circuit state to a temporary file then renames it, removes/reset circuit state and prunes backup groups/rebuild artifacts. Swap failure removes destinations and restores moved files in reverse order by separate renames; restoration is best-effort and may leave readable backups instead of a usable current path. These operations are not atomic with lease checks or peer commits. Original/rebuild SQLite handles may also create/delete rollback journals and WAL/SHM. The retained async integrity worker is joined before this manager closes; it does not join independent peer handles. |

Coordination databases and keys remain exactly those in the governing contract:
mutation uses `dirname(dirname(I))/.pi-hermes-locks.sqlite` and `mutation:I`;
recovery uses `dirname(D)/.pi-hermes-locks.sqlite` and `recovery:D`;
consolidation uses its configured/shared root and hashed identity key. They may
be different files and never constitute one shared transaction. The source's
recovery path does not acquire a Markdown or consolidation lease in reverse
order. The Go probe uses these concrete mutation and recovery paths/keys.

## Deterministic mixed-process findings

`run.mjs --source <source>` verifies all pinned packaged source bytes via the
existing source-only verifier, builds `probes/hermes-sqlite/publication-peer`, and
starts separate Go and TypeScript processes. The TypeScript process calls real
`MemoryStore.add`, reconciliation and `DatabaseManager.recoverFromCorruption`.
Only filesystem scheduling is intercepted at the named primitive; source files
are not modified. Each barrier has a bounded wait. Processes are closed/joined
in cleanup; their PID/exited receipts and stderr are retained under the scratch
case root. No daemon is started.

The Go peer is a **synthetic protocol participant**, not a production native
Hermes implementation. It uses the existing modernc SQLite dependency, the
baseline schema and token-conditional lease transaction. To avoid a five-minute
sleep it advances its acquisition clock to at least `acquired_at + 300000` while
retaining the live TypeScript PID. This checks the real stale-row condition,
not five minutes of wall-clock scheduling. It writes synthetic document/rows
with explicit scope/category/hit metadata; it is not a complete native
reconciliation implementation.

The runner compares documents, full selected row fields, target/project scope,
search fingerprint/count metadata, outcomes and successor token ownership. Its
retained cases cover:

1. **Serial success:** TypeScript completes, Go appends/commits; both accepted
   additions remain readable.
2. **Stale rename / lost accepted update:** create a real recovery snapshot,
   pause TypeScript after its fingerprint and snapshot-reuse decision but before
   `rename(write.tmp,MEMORY.md)`, let Go take over and commit an addition, then
   resume TypeScript. TypeScript returns success while its document and
   reconciled rows omit the Go accepted addition and associated metadata. Its
   token-checked release leaves the successor lease intact. This concretizes
   both retained publication counterexamples.
3. **Partial success and repair:** an injected mutation-observer failure returns
   the saved-document warning with stale search rows; real forced reconciliation
   subsequently repairs the scope. This is an observer failure injection, not
   evidence about every FTS error classification.
4. **Recovery stale owner / open handle:** introduce a synthetic CHECK-constraint
   violation in a disposable extra table so `quick_check` fails while baseline
   memory rows remain readable. Pause after the recovery ownership check and
   completed copy, before the first destructive rename. The open Go peer commits
   an additional row and takes over the recovery lease. Old-token TypeScript
   renewal returns false; old-token release cannot remove the successor.
   Nevertheless the resumed TypeScript recovery renames the file set and reports
   `rebuilt`; the current database lacks the Go accepted row, while the open
   peer handle still reads its row with category/hit metadata intact. The
   rebuilt metadata deliberately drops Markdown scope fingerprints. This
   concretizes both retained recovery counterexamples. The backup remains
   available; no claim is made that the row is physically destroyed.

These findings stop qualification. Do not interpret the two successful scenarios
as authorization to narrow either failed invariant.

## Assumptions and remaining evidence

- Executed platform: Linux arm64, Node 24.21.0 and Go 1.27.3, with local SQLite
  and same-machine PID visibility. Linux PID reuse/unknown-incarnation and
  renewal/release I/O failures are exhaustively modeled within their bounds;
  real kernel PID reuse, process-kill restart and lock-I/O failure injection are
  **not** process-qualified by these four scenarios.
- Filesystem assumptions: same-device local rename and hard-link behavior,
  stable inode identity, no adversarial fingerprint collision. An open handle
  can continue to identify the old file after a POSIX rename. This is not an
  NFS/SMB, Windows, macOS or power-loss guarantee.
- A crash discards volatile plans and connections, retaining completed durable
  operations. The model assumes atomic SQLite transactions and does not model
  torn pages, disk-full sector writes, fsync ordering or power loss. Markdown's
  normal write/rename sequence is not a demonstrated power-loss durability
  barrier.
- Content, parser, cancellation API, capacity and multi-document details are
  outside this finite safety abstraction, not silently certified. The displaced
  snapshot rollback path, all rollback failures, real corruption during live
  indexing and portable peer-handle behavior still need focused process checks
  if qualification is resumed after an approved protocol decision.
- The current source and experimental driver observations are not a recapture
  of the historical full executable closure. No live roots, credentials,
  sessions, billing endpoints or model requests are used.

## Reproduction tutorial

Use only the objective scratch root, including all source extraction,
installation caches, databases, dumps and temporary files:

```sh
export TMPDIR="$PWD/.pb/probes/10"
mkdir -p "$TMPDIR"
npm run test:hermes-publication

# Extract the public pinned archive into scratch and apply the retained patch.
# The repository already retains that exact public archive.
tar -xzf docs/compatibility/hermes-upstream-0.9.11.tar.gz -C "$TMPDIR"
source="$TMPDIR/pi-hermes-memory-71cf5ab7d909318a383bf52bb3ad3cf83a9efc03"
patch --batch --fuzz=0 -d "$source" -p1 < patches/hermes-0.9.11-async-integrity.patch
# Supply public native dependencies outside the frozen packaged source tree.
npm install --prefix "$TMPDIR/runtime" --cache "$TMPDIR/npm-cache" \
  --no-audit --no-fund better-sqlite3@13.0.3 strip-ansi@7.2.0
ln -s ../runtime/node_modules "$source/node_modules"
GOMAXPROCS=2 GOFLAGS=-p=2 node probes/hermes-publication/run.mjs \
  --check-evidence --source "$source"
```

Use a fresh extraction directory for a second reconstruction, rather than
reapplying a patch to an already patched tree. Review
`.pb/probes/10/publication-report.json` and each case's `observation.json` and
PID receipts. The generated model artifact can be regenerated with
`node probes/hermes-publication/run.mjs --write-evidence`; the default gate
compares it byte-for-byte. Retain and review counterexample changes; do not
regenerate away a failed safety assertion.
