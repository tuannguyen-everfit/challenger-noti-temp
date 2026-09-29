# HTTP / API — handlers, error envelope, DTOs, validation

Read this whenever you touch routes, request/response DTOs, the `apperr` package, or `respond`/`localization`.

## 1. Error envelope

```json
{ "code": "ITEMS_KEY_TAKEN", "message": "an item with that key already exists", "details": { /* optional */ } }
```

- `code` — UPPER_SNAKE_CASE `localization.Code`. Stable across releases.
- `message` — developer-facing fallback. Clients render from `code`.
- `details` — optional map (per-field validation, retry-after, etc.).

The legacy Node shape `{message, error, errorCode, additional, title}` is a hard STOP — never reintroduce it.

## 2. Per-feature error mapping (mandatory)

Every feature defines two files:

```go
// internal/features/<feature>/localization.go
const (
    CodeKeyTaken localization.Code = "<FEATURE>_KEY_TAKEN"
    // ...
)

// internal/features/<feature>/errors.go
var ErrConflict = errors.New("<feature>: duplicate key")
// ...

var errorMap = []apperr.Mapping{
    {Sentinel: ErrConflict, Build: apperr.Conflict, Code: CodeKeyTaken, Message: "an entry with that key already exists"},
    // ... one row per sentinel
}

func mapErr(err error) error { return apperr.MapSentinels(err, errorMap) }
```

- New sentinel = new table row. **No switch-case edits.**
- The table-based form is required so the mapping mechanism stays uniform across features.

## 3. Typed handler adapter

```go
r.Post("/", typed.JSON(http.StatusCreated, h.Create))
r.Get("/{key}", typed.JSON(http.StatusOK, h.Get))
r.Patch("/{key}", typed.JSON(http.StatusOK, h.Patch))
```

Handler signature: `func(ctx context.Context, req T) (Resp, error)`. The adapter handles:

- Body decode + size cap + `DisallowUnknownFields` + validator/v10.
- `AutoBind` from struct tags: `path:"key"`, `query:"limit"`, `header:"If-Match"`.
- Error → `respond.MapError` central translator.

## 4. Request DTOs

```go
type CreateRequest struct {
    Key       string     `json:"key"      validate:"required,max=128"`
    Owner     string     `json:"owner"    validate:"required,max=128"`
    Value     string     `json:"value"    validate:"max=4096"`
    ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type PatchRequest struct {
    IfMatch   int        `header:"If-Match" validate:"required,min=1"`
    Key       string     `path:"key"        validate:"required,max=128"`
    Value     *string    `json:"value,omitempty"     validate:"omitempty,max=4096"`
    ExpiresAt *time.Time `json:"expires_at,omitempty"`
}
```

- `validator/v10` struct tags only. Hand-rolled `func (r *Req) Validate() error` is a hard STOP — use tags + the shared singleton at `internal/platform/validate`.

## 5. Response DTOs — the wire seam

Domain types in `service.go` carry `bson:` tags only. Wire types live in `handler.go` with `json:` tags. A `to<Type>Response` mapper is the single seam.

```go
type ItemResponse struct {
    Key       string     `json:"key"`
    Owner     string     `json:"owner"`
    CreatedAt time.Time  `json:"created_at"`
    // ...
}

func toItemResponse(it Item) ItemResponse {
    return ItemResponse{Key: it.Key, Owner: it.Owner, /* explicit field copy */}
}
```

**Never** let a domain struct marshal to the wire directly — it locks storage shape and wire shape together forever. The explicit mapper is the audit point for "what leaves the service?"

## 6. Headers

- **Bare names only** (RFC 6648). `Request-Id`, `Idempotency-Key`, `If-Match`, `Cache-Status` — never `X-Request-Id`, `X-Idempotency-Key`, etc.
- `Request-Id` is server-generated per request (UUIDv4 minted in `middleware.RequestID`) and echoed in the response so clients can quote it in support tickets.
- To attach response headers from an error path, use `apperr.AppError.WithHeader` / `WithHeaderAdd` — see [headers.md](headers.md) for the full catalog (Retry-After, WWW-Authenticate, Allow, Location, Link, ETag, Cache-Control, Vary, …) and the "don't send" list.

## 7. JSON casing

- **`json:"snake_case"`** on every wire field. Symmetric with `bson:"snake_case"` in storage — operators reading API responses and `mongosh` output see the same names.
- ✓ `expires_at`, `created_at`, `updated_at`, `item_key`, `next_cursor`, `has_more`, `event_version`, `published_at`
- ✗ `expiresAt`, `createdAt`, `itemKey`, `nextCursor`, `hasMore` — older camelCase style, do NOT use for new fields.
- Pair the tags: `bson:"snake_case_name" json:"snake_case_name"` (often identical). The DTO seam (handler.go ItemResponse) stays — wire DTO is still a separate type from the domain Item, but both use snake_case.
- Clients (mobile / web / other services) must rename their deserializers when they upgrade. Coordinate the cutover.

