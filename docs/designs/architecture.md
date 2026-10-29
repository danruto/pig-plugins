# Native plugin architecture

## Status and scope

These views specify the target architecture for [native status](native-usage.md) and [native Hermes](pig-hermes.md). They do not describe two completed ports. The installed status package still uses its pinned TypeScript collector; native Hermes is absent. [Compatibility findings](../compatibility/hermes.md) are feasibility evidence only.

Goals are feature parity, safe shared knowledge, responsive foreground work and native packaging. Non-goals are Pi runtime adapters in this package, a new knowledge service, a new schema, a telemetry platform, personal deployment changes and reduced-feature replacements.

## System context

The system in scope is the assistant with these plugins. The peer assistant is the independently running Pi installation. Sharing knowledge does not mean direct communication between the assistants.

```mermaid
C4Context
    title Assistant and plugin system context
    Person(user, "Operator", "Asks questions, selects accounts and manages durable knowledge.")
    System(assistant, "Assistant with owned plugins", "Shows account usage and recalls durable knowledge.")
    System_Ext(peer, "Independent assistant", "Reads and updates the same durable knowledge.")
    System_Ext(providers, "Account and inference services", "Supply account information and model responses.")
    Rel(user, assistant, "Uses and configures")
    Rel(assistant, providers, "Requests account information and model responses")
    BiRel(assistant, peer, "Share memories and skills through local storage")
```

Legend: person = operator; system = software in scope; external system = independently owned software. Storage and process placement appear below, not at context level.

## Target containers

```mermaid
C4Container
    title Native plugin runtime and shared stores
    Person(user, "Operator", "Controls commands and explicit instruction edits.")
    System_Ext(providers, "Account and inference services", "Supply usage data and model responses.")
    System_Boundary(assistant, "Assistant with owned plugins") {
        Container(host, "Assistant host", "PiG", "Owns foreground sessions, authentication and extension dispatch.")
        Container(status, "Status extension", "Go", "Collects account reports and renders cached status.")
        Container(hermes, "Knowledge extension", "Go", "Owns knowledge operations and background learning.")
        Container(child, "Restricted maintenance assistant", "PiG", "Runs bounded memory-only fallback operations.")
        ContainerDb(ownSessions, "Current-host sessions", "JSONL", "Retains foreground conversation records.")
    }
    Container(peer, "Peer assistant", "Pi and TypeScript", "Independently uses shared knowledge.")
    ContainerDb(peerSessions, "Peer-host sessions", "JSONL", "Retains the peer conversation records.")
    ContainerDb(knowledge, "Knowledge search store", "SQLite", "Stores search rows, metadata and indexes.")
    ContainerDb(documents, "Knowledge documents", "Markdown", "Stores memory documents, skills and standing instructions.")
    ContainerDb(leases, "Coordination stores", "SQLite", "Coordinate operation-specific cross-process ownership.")
    Rel(user, host, "Uses commands and tools")
    BiRel(host, status, "Dispatches work and receives cached summaries", "SDK and extension bus")
    BiRel(host, hermes, "Dispatches knowledge operations", "SDK and extension bus")
    Rel(host, providers, "Resolves authentication and requests completions", "Provider APIs")
    Rel(status, host, "Resolves selected-model authentication", "SDK")
    Rel(status, providers, "Requests guarded account reports", "HTTPS")
    Rel(host, ownSessions, "Persists foreground messages", "JSONL")
    Rel(hermes, ownSessions, "Indexes and searches", "JSONL")
    Rel(hermes, peerSessions, "Indexes and searches", "JSONL")
    Rel(peer, peerSessions, "Persists foreground messages", "JSONL")
    Rel(hermes, knowledge, "Queries and updates", "SQL")
    Rel(hermes, documents, "Reads and publishes", "Files")
    Rel(hermes, leases, "Acquires operation leases", "SQL")
    Rel(peer, knowledge, "Queries and updates", "SQL")
    Rel(peer, documents, "Reads and publishes", "Files")
    Rel(peer, leases, "Acquires matching operation leases", "SQL")
    Rel(hermes, child, "Runs selected fallback work", "Process invocation")
    Rel(child, providers, "Requests attributed completions", "Provider APIs")
    Rel(child, documents, "Mutates through allowed memory tools", "Files")
    Rel(child, knowledge, "Reconciles through allowed memory tools", "SQL")
    Rel(child, leases, "Acquires matching operation leases", "SQL")
```

