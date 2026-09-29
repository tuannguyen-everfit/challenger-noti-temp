# Layout — directory tree, file naming, in-file ordering, DI

Read this whenever you create a new file, name a struct, or wire something in `app.go`.

## 1. Directory tree

```
internal/
├── app/                          ← wiring (top — bootstrap, shutdown, DI graph)
├── features/                     ← BUSINESS LOGIC, one package per bounded context
│   └── <feature>/
│       ├── service.go            — Service + Repo/Cache/EventPublisher interfaces
│       ├── service_test.go       — unit tests with in-memory mocks
│       ├── repository_mongo.go   — Repo adapter, MongoDB backend
│       ├── repository_valkey.go  — Cache adapter, Valkey backend (when needed)
│       ├── indexes.go            — required indexes + IndexEnsurer (see mongo.md)
│       ├── consumer_kafka.go     — Kafka consumer adapter (see kafka.md)
│       ├── handler.go            — HTTP layer + DTOs + mapper (see http.md)
│       ├── handler_test.go       — handler tests with mock service
│       ├── log.go                — `var log = logging.Module("feature.<name>")` (see logging.md)
│       ├── errors.go             — domain sentinels + apperr.Mapping table
│       ├── localization.go       — UPPER_SNAKE_CASE i18n codes for this feature
│       └── integration_test.go   //go:build integration (or per-role split — see testing.md)
├── infra/                        ← EXTERNAL-SYSTEM CLIENTS (I/O against actual systems)
│   ├── grpcclient/     — Dial(...Option) helper
│   ├── kafka/          — Producer + Consumer wrappers + JSONHandler
│   ├── mongox/         — Mongo client + IndexSpec + generic CRUD
│   ├── otelx/          — OTel SDK setup + Span helper
│   └── valkey/         — go-redis client
│
├── platform/                     ← SERVICE-SHAPED CONCERNS (references our types)
│   ├── apperr/         — typed AppError + Mapping helper
│   ├── buildinfo/      — embedded VERSION
│   ├── config/         — viper loader (this service's config struct)
│   ├── httpx/{decode, health, middleware, respond, typed, router}
│   ├── localization/   — Code type + cross-cutting codes
│   ├── logging/        — slog JSON with OTel-aligned fields
│   ├── pagination/     — Cursor + Page[T] (cursor-based, NEVER offset)
│   └── validate/       — shared *validator.Validate singleton
│
└── stdx/                         ← PURE-GO HELPERS (zero non-stdlib imports)
    ├── ptr/            — Of, Deref, DerefOr, OfNotZero
    ├── retry/          — Do, DoWithResult[T]
    ├── safego/         — WrapErr, Go for panic-safe goroutines
    └── timex/          — date formats + StartOfDay/EndOfDay/ParseDay
```

### Three buckets under `internal/` — admission rules

A new package passes **exactly one** rule. The import list decides.

| Bucket | Admission rule | Examples |
|---|---|---|
| `internal/infra/` | Imports an external-system driver (mongo-driver, go-redis, kafka-go, grpc, OTel SDK). Does network/disk I/O against an actual external system. | `mongox`, `valkey`, `kafka`, `grpcclient`, `otelx` |
| `internal/platform/` | Service-shaped. References our `localization.Code`, `apperr.AppError`, our config struct, or our logging conventions. Couldn't be lifted out as a standalone module without rewriting. | `apperr`, `httpx`, `localization`, `logging`, `pagination`, `validate`, `config`, `buildinfo` |
| `internal/stdx/` | Zero non-stdlib imports. Could `go build` as a standalone module tomorrow. Name = "stdlib extensions" — every member of the bucket extends a stdlib idiom. | `ptr`, `timex`, `retry`, `safego` |

Borderline cases to call out in review:
- `pagination/` — pure-Go on the surface, but it IS our service-wide pagination contract (Cursor shape, Page[T] envelope) → stays in `platform/`.
- `safego/` — uses `log/slog` (stdlib) and panics from our goroutines, but no service-specific imports → stays in `stdx/`.

