# Staged native usage gate

`scripts/check.sh` invokes `python3 scripts/check-native-usage.py` before the existing
package checks. A missing prerequisite exits **2**, rather than skipping the nested
module or substituting the installed host. This is additional gate coverage, not a
change to the selected mixed collector, `--prod`, package membership or deployment.

The reviewed prerequisite is an external build artifact, not bundled host source.
Supply both inputs explicitly:

```sh
PIG_NATIVE_USAGE_HOST=/path/to/reviewed/pig \
PIG_NATIVE_USAGE_SDK=/path/to/its/staged/sdk \
GOMAXPROCS=2 GOFLAGS=-p=2 npm test
```

The launcher accepts only host SHA256
`caa1427bf3fd721f813b446748408111e4f1eb0138506b66845bf572e1b4ac0a`
and SDK tree SHA256
`f32d435671402b2948d8e14ac010eb8078ce2ceaeb589adab43b8a93f4bb4750`.
The SDK digest hashes sorted relative POSIX file names, NUL, file bytes, NUL;
symlinks are rejected. These pins identify the reviewed effective-auth capability
build. They are not a supported-installed-host claim. The isolated host stages a
fresh SDK and that tree must match the supplied SDK. Host version and both hashes
are retained in `identity.json`. Replacing these pins needs prerequisite review;
a version string alone is insufficient.

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

The focused entrypoint is the same command used by `check.sh`:

```sh
PIG_NATIVE_USAGE_HOST=/path/to/reviewed/pig \
PIG_NATIVE_USAGE_SDK=/path/to/its/staged/sdk \
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
that unrelated Hermes package tests pass. Independent acceptance assertions remain
owned by the fresh test-writer and can use the retained real-host receipts.
