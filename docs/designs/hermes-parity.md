# Hermes consumer compatibility contract

## Source of truth

Derive compatibility from the [frozen source](../compatibility/hermes-baseline.json), runtime registration and executable observations. The [surface capture](../compatibility/hermes-surface.json) is an inventory: six tool names, ten command names, default registration metadata and 44 interface field names. It is not a complete parameter schema, configuration parser or behavioral contract.

The existing capture uses a disposable default configuration. Its `toolDetails` do not include parameter schemas. Configuration can change registration: `sessionSearch.variant=anchors` replaces the `session_search` input with a required `markdown` string, whereas legacy mode accepts `query`, `project`, `role`, `limit` and `snippetChars`. Disabling standing instructions can remove `/memory-pin`. Do not force the default command count onto every configuration.

## Derivation and coverage procedure

1. Enumerate tool, command, event, resource and prompt consumers from `src/index.ts` and their registration functions. Capture runtime parameter schemas as well as names/descriptions through real PiG introspection or an isolated registration harness loading the actual extension. Never replace the extension with a reduced implementation.
2. Enumerate accepted configuration inputs from `src/config.ts`, path normalization in `src/paths.ts`, constants and each consumer branch. The `MemoryConfig` interface supplies canonical field names, not every accepted JSON input.
3. For each branch, capture inputs, normalized configuration, registration, operation result, error/partial-success fields and observable side effects. Include default, omitted, malformed, boundary and disabled forms; use the default configuration plus targeted variations rather than a blind Cartesian product.
4. Assign each contract row a stable fixture ID, consumer source symbol, frozen source digest, native checkpoint and evidence path. An unrepresented consumer branch blocks the relevant checkpoint. Any explicit exclusion needs an owner decision; a complete-looking count is not an exclusion mechanism.
5. Compare TypeScript and Go observations on the same synthetic roots, clocks, session content and model/provider fixtures. Preserve ordering where observable. Separate intentional host differences from accidental API changes and obtain a decision for an unbridgeable contract.

The H1 operation corpus is incomplete. The table below defines required coverage families, not a claim that the fixtures exist or that these families enumerate every branch. H1 must produce the branch-to-fixture inventory before H2 acceptance; H5 must show every row passing.

## Required coverage families

Source paths are relative to the baseline package. Resolve the actual registration functions from `src/index.ts`; the full-tree fingerprints prevent silent source changes.

| Family | Consumer-side derivation | Required observations | Checkpoint |
| --- | --- | --- | --- |
| Configuration | `src/config.ts`, `src/types.ts`, `src/constants.ts`, `src/paths.ts` and all configuration reads | Defaults, aliases, validation, precedence, empty/zero/disabled forms, path resolution and parse/access failure fallback | H1/H2 |
| Memory writes | `src/tools/memory-tool.ts`, `src/store/memory-store.ts`, `src/store/sqlite-memory-store.ts` | Every action/target, project binding, categories, Unicode units, duplicate/ambiguous matching, cancellation and document/search partial success | H2/H3 |
| Memory search | `src/tools/memory-search-tool.ts` and `src/store/sqlite-memory-store.ts` query consumers | Input bounds, scope/category, FTS grammar, ranking, short strings, snippets, timestamps, hit tracking and errors | H3 |
| Session search | `src/tools/session-search-tool.ts`, both underlying search implementations and session parser | Legacy schema/results and anchors-only Markdown schema/results, both host roots, dates/roles/projects, branch/compaction/tool content and retention | H3/H4 |
| Skills | Registered skill tool, skill store and resource-discovery handlers | All actions and aliases, global/project/external scope, name/path guards, collisions, moves, deletion confirmation and partial success | H3/H4 |
| Context and standing instructions | Prompt/resource handlers, context preview and standing-instruction store | Both memory modes, all policy styles, lazy initialization, snapshot behavior, explicit pin provenance and disabled registration | H3 |
| Learning and consolidation | Review/correction/flush/consolidation handlers and child invocation helper | Thresholds, initial-turn floor, regex semantics, budgets, fallback ordering, attribution, no-op/failure and restricted child tools | H4 |
| Commands | Every command registered from `src/index.ts` | Parameters/defaults, terminal and RPC behavior, selection/cancellation, notifications and errors for all ten default commands | H4 |
| Lifecycle and maintenance | Initialization, indexing, shutdown and integrity/recovery consumers | Lazy/eager initialization, failed init, session replacement, compaction, reload without normal flush, interrupted cleanup and mixed-writer recovery | H2/H4 |

## Configuration inputs beyond the interface

Preserve the current parser behavior, including permissive inputs. Tightening a numeric validator or rejecting a legacy union form is a contract change, not automatic hardening.

| Input | Baseline behavior to preserve |
| --- | --- |
| `llmModelOverride` string | Trim and use a nonempty model override |
| `llmModelOverride` string array | Trim/filter entries, use the first as primary and the remainder as initial fallbacks |
| `llmFallbackModels` | Trim/filter and append unique fallbacks |
| `llmModelFallbacks` | Legacy accepted input, appended after the canonical fallback input |
| `fallbackModels` | Legacy accepted input, appended after `llmModelFallbacks` |
| `childExtensionPaths` | Trim/filter/deduplicate trusted configured sources |
| `memoryOverflowStrategy` and `autoConsolidate` | A valid explicit strategy wins and derives the boolean; otherwise the valid legacy boolean derives `auto-consolidate` or `reject` |
| `sessionSearch.variant` | `legacy` and `anchors` are distinct schemas/behaviors, not just a presentation setting |
| `flushCompactTimeoutMs` | Preserve zero/negative disable semantics and warning behavior for a positive below-default budget |
| `sessionRetentionDays` | Preserve finite nonnegative input and explicit zero disable |
| Correction-pattern arrays | Missing means defaults; an empty array disables that pattern group |
| Invalid JSON or configuration access failure | Preserve parser fallback to defaults rather than borrowing usage-settings failure policy |

The two fallback aliases are additional accepted input names, not new canonical `MemoryConfig` fields. Capture their combined ordering with array-form primary overrides and canonical fallbacks; testing each in isolation does not prove precedence.

## Host-boundary gates

- Direct and registry completions through an extension provider are demonstrated only by the existing offline probe. Native production use must separately prove cancellation, failure chains, provider attribution and operation/session ownership.
- Capture both command-mode behavior and real runtime schemas. A validation report's names/descriptions cannot prove argument acceptance or response fields.
- The child fallback must expose exactly the three memory mutation tools, not skill management, file/shell or MCP tools. Verify the real child registry and denied calls, including retry-without-overrides paths.
- Preserve trusted provider-extension selection and auth semantics. A model fallback cannot silently become a different billing/account contract.
- Keep Pi/PiG session roots separate and explicit; neither schema capture nor parsing one JSONL fixture establishes complete multi-root behavior.

## Completion rule

Replace the phrase “matches all 44 config keys” with “matches every captured consumer contract, including canonical fields, legacy aliases, nested/union forms, defaults, registration variants and failure behavior.” No row is complete because its tool or command name appears in a capture. Native Hermes remains outside installed package membership until all feature, mixed-writer, platform and cutover gates pass.
