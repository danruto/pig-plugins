# Staged native usage gate

`npm run test:native-usage-host` runs `python3 scripts/check-native-usage.py`, separately
from `npm test`. A missing prerequisite exits **2**, rather than skipping the nested
module or substituting the installed host. This is additional gate coverage, not a
change to the selected mixed collector, `--prod`, package membership or deployment.

The reviewed prerequisite is an external build artifact, not bundled host source.
The host input is the PiG executable at `libexec/pig/pig` in the PiG package, not
the `bin/pig` wrapper script that sets PATH and the agent directory before it
execs that executable. The gate supplies its own PATH and agent roots. Supply
both inputs explicitly from the devShell:

```sh
nix develop "path:$PWD" -c env \
  PIG_NATIVE_USAGE_HOST="$(dirname "$(readlink -f "$(which pig)")")/../libexec/pig/pig" \
  PIG_NATIVE_USAGE_SDK="$(pig reload --sdk-path)" \
  GOMAXPROCS=2 GOFLAGS=-p=2 npm run test:native-usage-host
```

The launcher accepts only PiG `0.7.0+1.5.0` with host SHA256
`d21d884ef0cab28224bf3eccfa302c8d70e430bc35d71a9a049f9eb1a612587b`
and SDK tree SHA256
`32342e9e495ac9016e110b1c8d043f8c0a5b59e6bd8a7f084515cd6a25cbf70b`.
The SDK digest hashes sorted relative POSIX file names, NUL, file bytes, NUL;
symlinks are rejected. The two pins identify one host executable and the SDK tree
that host stages for `pig reload --sdk-path`. The isolated host stages a fresh
SDK and that tree must match the supplied SDK. Host version and both hashes are
retained in `identity.json`. Changing either pin needs the owner's review of the
new host and SDK; a version string alone is insufficient.

Run on Linux with working Bubblewrap user/network/PID/mount namespaces and a Nix
store toolchain. Required executables on PATH: `go`, `cc` (race support), `node`,
`bash`, `env`, `strace`, `bwrap`, `git`, `nix-store`; the Python interpreter and
executables must resolve inside the Nix store. An explicit tool path can instead
be supplied as `PIG_NATIVE_USAGE_<TOOL>` (uppercase; `NIX_STORE` for `nix-store`).
These are existing system tools; there are no new Python/package dependencies.
Go must support the staged modules' Go version. Resolution is offline, telemetry
is disabled, and concurrency is fixed to `GOMAXPROCS=2`, `GOFLAGS=-p=2 -modcacherw`.
Unsupported systems or missing artifacts fail visibly; no fallback runs the host.

`PIG_NATIVE_USAGE_SCRATCH` optionally names an artifact parent. Each invocation
creates a new private child directory there (otherwise in the system temporary
directory), prints it, and retains it on success or failure. No existing roots
are reused. Only non-ignored repository status sources, the reviewed binary/SDK,
and Nix tool/runtime closures enter the sandbox. Personal HOME, credentials,
configuration, sessions, network and ambient environment are not mounted or
inherited. Runtime HOME/agent/cache/temp roots and model/auth files are created
from synthetic inputs. A caller running a focused objective should set the
scratch parent to that objective's disposable directory.

The focused entrypoint is the script that target runs:

```sh
nix develop "path:$PWD" -c env \
  PIG_NATIVE_USAGE_HOST="$(dirname "$(readlink -f "$(which pig)")")/../libexec/pig/pig" \
  PIG_NATIVE_USAGE_SDK="$(pig reload --sdk-path)" \
  python3 scripts/check-native-usage.py
```

It builds, vets and race-tests the nested adapter and conventional lifecycle
fixture with temporary modfiles; authored modules are never rewritten. It then
loads the real factory in the pinned PiG RPC host with synthetic OpenRouter and
DeepSeek models, drives held HTTPS through model/settings replacement and close,
and closes RPC input for shutdown. Readiness/command/receipt waits are capped
message waits, not sleep loops. Timeout or failed command is a failed gate.
The network namespace has no external interfaces; the factory verifies its local
TLS trust root, and traced IP connects must match its recorded loopback listener.
The runner joins its host and sandbox processes; the PID namespace is the cleanup
backstop on interruption. Receipts include traces, RPC output, fixture events,
namespace/environment proof, host wait and runner resource usage. Resources are
observations, not a resource-budget acceptance assertion.

This script supplies runnable build/check infrastructure and fixture orchestration.
It does **not** independently assert every stale-publication/lifecycle property,
repeat the five-minute expiry experiment, qualify the installed footer, or claim
that the whole package passes. Independent acceptance assertions remain
owned by the fresh test-writer and can use the retained real-host receipts.
