# Cross-feature communication — interface-first, narrow contracts

Read this whenever feature A needs to call feature B (sync or async).

## 1. Two valid paths

| Path | When | Mechanism |
|---|---|---|
| **SYNC** | Consumer needs fresh data at the moment of call | Consumer-side interface (`watchlist.itemsClient`) + production wires `*producer.Service` |
| **ASYNC** | Consumer reacts to a state change; can tolerate eventual delivery | Kafka — consumer subscribes to producer's `<Feature>Topic` constant (see [kafka.md](kafka.md)) |

**Producer never imports consumer.** Dependency direction is unidirectional: consumer → producer. CI can verify with `go list -deps ./internal/<producer>/...` — the consumer's path must NEVER appear.

## 2. Interface-first workflow (SYNC path)

Before writing the call site, declare the consumer-side interface listing ONLY the methods you'll actually call.

```go
// internal/features/<consumer>/service.go

// <producer>Client is the subset of <producer>.Service that <consumer> depends on.
// Lowercased because only <producer>.Service implements it.
type <producer>Client interface {
    Get(ctx context.Context, key string) (<producer>.Item, error)
}
```

Then `<consumer>.Service` accepts a `<producer>Client` (not `*<producer>.Service`):

```go
func New(p <producer>Client, repo Repo) *Service {
    return &Service{p: p, repo: repo, /* ... */}
}
```

### Narrow-subset rule

- ✓ Interface has the 1–3 methods the consumer actually calls.
- ✗ Interface mirrors the producer's full surface ("just in case").

**One method per real dependency.** If the consumer only needs `Get`, the interface has one method. Adding a method to a consumer-side interface is the trigger for "is this still the right consumer?"

Why narrow:
1. Tests stub one method, not five.
2. Producer changes to unrelated methods can't break this consumer at compile time.
3. Reviewers see at a glance what the consumer depends on.

## 3. Composition-root adapter (when signatures differ)

Sometimes the producer's method signature is wider than the consumer needs (e.g. an extra cache-hit `bool` that's a producer-internal concern). The consumer's interface returns only what the consumer cares about.

Bridge with a tiny adapter at `internal/app/app.go`:

```go
type <producer>ForConsumer struct{ s *<producer>.Service }

func (a <producer>ForConsumer) Get(ctx context.Context, key string) (<producer>.Item, error) {
    it, _, err := a.s.Get(ctx, key) // drop the extra return
    return it, err
}

// in Run():
consumerSvc := <consumer>.New(<producer>ForConsumer{producerSvc}, <consumer>.NewMongoRepository(mongoClient))
```

The interface stays narrow; the adapter does the type juggling at the composition root.

## 4. Same-package callers skip the interface

The interface rule applies to **cross-package** calls. Within ONE package, indirection is overhead without benefit.

| Situation | Right shape |
|---|---|
| `<feature>.Consumer` calls `<feature>.Service.OnEvent` | Pass the bound method value (`func(ctx, T) error`) directly to `NewKafkaConsumer` — no struct field, no one-method interface |
| `handler.go` calls its own `service.go` Service | Use a consumer-side `<feature>Service` interface (still in the same package) — this is the handler/service boundary, not cross-package coupling |

**"One method → function value":** when the consumer needs exactly one method, pass the bound method value (`func(ctx, T) error`) directly. The signature IS the contract — no struct field, no one-method interface.

## STOP rules

| Symptom | Why it's wrong |
|---|---|
| Feature A imports feature B's `Repo` (or `repository_mongo.go`) and calls it | **Hard STOP.** Repo is a low-level Mongo adapter — knows nothing about B's cache, business invariants, version checks, or Kafka emit. Skips ALL of that → silent bugs. Always call B's Service. |
| Feature A holds `*featureB.Service` as a concrete field (not interface) | Leaks B's full surface into A; A's tests pull in all of B; A re-couples to every B method. Define a consumer-side interface listing only the methods A uses. |
| Feature A's `handler.go` calls feature B's Service directly | Handlers call THEIR OWN feature's service (`h.svc`). Cross-feature dependencies live on the Service struct, not the handler. |
| Feature A redeclares a B domain type (e.g. `type Item struct{...}` in feature A when it already exists in B) | Import the type from B. Redeclaring creates two structs that drift independently. **Exception:** Kafka event payloads when A and B will eventually live in different repos — divergence is the explicit point. |
| Producer feature B imports consumer feature A | **Hard STOP — cycle risk.** Invert the dependency or extract the shared type into a third package both import. |
| Feature A subscribes to B's Kafka topic with an ad-hoc consumer-group name | Group MUST be `xx-<subscriber>-<event-name>` — see [kafka.md](kafka.md). |
| Two features write to the same Mongo collection | Each feature owns its collection. Shared state → put the state in a third feature. |
| Mixing communication modes for the same data flow | (a) Consumer needs *fresh* data at call-time → SYNC interface. (b) Consumer reacts to *state change*, can tolerate delay → ASYNC Kafka. Don't poll Kafka inside a request handler (RPC-over-Kafka). Don't synchronously call B from A inside B's own Kafka handler (you're now reactive — use the event payload). |
| Bypassing the composition-root adapter — A imports producer's wide-signature method and ignores extra returns | Use the adapter at `app.go`. Don't pollute the consumer-side interface with extra returns just to make the compiler happy. |

## References

- [layout.md](layout.md) §2 — feature scaffolding template (where consumer-side interfaces live, file slots).
- [kafka.md](kafka.md) §3 — the same-package function-value pattern (Consumer holds the bound method, not a struct field).
- `internal/app/app.go` — the composition root where cross-feature adapters live.