## 8. Status code 499

Used for `context.Canceled` (nginx convention). `respond.MapError` handles this automatically.

## 9. ID embedding in URLs

```go
r.Get("/{id}", typed.JSON(http.StatusOK, h.Get))
// req struct has: ID string `path:"id" validate:"required"`
```

`AutoBind` from the `path:` tag — no `chi.URLParam(r, "id")` in handler bodies.

## 10. API versioning, edge throttle, pool tuning

Routing concerns — versioned API group, the edge throttle, and the "when to tune downstream pools" criteria — live in [routing.md](routing.md). Touch that file when you add a new endpoint, change throttle config, or consider tuning a pool.

## 11. Request-shape limits — validator vs ENV-tunable Service ceiling

Two kinds of upper bounds on incoming requests; they go to different layers.

| Kind | Where it lives | Example |
|---|---|---|
| **Static shape limit** — a hard contract that NEVER changes per-environment (max body size, regex shape, fixed-length ID, enum membership) | `validate:"..."` tag on the DTO field in `handler.go` | `validate:"required,len=24,hexadecimal"`, `validate:"max=128"`, `validate:"oneof=public private"` |
| **Shared operational ceiling** — one knob that applies the same policy to every feature touching the same concern | Platform-level `<Concern>Config` in `internal/platform/config/`; each feature's `Config` pulls the value at composition-root | `Pagination.MaxLimit` (max `?limit=` on EVERY list endpoint) |
| **Per-feature operational ceiling** — a knob with semantics that ONLY exist inside one feature | Feature-namespaced `<Feature>Config` in `internal/platform/config/`, passed to that feature's `Config` | `MaxWindow` for `?window=N` on `GET /leaderboards/{id}/me` — concept is leaderboard-specific |

### Default to shared. Per-feature is the exception.

Before introducing a feature-namespaced env var, ask: "if a second feature added the same kind of endpoint tomorrow, would ops want the SAME value to apply?" If yes → shared at platform level. If no (the knob's semantics are inseparable from the feature's domain — like `window=N` neighbors) → feature-namespaced.

`?limit=` on a list endpoint is the textbook shared case: page-size policy is a service-wide concern, not a per-feature decision. Forcing ops to set `SVC_LEADERBOARD_MAX_LIMIT` and `SVC_CHALLENGES_MAX_LIMIT` separately for what's conceptually one knob is the smell §11's STOP rules call out.

### How to wire a shared operational ceiling (pagination is the canonical example)

1. **Define the platform-level config** in `internal/platform/config/config.go`:
   ```go
   type PaginationConfig struct {
       MaxLimit int `mapstructure:"max_limit"` // SVC_PAGINATION_MAX_LIMIT, default 100
   }
   ```
   Add `Pagination PaginationConfig` to the root `Config`, set a default via `v.SetDefault("pagination.max_limit", 100)`, and add a `validate()` guard so `0` / negative fails fast at boot.

2. **Define the feature-local config mirror** in each consuming feature's `service.go`:
   ```go
   type Config struct {
       MaxListLimit int  // populated from cfg.Pagination.MaxLimit at app.go
   }
   ```
   `New(... cfg Config)` accepts it; Service stores `cfg` as a field. The feature-local name (`MaxListLimit`) reads naturally inside the feature; the platform name (`Pagination.MaxLimit`) reads naturally at the composition root. The mapping at `app.go` is the seam.

3. **Wire in `app.go`** — every feature that takes a `?limit=` reads from the SAME platform-level value:
   ```go
   leaderboardSvc := leaderboard.New(
       repo, cache, chClient, profClient,
       leaderboard.Config{MaxListLimit: cfg.Pagination.MaxLimit},
   )
   // when challenges gains a paginated list:
   challengeSvc := challenge.New(repo, cache, challenge.Config{MaxListLimit: cfg.Pagination.MaxLimit})
   ```

4. **Drop the static upper bound from the validator tag** — keep only structural lower bounds. The validator no longer knows the ceiling.
   ```go
   // BEFORE: validate:"omitempty,min=1,max=100"
   // AFTER:  validate:"omitempty,min=1"
   ```