Legend: container = runtime unit; database shape = persistent data; boundary = host composition. The coordination shape represents several databases, not one shared filename. Their exact identities are in the [storage contract](shared-storage.md#coordination-identities).

Normal extensions run as subprocesses. Compatible Go factories may be fused only after equivalent lifecycle and capability checks; this view does not require fusion. The current executable footer must be converted before it can be fused. The maintenance child is a separate host invocation, not a goroutine in the foreground host.

## Component ownership

| Owner | Owns | Allowed dependencies |
| --- | --- | --- |
| Status registration/adapter | Commands, host-auth resolution, public usage API adaptation and worker lifetime | Collector and host SDK |
| Status collector | Provider normalization, guarded requests, account/target selection, settings generation and report cache | Explicit auth snapshots, transport, clocks and cancellation |
| Footer | Cached summaries, width-aware presentation and opaque native bridge status | Cached state and presentation helpers only |
| Hermes registration/adapter | Schemas, commands, prompt resources, host calls and session lifetime | Domain owners and host SDK |
| Hermes store | Database access, Markdown mutation/reconciliation, lease protocol, recovery and maintenance | Explicit paths, clocks and cancellation |
| Hermes skills | Skill scope, collision/path guards and discovery | Document operations through the store's guarded boundary |
| Hermes sessions | Parsing, live/backfill indexing, retention and both search variants | Store and explicit session roots |
| Hermes learning | Review/correction/consolidation plans and scheduling | Store operations and an injected completion capability |

The SDK does not enter domain packages. Learning and session indexers do not open independent database connections or implement their own leases. Skills owns skill policy; the store owns guarded publication. Status has no Hermes or SQLite dependency. Create packages when needed, not as empty scaffolding.

## Usage refresh sequence

```mermaid
sequenceDiagram
    participant Host
    participant Status
    participant Provider
    participant Footer
    Host->>Status: Session, model, command or refresh trigger
    Status->>Host: Resolve effective model authentication
    Host-->>Status: Auth and model identity
    Status->>Status: Capture session, model, account, target and settings generation
    Status->>Provider: Bounded request after origin and identity checks
    alt Cancelled, expired or owner changed
        Status->>Status: Discard response and invalidate affected cache
        Status-->>Footer: Unknown or still-valid current-owner cache only
    else Provider failed
        Provider-->>Status: Failure
        Status->>Status: Apply baseline backoff and partial-result policy
        Status-->>Host: Sanitized command or query failure
    else Current owner and valid response
        Provider-->>Status: Account report
        Status->>Status: Revalidate identity and normalize report
        Status-->>Footer: Cached summary with freshness
    end
    Footer->>Footer: Render without credentials, history or network reads
```

The public query API includes callbacks, cancellation and session object identity. Native adaptation is an early feasibility gate, not a claim that serializing the request preserves these semantics. Check it before porting providers. No fallback may change the account or query other providers without the existing explicit selection.

## Owned work lifecycle

This state machine describes target lifecycle ownership, not the contents of a persisted table.

```mermaid
stateDiagram-v2
    [*] --> Initializing
    Initializing --> Active: Initialization succeeds
    Initializing --> Draining: Initialization fails
    Active --> Active: Start bounded owned work
    Active --> Quiescing: Compact or flush boundary
    Quiescing --> Active: Required work settles and owner remains current
    Active --> Draining: Shutdown, reload or session replacement
    Quiescing --> Draining: Shutdown, reload or session replacement
    Draining --> Closed: All owned work joined and resources closed
    Draining --> Stopping: Cancellation budget exhausted
    Stopping --> Closed: Child termination and worker join acknowledged
    Closed --> [*]
```

A session owner contains a cancellation source, monotonically changing generation and a registry of outstanding operations. Request-scoped SDK contexts end with their handlers. Awaited work stays inside the handler; deliberately asynchronous work has independent ownership and obtains a fresh awaited host-call context through the characterized bridge.

1. Close admission before draining; stop refresh and review scheduling.
2. On ordinary shutdown, run the eligible baseline flush before cancelling the work that flush needs. Reload skips that flush. Session replacement cannot carry an old plan into the new session.
3. Apply each operation's captured baseline budget, propagate cancellation, terminate owned fallback processes when required and join them.
4. Finish or cancel indexers, learners and integrity scans before closing database handles. A failed initialization follows the same resource cleanup path without admitting learning work.
5. Reject publication from cancelled or replaced owners. A timeout is failure, not evidence of a clean drain. Never close a database beneath a live worker to make shutdown appear fast.

The host may force termination before cleanup finishes. Report that case as interrupted cleanup; crash/restart qualification must demonstrate recovery. The design does not assume cooperative cancellation always succeeds.

## Logical knowledge model

The logical relationships mirror upstream `src/store/schema.ts`; field selection below is explanatory, not a replacement schema. FTS indexes and triggers remain derived from their owning rows.

```mermaid
erDiagram
    SESSIONS ||--o{ MESSAGES : contains
    SESSIONS ||--o{ SESSION_FILES : indexed_from
    SESSIONS {
        string id PK
        string project
        string cwd
        datetime started_at
        datetime ended_at
    }
    MESSAGES {
        string id PK
        string session_id FK
        string role
        string content
        datetime timestamp
    }
    SESSION_FILES {
        string path PK
        string session_id FK
        int size
        int mtime_ms
    }
    MEMORIES {
        int id PK
        string project
        string target
        string category
        string content
        int hit_count
        datetime last_hit_at
    }
    EXTENSION_METADATA {
        string key PK
        string value
    }
```

Skills and standing instructions are guarded documents, not new database tables. Project attribution is retained on rows; no new project registry is introduced. Lease rows belong to their operation-specific coordination databases, outside this logical knowledge model. [Shared storage](shared-storage.md) defines authority and partial-publication behavior.

## Trust boundaries and threats

These are data-flow diagrams: arrows carry data, not execution order. Trusted extension code runs with the operator's process privileges. Tool filtering restricts model-callable capabilities; it is not an operating-system sandbox for arbitrary provider-extension code.

```mermaid
flowchart LR
    subgraph external[External data and model output]
        provider[Account and inference services]
        reply[Model-generated memory proposal]
    end
    subgraph host[Operator-approved host and plugin code]
        auth((Effective-auth resolver))
        collector((Guarded usage collector))
        learn((Learning and content scanner))
        store((Guarded knowledge operations))
        render((Cached footer renderer))
        pin((Explicit standing-instruction command))
    end
    subgraph local[Shared local data]
        knowledge[(Knowledge rows and documents)]
        instructions[(Standing instructions)]
        sessions[(Separate host session roots)]
    end
    operator[Operator] -->|Explicit instruction text| pin
    pin -->|Scanned user-authorized content| instructions
    auth -->|Resolved credentials and identity| collector
    collector -->|Guarded authenticated request| provider
    provider -->|Untrusted account report| collector
    collector -->|Sanitized cached summary| render
    reply -->|Untrusted proposal| learn
    sessions -->|Conversation content| learn
    learn -->|Scanned mutation plan| store
    knowledge -->|Existing scoped content| store
    store -->|Validated publication| knowledge
    knowledge -->|Fenced context data| learn
```

```mermaid
flowchart LR
    subgraph parent[Knowledge extension]
        launch((Trusted child selection))
        apply((Allowed memory operations))
    end
    subgraph child[Separate maintenance host]
        model[Selected model]
        registry((Runtime tool allowlist))
        adapter[Explicit trusted provider extensions]
    end
    launch -->|Selected executable, extensions and cancellation| registry
    adapter -->|Provider configuration| model
    model -->|Tool requests| registry
    registry -->|Memory mutations only| apply
    apply -->|Scoped results| registry
    registry -->|Outcome and attribution| launch
```

| Threat | Required control | Qualification |
| --- | --- | --- |
| Account spoofing or credential disclosure | Effective host auth, official origins, header-override checks, account revalidation and redirect refusal | Local fixtures change credentials/model/target during requests and attempt redirects |
| Model output promoted into standing instructions | Separate explicit-user write path, content scans and prompt fencing | Automatic review, correction and consolidation cannot invoke the standing-instruction writer |
| Child gains file, shell, skill-management or MCP tools | Explicit child extension selection and runtime allowlist of `memory_add`, `memory_replace`, `memory_remove` only | Enumerate the real child's tools and attempt denied tool calls in a disposable process; do not rely on prompt wording |
| Untrusted provider code runs | Operator-approved provider-extension selection only; no model-selected executable/extension paths | Reject untrusted selection and preserve provider attribution; do not claim a sandbox |
| Path traversal, symlink escape or skill collision | Canonical identities, existing guards and conflict-safe moves | Unicode, missing-tail, alias-path, collision and concurrent-move corpus |
| Peer or external-editor write invalidates a plan | Shared operation identities and publication validation | Bounded concurrency model plus deterministic mixed-writer barriers |
| Recovery destroys readable data | Bounded recovery, retained readable rows, compatible schema and checked rollback | Corruption and crash fixtures including peers with open handles |
| Private content leaks through diagnostics | Fixed event/reason codes, redaction and no prompt/content/credential logging | Canary secrets and terminal-control characters never appear in retained diagnostic output |

External editors and peer writers can bypass this extension's scans. Treat their content as data on reads and never infer standing-instruction provenance merely because text appears in memory. Session roots are explicit data inputs; sharing knowledge never shares settings or authentication roots.

## Operational signals

Use host notifications, command/tool results and bounded diagnostic output. Do not add a telemetry backend. All diagnostic emission happens outside render callbacks.

Each operation has an ephemeral correlation ID, owner generation, operation kind, outcome, duration and a fixed reason code. Do not persist new operation history. Never include memory content, prompts, absolute private paths, account identifiers, credentials or arbitrary provider error bodies. Preserve baseline tool response fields and configuration-controlled notification behavior; additional diagnostics are not new public tool fields.

| Failure | User-visible behavior | Resource behavior |
| --- | --- | --- |
| Usage request fails or becomes stale | Preserve structured failure reason; `/usage` explains the sanitized failure; footer never invents zero | Preserve valid current-owner cache only until its baseline expiry, then show unknown; use baseline backoff |
| Lease busy or lost | Explicit deferred/conflict/cancelled operation, not plain success | Do not publish an obsolete plan; release only the owned token |
| Knowledge document saved but search reconciliation fails | Preserve partial-success warning and `/memory-sync-markdown` repair advice | Retain the durable document and mark search state degraded; do not repeat the mutation blindly |
| Integrity scan or recovery fails | Sanitized degraded/recovery diagnostic and baseline operation failure | Retain backups/readable data, stop unsafe writes and honor recovery circuit limits |
| Live index or backfill fails | Bounded indexing warning; search results are not represented as complete | Preserve retryable file metadata and cancellation ownership |
| Review or fallback fails | Preserve no-op/failure distinction, configured warnings and attribution | Cancel and join owned work; never mutate foreground conversation state |
| Drain exceeds budget | Explicit interrupted-cleanup diagnostic | Cancel/terminate and join; do not report clean shutdown from exit status alone |

Coalesce repeated background diagnostics by operation kind, owner and reason until recovery or owner change; explicit command/tool failures are always returned. Use process-local bounded state only. A new optional diagnostics configuration requires its own contract review.

## Decision records and validation

- [Native runtime and ownership](../adr/0001-native-runtime-ownership.md)
- [Frozen shared-storage baseline and safety gate](../adr/0002-frozen-shared-storage.md)
- [SQLite driver candidate](../adr/0003-sqlite-driver-candidate.md)
- [Consumer-derived Hermes parity](hermes-parity.md)

Checkpoint evidence must identify the source/host versions, selected composition, fixture, command, result and retained artifact hashes. A registration capture, a model, an offline fixture, a platform run and a live-provider result answer different questions; none substitutes for the others. No deployment, source publication or live-data change follows from a diagram or a checked document alone.
