# Hermes compatibility findings

## Scope

Observed with PiG **0.5.3+1.4.0.74f46864**, Go **1.26.8**, Node **24.21.0**, patched `pi-hermes-memory` **0.9.11** and `modernc.org/sqlite` **1.52.0**. All memory/database/session fixtures were disposable. No personal memory was copied or migrated.

These are feasibility results, not acceptance credit for an unimplemented native extension.

## Observed results

| Check | Result | What remains unproven |
| --- | --- | --- |
| Frozen source reconstruction | Pinned archive plus retained integrity patch reconstructs all 92 packaged files; source digest verified | Dependency/runtime closure and native behavior |
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

From the repository root, after `npm ci`, point `HERMES_PACKAGE` at the pinned source installation, not its data directory:

```sh
export HERMES_PACKAGE="$HOME/.pig/agent/npm/node_modules/pi-hermes-memory"

python3 probes/capture-hermes.py "$HERMES_PACKAGE" --verify-source-only
python3 probes/test-capture-hermes.py
python3 probes/capture-hermes.py "$HERMES_PACKAGE" \
  --output /tmp/hermes-surface.json
cmp docs/compatibility/hermes-surface.json /tmp/hermes-surface.json
rm /tmp/hermes-surface.json

(cd probes/hermes-sqlite && go test -race -v ./...)
python3 probes/check-completion.py
node --import tsx probes/check-session-format.mjs
```

The SQLite probe starts the real TypeScript database/lock modules with a fresh `PI_CODING_AGENT_DIR` for each test. It never opens the package's default data root. The completion/session probes use a static provider that cannot contact a paid endpoint. Temporary processes and directories are cleaned up.

The captured surface is generated; regenerate it with `probes/capture-hermes.py`, not by hand. Capture refuses a source-tree or retained-patch mismatch before starting PiG. A changed source fingerprint needs a deliberate baseline review. The source-only check starts no PiG process. The five source-verification tests use copied package source in temporary roots and reject changed, added, removed and symlinked source files.

The [baseline manifest](hermes-baseline.json) pins the upstream archive SHA-256, exact retained patch and a digest of sorted relative-path/file-hash pairs for `src/`, `scripts/`, `docs/`, `LICENSE`, `README.md` and `package.json`. The [patch](../../patches/hermes-0.9.11-async-integrity.patch) changes only `src/index.ts` and `src/store/db.ts`; it retains the existing worker-based integrity scan and shutdown wait, not a new live-installation change. All 92 packaged files, including both changed files, are fingerprinted in the generated surface. The declared upstream commit is provenance metadata backed by reconstruction, not a claim that a version string proves source identity.

To reconstruct source independently, run this disposable check from the repository root. It downloads public source only and neither installs dependencies nor opens a memory database:

```sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
python3 - "$tmp" <<'PY'
import hashlib, io, json, sys, tarfile, urllib.request
from pathlib import Path
manifest = json.loads(Path('docs/compatibility/hermes-baseline.json').read_text())
raw = urllib.request.urlopen(manifest['upstreamArchive']['url'], timeout=30).read()
assert hashlib.sha256(raw).hexdigest() == manifest['upstreamArchive']['sha256']
root = Path(sys.argv[1]).resolve() / 'source'
root.mkdir()
with tarfile.open(fileobj=io.BytesIO(raw), mode='r:gz') as archive:
    for member in archive.getmembers():
        if not member.isfile() or '/' not in member.name:
            continue
        relative = member.name.split('/', 1)[1]
        if relative not in manifest['sourceFiles'] and not any(relative.startswith(name + '/') for name in manifest['sourceRoots']):
            continue
        target = (root / relative).resolve()
        assert target.is_relative_to(root)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(archive.extractfile(member).read())
PY
patch --batch --forward --fuzz=0 -d "$tmp/source" -p1 < patches/hermes-0.9.11-async-integrity.patch
python3 probes/capture-hermes.py "$tmp/source" --verify-source-only
```

These hashes identify source, not the production dependency closure. H1 must separately retain the installed dependency graph/lock, PiG executable and staged SDK digests, runtime/SQLite versions, provider fixture and selected configuration with each behavior capture. Do not invent a historical dependency pin from a source manifest or claim the operation corpus is complete.

The [consumer parity contract](../designs/hermes-parity.md) covers the runtime parser aliases and registration variants omitted by the default inventory. The [coordination table](../designs/shared-storage.md#coordination-identities) corrects the assumption that all callers use a single lock database. The existing lease probe checks a synthetic `mutation:fixture` key; it does not establish consolidation or recovery key/path parity.

## Status integration qualification

The status package's tests exercise versioned usage requests, normalized quota, late-response rejection, invalidation, reset expiry, cache-token accounting, used-versus-left labeling, native discovery and a real terminal at 120/80 columns. The Nix derivation builds a precompiled footer and includes locked production usage dependencies.

The pinned bridge's full offline race suite passed on rerun. The first run exposed an upstream intermittent `TestUnknownControlSubtypeErrorText` failure: the test waits for response `u5` before asserting other concurrently dispatched responses have arrived. No upstream test or production source was modified. This is recorded rather than hidden by deleting or narrowing the test.

A live, prompt-free `TestGetUsageAnswersWithKeyedWindows` invocation received a valid reply with `rate_limits_available: true` and `rate_limits: null`, so its assertion requiring keyed windows failed. This is **not** proof of live percentage availability. The bridge retains its event-window fallback; status does not invent quota or treat unknown as zero. Real terminal tests use labelled synthetic quota and are not presented as a live account measurement.

## Next checkpoint

H1 still needs the consumer-derived operation/configuration/search corpus, retained dependency/runtime closure and matched synthetic performance baseline. H2 requires bounded publication/recovery modeling and real mixed-writer tests before storage acceptance. An unresolved frozen-writer counterexample blocks cutover; it does not authorize a native-only workaround or an unapproved TypeScript patch. See the [native Hermes contract](../designs/pig-hermes.md); there is no native replacement to activate yet.