### Using existing `stdx/` packages

| Package | Use it when |
|---|---|
| `stdx/ptr` | Go's address-of-literal rule forces `*T` in a struct literal (DTOs, test fixtures). Use `ptr.Of(v)`, `ptr.Deref(p)`, `ptr.DerefOr(p, fb)`, `ptr.OfNotZero(v)`. **NOT** for general "I need a pointer" — `&v` inline is fine when it compiles. |
| `stdx/safego` | EVERY goroutine. `safego.Go(fn)` for fire-and-forget; `safego.WrapErr(fn)` for errgroup. See [concurrency.md](concurrency.md). Unwrapped `go func()` is a hard STOP. |
| `stdx/retry` | Bounded retry of an idempotent operation against a flaky external system (ping at boot, transient-error recovery). `retry.Do(ctx, fn, opts...)` or `retry.DoWithResult[T](...)`. NOT for retrying unbounded or for the request hot path. |
| `stdx/timex` | Date math / formatting where stdlib `time` is awkward. `timex.ParseDay`, `timex.StartOfDay`, `timex.EndOfDay`, `timex.DaysBetween`. Wire-format dates use `timex.DateFormat`. |

Importing from `stdx/` is preferred over reinventing — but don't reach for a helper when 3 lines of stdlib are clearer.

### Creating a new `stdx/` package

A new `internal/stdx/<name>/` is justified when **all** are true:

1. **Zero non-stdlib imports.** If you'd import `github.com/...`, it doesn't belong in `stdx/` — go to `platform/` or `infra/`.
2. **One focused responsibility.** Named for what it does (`ptr`, `retry`). Not a catch-all (`util`, `helpers`).
3. **< ~200 LoC.** Larger means it's probably a feature or a platform concern, not a helper.
4. **≥ 2 use sites.** Don't add a helper for a single caller — inline it. Promote to `stdx/` when the second caller appears.
5. **Reusable across features.** Not specific to one feature's domain (a feature-specific helper lives in the feature package).

**Naming:**

| Form | When | Examples |
|---|---|---|
| Just the noun | The name doesn't shadow stdlib | `ptr`, `retry`, `safego` |
| Noun + `x` suffix | The name would shadow a stdlib package | `timex` (shadows `time`), future `slicex` (shadows `slices`), `stringx` (shadows `strings`) |

The `x` suffix follows the `golang.org/x/` convention for "extends stdlib package of the same root name."

### `stdx/` STOP rules

| Symptom | Why |
|---|---|
| Adding `stdx/utils`, `stdx/helpers`, `stdx/common`, `stdx/lib` | Buckets that hide what's inside. Name the concern. |
| Adding `stdx/<x>` for a single use site | Inline first. Promote when the second use lands. |
| Importing a non-stdlib package from `stdx/<x>` | Violates the admission rule. The package belongs in `platform/` instead. |
| Re-implementing a Go stdlib function (e.g. `stdx/slicex.Contains` when `slices.Contains` exists) | Use stdlib. `stdx/<x>` covers gaps, not duplicates. |
| Adding `stdx/<x>` speculatively "we might need it" | Wait for a real use site. Premature abstraction is the §1.G smell. |

## 2. Adding a feature

A feature is a self-contained bounded context under `internal/features/<name>/`. Scaffolding a new one means four things: per-feature code files (§2.1), layer naming (§2.2), two wiring edits outside the package (§2.3), and the matching per-feature docs directory (§2.5).

### 2.1 Per-feature file set (in `internal/features/<name>/`)

