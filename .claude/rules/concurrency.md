# Concurrency — goroutines, locks, channels

Read this whenever you write `go`, `sync.*`, `chan`, or coordinate work across goroutines.

## 1. `safego` — every goroutine must be panic-safe

Unwrapped `go func() { ... }()` is a hard STOP. A panic in any goroutine crashes the **whole** process by default. Always wrap.

Two flavors:

```go
import "github.com/Everfit-io/go-service-template/internal/stdx/safego"

// Fire-and-forget (no caller waiting for an error)
safego.Go(func() {
    doBackgroundWork(ctx)
})

// In an errgroup (caller wants the error)
g, ctx := errgroup.WithContext(ctx)
g.Go(safego.WrapErr(func() error { return doRiskyWork(ctx) }))
if err := g.Wait(); err != nil { ... }
```

- `safego.Go` recovers the panic and logs it via the default slog logger.
- `safego.WrapErr` converts the panic into a returned error containing the value + stack — designed for `errgroup`.

## 2. Liveness / readiness flags — `atomic.Bool`, not Mutex

The shutdown flag on `health.Handler` uses `atomic.Bool`:

```go
type Handler struct {
    shuttingDown atomic.Bool
}

func (h *Handler) Shutdown() { h.shuttingDown.Store(true) }

func (h *Handler) Livez(w http.ResponseWriter, r *http.Request) {
    if h.shuttingDown.Load() {
        w.WriteHeader(http.StatusServiceUnavailable)
        return
    }
    // ...
}
```

**Do NOT use `sync.Mutex.TryLock` for state flags.** It caused a sporadic-503 bug in the past — the lock was held by a different code path and the liveness probe got a false 503. `atomic.Bool` has no such failure mode.

## 3. One-of-many errors → `errgroup`

When fanning out work and the FIRST error should cancel the others:

```go
g, ctx := errgroup.WithContext(ctx)
for _, w := range work {
    w := w
    g.Go(safego.WrapErr(func() error { return process(ctx, w) }))
}
return g.Wait()
```

`errgroup.WithContext` cancels `ctx` on the first non-nil error — sibling goroutines see `ctx.Done()` and bail. The returned error is the first one.

**Don't use `chan error` directly for this pattern** — error reporting + cancellation + Wait semantics are exactly what `errgroup` solves.

## 4. Background-worker lifecycle (Kafka consumer pattern)

```go
var (
    watchlistConsumer *watchlist.KafkaConsumer
    cancelConsumer    context.CancelFunc = func() {} // no-op default
)
if producer != nil {
    // ... build wlReader ...
    watchlistConsumer = watchlist.NewKafkaConsumer(wlReader, watchlistSvc.OnItemPublished, log)
    var consumerCtx context.Context
    consumerCtx, cancelConsumer = context.WithCancel(ctx)
    defer cancelConsumer()
    safego.Go(func() {
        if err := watchlistConsumer.Run(consumerCtx); err != nil {
            log.Error("consumer exited", slog.String("error", err.Error()))
        }
    })
}
```

Key points:
- `cancelConsumer` is **deferred immediately after creation** (vet-clean: no lost cancel on early-return paths).
- `safego.Go` wraps the goroutine — panic doesn't crash the process.
- On shutdown, the explicit `cancelConsumer()` + `watchlistConsumer.Close()` happens in the shutdown sequence — see `internal/app/app.go`.

## 5. Shutdown registry — every long-lived resource closes in `app.go::shutdown`

The shutdown sequence in `internal/app/app.go::shutdown` is the **canonical registry** of resources the service owns. Anything long-lived MUST appear there, in the right order, with the right budget. Adding a new feature that introduces a long-lived resource and forgetting this row is a hard STOP.

### Current ordering (do NOT reshuffle without a reason)

```
1. healthHandler.Shutdown()   — flip liveness flag first; k8s pulls pod from Service
2. HTTP server drain          — capped at 80% of ShutdownTimeout (httpCtx)
3. Kafka consumer cancel+Close — stop reading before closing the conn
4. Kafka producer Close       — flush in-flight produces
5. OTel exporter shutdown     — flush spans before the pod dies
6. Valkey Close               — cache is non-durable, lose it after producers
7. Mongo Close                — DB last; readers above might still be finishing
```

