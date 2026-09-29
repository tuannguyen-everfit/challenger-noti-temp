# Testing

Read when creating any `_test.go` file or wiring a test double.

> ⏸ **Current policy (2026-05-19): integration tests are SKIPPED.** Do not write new `integration_test.go` files today. The template (`//go:build integration` build tag, filename rules, coverage-integration-gate plumbing) stays documented in this file so we can re-enable it without re-deriving the convention — but until that decision is reversed, unit tests are the only required bar.

## 1. What's required

| Tier | Required? | Trigger |
|---|---|---|
| **Unit** (`_test.go`, no build tag) | YES — every Service, Handler, non-trivial helper | Default |
| **Integration** (`integration_test.go`, `//go:build integration`) | **Skipped today** — template preserved for future re-enable | (paused) |

Unit tests cover business logic with hand-written mocks for external collaborators (Repo, Cache, another feature's Service). They are the required bar.

§2 (filename), §3 (build tags), §1.1 (coverage-gate exclusion list), and §1.2 (coverage-integration-gate) below are the integration-test template — kept documented so re-enabling is mechanical. If a wire-level concern feels critical enough to demand integration coverage, raise it with the team rather than silently bringing back the practice for one feature.

**When integration tests are re-enabled, the criteria for writing them will be:** non-trivial driver-level behavior (compound bson queries, transactions, optimistic locking under contention), or a wire-level bug that burned a production incident. Don't add an integration test "to check the box" — the unit test for the same code path is the actual requirement.

**To reverse the pause (when the team decides):**

1. Delete the ⏸ banner above.
2. Restore the "Integration" row in the §1 table to its previous "No — optional / Add per the criteria below" form.
3. Drop the matching STOP trigger from `CLAUDE.md` §1 Testing ("Writing a new `integration_test.go` today → STOP …").
4. Update the layout.md §2.1 row for `integration_test.go` from "**paused** today" back to "optional per [testing.md §1](testing.md) trigger".

§1.1, §1.2, §2 (filename), §3 (build tag) sections stay as-is — they are the template the pause is preserving.

### 1.1 Coverage gate — ≥85% per file, with explicit exclusions

`make coverage-gate` enforces ≥85% per Go file. The following files are **legitimately excluded** because they exist to wire/bootstrap external systems and are exercised by `make test-integration` or by running the binary, not by unit tests:

```
cmd/server/main.go                           — process entry point
internal/app/app.go                          — manual DI graph
internal/features/*/indexes.go               — IndexEnsurer (integration: real Mongo)
internal/features/*/repository_mongo.go      — driver-shaped queries (integration: real Mongo)
internal/features/*/repository_valkey.go     — go-redis client wrapper (integration: real Valkey)
internal/features/*/consumer_kafka.go        — kafka-go Reader loop (integration: real Kafka)
internal/infra/mongox/{client,crud,index}.go — mongo-driver Connect + generic CRUD + IndexSpec.CreateMany
internal/infra/otelx/{setup,span}.go         — OTel SDK assembly
internal/infra/valkey/client.go              — go-redis client wrapper
internal/infra/kafka/{producer,consumer}.go  — segmentio/kafka-go Reader/Writer wrappers; pure logic (JSONHandler) is in jsonhandler.go at 100%
internal/platform/validate/validate.go       — validator/v10 singleton bootstrap
```

> **Worth revisiting:** `kafka/consumer.go` has real retry/dispatch logic (`processWithRetry`, `convertMessage`) that *could* be unit-tested if we extracted a `messageReader` interface around `kafka-go`'s concrete `Reader`. Filed as future work, not blocking the gate.

### 1.2 Integration coverage gate — `make coverage-integration-gate`

`make coverage-gate` runs without infra and only enforces unit-testable files. Files in the §1.1 exclusion list still need eyes on coverage, just from a different gate:

`make coverage-integration-gate` runs `go test -tags=integration` against the compose-up'd stack and enforces ≥30% per file on the subset that integration tests exercise today (lower than `coverage-gate`'s 85% — see "why 30% not 85%" below). The positive-match list is maintained in the Makefile target; add a feature's `repository_mongo.go` / `repository_valkey.go` / `consumer_kafka.go` to it when its integration tests land.

Prereqs: `make compose-up` first, and `SVC_MONGO_URI` + `SVC_VALKEY_ADDR` set (mise.local.toml usually handles this).