| File | Purpose | Required? |
|---|---|---|
| `service.go` | `Service` struct + `Repo` / `Cache` / `EventPublisher` / cross-feature consumer-side interfaces + flow-through types + **every** method. **One file per feature, regardless of how many methods.** Only split (last layer to split) when this file exceeds 1000 LoC. See §2.6. | yes |
| `service_test.go` | **One** unit-test file per feature covering every method. Hand-written `mock*` doubles ([testing.md](testing.md)) live here too. **Never split** — even when production splits. | yes |
| `repository_mongo.go` | `NewMongoRepository(...) Repo` adapter | when feature persists |
| `repository_valkey.go` | `NewValkeyCache(...) Cache` adapter | when feature caches |
| `indexes.go` | `requiredIndexes []mongox.IndexSpec` + `NewIndexEnsurer` ([mongo.md](mongo.md)) | when Mongo-backed |
| `consumer_kafka.go` | `Consumer` struct + `NewKafkaConsumer(...) *Consumer` ([kafka.md](kafka.md)) | when subscribed to Kafka |
| `handler.go` | `Handler` struct + consumer-side `<feature>Service` interface + `Routes()` + every handler method + every DTO. **One file per feature** until it exceeds 1000 LoC. ([http.md](http.md)) | when HTTP-exposed |
| `handler_<concern>.go` | Only when `handler.go` exceeds 1000 LoC. Group handler methods + their DTOs by whatever reads naturally — name is the author's call. Shared bits (`Routes()`, `Handler` struct, consumer-side interface) stay in `handler.go`. See §2.6. | only when 1000-LoC threshold exceeded |
| `handler_test.go` | **One** test file per feature covering every method. Hand-written `mock<Feature>Service` double lives here. **Never split**, even after production splits. | yes if handler.go exists |
| `errors.go` | Domain sentinel errors + `apperr.Mapping` table + `mapErr(err) error` helper ([http.md](http.md) §2) | yes if handler.go exists |
| `localization.go` | `UPPER_SNAKE_CASE` `localization.Code` constants used by the Mapping table | yes if errors.go exists |
| `log.go` | One line: `var log = logging.Module("feature.<name>")` ([logging.md](logging.md)) | yes |
| `integration_test.go` | `//go:build integration` end-to-end against real Mongo/Valkey/Kafka | **paused** today — see [testing.md §1](testing.md) banner; do not create |

### 2.2 Layer naming — unprefixed, consistent within a feature

The four canonical exported layer names are **unprefixed** — the package name disambiguates:

| Layer | Exported name | Where it lives |
|---|---|---|
| HTTP | `Handler` (struct) | `handler.go` |
| Business logic | `Service` (struct) | `service.go` |
| Persistence | `Repo` (interface, in `service.go`) + lowercase `<infra>Repo` adapter struct in `repository_<infra>.go` | service.go + adapter file |
| Message-bus subscriber | `Consumer` (struct) | `consumer_<infra>.go` |

Constructors carry the infra name (NOT the type):

- `New(...)` for Service and Handler.
- `NewMongoRepository(...) Repo` — returns the interface.
- `NewValkeyCache(...) Cache` — returns the interface.
- `NewKafkaConsumer(...) *Consumer` — returns the struct (consumer is single-backend today, no interface needed).

**No module prefix on layer types.** `feature.Service`, `feature.Handler`, `feature.Repo`, `feature.Consumer` — the package name does the disambiguating. `ItemService` / `ItemHandler` is a Java/NestJS pattern that stutters in Go (`items.ItemService`) and conflicts with stdlib idioms (`http.Handler`, `sql.DB`, `mongo.Client`).

### 2.3 Wiring (files OUTSIDE the feature package)

Exactly two files outside `internal/features/<name>/`:

1. `internal/app/app.go` — construct `Service`, `Handler`, optional `Consumer`; pass adapters; register `IndexEnsurer` in the `EnsureIndexes(...)` call; if the feature spawns a long-lived goroutine or holds a closeable resource, add a row to `shutdown(...)` per [concurrency.md §5](concurrency.md).
2. `internal/platform/httpx/router.go` — add the feature's `*Handler` field to `RouterDeps`; mount with `r.Mount("/<name>", deps.<Name>.Routes())` inside the versioned API group.

