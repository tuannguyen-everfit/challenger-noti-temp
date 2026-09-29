# Clean code — readability rules the linter can't catch

Read this whenever you write or review a function body. It covers the clean-code concerns that are **judgment calls**, not lint failures. The mechanical ones (`gocyclo` ≤ 15, `errorlint` `%w`, revive early-return) are already enforced by `.golangci.yml` — this file explains the WHY behind them and fills the gaps the linter leaves open.

> ⚠️ This is the LAST file to cite as a STOP trigger — naming ([naming.md](naming.md)), structure/comments ([layout.md](layout.md)), error envelope ([http.md](http.md)) come first. Clean-code rules apply to the *inside* of a function once those have placed it. No rule here overrides gofmt, gopls, or a passing `golangci-lint run`.

---

## 0. The split: enforced vs. judgment

| Concern | Status | Where |
|---|---|---|
| Cyclomatic complexity ≤ 15 | **lint-enforced** | `.golangci.yml` `gocyclo` |
| No `else` after a returning `if` | **lint-enforced** | revive `indent-error-flow`, `superfluous-else`, `if-return` |
| Wrap errors with `%w` | **lint-enforced** | `errorlint` |
| Unused params | **lint-enforced** | `unparam` |
| **Function length / does-one-thing** | judgment (§1) | this file |
| **Nesting depth ≤ 3** | judgment (§2) | this file |
| **Magic numbers / strings** | judgment (§3) | this file |
| **Single responsibility** | judgment (§4) | this file |
| **Error handling clarity** | partly enforced + judgment (§5) | this file + `errorlint` |
| **General smells** | judgment (§6) | this file |

If a rule here and a passing linter ever disagree, the linter wins — and update this file.

---

## 1. Function length — a smell, not a number

We do **not** enable `funlen`. There is no hard LoC limit. `gocyclo` ≤ 15 already catches branchy functions; a long *linear* function (40 sequential statements, complexity 1) passes lint and may be perfectly clear (a DTO mapper, a config builder).

The real test is **does it do one thing** (§4), not how many lines. But length is a useful *smell*:

- A function that scrolls off the screen AND mixes phases (validate → fetch → transform → persist → emit) → extract each phase into a `validate*` / `build*` / `collect*` helper ([naming.md](naming.md) §2).
- A function that's long but does ONE linear thing (field-by-field DTO copy) → leave it. Splitting it adds indirection without clarity.

STOP: don't extract a helper called once, used nowhere else, that only exists to shorten the parent. Inline is clearer until there's a second caller ([layout.md](layout.md) stdx admission rule — same principle).

---

## 2. Nesting depth — flatten past 3 levels

`nestif` is not enabled, so the linter won't reject deep nesting — but it's the #1 readability killer. Keep control-flow nesting **≤ 3 levels**. The tools to flatten (all already lint-enforced, so this is the idiom you're expected to reach for):

**Guard clauses / early return** — handle the exceptional case first, return, keep the happy path at the left margin:

```go
// ✗ arrow code — happy path buried 3 deep
func (s *Service) Finalize(ctx context.Context, id string) error {
    ch, err := s.repo.Get(ctx, id)
    if err == nil {
        if ch.Status == StatusActive {
            if ch.EndsAt.Before(s.now()) {
                // ... real work, indented 4x
            }
        }
    }
    return err
}

// ✓ guard clauses — work at the left margin
func (s *Service) Finalize(ctx context.Context, id string) error {
    ch, err := s.repo.Get(ctx, id)
    if err != nil {
        return err
    }
    if ch.Status != StatusActive {
        return ErrNotActive
    }
    if ch.EndsAt.After(s.now()) {
        return ErrNotEnded
    }
    // ... real work, indented once
}
```

revive's `indent-error-flow` + `superfluous-else` already reject the `else`-after-return form. This section is the positive pattern those linters push you toward.

Other flatteners: extract the inner block into a helper; replace a nested `if/else` ladder with a `switch`; invert the condition with `continue` inside a loop.

---

## 3. Magic numbers & strings — name the meaning

`mnd`/`gomnd` is not enabled. Bare literals whose meaning isn't self-evident are a STOP — name them as a constant near the use site ([naming.md](naming.md) §6 governs the naming).

```go
// ✗ what is 86400? what is "published"? why 3?
if time.Since(ch.CreatedAt) > 86400*time.Second { ... }
if status == "published" { ... }
peers := pickRandomPeers(members, 3)

// ✓ meaning is named
const challengeStaleAfter = 24 * time.Hour
if time.Since(ch.CreatedAt) > challengeStaleAfter { ... }

const StatusPublished Status = "published"   // enum const, not a bare string
if status == StatusPublished { ... }

const randomPeersPerRow = 3
peers := pickRandomPeers(members, randomPeersPerRow)
```

**Fine to leave bare:** `0`, `1`, `-1`, `""`, `2` in obvious idioms — loop starts, `len(x) == 0`, `x + 1`, `limit*2` backlog sizing where the relationship is the point. Don't constant-ify `i := 0`.

Status/type strings are **always** named constants (also a `bson`/wire contract — see [http.md](http.md) §7, [mongo.md](mongo.md) §1).