**Why 30% not 85% for the integration gate.** Integration coverage is bounded by which paths the integration tests actually exercise, not by what unit tests can mock. Several files (e.g. `mongox/client.go` has 5-attempt ping retry + EnsureIndexes + Close branches) get only their happy-path exercised today. The 30% threshold says "tests genuinely hit the real wire (Mongo / Valkey / Kafka)" — a regression below that means the wire layer didn't run at all, which IS a real signal. Tighten the threshold as integration tests grow to cover more paths.

`mongox/crud.go` is dropped from the positive-match list — it holds generic helpers (`FindByID`, `UpsertByID`, …) where only some are currently used by features. Add it back to the gate when every helper has a real feature-side consumer.

Adding a new integration test for a file moves it from the "bootstrap exclusion" list in `coverage-gate` into the positive-match list of `coverage-integration-gate`. A file should appear in ONE of those, never both.

**Admission rule for exclusion:** the file is either (a) a thin wrapper over an external driver where unit tests would assert "the wrapper returns what the driver returns" (coverage theater), OR (b) imperative bootstrap that has no testable branches. Anything with business logic, error mapping, or behavioral branches does NOT qualify — write the tests.

**Adding to the exclusion list** requires a one-line justification in `Makefile` next to the grep pattern. Reviewers should push back if a file with branches lands in the list.

## 2. Filename — truth in naming

The filename must match what's inside. No broader, no narrower.

| Layout | Filename | When |
|---|---|---|
| One file covers many surfaces wired end-to-end | `integration_test.go` | Small feature, single setup, scope is "the whole package against real infra" |
| One file per surface | `service_integration_test.go`, `repository_integration_test.go`, `cache_integration_test.go`, `handler_integration_test.go` | Each file targets exactly one surface; no shared fixtures across files |

Mirrors the unit-test convention (`service_test.go` next to `service.go`).

## 3. Build tags

Mark integration tests with `//go:build integration`:

```go
//go:build integration

package <feature>
```

`make test` (no tag) skips them — keeps the unit loop fast. `make test-integration` (sets `-tags=integration`) runs them.

For a second axis like speed, **use a second build tag, not a filename**:

```go
//go:build integration && slow
```

Run: `go test -tags 'integration,slow'`. Skip slow: `go test -tags 'integration,!slow'`. Tags compose; filenames don't.

## 4. Hand-written mocks — codegen last resort

When a unit test needs to stand in for an external collaborator, **write the mock by hand**. Don't generate via `mockery`, `gomock`, or `moq`.

> **Terminology note.** Strict xUnit taxonomy splits "mock" (verifies interactions), "fake" (working in-memory impl), "stub" (canned returns) and "spy" (records calls). This codebase uses **"mock" as the umbrella term** for any hand-written test double — naming structs `mockRepo`, `mockCache`, `mockPinger`, etc. Pick this term consistently; do not mix `fakeX` and `mockX` in the same repo.
>
> **Receiver name.** Every `mock*` double uses receiver **`m`** — `func (m *mockRepo) FindByID(...)`, `func (m *mockCache) Get(...)`, `func (m *mockOtpServer) RequestOtp(...)`. NOT a role-letter (`r`/`c`/`s`/`p`) and NOT a leftover initial (`f` from a former `fakeX`). Why uniform `m`: role-letters collide — a package with both `mockCache` and `mockChallenges` would want `c` for both — and `m` (first letter of `mock`) is unambiguous and Go-idiomatic ("receiver = abbreviation of the type name"). The receiver is method-scoped, so every mock using `m` never clashes; in test bodies the doubles are still named by role (`repo`, `cache`).

### Why hand-written wins for this codebase

1. Each one is typically <50 lines of plain Go. Reads like real code.
2. Captures exactly the assertions THIS test cares about — e.g. a `lastPatchVer int` field the test reads, no `gomock.Any()` ceremony.
3. No external tool to pin, no `make generate` step, no `_mock.go` clutter.
4. Interface drift is a 30-second edit (rename a method, fix the mock).

Reference: a feature's `handler_test.go::mock<Feature>Service` — a struct with the methods of `<feature>Service` plus a few fields capturing call args.

### When codegen wins — switch for THAT interface

- **N consumers, each needing different behavior per test** — N hand-written variants becomes combinatorial.
- **Call-count or ordering assertions** — `.Times(N)`, `gomock.InOrder(...)`. Hand-written can use counter fields but gets verbose past 2–3.
- The hand-written mock has started **re-implementing actual business logic** — at that point it's a parallel impl, not a test double.

Today (2 features, small interfaces): hand-written. If a trigger fires later, switch for that interface only — not repo-wide.

## 5. Clock injection — in-package field write

