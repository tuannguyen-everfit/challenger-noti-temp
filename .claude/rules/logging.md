# Logging — per-module log.go, slog conventions

Read this whenever you write a `log.Info / Warn / Error` line inside feature code.

## 1. Per-feature `log.go` (mandatory)

Every feature package owns a `log.go`:

```go
// internal/features/<feature>/log.go
package <feature>

import "github.com/Everfit-io/go-service-template/internal/platform/logging"

var log = logging.Module("feature.<feature>")
```

`logging.Module(name)` is a factory — it pre-builds the `slog.Attr` once at init and returns a `func(ctx) *slog.Logger` that adds the module tag on top of the request-scoped logger from context.

**Naming convention: `<bucket>.<package>`.** Mirrors the directory layout so a single field tells you which folder the log line came from:

| Package | `module` value |
|---|---|
| `internal/features/<feature>/` | `feature.<feature>` |
| `internal/infra/mongox/` | `infra.mongox` |
| `internal/platform/httpx/respond/` | `platform.httpx.respond` |
| `internal/app/` | `app` |

Singular `feature` (not `features`) — reads better in filters and matches the bucket-as-namespace pattern.

Inside the package, **always call `log(ctx).Info(...)`** instead of `slog.Default()` or bare `logging.FromContext(ctx)`. Result: every log line carries:

| Field | Source |
|---|---|
| `module` | this helper |
| `request_id` | RequestID middleware |
| `trace_id`, `span_id` | OTel SDK (auto-injected by `logging.otelHandler` from `trace.SpanFromContext`); present whenever an OTel span is active in ctx — always for inbound HTTP requests via otelhttp |
| `service.name`, `service.version`, `deployment.environment` | `logging.New` resource attrs |

Operators filter by module for per-feature cost, error-budget, and debug:

```sh
make compose-logs | jq -c 'select(.module=="feature.<feature>")'
make compose-logs | jq -r '.module // "platform"' | sort | uniq -c | sort -rn
```

## 2. What's in every line (the OTel-aligned shape)

```json
{
  "time": "2026-05-15T08:01:12.001Z",
  "severity_text": "WARN",
  "severity_number": 13,
  "level": "warn",
  "msg": "cache set failed",
  "service.name": "go-service-template",
  "service.version": "0.1.0",
  "deployment.environment": "local",
  "request_id": "4b9c8d3a-...",
  "trace_id": "0af76519...",
  "span_id": "b7ad6b71...",
  "module": "feature.<feature>",
  "key": "k42",
  "error": "valkey: connection reset"
}
```

Field naming maps to the OpenTelemetry Log Data Model. OTel collectors automatically rename `time`→`timestamp`, `msg`→`body`, etc.

Severity is emitted **twice on purpose**: `severity_text` (`INFO`) is what OTel consumers read; `level` (`info`) is what Datadog reads for log status — it does not recognise `severity_text`. `level` is also the key `@everfit-io/module-observability` emits, so Datadog facets and monitors are shared across services. It is added by `otelHandler`, which only wraps the stdout branch, so the OTLP side is unaffected.

## 3. Inside a handler — how to log

```go
import "log/slog"

func (h *Handler) Create(ctx context.Context, req CreateRequest) (Response, error) {
    // log(ctx) is the per-feature helper from this package's log.go
    out, err := h.svc.Create(ctx, CreateInput(req))
    if err != nil {
        return Response{}, mapErr(err)
    }
    return toResponse(out), nil
}
```

You DON'T pass the logger explicitly — `ctx` carries it. Service-layer code does the same (`log(ctx).Warn(...)` in `cacheSet`, `invalidate`, publish-event-failure).

## 4. Background goroutines

Goroutines spawned from `app.go` (e.g. the Kafka consumer) don't have a request scope. They use the bootstrap logger passed in:

```go
log.With(slog.String("<feature>.consumer", "<event-name>"))
```

Per-message logs inside `kafka.JSONHandler[T]` already use this logger and carry the `module` field if the handler itself comes from a feature package.

## 5. Levels

| Level | When | Example |
|---|---|---|
| `Debug` | Per-call detail useful only when debugging | "publish: lookup result" |
| `Info` | Lifecycle (boot, shutdown) and successful state transitions | "consumer started", "event published" |
| `Warn` | Degraded behavior — request continued, but something went sideways | "cache set failed (request used Mongo fallback)" |
| `Error` | Operation failed | "malformed Kafka event — committing and skipping" |

**Cache-aside degradation is `Warn`, not `Error`.** The request succeeded; we just couldn't cache. Same applies to "publish event failed (consumer may miss)" — Mongo write succeeded; Kafka emit failed.

## STOP rules

