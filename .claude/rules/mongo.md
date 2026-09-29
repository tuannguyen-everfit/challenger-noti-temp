# Mongo — bson casing, cursor pagination, indexes-as-data

Read this whenever you touch `bson:` tags, `r.coll.*` calls, query filters, or `indexes.go`.

## 1. Field casing

```go
type Item struct {
    Key       string     `bson:"_id"`
    Owner     string     `bson:"owner"`
    CreatedAt time.Time  `bson:"created_at"`
    UpdatedAt time.Time  `bson:"updated_at"`
}
```

- ✓ `bson:"snake_case"`
- ✗ `bson:"camelCase"` — breaks `mongosh` operator queries, confuses cross-language reads, causes silent index-name collisions.
- Domain types carry **`bson:` tags ONLY** — wire shape (`json:`) lives on response DTOs in `handler.go` (see [http.md](http.md) DTO seam).

## 2. Cursor pagination (the ONLY pagination)

Offset/page pagination is a hard STOP everywhere in this service. Reasons:

1. **O(n) under load** — `LIMIT 20 OFFSET 10000` scans 10020 rows to return 20.
2. **Inconsistent under writes** — rows inserted between pages get duplicated or skipped.
3. **Unbounded** — no upper limit on `?page=999999`.

### Wire shape

**Request:**

```
GET /api/v1/items?cursor=eyJ0Ijoi...&limit=20
```

| Param | Type | Notes |
|---|---|---|
| `cursor` | string (opaque, base64) | absent → start from the beginning |
| `limit`  | int | 1–100, default 20; clamped by `pagination.Query.EffectiveLimit()` |

**Response:**

```json
{ "items": [ ... ], "next_cursor": "eyJ0Ijoi...", "has_more": true }
```

- `next_cursor` is omitted/empty when `has_more: false`.
- `items` is always present, even when empty.
- Client passes `next_cursor` verbatim as the next request's `cursor`.

### Handler pattern

```go
type ListRequest struct {
    Cursor string `query:"cursor" validate:"omitempty"`
    Limit  int    `query:"limit"  validate:"omitempty,min=1,max=100"`
}

func (h *Handler) List(ctx context.Context, req ListRequest) (pagination.Page[ItemResponse], error) {
    cur, err := pagination.Decode(req.Cursor)
    if err != nil {
        return pagination.Page[ItemResponse]{}, apperr.BadRequest(localization.CodeInvalidRequest, "invalid cursor").Wrap(err)
    }
    q := pagination.Query{Cursor: req.Cursor, Limit: req.Limit}
    page, err := h.svc.List(ctx, cur, q.EffectiveLimit())
    if err != nil {
        return pagination.Page[ItemResponse]{}, mapErr(err)
    }
    return pagination.Page[ItemResponse]{
        Items:      toItemResponses(page.Items),
        NextCursor: page.NextCursor,
        HasMore:    page.HasMore,
    }, nil
}
```

### Repo / Mongo side

Universal sort key: **`(created_at DESC, _id DESC)`** (or the equivalent feature-specific timestamp + `_id`). Cursor encodes the *last seen* `(timestamp, _id)` tuple; the next query filters by `$or`:

```go
q["$or"] = []bson.M{
    {"created_at": bson.M{"$lt": after.CreatedAt}},
    {"created_at": after.CreatedAt, "_id": bson.M{"$lt": after.ID}},
}
```

Query `limit + 1` rows so `pagination.Build` can detect `has_more` without a `count_documents()`. If a feature needs a different sort key (e.g. `last_active DESC`), define a per-feature `Cursor` type — don't bend the shared one.

### STOP rules

- **No `total`, `pageCount`, or `currentPage`** in the response. They require full scans.
- **No `?offset=` or `?skip=`** in requests. Even if a client asks, push back.
- **`next_cursor` must be empty when `has_more: false`** so the client knows to stop. Don't compute it from the LAST row "just in case."

## 3. Indexes as data (the audit surface)

Every collection has an `indexes.go` declaring **`var requiredIndexes []mongox.IndexSpec`** at the top. The slice IS the audit surface — any Find/Update against the collection must map to a listed spec.

```go
// internal/features/<feature>/indexes.go
var requiredIndexes = []mongox.IndexSpec{
    {
        Name: "<feature>_list",
        Keys: bson.D{
            {Key: "status", Value: 1},
            {Key: "owner", Value: 1},
            {Key: "created_at", Value: -1},
            {Key: "_id", Value: -1},
        },
    },
}

func NewIndexEnsurer(m *mongox.Client) mongox.IndexEnsurer {
    return indexEnsurer{coll: m.DB.Collection(collectionName)}
}

type indexEnsurer struct{ coll *mongo.Collection }

func (e indexEnsurer) Ensure(ctx context.Context, _ *mongo.Database) error {
    return mongox.EnsureSpecs(ctx, e.coll, requiredIndexes)
}
```

### Adding a new feature

1. Create `internal/features/<feature>/indexes.go` following the shape above.
2. Add **one line** to `app.go`:

   ```go
   if err := mongoClient.EnsureIndexes(ctx,
       // ... existing features
       newfeature.NewIndexEnsurer(mongoClient),   // ← new
   ); err != nil { ... }
   ```

3. On next boot the index gets created. Mongo's `CreateIndexes` is idempotent on (name + keys).

### STOP rules