Service holds `now func() time.Time`. Tests reach the field directly (allowed because they're in the same package — `package <feature>`):

```go
func newSvc(now time.Time) (*Service, *mockRepo, *mockCache, *mockPublisher) {
    repo, cache, pub := newMockRepo(), newMockCache(), &mockPublisher{}
    svc := New(repo, cache, pub)
    svc.now = frozenTime(now) // in-package — no exported clock seam
    return svc, repo, cache, pub
}

func frozenTime(t time.Time) func() time.Time { return func() time.Time { return t } }
```

Don't expose `WithClock(fn)` or a separate `NewWithDeps`. One constructor, one private field, in-package write.

## 6. What to mock vs. what to use as-is

A unit test substitutes ONLY the consumer-side interfaces of the layer under test. Not transitive deps. Not pure helpers. Not `stdx`.

| Layer under test | Substitute these | Use as-is (do NOT mock) |
|---|---|---|
| `Handler` | the feature's `<feature>Service` interface declared in `handler.go` | repo, cache, publisher, other-feature clients, `stdx/*`, `logging`, `apperr` |
| `Service` | `Repo`, `Cache`, `EventPublisher`, other-feature consumer-side interfaces declared in `service.go` | `stdx/ptr`, `stdx/retry`, `stdx/safego`, `stdx/timex`, `logging`, `apperr`, `pagination` |
| `Repo` | (rare — integration test against real Mongo is the right tier; see §1) | `mongox` helpers, `bson` types, `stdx/*` |
| `stdx/<x>` | nothing — `stdx` is leaf code | tested with table tests against real inputs |

### Why `stdx` never gets mocked

1. **No I/O.** Pure-Go, no network, no disk, no clock-of-its-own. There's nothing to mock — substituting `ptr.Of(x)` or `timex.StartOfDay(t)` adds indirection with zero confidence gain.
2. **Stdlib-shaped.** Same reason you don't mock `strings.Split` or `time.Time`. `stdx` packages extend stdlib idioms ([layout.md](layout.md) §1) — treat them with the same trust.
3. **Determinism is injected at the Service boundary, not at stdx.** If a test needs a frozen clock, the pattern is §5 — `Service.now func() time.Time` — NOT mocking `stdx/timex`. `timex` itself stays a thin wrapper.
4. **`safego` is operationally invisible.** Test the code that *spawns* the goroutine, not `safego.Go`. The panic-recovery behavior is covered by `stdx/safego/safego_test.go`.
5. **`retry` is configured, not mocked.** Pass options (`retry.WithAttempts(1)`, `retry.WithBaseDelay(0)`) to make tests fast — see references below.

### Why `logging` / `apperr` / `pagination` don't get mocked either

- `logging` — tests use the real logger writing to `io.Discard` or a `bytes.Buffer`. Mocking slog is busywork. The `log(ctx)` helper in feature code stays untouched.
- `apperr` — typed errors, not behavior. `errors.Is(err, ErrConflict)` works against the real `apperr.AppError`.
- `pagination` — `Encode`/`Decode` are deterministic functions; pass real cursors.

### When the rule breaks

The only legitimate reason to mock something in this column is **it starts doing I/O**. Today nothing in `stdx/` does. If `stdx/<new>` ever wraps an HTTP client or filesystem call, it has stopped being `stdx` — it belongs in `infra/` ([layout.md](layout.md) §1 admission rules), and *then* you mock it at the consumer-side interface.

References:
- A feature's `handler_test.go::mock<Feature>Service` — handler test substitutes the IMMEDIATE consumer-side service interface; uses `stdx/ptr` directly.
- A feature's `service_test.go::mockRepo` / `mockCache` / `mockPublisher` / etc. — service test substitutes its dependency interfaces only; uses real `stdx/ptr`, real `apperr`, real `pagination`.
- For cross-feature dependencies, substitute the *consumer-side* client interface declared in the consumer's `service.go`, never the producer's `Service` or `Repo` directly — see [cross-feature.md](cross-feature.md).
- `internal/stdx/ptr/ptr_test.go`, `internal/stdx/timex/timex_test.go` — leaf packages have their own table tests, so downstream callers can trust them.

## 7. Fixture hygiene

The setup of a test IS part of the test. If setup silently breaks, every test using that setup passes-but-lies. These rules keep fixtures honest.

### 7.1 Setup calls must fail loudly — no `_, _ = svc.Op(...)`

Wrong:

```go
_, _ = svc.Create(ctx, CreateInput{Key: "k", Owner: "u", Value: "v"})
// ... test continues, assuming "k" exists
```

If `Create` ever regresses, the test continues into a corrupted state and reports a misleading failure downstream — or worse, passes.

Right — use a `t.Helper()`-marked seed function that `t.Fatalf`s on error:

```go
func seedDraft(t *testing.T, svc *Service, key, value string) Item {
    t.Helper()
    it, err := svc.Create(context.Background(), CreateInput{Key: key, Owner: "u", Value: value})
    if err != nil {
        t.Fatalf("seed Create(%q): %v", key, err)
    }
    return it
}

// in tests:
seedDraft(t, svc, "k", "v")
```

`t.Helper()` makes Fatalf point at the test's line, not the helper's. The seed call site stays one line. A future Create regression shouts.

Multi-step seed (e.g. Create→Publish in TestPublish_AlreadyPublishedRejected) — inline the second step with an explicit error check, OR add a second helper if it appears 2+ times:

```go
seedDraft(t, svc, "k", "v")
if _, err := svc.Publish(ctx, "k", 1); err != nil {
    t.Fatalf("seed Publish: %v", err)
}
```

Pattern reference: a feature's `service_test.go::seed<DomainType>` helper.

### 7.2 Counter / spy fields: assert or delete

A counter field on a mock (`insertCalls`, `findCalls`, `setCalls`, …) is a **claim** that the test is going to assert on call patterns. If no test does, it's dead weight — and worse, a latent missed assertion. Common missed assertions:

| Mock field | Assertion that often matters |
|---|---|
| `repo.findCalls` | "cache HIT skipped the repo" — `findCalls == 1` after two Gets proves caching works, not just that the second Get returned the right value. |
| `cache.setCalls` | "PUBLISHED is cached" / "DRAFT bypasses cache" — already asserted in `TestGet_*`. |
| `publisher.calls` | "event fired exactly once on transition" — already asserted in `TestPublish_*`. |

Rule: if you add a counter, write the assertion in the same commit. If you find one with no assertion, delete it or add the assertion — don't leave it floating.

### 7.3 Avoid clever code in tests

Tests are documentation. Obscurity kills their job:

```go
// ✗ Clever:
func stringID(i int) string {
    return "k" + strings.Repeat("0", 0) + string(rune('0'+i))
}

// ✓ Plain:
key := fmt.Sprintf("k%d", i)
```

`strings.Repeat("0", 0)` is always `""` — every reader has to verify that. `string(rune('0'+i))` only works for `i ∈ [0, 9]`. The plain form is shorter AND correct for any `i`.

Same rule applies to test helpers, table-driven case names, and assertion messages. If a reader has to pause to decode it, replace it.

### 7.4 `t.Fatalf` vs `t.Errorf`

- `t.Errorf` — independent assertion. Reports the problem and keeps going so the rest of the assertions run. Use for "check these N independent facts about the result."
- `t.Fatalf` — gating assertion. Use when a subsequent line dereferences the result, performs a follow-up action that requires success, or would panic on a zero value.

```go
it, err := svc.Get(ctx, "k")
if err != nil {
    t.Fatalf("Get: %v", err) // can't continue: `it` is zero
}
if it.Version != 2 { t.Errorf("version = %d, want 2", it.Version) } // independent
if it.Status != StatusPublished { t.Errorf("status = %v", it.Status) } // independent
```

Setup helpers (§7.1) always use Fatalf — there's nothing left to assert if setup failed.

### 7.5 Helper for the alternate constructor mode

If the production code accepts nil for an optional dep (e.g. `EventPublisher` nil = "Kafka not configured"), build a parallel helper alongside `newSvc`:

```go
func newSvc(now time.Time) (*Service, *mockRepo, *mockCache, *mockPublisher) { ... }
func newSvcNoPublisher(now time.Time) (*Service, *mockRepo, *mockCache) { ... }
```

Don't open-code `svc := New(repo, cache, nil); svc.now = ...` in three different tests. After the second test that needs the variant, write the helper.

## 8. Goroutine-leak detection — `main_test.go` + goleak

[concurrency.md §6](concurrency.md) is the hard rule: a test that spawns a goroutine MUST assert it dies. The mechanism is `go.uber.org/goleak`, and the file slot for it is a **per-package `main_test.go`**.

### When to create `main_test.go`

Create one in a package **the first time any `_test.go` in that package spawns a goroutine** — directly (`go func()`, `safego.Go`) or transitively (a `grpc.NewServer()` served in a goroutine, a `time.AfterFunc`, an in-memory broker, a worker the test starts). One `main_test.go` covers the whole package, so you add it once per package, not per test.

Do NOT add it to a package whose tests spawn nothing — an empty `goleak.VerifyTestMain` is noise, and a package can only have one `TestMain`.

```go
// internal/<pkg>/main_test.go
package <pkg>   // or <pkg>_test if the package's tests use the external test package

import (
    "testing"

    "go.uber.org/goleak"
)

// TestMain asserts no goroutine outlives the package's tests (concurrency.md §6).
func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```

- Match the `package` clause to what the other `_test.go` files in the directory use — internal (`package foo`) or external (`package foo_test`). A mismatch is a compile error (`found packages foo and foo_test`).
- `VerifyTestMain` runs the check ONCE after all the package's tests finish — cheaper and less flaky than per-test. Use `defer goleak.VerifyNone(t)` only when one specific test needs isolation (e.g. it's the sole goroutine-spawner and you want a precise attribution).
- If a third-party driver leaks a goroutine you can't close (some gRPC / SDK internals), add a scoped `goleak.IgnoreTopFunction("pkg.func")` option **with a one-line comment naming why** — never a blanket ignore. Today no package needs one: bufconn gRPC servers clean up via `srv.Stop()` + `lis.Close()` in `t.Cleanup`, and that's enough.