---

## 4. Single responsibility — one reason to change

A function does **one thing**; a type holds **one concept**. The Go test: can you name it with a single verb from the [naming.md](naming.md) §2 table without "and"? `validateAndBuildRows` is two things — split into `validateListLimit` (returns error) + `buildListRows` (returns data). `naming.md`'s STOP rule (`validate*` returns error, `build*` returns data) IS the SRP rule applied to helpers.

For types: layer types stay split by concern, not bundled ([layout.md](layout.md) §4 — one interface per concern: `Repo`, `Cache`, `EventPublisher`, never a fat `Storage`).

STOP: a Service method that both decides business state AND formats a wire response. The DTO seam (`to<Type>Response` in `handler.go`) is the boundary — Service returns domain, handler maps to wire ([http.md](http.md) §5).

---

## 5. Error handling clarity

`errorlint` enforces `%w` wrapping mechanics. The judgment parts:

- **Add context, don't restate.** `fmt.Errorf("finalize challenge %s: %w", id, err)` — say what you were doing, not "error occurred". The wrapped chain reads as a breadcrumb trail.
- **Never swallow.** `if err != nil { return ... }` or log-and-continue with a documented WHY ([logging.md](logging.md) — cache degradation is `Warn` + continue, and that's a deliberate decision, not a swallow). A bare `_ = doThing()` on a fallible call is a STOP unless the ignore is intentional and commented.
- **Don't lose the cause.** `errors.New("failed")` discarding `err` is a STOP — wrap it (`%w`) so `errors.Is`/`errors.As` works upstream.
- **Sentinels + table mapping, not string matching.** Domain errors are sentinels in `errors.go` mapped via the `apperr.Mapping` table ([http.md](http.md) §2). Never `strings.Contains(err.Error(), "duplicate")`.
- **No control flow via panic/recover.** `recover` lives only in `middleware.Recover` and `safego.WrapErr` ([concurrency.md](concurrency.md) §1). Everywhere else: return errors.

---

## 6. General smells — quick-scan

| Smell | Why | Fix |
|---|---|---|
| **Boolean / flag param** (`build(x, true, false)`) | call site is unreadable; the bool usually means the func does two things | split into two named funcs, or pass a named enum/option |
| **Primitive obsession** (`string` for an ID that's really an `ObjectID`, money as `float64`) | invariants unenforced, conversions scattered | a domain type; convert at the boundary ([mongo.md](mongo.md) §5) |
| **Returning bare `nil` slice vs empty** inconsistently | callers branch on the wrong thing | pick one (empty slice for "no results"); document on the method |
| **Stutter** (`item.ItemID`, `items.ItemService`) | noise; fights Go convention | drop the prefix ([naming.md](naming.md) §1, [layout.md](layout.md) §2.2) |
| **Speculative generality** (`Map[A,B]`, an interface with one impl "for testing") | YAGNI; abstraction with no second caller | inline; add the seam when the 2nd use lands |
| **Comment restating code** (`// increment i` above `i++`) | rots, adds noise | delete; comment only the non-obvious WHY ([layout.md](layout.md) §2.4) |
| **Long param list** (5+ positional args) | call site unreadable, easy to transpose | group into an `Input` struct ([naming.md](naming.md) §1) |
| **Deeply chained access** (`a.B().C().D.E`) without nil-safety | hidden panic surface | bind intermediates, guard, or expose a method that does the walk |

---

## STOP rules

- ✗ Extracting a single-call helper purely to shorten a parent function. Length alone isn't the trigger — mixing phases is (§1).
- ✗ Control-flow nesting past 3 levels. Flatten with guard clauses / extracted helper / `switch` (§2).
- ✗ Bare magic number or status string with non-obvious meaning. Name it (§3). Obvious `0`/`1`/`""` idioms are fine.
- ✗ A function/type that does two things — `validateAndBuild*`, a Service method that also formats wire output (§4).
- ✗ `errors.New("...")` that drops the underlying cause, or `_ =` swallowing a fallible call without a commented reason (§5).
- ✗ Boolean flag parameter that selects between two behaviors — split the function (§6).
- ✗ Adding a "for testing" interface/abstraction with one production impl before a second caller exists (§6, YAGNI).
- ✗ Re-deriving a rule from another `.claude/rules/` file here. Cross-link instead — this file owns only the inside-the-function judgment calls.

## References

- `.golangci.yml` — the enforced half: `gocyclo` (15), `errorlint`, revive `indent-error-flow`/`superfluous-else`/`if-return`, `unparam`, `prealloc`, `gocritic`.
- [naming.md](naming.md) — verb prefixes, constant naming, generic-suffix STOP list (governs §1, §3, §4).
- [layout.md](layout.md) §2.4 (comment policy), §2.6 (file-split-by-LoC), §4 (one interface per concern).
- [http.md](http.md) §2 (error mapping table), §5 (DTO seam).
- [concurrency.md](concurrency.md) §1 (`recover` only in two places).
- [Pungyeon/clean-go-article](https://github.com/Pungyeon/clean-go-article) — canonical Go clean-code reference (external).