The order matters: stop accepting input → stop emitting output → flush observability → drop caches → close storage. New rows slot in by category, not at the end.

### Budgeting

- One overall `shutCtx` with `cfg.ShutdownTimeout`.
- HTTP drain gets a sub-context capped at **80% of total** (`timeout*4/5`) so a slow client can't exhaust the budget before the tail runs.
- OTel and Mongo are the only tail steps that take a ctx — they need real time left, hence the 20% reservation. The rest (`Close()` without ctx) are fast.

### Adding a long-lived resource — the checklist

When a feature adds **any** of:

- a `safego.Go(...)` background goroutine in `app.go`
- a `*grpc.ClientConn` from `grpcclient.Dial` (must `defer conn.Close()` AND a shutdown row)
- a new external client (extra Kafka producer, second Valkey instance, S3 client, etc.)
- a worker pool / job queue / long-poll loop

you MUST:

1. Add a parameter to `shutdown(...)` for the closer (or cancel fn).
2. Add a numbered row to the sequence in the right slot.
3. Update the `Run(...)` call site that invokes `shutdown(...)`.
4. If the closer takes a `ctx`, pass `shutCtx` and confirm the 20% tail reservation is still enough — if not, raise `cfg.ShutdownTimeout` rather than shrinking the HTTP drain.
5. If there's a sibling cancel (`context.CancelFunc`), `defer cancel()` immediately at creation site so early-return paths don't leak.

Reference: the watchlist consumer (`cancelConsumer` + `Close`) is the canonical pattern for the consumer pair. The HTTP server is the canonical pattern for the budget-capped drain.

### STOP rules (added)

- ✗ A new `safego.Go(...)` in `app.go` without a matching row in `shutdown(...)`. Background goroutines without a stop signal leak past the deadline.
- ✗ A `grpcclient.Dial(...)` without `conn.Close()` in `shutdown(...)`. gRPC conns hold network state and goroutines.
- ✗ Letting HTTP drain use the full `shutCtx`. The 80/20 split exists because OTel/Mongo need real time at the tail — don't merge them back.

## 6. Orphan / leaked goroutines — HARD STOP, NEVER accepted

A leaked goroutine is one that outlives the scope it was started in. **AI assistants and reviewers must actively look for this pattern and reject it** — leaks are silent: the test passes, the binary builds, and the process just slowly bleeds memory + file descriptors until it OOMs or the connection pool exhausts.

### What counts as a leak

Any goroutine that, at the moment the function that spawned it returns, is still running and CANNOT be stopped by the caller. Concretely:

| Smell | Why it leaks |
|---|---|
| `go func() { for { select { case <-ch: ... } } }()` with no `<-ctx.Done()` or quit case | Nothing can stop it — it runs until the process dies. |
| Holding `ctx context.Background()` instead of a cancellable parent | Never cancelled → never returns. |
| `time.NewTicker(d)` without `defer t.Stop()` | The timer goroutine + channel sits forever. |
| Subscriber registered with no `Unsubscribe` / `Close` path | Holds a reference; producer keeps sending; goroutine never exits. |
| Buffered channel where the SENDER goroutine blocks on a full channel and the receiver was already cancelled | Sender is parked forever. |
| `go func() { conn.Read(...) }()` with no `conn.SetReadDeadline` and no Close from the outside | Blocked syscall — cancellable only by closing the conn. |
| Background goroutine started inside a **request handler** | Request returns; goroutine outlives request scope; no one closes it. **Always route through `app.go`** (§5). |
| `errgroup.Go` siblings that don't observe `ctx.Done()` | First error cancels ctx, but siblings ignore it → they finish their full work anyway. |
| `for range ch` where `ch` is never closed | Loop never exits. |
| Recursive `safego.Go` (a goroutine that spawns another, that spawns another) without a single root cancellation | Tree of goroutines, no way to kill the root. |

### Defenses (mandatory for code that spawns goroutines)