`internal/platform/` and `internal/infra/` are **never** modified to add a feature.

### 2.4 Comment density — default no comments

Generated feature code defaults to **no comments**. Add one only when:

- It's a godoc doc string on an exported identifier — keep it **one short line**.
- The WHY is non-obvious from names: a hidden invariant, surprising semantics ("Failure is logged, NOT failed — item is already PUBLISHED in Mongo"), or a workaround.

Do NOT write:

- File-level "layout" or "what this file contains" banners — multi-paragraph headers at the top of feature files.
- Section dividers inside structs (`// === Public methods ===`).
- Comments restating WHAT the code does (well-named identifiers do that).
- Project-pattern explainers that duplicate `.claude/rules/` — those files are the source of truth; code should not re-derive them.

If you find yourself writing a comment that re-explains a rule from `.claude/rules/`, delete it and trust the rule file. The cost of a stale-and-duplicated rule is higher than the cost of a one-time lookup.

### 2.5 Per-feature docs — `docs/features/<name>/`

Every feature has a matching docs directory at the repo root. Layout (mirrors [`docs/README.md`](../../docs/README.md)):

```
docs/features/<name>/
├── specs.md                 — product requirements / user stories
├── test-cases.md            — acceptance scenarios the feature must satisfy
├── solution-design.md       — technical design: data model, API shape, cross-feature interactions
└── implementation-plan.md   — phased plan: milestones, dependencies, sequencing
```

Cross-cutting design (scaffolding, routing model, observability strategy) lives in `docs/architectures/`, NOT inside a feature's directory.

Create the directory + the four files when you start the feature, even if some are stubs initially. They are the audit trail for "why does this feature exist and how was it built." Code in `internal/features/<name>/` IS the implementation; `docs/features/<name>/` is the specification + reasoning.

### 2.6 File splitting — purely by LoC, never by domain

**One file per layer. Period. Until the file exceeds 1000 LoC.**

It doesn't matter how many endpoints / methods / flows / verbs the feature has. A feature with 10 endpoints in a 700-LoC `handler.go` stays one file. A feature with 2 endpoints in a 1200-LoC `handler.go` splits. **The number of "flows" is not a trigger — line count is the only trigger.**

| Layer | Default (≤ 1000 LoC) | After threshold (> 1000 LoC) |
|---|---|---|
| **Service** | `service.go` carries every method | Split last. Methods share interfaces + helpers + error tables, so cross-file coupling rarely pays off. If you must, mirror the handler pattern below. |
| **Handler** | `handler.go` carries every method + DTO | Keep shared bits (`Routes()`, `Handler` struct, consumer-side `<feature>Service` interface) in `handler.go`; move method+DTO groups into `handler_<concern>.go`. Group by whatever reads naturally — endpoint family, resource verb, anything sensible. The grouping name is the author's call; reviewers don't enforce a taxonomy. |
| **Tests** | One `service_test.go` + one `handler_test.go` covering every method | **Never split.** Mocks + setup are shared across tests; splitting forces duplication or cross-file fixture juggling. If `*_test.go` feels huge, the production file is the smell — split that first; the test file shrinks per-area naturally. |

**The 1000-LoC threshold is a number, not a guideline.** Don't pre-split at 800 "because it's getting close". Don't split a 1100-LoC file just to have 600+500. Wait for the pain. Premature splitting is a worse smell than a long file because:

- A long file gets faster to grep, faster to navigate, easier to follow one request end-to-end
- Split files force cross-file dependencies (shared types, helpers, interfaces) that add nothing
- Splitting is reversible; the diff to consolidate later is mechanical

**Why service splits last** (when it does): methods share heavy infrastructure — consumer-side interfaces, error-translation tables, shared helpers like `issueTokens` / `ensureProfileSnapshot`. The cross-file coupling cost is real.

