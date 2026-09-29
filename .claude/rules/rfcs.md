# RFC conformance — what specs this repo follows, where, and what we deliberately don't

Read this when you change status-code semantics, add a response header, touch the wire format, or push back on a "make it match RFC X" review comment.

## 1. RFCs honored today

| RFC | Topic | Where honored |
|---|---|---|
| **RFC 9110** — HTTP Semantics | status codes, methods, header semantics | `apperr.*` constructors map to RFC-correct codes (400/401/403/404/405/409/410/412/422/428/429/503). 401-vs-403, 412-vs-428-vs-409 distinctions enforced in [headers.md](headers.md) §5 + items error map. |
| **RFC 9110 §15.5.6** — 405 MUST include `Allow` | `Allow` lists valid methods | `apperr.MethodNotAllowed(code, msg, methods...)` populates Allow. chi-fallback 405 omits Allow (v5 quirk — `router.go`). |
| **RFC 9110 §15.5.13** — 412 Precondition Failed | If-Match value mismatch → 412 | `apperr.PreconditionFailed`. items `ErrVersionConflict` maps here (`errors.go`). 409 is reserved for non-precondition conflicts (duplicate key). |
| **RFC 9111** — HTTP Caching | `Cache-Control` directives | [headers.md](headers.md) §2 — default `no-store` on error paths; success endpoints opt in. |
| **RFC 9112** — HTTP/1.1 message framing | hop-by-hop headers | `headers.md` §3 STOP — never set `Connection`, `Keep-Alive`, `Transfer-Encoding`, `Content-Length` manually; `net/http` manages these. |
| **RFC 7232** — Conditional Requests | `ETag` emission + `If-Match` consumption | `respond.HeaderProvider` interface (`respond.go`); `ItemResponse.ResponseHeaders()` emits `W/"v<N>"`. `typed.JSON` applies any HeaderProvider before WriteHeader. items PATCH consumes `If-Match` and returns 412 on mismatch / 428 when missing. |
| **RFC 7231 §7.1.3** — `Retry-After` format | seconds OR HTTP-date | `apperr.TooManyRequests` / `ServiceUnavailable` sugar emits seconds; explicit `WithHeader("Retry-After", ...)` supports HTTP-date. |
| **RFC 7239** — `Forwarded` | client IP via forwarding headers | `middleware.ClientIP` parses `Forwarded` (RFC 7239), then `X-Forwarded-For` (leftmost), then `X-Real-IP`, then `r.RemoteAddr`. Stamps `client_ip` on request logger. |
| **RFC 6585 §3** — 428 Precondition Required | force conditional requests | `apperr.PreconditionRequired`. items PATCH / Publish return 428 when `If-Match` is absent. |
| **RFC 6648** — deprecate `X-` prefix | bare header names | [http.md](http.md) §6 STOP rule. All headers use bare names (`Request-Id`, `Cache-Status`, `Idempotency-Key`, `If-Match`). No `X-` prefix anywhere. |
| **RFC 5789** — PATCH method | partial-update verb | items + watchlist handlers use PATCH for partial updates. Body is "partial object," NOT JSON Patch (RFC 6902) or JSON Merge Patch (RFC 7396) — see §2. |
| **RFC 8288** — Web Linking | `Link` header relations | [headers.md](headers.md) §2 — multi-value via `WithHeaderAdd("Link", ...)`. |
| **RFC 8259** — JSON | wire format | `decode.Body` (`DisallowUnknownFields` + size cap), `respond.JSON` (utf-8, content-type set explicitly). |
| **RFC 3339** — ISO 8601 date-time | timestamp format on the wire | `time.RFC3339` for all `*time.Time` JSON fields (mongo.md §5). Date-only fields use `timex.DateFormat` (`2006-01-02`). |
| **RFC 4122 / 9562** — UUID | request id format | `middleware.RequestID` mints UUIDv4 for every request (server-only generation; no inbound honoring). |
| **W3C Trace Context** (`traceparent`) | distributed tracing propagation | `otelhttp.NewHandler` (outermost in `router.go`) extracts/injects on HTTP; `otelgrpc.NewClientHandler` propagates via gRPC metadata; `otelmongo` + `redisotel` emit child spans on driver calls; Kafka producer injects + consumer extracts `traceparent` via message headers (see `infra/kafka/otel.go::headerCarrier`); `logging.otelHandler` stamps `trace_id`/`span_id` from `trace.SpanFromContext`. |
| **OpenTelemetry Log Data Model** | log field names | `internal/platform/logging/logger.go` — `severity_text`, `severity_number`, `service.name`, `service.version`, `deployment.environment`. Plus `level` (`debug`/`info`/`warn`/`error`) — NOT an OTel field, a deliberate concession to Datadog, which reads log status from `level` and does not recognise `severity_text`; same key as `@everfit-io/module-observability`. Stdout branch only; the OTLP export is unchanged. |
| **OpenTelemetry Semantic Conventions** | resource attributes + messaging conventions | `service.name=go-service-template`, `service.version=<embedded VERSION>`, `deployment.environment=<env>`. Driver-level auto-instrumentation: `otelmongo` (Mongo CommandMonitor), `redisotel` (go-redis hook), `otelgrpc` (gRPC stats handler), `otelhttp` (HTTP server). Kafka producer/consumer emit `kafka.publish <topic>` / `kafka.consume <topic>` spans with `messaging.system=kafka`, `messaging.destination.name=<topic>`, `messaging.operation=publish|process` (see `infra/kafka/{producer,consumer,otel}.go`). No manual `Span()` calls in business code — instrumentation lives at the driver boundary only. |
| **`Idempotency-Key` draft** (`draft-ietf-httpapi-idempotency-key-header`) | idempotent POST replay | `middleware.Idempotency` reads/stores by header; replay returns cached response. Bare header name (RFC 6648). |