### Why this isn't optional

A leaked goroutine is silent — the test passes, the binary builds, and the process bleeds memory/FDs in prod until it OOMs (concurrency.md §6). goleak turns that into a CI failure at the moment the leak is introduced, which is the only cheap time to fix it.

## STOP rules

| Symptom | Why |
|---|---|
| Writing a new `integration_test.go` today | Integration tests are SKIPPED per the §1 banner. The template stays preserved for future re-enable; don't reintroduce per-feature. |
| `service_integration_test.go` that actually drives Service + Repo + Cache + Handler end-to-end | Lying filename. Rename to `integration_test.go` OR split so each `<role>_integration_test.go` only touches one surface. |
| `xxx_unit_test.go` / `service_unit_test.go` | The `_unit_` qualifier is redundant — plain `_test.go` already means unit. |
| `tests/` or `test/` directory separate from the package | Go runs tests from the same package dir (or `<pkg>_test` sibling). Separate `tests/` is a Java/Python habit and breaks `go test ./...` discovery + coverage attribution. |
| Splitting integration tests by speed via filenames (`fast_*` / `slow_*`) | Use a second build tag. Tags compose; filenames don't. |
| `time.Sleep(N * time.Second)` in tests | Use `require.Eventually`, channels, or testcontainer healthchecks — see [concurrency.md](concurrency.md). |
| Introducing `mockery` / `gomock` / `moq` without one of §4's triggers met | Hand-written is the convention. Switch only when the criteria fire. |
| Substituting a transitive dep (handler test mocks Repo instead of Service) | Mock at the IMMEDIATE consumer-side interface only — §6. Reaching past Service into Repo means the handler test now owns Service's responsibilities too. |
| Mocking `stdx/*`, `logging`, `apperr`, `pagination`, or other pure-Go helpers | They have no I/O — there's nothing to substitute. Use them directly. Inject determinism at the Service boundary (e.g. `now func() time.Time` per §5), not by mocking the helper. |
| Naming a hand-written test double `fakeX` / `stubX` / `spyX` | Project convention is `mockX` for any hand-written double, regardless of strict xUnit role — §4 terminology note. Pick `mockX` uniformly; don't mix terms in the same repo. |
| Mock receiver named `r` / `c` / `s` / `p` / `f` (role-letter or `fakeX` leftover) | Every `mock*` uses receiver `m` — §4 receiver note. Role-letters collide (`mockCache` + `mockChallenges` both want `c`); `m` is uniform and unambiguous. |
| A `_test.go` spawns a goroutine but the package has no `main_test.go` with `goleak.VerifyTestMain` | The leak slips past CI silently — §8 + [concurrency.md §6](concurrency.md). Add the per-package `main_test.go`. |
| Blanket `goleak.IgnoreTopFunction` / disabling goleak to make a test pass | A real leak is the likely cause — fix the cleanup (`Close`/`cancel`/`Stop` in `t.Cleanup`). Scoped ignores need a one-line justification — §8. |

## References

- [layout.md](layout.md) §2.1 — `service_test.go` / `handler_test.go` / `integration_test.go` file slots.
- [concurrency.md](concurrency.md) §6 — goroutine-leak rules; `main_test.go` + goleak is the test-side enforcement (§8).
- `internal/stdx/retry/main_test.go`, `internal/infra/auth/main_test.go` — canonical `main_test.go` slots (internal-package and external `_test`-package forms).
- `internal/infra/mongox/index_integration_test.go` — platform-level integration test (template for infra-side integration tests).
