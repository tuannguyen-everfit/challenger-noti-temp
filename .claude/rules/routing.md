# Routing — API versioning, edge throttle, pool tuning

Read this whenever you add a new endpoint, change the throttle config, or consider tuning a downstream pool.

## 1. `/api/v1` lives in ONE place

`internal/platform/httpx/router.go`:

```go
const apiVersionPrefix = "/api/v1"

r.Route(apiVersionPrefix, func(r chi.Router) {
    r.Use(middleware.Throttle(...))
    r.Use(middleware.Idempotency(...))
    r.Mount("/items", deps.Items.Routes())
    r.Mount("/watchlist", deps.Watchlist.Routes())
})
```

Feature `Routes()` return a sub-router rooted at `/` — they don't know about the version prefix. That's the seam that makes version bumps cheap.

**Bumping to v2:** add a second `r.Route("/api/v2", func(r chi.Router) { ... })` block alongside v1. Features mount in the new group when they have a v2 handler; old ones stay on v1. A feature can live in BOTH groups during deprecation — `Routes()` gets called twice.

**Adding a new endpoint:** route inside the feature's `Routes()` (relative paths only). Mount the feature inside the versioned group in `router.go`. Throttle + Idempotency are inherited.

**Health endpoints (`/healthcheck`, `/liveness`, `/readiness`) sit OUTSIDE the versioned group** — kubelet doesn't know about API versions; a probe that 404s during a v1→v2 cutover would kill every pod.

## 2. Edge throttle

`internal/platform/httpx/middleware/throttle.go` caps concurrent in-flight requests through the versioned group. Overflow goes to a bounded backlog; requests waiting longer than `BacklogTimeout` return `503 SERVICE_BUSY` + `Retry-After`. Health endpoints are never throttled (§1).

| Knob | Env var | Default |
|---|---|---|
| Max | `SVC_HTTP_THROTTLE_MAX` | 40 |
| Backlog | `SVC_HTTP_THROTTLE_BACKLOG` | 80 |
| BacklogTimeout | `SVC_HTTP_THROTTLE_BACKLOG_TIMEOUT` | 2s |

Sizing rule: `Backlog ≈ 2 × Max` — rides out transient bursts, fails fast under sustained overload.

**The math:**

```
HTTPThrottleMax ≤ min(MongoPool, ValkeyPool, KafkaPublishConcur)
```

With driver defaults today (Mongo=100, Valkey=10×GOMAXPROCS, Kafka=unbounded), throttle Max=40 sits well below all three so the edge is the binding constraint. Raise Max only when p99 backlog wait is consistently high AND downstream pools have headroom.

**Why custom middleware over chi's `ThrottleBacklog`:** chi's emits plain-text 429/503, breaking the `{code, message, details}` envelope ([http.md](http.md) §1). Same algorithm, service-shaped surface.

## 3. Pool tuning — deferred until data exists

Driver defaults are used today. Add a `MaxPoolSize` / `PoolSize` knob ONLY when you have:

1. **Driver-level wait-time metric** trending toward the request timeout, OR
2. **A reproducible load test** showing pool saturation at the throttle ceiling, OR
3. **An incident postmortem** where the missing knob was the root cause.

"It seems like a good idea" is not a trigger. Driver defaults exist because the driver authors tested them; overriding without data means tuning blind.

When you DO tune: add the field to `config.<X>Config`, `v.SetDefault(...)` matching the driver default, wire it into the constructor, land on its own PR with the metric/incident citation.

## STOP rules

| Symptom | Why |
|---|---|
| Feature `Routes()` returning `r.Get("/api/v1/...", ...)` | Prefix is owned by `router.go`; hardcoding breaks v2 cutover and middleware scoping (§1). |
| Hardcoding `"/api/v1/"` outside `router.go` | Use the `apiVersionPrefix` constant. |
| New unversioned public endpoint (other than the three health probes) | Mount under the versioned group. |
| Wrapping health endpoints in `middleware.Throttle` | Kubelet sees 503 under load and kills the pod. |
| `HTTPThrottleMax = 0` in prod | Disables the edge — overload surfaces as Mongo/Valkey starvation with 504/30s timeouts instead of fast 503s. |
| Raising `HTTPThrottleMax` without raising the smallest downstream pool | Moves the bottleneck downstream where backpressure surfaces as latency. |
| Adding `MaxPoolSize` / `PoolSize` without a §3 trigger | Premature tuning. |

## References

- `internal/platform/httpx/router.go` — versioned API group + `apiVersionPrefix` + middleware ordering.
- `internal/platform/httpx/middleware/throttle.go` — edge throttle.
- `internal/platform/httpx/middleware/throttle_test.go` — concurrency, backlog, timeout, client-cancel cases.
- `internal/platform/config/config.go::ThrottleConfig` — knobs.
- `internal/platform/localization/code.go::CodeServiceBusy` — 503 envelope code.
- [http.md](http.md) — error envelope, DTOs, handler shape (the inner layer below routing).
- [concurrency.md](concurrency.md) — goroutine ownership; the throttle bounds inbound concurrency before it spawns N handler goroutines.