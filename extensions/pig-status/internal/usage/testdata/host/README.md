# Disposable native authentication fixture

This is a staging adapter, not an installed extension. It imports the shipping SDK-free `usage.Resolver` and `usage.Dispatcher`; it does not reproduce their policy. Its SDK calls use only the current awaited command context. The two query workers and the cancellation observer are joined before the handler returns. Its TLS transport maps logical official destinations only to its own declared loopback listener, using the existing production client and a test-certificate trust root.

The fixture depends on the frozen reviewed host and its actually staged Go SDK under `.pb/probes/host-auth/`. Do not substitute the installed host or modify that dependency. The root status module and package membership intentionally remain unchanged. No module contains a machine-local replacement: the build coordinator supplies owned temporary modfiles.

From the repository root:

```sh
PY=/nix/store/q1khrhbn506imjxx7kiqcbihycawa6kl-python3-3.13.15/bin/python3
env -i "$PY" probes/native-auth/run.py build
env -i "$PY" probes/native-auth/run.py UNIQUE_NAME process
```

Use a new lowercase/hyphenated name on every run. The launcher fails closed if namespaces or the reviewed binary identity are unavailable. It mounts plugin/dependency inputs read-only and grants writes only to a fresh issue21 root. Neither `--offline` nor a fake resolver is used as an egress boundary. Inner child environments are independently reconstructed rather than inherited. The process mode captures actual process/connect traces, consuming waits, resource samples, forbidden runtime events and handled command dispositions. All IP connects must match the TLS listener recorded in `native.jsonl`. Reports go to `.pb/evidence/native-auth-20261104/resumed`; fixture roots go to `.pb/probes/21/auth`.

The process recipe drives current/other/all selection, absent/unsupported authentication, provider failure with partial results, discovered/exact/missing Fireworks targets, native OpenAI and opt-in companion collection, selected authorization replacement/deletion, changed credentials/model configuration/origin during HTTPS, and fresh OAuth mismatch/ambiguity. It records results and safety receipts; independent acceptance assertions belong to the evidence writer.

Additional isolated gates:

```sh
env -i "$PY" probes/native-auth/run.py ANOTHER_UNIQUE_NAME checks
env -i "$PY" probes/native-auth/run.py THIRD_UNIQUE_NAME footer
```

`checks` executes the precompiled owning usage/footer race binaries, four pinned-source comparisons, TypeScript checking and existing usage/docs tests. `footer` runs unchanged discovery and terminal checks against an owned byte-copy of the source package, because the host creates its temporary build modfile beside that source. The copied manifest still selects the existing Node collector. This is not activation or deployment.

The registered fixture commands are `/na-query`, `/na-model` and `/na-control`; no free-form model input or raw reload command is sent. Control state and TLS hold/release files are disposable stimuli, not production options. The fixture never logs snapshots, API keys, OAuth grants or provider error bodies. Request receipts use symbolic credential tags, not credentials.

The ordinary full `npm test` remains independently red on the pre-existing Hermes baseline. Passing these focused checks or the package Nix build is not a claim that full baseline is green. No live service, real login/refresh, reset/redemption, all-platform or runtime cutover qualification is implied.
