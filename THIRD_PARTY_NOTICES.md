# Third-party sources

- `@narumitw/pi-usage` **0.68.3**, MIT, by narumiruna: https://github.com/narumiruna/pi-extensions/tree/main/packages/pi-usage. Used as the internal status collection backend, not installed as a second PiG extension package. The selected TypeScript provider adapters, usage menu, account handling and Codex controls remain upstream code. Native translations in `extensions/pig-status/internal/usage/` derive from its `types.ts`, `query.ts`, `core.ts`, `openai-companion-auth.ts`, `auth-resolution-guard.ts`, `oauth-credential-source.ts`, `usage-targets.ts`, `usage-helpers.ts` and provider modules (including `command-code.ts`, `opencode-zen.ts`, `kimi-coding.ts` and `xai.ts`); the upstream license is retained below.
- `pig-claude-bridge` **0.2.0**, commit `670c5a379a9843db82ea069aad840f71099afde2`, MIT, by balcsida: https://github.com/balcsida/pig-claude-bridge. Its native usage status is consumed through PiG's footer data. The bridge is not modified or redistributed here.
- Staged `pig-hermes` source reference: `pi-hermes-memory` **0.9.11**, MIT, by chandra447, https://github.com/chandra447/pi-hermes-memory. Native configuration, path resolution and SQLite schema/migrations in `extensions/pig-hermes/` derive from its `src/config.ts`, `constants.ts`, `paths.ts`, `project.ts`, `index.ts`, `store/memory-store.ts`, `store/schema.ts` and `store/db.ts` at the pinned commit below. The extension remains unselected; its upstream license is also retained in `extensions/pig-hermes/LICENSE`. `patches/hermes-0.9.11-async-integrity.patch` retains the existing local integrity-worker changes against commit `71cf5ab7d909318a383bf52bb3ad3cf83a9efc03`; its patch context is upstream code under the license retained in `patches/pi-hermes-memory.LICENSE`. Source and patch hashes are recorded in `docs/compatibility/hermes-baseline.json`. Preserve this attribution and the upstream Hermes attribution when porting code.

- Docs preview: `markdown-it` **14.3.3**, MIT, https://github.com/markdown-it/markdown-it, renders Markdown with raw HTML disabled. Aqua Cutter **2.0.0** assets are fetched from public release commit `9be4e3d9cab518de4cae13471388ca516d47978f`, https://github.com/danruto/aqua-cutter-releases, and verified against pinned SHA-384 hashes. The release retains its dependency notices under `releases/2.0.0/licenses/`.

- `pi-openai-server-compaction` **0.1.0**, MIT, by algal, pinned to commit `8a3de2f3b0c178fdd6f73f2f94172dfc3943e466`: https://github.com/algal/pi-openai-server-compaction. The tarball is a development-only preparation input. The explicit preparation script verifies the upstream source and generates the compaction hooks, summary generation and state reconstruction modules, retaining its `LICENSE.md`. Remote-history replay uses the owned context-preserving helper. The custom provider and WebSocket implementation are not generated or selected; PiG supplies native model transports. Neither the upstream tarball nor its `ws` dependency is required in the production dependency closure.

- Usage fixture reconstruction archive (`docs/compatibility/usage/production.tar.gz`): the locked backend above and `@narumitw/pi-tui-kit` **0.67.0** (MIT, narumiruna), `diff` **9.0.0** (BSD-3-Clause), `grok-mermaid` **0.2.3** (Apache-2.0), and `highlight.js` **11.12.0** (BSD-3-Clause). Complete installed distributions, including their original license/notice files, are retained for offline reconstruction. Registry integrity and installed file fingerprints are in `docs/compatibility/usage/provenance.json`.

Dependencies retain their own license files. No personal memory, session history, authentication data or machine configuration belongs in this repository.

## pi-usage native translations — MIT License

Copyright (c) 2026 narumiruna

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