5. **Enforce in Service** as the first check (cheapest reject):
   ```go
   if s.cfg.MaxListLimit > 0 && in.Limit > s.cfg.MaxListLimit {
       return ListResult{}, apperr.BadRequest(
           localization.CodeInvalidRequest,
           fmt.Sprintf("limit must be ≤ %d", s.cfg.MaxListLimit),
       )
   }
   ```

6. **Document the env** in `mise.local.example.toml` near the other platform-shared knobs (not in a per-feature block).

### How to wire a per-feature operational ceiling

Same recipe, but the platform config struct is feature-namespaced (e.g. `LeaderboardConfig { MaxWindow int }` env `SVC_LEADERBOARD_MAX_WINDOW`) and only that feature reads it. Use ONLY when the knob's semantics don't transfer to other features.

### Why split this way

- **Validator is per-request**: it doesn't know the deploy-time config, and binding it dynamically fights `validator/v10`'s tag model.
- **Service IS the policy boundary**: errors at this layer already flow through `respond.MapError` → wire envelope; the dynamic max value lands in the `details` message client-side.
- **Tests stay clean**: handler tests cover validator-layer rejection (negative / missing); Service tests cover ENV-driven rejection (over-ceiling). No double-coverage.
- **Future-proof**: PR3's `?window=N` ceiling slots into the same `<feature>.Config` struct — one wiring pattern.

## STOP rules

- ✗ Manual `r.Body` decode via `io.ReadAll` + `json.Unmarshal`. Use `typed.JSON` (which uses `httpx/decode.JSON` internally).
- ✗ `func(w, r)` with three `respond.Error` calls duplicated. Use `typed.JSON` or `respond.Wrap`.
- ✗ Global body-parsing middleware (`app.use(express.json())` mindset). Body parsing is per-handler.
- ✗ Auth middleware blanket-applied to ALL routes including `/healthcheck`. Apply auth to mounted sub-routers (`/api/v1/*`), not globally.
- ✗ Returning raw internal error strings to clients (`"mongo: connection refused"`). Internal errors → log + `respond.MapError` default (500 + safe message). Domain errors → `apperr.*` with a stable `localization.Code`.
- ✗ `try { } catch { }` mindset wrapping every line in `if err != nil` returns to a top-level `defer recover()`. Use `errors.Is`/`errors.As` + sentinel errors + `fmt.Errorf("...: %w", err)`. `recover` belongs in `middleware.Recover` and `safego.WrapErr`, nowhere else.
- ✗ Class hierarchies of errors (`BadRequestError extends HTTPError extends Exception`). One typed error per shape: `apperr.AppError`. Constructors are functions, not a class tree.
- ✗ Generic `Handler[Req, Resp]` adapter beyond `typed.JSON` (the existing one). Already chosen.
- ✗ Hardcoding a per-environment ceiling on a paginated/range query (`limit`, `window`, bulk size) into the validator tag. Validator is static; ops can't shrink a hardcoded `max=100` under load. Use the `<Feature>Config` + Service-layer reject pattern in §11.
- ✗ Adding `max=N` to BOTH the validator AND the Service check for the same field. One source of truth; the Service-layer cap is the policy boundary. Validator only enforces structural shape (min, regex, enum).
- ✗ Duplicating the same operational ceiling across multiple `SVC_*` env vars for what's conceptually one knob (e.g. introducing `SVC_<FEATURE>_MAX_LIMIT` per feature for `?limit=`). Use the shared `SVC_PAGINATION_MAX_LIMIT` and map at the composition root.
- ✗ Creating a feature-namespaced env (e.g. `SVC_LEADERBOARD_MAX_LIST_LIMIT`) for a knob that conceptually applies to MORE THAN ONE feature today or in the near future. Default to shared; per-feature is the exception when semantics don't transfer.

(STOP rules for versioning, throttling, and pool tuning live in [routing.md](routing.md).)

## References

- [layout.md](layout.md) §2.1 — `handler.go` / `errors.go` / `localization.go` file slots in every feature.
- `internal/platform/apperr/` — typed AppError + Mapping helper.
- `internal/platform/httpx/typed/` — JSON handler adapter + AutoBind.
- `internal/platform/httpx/respond/` — MapError central translator.
- `internal/platform/localization/` — Code type + cross-cutting codes (INVALID_REQUEST, PAYLOAD_*, etc.).
- `internal/platform/config/config.go::PaginationConfig` — canonical shared-ceiling example (`MaxLimit` for every `?limit=`).
- `internal/features/leaderboard/service.go::Config` — canonical feature-local mirror of a platform-shared knob (`MaxListLimit` ← `cfg.Pagination.MaxLimit`) (lives in the upstream challenger-service repo — not in this template).
- [routing.md](routing.md) — API versioning, edge throttle, pool-tuning criteria.
