# HTTP response headers — what to send when

Read this whenever you want a handler to attach a non-default response header. The seam is `apperr.AppError.WithHeader` / `WithHeaderAdd` — `respond.MapError` flushes them before the status line.

## 1. How to attach a header

```go
return apperr.Unauthorized(CodeAuthRequired, "missing token").
    WithHeader("WWW-Authenticate", `Bearer realm="api", error="invalid_token"`)
```

- `WithHeader(k, v)` — set/replace (use for single-value headers).
- `WithHeaderAdd(k, v)` — append (use for multi-value headers like `Link`, `Vary`).
- Headers are written **before** the status line by `respond.MapError`.
- Explicit `WithHeader` wins over the `RetryAfter` sugar — callers can supply an HTTP-date instead of integer seconds when they want a fixed wall-clock target.

For success responses (not errors), set headers on `w.Header()` directly inside the handler before returning the value — `typed.JSON` doesn't re-order header writes.

## 2. The catalog — when each one matters

| Header | Status | When | Example |
|---|---|---|---|
| `Retry-After` | 429, 503 | Tell the client when to retry. Seconds (int) or HTTP-date. | `Retry-After: 30` |
| `WWW-Authenticate` | 401 | Required by RFC 9110 — without it, browsers won't prompt. | `WWW-Authenticate: Bearer realm="api"` |
| `Allow` | 405 | List allowed methods on the resource. | `Allow: GET, POST` |
| `Location` | 201, 301, 302, 303, 307, 308 | URL of created / moved resource. | `Location: /api/v1/items/42` |
| `Link` | 200 (paginated), any | RFC 8288 relations — pagination next/prev, related resources. Multi-value. | `Link: </items?cursor=...>; rel="next"` |
| `ETag` | 200, 201 | Resource version for cache validation + optimistic locking. | `ETag: "W/\"v3\""` |
| `Last-Modified` | 200 | Cache validation alternative to ETag. HTTP-date. | `Last-Modified: Wed, 21 Oct 2026 07:28:00 GMT` |
| `Cache-Control` | any | Caching directives. Default to `no-store` on error responses. | `Cache-Control: no-store` |
| `Content-Language` | any | RFC 9110 — the language(s) of the response body. Pair with `localization.Code`. | `Content-Language: vi-VN` |
| `Vary` | any | "Response varies on this request header" — critical when caches are in front of you. Multi-value. | `Vary: Accept-Language` |
| `Idempotency-Key` (echo) | 200 (replay) | Echo the request's key so clients can confirm replay vs. fresh result. | `Idempotency-Key: <uuid>` |

## 3. What NOT to send

| Header | Why not |
|---|---|
| `X-Powered-By`, `X-Generator`, `Server` (with version) | Information leak — tells an attacker which CVEs apply. Strip via reverse proxy if the runtime forces them. |
| `X-Frame-Options`, `Strict-Transport-Security`, `Content-Security-Policy`, `X-Content-Type-Options` | Set these at the **edge / reverse proxy**, not per-handler. Centralized policy beats per-feature drift. |
| Any new `X-Foo` header | RFC 6648 deprecated the `X-` convention. New headers use bare names (`Cache-Status`, `Idempotency-Key`). See [http.md](http.md) §6. |
| `Connection`, `Keep-Alive`, `Transfer-Encoding`, `Upgrade`, `Trailer`, `TE` | Hop-by-hop headers — the `net/http` server manages these. Setting them from a handler is a bug. |
| `Content-Length` | `net/http` sets this from the response body. Manual override breaks chunked-encoding fallback. |
| `Set-Cookie` with `Domain` matching the apex | Cookie scope leak across subdomains. If we ever ship cookies, set `Domain` to the exact host. |

## 4. Rate-limit headers (when we add per-key limits)

Not shipped today (the edge throttle is server-wide, see [routing.md](routing.md) §2). When per-API-key rate limits land, return on 429:

```
X-RateLimit-Limit: 60
X-RateLimit-Remaining: 0
X-RateLimit-Reset: 1735689600   (unix seconds)
Retry-After: 12
```

The `X-RateLimit-*` family is non-standard but well-established (GitHub, Stripe, Twitter all use it). When designing the rate-limit middleware, prefer the bare-name variant if a standard emerges (`RateLimit-Limit`, draft RFC).

## 5. STOP rules

- ✗ Setting headers AFTER `respond.JSON` / `respond.Error`. `WriteHeader` has already been called — your set is silently dropped.
- ✗ Using `WithHeader` for multi-value headers like `Link` or `Vary`. Use `WithHeaderAdd` — `Set` overwrites; clients only see the last value.
- ✗ Hand-rolling `w.Header().Set("Retry-After", ...)` inside a handler. Use `apperr.TooManyRequests(... retryAfter)` or `WithHeader` on an `AppError`.
- ✗ Emitting `WWW-Authenticate` on 403. Spec is clear: 401 = "auth missing/bad, here's how to auth"; 403 = "auth fine, you're still not allowed".
- ✗ Echoing `Authorization` / `Cookie` / `Set-Cookie` request headers back into a response (e.g. in a debug header). Token / session leak.
- ✗ Putting PII in headers. Headers land in access logs, reverse-proxy logs, and CDN dashboards — assume they're stored forever.

## References

- `internal/platform/apperr/apperr.go::WithHeader`, `WithHeaderAdd`.
- `internal/platform/httpx/respond/maperror.go` — flush order: AppError.Headers → RetryAfter sugar (if not already set) → status line.
- [http.md](http.md) §1 (error envelope) and §6 (X-prefix STOP).
- [routing.md](routing.md) §2 — edge throttle uses `Retry-After` via `apperr.ServiceUnavailable`.
- RFC 9110 (HTTP Semantics) — authoritative for status-code/header pairings.
- RFC 8288 (Web Linking) — `Link` header.