- ✗ `slog.Default()` inside feature code. Use `log(ctx)` — that helper rides request scope AND adds `module=<feature>`.
- ✗ Bare `logging.FromContext(ctx)` inside feature code. Same reason — you lose the `module` tag.
- ✗ Logging request bodies, response bodies, headers, or auth tokens **by hand**. PII / secret leak risk. The ONE sanctioned path is the opt-in capture in §6 — never `slog.String("body", string(raw))` in a handler or service.
- ✗ `fmt.Println` / `log.Println` (stdlib `log`). Always `slog` via the per-feature helper.
- ✗ String-formatted attributes (`slog.String("key", fmt.Sprintf("k=%s,v=%d", k, v))`). Use multiple typed attrs: `slog.String("key", k), slog.Int("value", v)`.

## 6. Payload capture — the controlled exception

§5's STOP rule bans hand-rolled body logging. `middleware.AccessLog` carries the
only sanctioned alternative, driven by `config.HTTPLogConfig`:

| Env | Default | Effect |
|---|---|---|
| `SVC_HTTP_LOG_BODIES` | `true` | capture request + response JSON bodies |
| `SVC_HTTP_LOG_MAX_VALUE_LEN` | `512` | longest string value kept inside a body |
| `SVC_HTTP_LOG_MAX_FIELDS` | `200` | total nodes kept per body |
| `SVC_HTTP_LOG_HEADERS` | `Content-Type,Accept,User-Agent` | request header **allowlist** |
| `SVC_HTTP_LOG_QUERY` | `true` | log **every** query param |
| `SVC_HTTP_LOG_QUERY_DENY` | `secret,sig,signature` | query params dropped entirely (**denylist**) |

Capture is **ON by default in every environment** — a deliberate deviation from
the original off-by-default posture (see `config.go::Load`'s `SetDefault`
block). Set `SVC_HTTP_LOG_BODIES`/`_QUERY=false` per-env when a deploy
needs it off.

What makes it safe — do not remove any of these when touching the code:

1. **Redaction runs before the value reaches a log record.** `redactJSON` walks
   the decoded JSON tree and replaces any value whose key contains `password`,
   `token`, `secret`, `email`, `identifier`, … (see `sensitiveKeys` in
   `redact.go`). Substring matching is deliberate: it over-hides rather than
   under-hides, and a payload that gains `new_password` tomorrow is covered
   with no code change. Keep entries long enough not to collide with ordinary
   names — `pin` was removed because it matched `shipping`/`pinned`.

   The list only knows CREDENTIALS. Ordinary PII (names, addresses) is NOT
   redacted — keep that in mind before relying on capture output for anything
   beyond debugging, even though capture itself defaults ON.
2. **Only `application/json` is captured.** Multipart and binary bodies are
   recorded as a size, never as content — that is what keeps uploaded files
   out of the log.
3. **Headers are an allowlist; query params are a denylist.** A header added to
   the API later stays invisible until someone names it — `Authorization` and
   `Cookie` are one typo from a credential in the log, so opting in per key is
   worth the friction. Query params go the other way: debugging usually means
   looking at a param nobody anticipated, and the per-key redaction below
   already hides `?token=` whether or not anyone denied it. An
   allowlisted-but-sensitive header is redacted too — the allowlist alone is
   not trusted.
4. **Bodies are emitted as nested objects** (`slog.Any`), so a backend can
   filter on `request_body.type`. That rules out a byte cap — cutting an object
   at N bytes yields invalid JSON — so size is bounded structurally instead:
   `MaxValueLen` caps any single string, `MaxFields` caps total nodes, and
   `captureReadLimit` separately bounds memory while reading — tied to
   `decode.DefaultMaxBodyBytes`, since anything larger is about to 413 anyway.
6. **The field is always an object**, including the "can't capture" paths
   (`{"_not_captured": "...", "_bytes": N}`). A field that is sometimes an
   object and sometimes a string breaks index mappings in the log backend.
5. **`path_params` needs no allowlist** because those values already appear
   verbatim in `path` and in the span's `url.path`.

Operationally: capture is on by design, not a debugging toggle — raised log
volume and the redaction blast radius are accepted trade-offs. If a new
endpoint introduces a sensitive field name not covered by `sensitiveKeys`, add
it there in the SAME commit; unlike the credential list, capture itself is not
something to remember to turn back off.

## References

- [layout.md](layout.md) §2.1 — `log.go` file slot in every feature.
- `internal/platform/logging/logger.go` — `New`, `FromContext`, `ContextWithLogger`, OTel-shape JSON handler.
- `internal/platform/httpx/middleware/requestid.go` — stamps `request_id` / `trace_id` / `span_id` on the per-request logger.
- `internal/platform/httpx/middleware/logger.go` — access-log middleware (one line per non-health request).