1. **Every goroutine accepts a cancellation signal.** Either: (a) a `context.Context` it observes via `<-ctx.Done()`, or (b) a quit channel closed by its owner, or (c) it's bounded — runs to completion in well under a second on its own.
2. **Every spawn site has a documented owner that calls the cancel.** For background workers, the owner is `app.go::shutdown` (§5). For request-scoped work, the owner is the handler — and the handler MUST wait for it before returning (or it's a leak).
3. **No goroutines from inside handlers without `errgroup.Wait()`** at the handler boundary. Fire-and-forget from a handler IS a leak by definition — the request returns, the goroutine outlives request scope, nothing cleans it up. If async work is needed, publish to Kafka (see [kafka.md](kafka.md)) or hand off to a registered worker.
4. **Tests assert no leak.** Use `go.uber.org/goleak` in any test that spawns a goroutine:

    ```go
    func TestMain(m *testing.M) {
        goleak.VerifyTestMain(m)
    }
    ```

   Or per-test: `defer goleak.VerifyNone(t)`. This catches leaks BEFORE they ship.
5. **Shutdown verifies count.** `runtime.NumGoroutine()` logged at start of `shutdown()` and again at end. A delta > expected (HTTP server + signal handler + a few stdlib bookkeepers) means a leak survived shutdown — fail loudly in non-prod, log warn in prod.

### AI reviewer checklist (apply on EVERY diff that introduces `go`, `safego.Go`, `errgroup.Go`)

Before approving the diff, the assistant MUST answer all five:

1. **Who cancels it?** Name the cancellation path. If "the process exits eventually" — that's a leak.
2. **Does it observe ctx.Done() (or equivalent)?** If not, why not? "It's fast" is not an answer — fast-by-design goroutines must still be bounded.
3. **Does the owner wait for it?** If the spawning function returns before the goroutine, name what guarantees cleanup. For `app.go`, this is the shutdown registry. For tests, this is `goleak`. For handlers — STOP, this is a leak.
4. **What happens on panic?** Must be `safego.Go` / `safego.WrapErr` (§1).
5. **What happens on context cancel during the goroutine's work?** The goroutine must return — not finish its full work after cancel.

If any answer is unclear or missing — **reject the change and ask for the cancellation path**.

### STOP rules (added)

- ✗ Spawning a goroutine inside an HTTP/gRPC handler. Handler-scope async work IS a leak. Publish a Kafka event or hand off to a registered worker.
- ✗ `go func() { ... }()` (or `safego.Go`) without a documented cancellation path. The reviewer must be able to name the owner.
- ✗ `context.Background()` inside a long-lived goroutine. Use the cancellable parent — even if just for shutdown.
- ✗ `time.NewTicker` / `time.NewTimer` without `defer t.Stop()`. The runtime goroutine + channel leak.
- ✗ Tests that spawn goroutines and don't use `goleak`. If you spawn it, you assert it dies.
- ✗ "It'll be cleaned up when the process exits" — not a cleanup strategy. The process is a goroutine's worst-case container, not its lifecycle manager.

### References

- `internal/app/app.go::shutdown` — every long-lived goroutine has a cancellation path here (§5).
- `internal/stdx/safego` — panic-safe wrappers; mandatory for `go`.
- Every feature's `Consumer.Run(ctx)` ([kafka.md](kafka.md) §3) — observes `ctx.Done()`, returns nil on cancel; the cancel happens in `app.go::shutdown`.

## 7. Tests that wait for state — `require.Eventually`, NOT sleep

```go
require.Eventually(t, func() bool { return svc.IsReady() }, 5*time.Second, 100*time.Millisecond)
```

**Never** `time.Sleep(N * time.Second)` in tests — it makes the suite slow and brittle.

## STOP rules

- ✗ Unwrapped `go func() { ... }()`. Wrap with `safego.Go` or `safego.WrapErr`.
- ✗ `sync.Mutex.TryLock` for liveness/readiness state. Use `atomic.Bool`.
- ✗ `chan error` for one-error-out-of-many. Use `errgroup.WithContext`.
- ✗ Sleep-based "wait until ready" in tests. Use `require.Eventually`, channels, or testcontainer healthchecks.
- ✗ Cancel-leak: creating a context with `WithCancel` and not deferring the cancel. Always `defer cancel()` immediately.
- ✗ Adding a long-lived resource (goroutine, gRPC conn, external client) without a matching row in `app.go::shutdown` — see §5.

## References

- `internal/stdx/safego/safego.go` — `Go`, `WrapErr`.
- `internal/platform/httpx/health/` — atomic.Bool for shutdown flag.
- `internal/app/app.go` — consumer-lifecycle wiring as a reference pattern.