## 2. RFCs we deliberately deviate from

| RFC | What it says | What we do | Why |
|---|---|---|---|
| **RFC 9457 / 7807** — Problem Details (`application/problem+json` with `type/title/status/detail/instance`) | error body shape with a `type` URI to docs | Custom envelope `{code, message, details?}` ([http.md](http.md) §1) | Clients localize from the stable UPPER_SNAKE_CASE `code`. RFC 9457's `type` URI requires clients to dereference URIs they can't reach (offline mobile, no doc server in dev). Future per-feature exception possible via `Accept: application/problem+json` negotiation; the default stays our envelope. |
| **RFC 6902** — JSON Patch (`[{"op":"replace","path":"/value","value":"x"}]`) | patch documents as op arrays | Partial object body | Op arrays are powerful but error-prone (path encoding, op ordering); clients usually want "set these fields." Partial-object PATCH covers 95% of cases. If a feature needs op semantics later, accept `application/json-patch+json` on that endpoint specifically. |
| **RFC 7396** — JSON Merge Patch (`{"value": null}` deletes a field) | partial-doc body where `null` = remove | Partial object, but `null` is currently indistinguishable from "missing" | Three-state semantics (omitted / null / value) require `json.RawMessage` or a tagged-union wrapper. Today our PATCH treats nil-pointer as "no change"; explicit-null-as-delete is not supported. Documented inline at `items.PatchRequest`. Move to RawMessage + custom Bind when a feature actually needs delete semantics. |
| **RFC 5988** — Web Linking (older) | n/a — superseded by 8288 | n/a | We're on 8288. |
| **RFC 7230–7235** — older HTTP/1.1 | n/a — superseded by 9110/9111/9112 | n/a | We cite 9110 family. |

## 3. RFCs not yet relevant (might matter later)

| RFC | When it'd kick in |
|---|---|
| **RFC 6749** (OAuth 2.0) + **RFC 6750** (Bearer Token) + **RFC 7519** (JWT) | Auth moves from the upstream gateway into this service. Today auth is upstream; the gateway forwards verified principal info via headers. When the service starts validating JWTs locally: add a middleware that parses Bearer, validates signature against JWKs (RFC 7517), and stamps `user_id` / `tenant_id` on the request logger alongside `client_ip`. |
| **CORS** (Fetch spec — not an RFC but normative) | A browser-based client (web UI) hits the API directly. Today all callers are server-side / mobile native, so no preflight handling. If a browser client lands: configure CORS at the edge (preferred) OR add a CORS middleware scoped to the versioned API group. |
| **RFC 7233** (Range requests) | We start serving large files / video / chunked downloads. |
| **RFC 9421** (HTTP Message Signatures) | Webhook receive paths require signed requests. |
| **RFC 9540** (Origin-Bound Tokens) | Inter-service auth without a shared secret. |
| **RFC 8470** (Early data) | TLS 1.3 0-RTT handling at the app layer (usually edge concern). |

## 4. RFC drift in code review

When a reviewer says "this isn't RFC X compliant," check this file FIRST:

1. Is the RFC in §1 (honored)? Then the deviation needs a fix in code OR a citation in §2.
2. Is it in §2 (deliberate deviation)? Cite the reason; this is intentional.
3. Is it in §3 (not yet relevant)? Note that we don't ship that surface today.
4. Not listed at all? Either it's irrelevant (no `Accept-Ranges` for a service that doesn't serve files) or this file needs an update.

## STOP rules

- ✗ "Make it match RFC X" without checking §2. Some deviations are deliberate.
- ✗ Adding a new RFC reference inline in a rule file without updating §1 here. RFC conformance is cross-cutting — pin it once.
- ✗ Citing **superseded** RFCs in new code or docs (5988, 7234, 7230–7235). Use the 8xxx / 91xx successors.
- ✗ Adding `X-` prefixed headers because "someone else does it." See §1 RFC 6648. Bare names only.
- ✗ Switching the error envelope to RFC 9457 by default. Per-feature exception via `Accept` negotiation is allowed; the default stays our envelope.
- ✗ Adding JSON Patch / Merge Patch handling without explicit content-type negotiation. Default PATCH stays partial-object.
- ✗ Returning 409 Conflict for optimistic-lock failure. Use `apperr.PreconditionFailed` (412) — 409 is for non-precondition state conflicts (duplicate key on Create).
- ✗ Returning 400 when `If-Match` is missing on a mutating endpoint that requires it. Use `apperr.PreconditionRequired` (428).

## References

- [http.md](http.md) — status code → constructor mapping, error envelope.
- [headers.md](headers.md) — per-header catalog with RFC pointers inline.
- [logging.md](logging.md) — OTel log/semantic conventions.
- [mongo.md](mongo.md) §2 — cursor pagination wire shape.
- `internal/platform/apperr/apperr.go` — status code → AppError constructor seam.
- `internal/platform/httpx/respond/maperror.go` — central error translator.
- `internal/platform/httpx/respond/respond.go::HeaderProvider` — response-attached headers seam (RFC 7232 ETag, etc.).
- `internal/platform/httpx/middleware/clientip.go` — RFC 7239 + de-facto forwarding headers.