**Why handler splits first** (when it does): handler methods carry per-method DTOs that don't share state. Cheap separation. But still — only at 1000+ LoC.

**Why tests never split**: one mock implementation of the feature's consumer-side interface covers every method. Splitting test files means duplicating that mock across files or building cross-file shared state. If tests look bloated, fix the production file shape first.

**Naming when handler splits**: the suffix names whatever reads naturally for the feature. Don't invent a taxonomy. Examples that are all fine: `handler_check.go`, `handler_admin.go`, `handler_v2.go`, `handler_write.go`. There's no rule that suffixes match HTTP path segments or "flow" names. The author picks; reviewers don't bikeshed.

Reference (upstream challenger-service repo, not in this template): `internal/features/auth/` is the canonical example — `service.go` (~470 LoC, one file) + `handler.go` (under threshold today, **could** split if it grew past 1000) + `service_test.go` + `handler_test.go`. The smaller `internal/features/challenge/` has one of each.

## 3. Adapter file naming — `repository_<infra>.go`

The suffix names the **infrastructure** (the actual external system), NOT the role:

- ✓ `repository_mongo.go`, `repository_valkey.go`, `repository_redis.go`, `repository_s3.go`, `repository_kafka.go`, `repository_postgres.go`
- ✗ `repository_cache.go`, `repository_store.go`, `repository_primary.go` — these mix the role into the filename and break the convention.

**Reasoning:** the role is already encoded in the interface name (`Cache`, `Repo`, `Outbox`). The filename's job is to tell you *which external system this adapter talks to*. Two features with a Cache concern should both have `repository_valkey.go` (or whichever backend) — uniformity means `find . -name 'repository_valkey.go'` enumerates every Valkey adapter.

## 4. Multi-backend rule

When a feature needs more than one external system:

- One interface per concern on `Service`: `Repo`, `Cache`, `EventPublisher`, `Blobs`, `Outbox`, …
- Do **NOT** bundle behind a fat `Storage` interface — `Repo` errors propagate, `Cache` errors degrade. Bundling re-couples lifecycles.
- One adapter file per backend: `repository_<infra>.go`.
- Each adapter exports its constructor (`NewMongoRepository`, `NewValkeyCache`, …) so `internal/app/app.go` composes them and passes them to `feature.New(...)`.
- The feature's `New` takes **interfaces only** — never the concrete `*mongox.Client`.

## 5. In-file ordering

Every business file (`service.go`, `handler.go`, `repository_*.go`) reads top-to-bottom in this shape:

```
const + enums
→ dependency interfaces (consumer side)
→ struct + constructor
→ flow-through types (domain / inputs / outputs)
→ method implementations
→ internal helpers
```

Reviewers expect this shape — match it.

## 6. Consumer-side interfaces (where they live)

"Accept interfaces, return structs." Define the interface at the **consumer** boundary:

- Handler consumes Service → `<feature>Service interface` lives in `handler.go`.
- Service consumes Repo + Cache + EventPublisher → those interfaces live in `service.go`.
- Feature A consumes Feature B → `featureBClient interface` lives in A — see [cross-feature.md](cross-feature.md).

Same-package callers can skip the interface when the producer is right there — indirection without benefit.

## 7. Dependency injection — manual only

Construct the dependency graph in `internal/app/app.go::Run`. Read top-to-bottom, that IS the documentation.

## STOP rules

