# Hermes compatibility findings

## Scope

Observed with PiG **0.5.3+1.4.0.74f46864**, Go **1.27.3**, Node **24.21.0**, patched `pi-hermes-memory` **0.9.11** and `modernc.org/sqlite` **1.52.0**. The original feasibility run used Go 1.26.8; the closure-bound reproduction uses the exact versions and digests in the [executable manifest](hermes-closure.json). All memory/database/session fixtures were disposable. No personal memory was copied or migrated.

These are feasibility results, not acceptance credit for an unimplemented native extension.

## Observed results

| Check | Result | What remains unproven |
| --- | --- | --- |
| Frozen source reconstruction | Retained pinned archive plus integrity patch reconstructs all 92 packaged files; source digest verified offline | Native behavior |
| Real upstream registration | 6 default tools, 10 default commands, 44 canonical interface fields captured | Parameter schemas, parser aliases, configuration-dependent registration and native result parity |
| Open TypeScript-created database from Go | Passed; TypeScript SQLite 3.53.4, Go SQLite 3.53.2 | All recovery paths and supported platforms |
| FTS5 trigram search | Japanese substring found through the real upstream schema/index | Full query grammar, ranking, short-query behavior and every tokenizer edge |
| Go write, TypeScript read | Both entries readable; TypeScript `integrity_check` returned `ok` | End-to-end Markdown/SQLite atomicity and concurrent mutations |
| Shared lease representation | Go sees TypeScript's token; TypeScript releases its own lease and refuses to steal a live Go-created lease | Full native acquisition, incarnation, stale-takeover and fencing implementation |
| PiG `Context.Complete` | Completed through an extension-provided offline model | Real-provider attribution/auth behavior and fallback chains |
| Registry completion/stream-simple | Both completed through the same extension provider | Cancellation and transport failure corpus |
| Owned background completion | Completed after startup returned while foreground `get_state` stayed responsive; clean shutdown | Production operation ownership, session replacement and draining under failures |
| Real PiG session JSONL | Existing TypeScript parser returned the exact user and assistant fixture content | Full branch/compaction, tool-call and retention corpus |

The background probe deliberately gates its completion until the real foreground RPC responds. A worker that blocks startup cannot pass by merely finishing quickly.

## Reproduce

From the repository root, after `npm ci`, select source explicitly, never a memory directory. Keep all disposable state under the objective scratch root:

```sh
export TMPDIR="$PWD/.pb/probes/2"
mkdir -p "$TMPDIR"
export HERMES_PACKAGE="$HOME/.pig/agent/npm/node_modules/pi-hermes-memory"

python3 probes/hermes-closure.py --check
python3 probes/hermes-closure.py --verify-installed
python3 probes/capture-hermes.py "$HERMES_PACKAGE" --output "$TMPDIR/hermes-surface.json"
cmp docs/compatibility/hermes-surface.json "$TMPDIR/hermes-surface.json"

python3 probes/hermes-closure.py --observe bash -c \
  'cd probes/hermes-sqlite && go test -race -v ./...'
python3 probes/hermes-closure.py --observe python3 probes/check-completion.py
python3 probes/hermes-closure.py --observe node --import tsx probes/check-session-format.mjs
```

The SQLite probe starts the real TypeScript database/lock modules with a fresh `PI_CODING_AGENT_DIR` for each test. It never opens the package's default data root. The completion/session probes use a static provider that cannot contact a paid endpoint. The observation wrapper supplies disposable home/configuration roots, omits inherited credentials, disables Go dependency downloads and prints the verified closure digest before the command's output. Temporary processes and directories are cleaned up. Direct, unwrapped legacy probe output is not closure-bound acceptance evidence.

The captured surface is generated; regenerate it with `probes/capture-hermes.py`, not by hand. Full capture now verifies the installed executable closure before starting registration and retains its digest in the surface. The source-only check starts no PiG process. The five existing source-verification tests use copied source in temporary roots and reject changed, added, removed and symlinked files; normal package checks run them against reconstructed source without requiring a personal installation.

The [source manifest](hermes-baseline.json) pins the public upstream archive SHA-256, exact retained patch and a digest of sorted relative-path/file-hash pairs for `src/`, `scripts/`, `docs/`, `LICENSE`, `README.md` and `package.json`. The [retained archive](hermes-upstream-0.9.11.tar.gz) has that exact public digest. `--check` extracts only the packaged source to a disposable root, applies the [patch](../../patches/hermes-0.9.11-async-integrity.patch) with zero fuzz, and verifies all 92 files. No dependency installation, substitution, network access or memory database is involved. The patch retains the existing worker-based integrity scan and shutdown wait. The declared upstream commit is backed by reconstruction, not merely a version string.

