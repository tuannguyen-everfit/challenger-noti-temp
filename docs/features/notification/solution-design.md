# Notification — solution design

Upstream design: Confluence 3886907646 and challenger-service `docs/features/notification/solution-design.md` (§12.1–12.2 audit fields). This file records what this service implements and the local decisions.

## 1. API

Both endpoints are under the versioned group and require `Authorization: Bearer <access token>`. The JWT `sub` must be the account ObjectID (hex); anything else is 401. Both responses send `Cache-Control: private, no-store`.

### `GET /api/v1/notifications`

| Param | Type | Notes |
|---|---|---|
| `tab` | `all` \| `activities` \| `system` | default `all` |
| `cursor` | opaque string | absent → first page |
| `limit` | int ≥ 1 | absent / `0` → 20; above `SVC_PAGINATION_MAX_LIMIT` → 400 |

```json
{
  "items": [
    {
      "id": "66f8c0e1a1b2c3d4e5f6a7b8",
      "kind": "challenge_published",
      "tab": "system",
      "title": "Run Club: 30 days of 5k",
      "body": "Lace up!",
      "image": { "type": "challenge", "url": "https://cdn/…/thumb.png" },
      "navigate": { "type": "challenge_detail", "challenge_id": "65a1b2c3d4e5f6a7b8c9d0e1" },
      "buttons": ["jump_in"],
      "is_read": false,
      "activity_at": "2026-09-29T03:00:00Z",
      "created_at": "2026-09-29T03:00:00Z"
    }
  ],
  "next_cursor": "eyJhIjoi…",
  "has_more": true
}
```

- Sort: `activity_at DESC, _id DESC`. `items` is always an array; `next_cursor` is omitted when `has_more` is false.
- `buttons` is `[]` once the buttons were hidden (`buttons_hidden_at` set); `is_read` = `read_at` set.
- **Cursor contract.** The cursor is a snapshot over `activity_at`, which a stack update moves forward. A card bumped while the client scrolls lands above the cursor and is not returned on later pages. Recovery: pull-to-refresh, or refetch from `cursor=""` when `summary.unread_total` changes.

### `GET /api/v1/notifications/summary`

| Param | Type | Notes |
|---|---|---|
| `tab` | `all` \| `activities` \| `system` | filters `groups` only |
| `tz` | IANA zone, e.g. `Asia/Saigon` | absent → UTC; offsets (`+07:00`), `Local`, unknown → 400 |

```json
{
  "unread_total": 3,
  "unread_by_tab": { "activities": 2, "system": 1 },
  "groups": { "today": 4, "this_week": 7, "earlier": 9 }
}
```

- `unread_by_tab` always covers both tabs; `groups` follow `tab` and count read + unread rows.
- Groups in `tz`: today = `[local midnight, ∞)`, this week = the previous 6 local days `[midnight − 6d, midnight)`, earlier = before that. Local-calendar arithmetic, so DST is handled. Known edge: in zones whose DST jump is at midnight (e.g. America/Havana), local midnight doesn't exist on that one day and the boundary shifts by an hour.
- **Mobile must send the device IANA zone.** Without `tz` the groups use UTC and won't match device-local grouping.
- **Count cap.** Every count stops at 100 (`summaryCountCap`); a value of 100 means "99+". `unread_total` is the sum of two capped counts, so it can reach 200 — clients render ≥ 100 as `99+`.
- The five counts run in parallel (errgroup); the request fails if any one fails.

### Errors

| Status | `code` | When |
|---|---|---|
| 400 | `INVALID_REQUEST` | bad `tab`, negative / over-max `limit`, bad `cursor`, bad `tz` |
| 401 | `AUTH_TOKEN_MISSING` / `AUTH_TOKEN_INVALID` / `AUTH_TOKEN_EXPIRED` | Bearer middleware |
| 401 | `UNAUTHORIZED` | `sub` not a non-zero ObjectID |
| 500 | `INTERNAL_ERROR` | storage failure |

## 2. Data model

Collection `notifications`, one document per card, owned by this feature. Full struct in `internal/features/notification/service.go` (`Notification`, `Stack`, `StackActor`, `Actor`, `Image`, `Navigate`, `Source`, `PushState`). `Kind` values follow Confluence §3.0 (`friend_invite` … `challenge_ended`).

| Index | Keys | Used by |
|---|---|---|
| `notifications_feed` | `user_id, activity_at -1, _id -1` | feed + range counts, `tab=all` |
| `notifications_feed_tab` | `user_id, tab, activity_at -1, _id -1` | feed + range counts, one tab |
| `notifications_unread` | `user_id, tab, read_at` | unread counts |
| `notifications_ttl` | `created_at` (TTL 180 d) | retention |

`notifications_unread` is not partial: `partialFilterExpression` can't express `$exists: false`. The count cap bounds the FETCH cost.

## 3. Writer invariants (for later cards)

- Every insert sets `created_at` — TTL skips documents without it, so they'd be kept forever — and `activity_at`.
- `user_id` = JWT `sub` (account ObjectID).
- `activity_at` is never in the future — "today" is `[local midnight, ∞)` and the feed sorts on it, so a future-dated row sits at the top.
- Un-read = `$unset: {read_at: ""}`, never `$set: {read_at: null}`.
- Upserts rely on `_id,omitempty` / `created_at,omitempty` + `$setOnInsert` (mongo.md §6).

Sanity check after any writer card:

```js
db.notifications.countDocuments({ created_at: { $exists: false } }) // must be 0
```

## 4. Retention runbook

Retention is `const retention` in `indexes.go` (Confluence §7 `RetentionDays`), deliberately not an env. Old and new pods both run `EnsureSpecs` at boot; a TTL mismatch → `IndexOptionsConflict` → crashloop. To change it:

1. Before the deploy, change the live index:
   ```js
   db.runCommand({ collMod: "notifications", index: { name: "notifications_ttl", expireAfterSeconds: <days * 86400> } })
   ```
2. Ship the code change with the same value.

## 5. Local decisions

| # | Decision | Why |
|---|---|---|
| D1 | Per-feature `feedCursor{activity_at, _id}`, strict parse, ms precision | sort key differs from the shared cursor (mongo.md §2) |
| D2 | `limit` ceiling = shared `SVC_PAGINATION_MAX_LIMIT`, enforced in Service | http.md §11 |
| D3 | Summary = 5 parallel counts, each capped at 100 | bounded cost per request |
| D4 | Stack members are `StackActor`; `Actor{type,id,via}` is for `*_by` | name clash between §2.2 and §12.1 |
| D8 | `buttons = []` when hidden; `is_read = read_at != nil` | Confluence Fig. 15 |
| D9 | `tz` IANA only; empty → UTC; week start = local midnight − 6 days | DST-safe |
| D10 | Retention is a const | a tunable TTL crashloops on change |
| D11 | JWT secrets required at boot | every route is authed; otherwise a silent 404 |
| D12 | `Cache-Control: private, no-store` on both responses | personalised data |

Deviations from `.claude/rules`: no `log.go` / `localization.go` yet (nothing logs, no feature code); request-shape rejects (limit, cursor, caller id) use `apperr.*` directly, only `tz` is a sentinel.