| Symptom | Why |
|---|---|
| `wire`, `fx`, `dig`, decorator-based DI, container.Register | Manual DI in `app.go` is the convention. The graph fits in one file. |
| `func New(opts Options) *X` with a fat options struct of pointers | Use functional options (`X(...Option)`) — see `grpcclient.Dial`. |
| Singleton via `init()` setting a package-level mutable var | Go community avoids `init()`. Build in `main`, pass in. **Exception:** pre-warmed pure-function caches (validator) with `init()` documented at the call site. |
| `var Service service.Service = service.New()` at package level | Package-level mutable state breaks parallel tests. Construct in `app.go`. |
| New `helpers/`, `utils/`, `common/`, `generic/` package | Naming a package by what it doesn't contain is the Java/Node bag-of-utility pattern. Find a real domain name. |
| Section comments inside structs (`// === Public methods ===`) | Godoc ordering + exported-first convention. Visual section dividers are noise. |
| Setters/getters wrapping plain fields without reason | Export the field. Only introduce a method when there's invariant maintenance, validation, or thread-safety. |
| Spaces (any count) for `.go` indentation | gofmt enforces tabs. Set editor `tabSize` to render narrow. |
| `// File: foo.go` header banners, ASCII boxes, multi-paragraph file headers | Go convention is a one-line `// Package x ...` doc comment. |
| Generic `Map[A, B]` / `Filter` over slices invented locally | A 3-line `for ... append` is clearer. `maps.Keys(m)` etc. live in stdlib (Go 1.21+). |
| `ToPtr` / `FromPtr` everywhere instead of struct-literal field assignment | Allowed in DTO / test code where Go's address-of-literal rule forces it. See `internal/stdx/ptr`. |
| Module-prefixed layer types (`ItemService`, `WatchlistHandler`, `OrderRepo`) | Stutters at every call site (`items.ItemService`) and fights Go convention (`http.Handler`, `sql.DB`, `mongo.Client`). Use unprefixed `Service` / `Handler` / `Repo` / `Consumer` — see §2.2. |
| Exporting the Mongo adapter struct (e.g. `type MongoRepository struct`) | The adapter struct is lowercase + private; only the constructor `NewMongoRepository` is exported. The Service holds the `Repo` interface, never the concrete type. See §2.2. |
| Naming the Kafka consumer struct `KafkaConsumer` | Layer name is `Consumer` (unprefixed, like the others). Infra goes in the filename (`consumer_kafka.go`) and the constructor (`NewKafkaConsumer`), not the type. |
| Generating new feature code with multi-paragraph file-header banners or section dividers | Default to NO comments. One-line godoc on exported identifiers; WHY-comments only when non-obvious. See §2.4. |
| Comments in feature code re-explaining patterns from `.claude/rules/` | `.claude/rules/` is the source of truth. Code restating those rules rots; delete the comment, trust the rule. See §2.4. |
| New feature in `internal/features/<name>/` without a matching `docs/features/<name>/` directory | Docs are part of scaffolding, not optional. See §2.5 + [docs/README.md](../../docs/README.md). |
| Feature-specific design notes under `docs/architectures/` | Cross-cutting only — feature-specific work belongs in `docs/features/<name>/`. See §2.5. |
| Splitting any production file before it exceeds **1000 LoC** | LoC is the only trigger — not number of endpoints, not "flows", not "this feature has 7 methods". Premature splitting is a worse smell than a long file. See §2.6. |
| Splitting `service.go` while under 1000 LoC | Service shares interfaces + helpers + error tables; the savings rarely justify cross-file coupling. Service splits LAST when any split happens. See §2.6. |
| Splitting `service_test.go` or `handler_test.go` — **ever** | Test files don't split. Tests share mocks + setup; splitting forces duplication or cross-file fixture juggling. If a test file feels huge, split the production file it covers. See §2.6. |
| `handler.go` over 1000 LoC | Now split — group handler methods + DTOs by whatever reads naturally (suffix is the author's call); keep `Routes()` + struct + consumer-side interface in `handler.go`. See §2.6. |

## References

- `.claude/rules/` (this directory) — the layered template lives here. Future features follow the file set in §2.1, the naming in §2.2, the wiring in §2.3, the comment policy in §2.4, and the docs layout in §2.5.
- [`docs/README.md`](../../docs/README.md) — the doc layout (`architectures/` + `features/<name>/`).
- `internal/app/app.go` — full DI graph in one file.
- `internal/platform/httpx/router.go` — Mount table.