## Frozen executable closure

The generated [executable manifest](hermes-closure.json) and its [SHA-256 receipt](hermes-closure.sha256) are distinct from the source fingerprints. They retain:

- The Hermes-reachable npm dependency/peer graph, exact versions, public registry URLs/integrities and installed file digests plus owner/group/other execute bits, with the same permission-aware fingerprints for the separately resolved `tsx` probe loader, its platform dependency, the staged SDK and the Node bridge. Unrelated host packages and the personal installation's root lockfile are not copied.
- PiG, Node, npm and Go executable versions/file digests; the complete recursive Nix store graph with NAR hashes; the staged SDK identity and all file digests; the complete PiG Node bridge and bundled package metadata. Hermes peers are supplied by that bridge, not fictitious npm peer installations.
- TypeScript SQLite 3.53.4 and Go SQLite 3.53.2, their exact source IDs, Node ABI/runtime versions, SQLite compile options and the actually loaded `better-sqlite3` native binary digest. Go module versions/sums and probe module-file digests are retained separately.
- Synthetic fixture hashes and the isolated, provider-free capture configuration. Provenance contains no home paths, personal configuration, credentials, memory or session content. Nix store paths identify public runtime artifacts, not personal roots.

`npm test` and the Nix package check always run `--check`: verify the retained closure receipt, source/archive/patch/fixture/module provenance, all retained tree fingerprints, and the generated registration's closure/source linkage. When `HERMES_PACKAGE` is set, `npm test` also runs `--verify-installed`, comparing the actual dependency graph/bytes, freshly staged SDK/bridge and queried SQLite identities. An artifact-only pass does **not** claim the installed runtime was checked.

Installed verification is intentionally specific to this observed Linux arm64 Nix corpus. It verifies actual recursive Nix store contents (`nix store verify --no-trust --recursive`) and cached Go module contents (`go mod verify`); it does not silently install missing packages, select newer versions or fall back to another runtime. Missing original runtime/dependencies or drift fails the executable check. Runtime bytes are not vendored here: restore the exact Nix paths/NAR identities and exact registry artifacts, then verify installed-byte equality before an observation. Source reconstruction is independently available offline from the retained archive and patch.

To deliberately recapture after an approved baseline review (not to repair drift by substitution):

```sh
python3 probes/hermes-closure.py --capture
python3 probes/capture-hermes.py "$HERMES_PACKAGE" --output docs/compatibility/hermes-surface.json
python3 probes/hermes-closure.py --check
```

These are new, reproducible observations, not an invented historical dependency pin. Retaining this closure does not establish a complete operation corpus, consumer inventory, performance baseline or native Hermes parity.

The [consumer parity contract](../designs/hermes-parity.md) covers the runtime parser aliases and registration variants omitted by the default inventory. The [coordination table](../designs/shared-storage.md#coordination-identities) corrects the assumption that all callers use a single lock database. The existing lease probe checks a synthetic `mutation:fixture` key; it does not establish consolidation or recovery key/path parity.

## Status integration qualification

The status package's tests exercise versioned usage requests, normalized quota, late-response rejection, invalidation, reset expiry, cache-token accounting, used-versus-left labeling, native discovery and a real terminal at 120/80 columns. The Nix derivation builds a precompiled footer and includes locked production usage dependencies.

The pinned bridge's full offline race suite passed on rerun. The first run exposed an upstream intermittent `TestUnknownControlSubtypeErrorText` failure: the test waits for response `u5` before asserting other concurrently dispatched responses have arrived. No upstream test or production source was modified. This is recorded rather than hidden by deleting or narrowing the test.

A live, prompt-free `TestGetUsageAnswersWithKeyedWindows` invocation received a valid reply with `rate_limits_available: true` and `rate_limits: null`, so its assertion requiring keyed windows failed. This is **not** proof of live percentage availability. The bridge retains its event-window fallback; status does not invent quota or treat unknown as zero. Real terminal tests use labelled synthetic quota and are not presented as a live account measurement.

## Next checkpoint

H1 still needs the consumer-derived operation/configuration/search corpus and matched synthetic performance baseline. H2 requires bounded publication/recovery modeling and real mixed-writer tests before storage acceptance. An unresolved frozen-writer counterexample blocks cutover; it does not authorize a native-only workaround or an unapproved TypeScript patch. See the [native Hermes contract](../designs/pig-hermes.md); there is no native replacement to activate yet.