- ✗ New `r.coll.Find(...)` / `UpdateMany(...)` that doesn't match any existing `IndexSpec`. Either add (or reuse) a spec, or accept that you'll need a comment justifying the COLLSCAN.
- ✗ Imperative `db.CreateIndex(...)` calls in code. Always go through `mongox.EnsureSpecs`.
- ✗ Implicit names (`Name` field empty). Always name indexes explicitly — auto-generated names collide silently when keys are renamed.
- ✗ Dropping a renamed index automatically. Removing a spec from `requiredIndexes` does NOT drop the index — drop manually in `mongosh` with operator review.

### Each query method names its index

In `repository_mongo.go`, every Find/Update method has a one-line comment naming its index:

```go
// FindMany applies the filter + cursor and returns up to limit entries.
// Uses index `<feature>_list` (see indexes.go).
func (r *mongoRepo) FindMany(...) { ... }
```

Reviewer can cross-check this against `indexes.go` without grepping.

## 4. Repo ↔ Service split

- **Repo** is a low-level Mongo adapter. Returns the feature's domain model, translates driver errors to domain sentinels (`mongox.ErrNotFound` → `ErrNotFound`).
- **Service** owns business logic, cache-aside, optimistic locking, event emission.
- Cross-feature callers ALWAYS go through Service, never Repo. See [cross-feature.md](cross-feature.md).

### STOP rules

- ✗ `BaseRepository[T]` generic base struct that all repos embed. Use `internal/infra/mongox` **functions** (`FindByID[T]`, `UpsertByID`, etc.).
- ✗ ORM-style fluent builders (`db.Where(...).Order(...).Find(&out)`). Mongo driver + bson is the contract.
- ✗ Repo interface exposing `bson.M` filters to the Service layer. Repo methods take typed parameters, return domain models.
- ✗ Two features writing to the same Mongo collection. Each feature owns its collection.

## 5. ObjectID + time

- **ObjectID:** hex `string` at API boundary, `bson.ObjectID` in models. Convert at the DTO mapper, not in handlers.
- **Time:** `time.RFC3339` for timestamps on the wire; `timex.DateFormat` (`2006-01-02`) for date-only strings.

## 6. Upsert hygiene — `omitempty` on `_id` and `created_at`

Mongo does NOT auto-generate values for fields you explicitly include in an upsert payload. A struct without `omitempty` marshals zero values literally, and that's what gets written.

### The failure mode (don't repeat)

```go
type Challenge struct {
    ID        bson.ObjectID `bson:"_id"`         // NO omitempty
    CreatedAt time.Time     `bson:"created_at"`  // NO omitempty
    // ...
}

// Seeder that never sets ID / CreatedAt:
setDoc, _ := toBSONMap(ch)
coll.UpdateOne(ctx, filter,
    bson.M{"$set": setDoc},
    options.UpdateOne().SetUpsert(true),
)
```

Result in Mongo:

```json
{ "_id": ObjectId("000000000000000000000000"),
  "created_at": ISODate("0001-01-01T00:00:00Z"), ... }
```

Both fields are zero values, marshalled literally because the tag has no `omitempty`. On the first run you get a junk all-zero `_id` and zero-time `created_at`. On the second run Mongo rejects with `Performing an update on the path '_id' would modify the immutable field '_id'`.

### The fix

```go
type Challenge struct {
    ID        bson.ObjectID `bson:"_id,omitempty"`
    CreatedAt time.Time     `bson:"created_at,omitempty"`
    UpdatedAt time.Time     `bson:"updated_at,omitempty"`
    // ...
}
```

Then in the upsert:

```go
coll.UpdateOne(ctx, filter,
    bson.M{
        "$set":         setDoc,                              // updated_at refreshed every run
        "$setOnInsert": bson.M{"created_at": time.Now().UTC()}, // owned by insert path only
    },
    options.UpdateOne().SetUpsert(true),
)
```

Why this works:
- `_id,omitempty` + zero `ObjectID` → field elided from the doc → Mongo mints a fresh `ObjectID` on insert and `$set` never touches `_id` on update (no immutable-field error).
- `created_at,omitempty` + zero `time.Time` → field elided from `$set` → `$setOnInsert` is the only writer → first run stamps `now`, subsequent runs preserve it.
- `updated_at,omitempty` is a defensive default: future write paths that forget to set it keep the previous value instead of writing zero time. Pair with a discipline that every write site assigns `UpdatedAt = time.Now().UTC()` explicitly.

### Avoid these workarounds

- Marshalling the struct then `delete(setDoc, "_id"); delete(setDoc, "created_at")` — works but tag-and-marshal should be the contract, not a post-marshal patch. Use `omitempty`.
- Setting `ch.ID = bson.NewObjectID()` in the seeder — defeats Mongo's `_id` generation, makes IDs non-monotonic across seed runs, and hides the real cause.

### STOP rules

- ✗ `bson:"_id"` (no `omitempty`) on a struct that gets upserted. Zero `ObjectID` writes literally and breaks the second run.
- ✗ `bson:"created_at"` (no `omitempty`) when the same struct is used for both reads and inserts via marshal-then-upsert. Use `omitempty` + `$setOnInsert`.
- ✗ Putting the same timestamp field in BOTH `$set` and `$setOnInsert`. Mongo rejects with a path-conflict error.
- ✗ Using `delete(setDoc, "_id")` / `delete(setDoc, "created_at")` to patch the marshalled doc. Fix the tag instead.

## References

- [layout.md](layout.md) §2.1 — `repository_mongo.go` / `indexes.go` file slots in every feature.
- `internal/infra/mongox/` — generic CRUD + `IndexSpec` + `EnsureSpecs`.
- `internal/platform/pagination/` — `Cursor`, `Query`, `Page[T]`, `Build`, `Decode`.
