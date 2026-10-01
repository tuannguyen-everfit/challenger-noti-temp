# Notification — specs

**Source of truth:** Confluence page 3886907646 (Notification solution design + ACs). Mirror: challenger-service `docs/features/notification/spec.md`. When the two disagree, Confluence wins.

## What this service owns

The in-app notification centre for challenger users: a feed of cards in two tabs (`activities`, `system`), the unread badge / tab dots / date-group counts, and the push-device registry (one FCM token per user device).

## Delivered so far

| Card | Scope |
|---|---|
| CHAL-399 | `GET /api/v1/notifications` (cursor feed) + `GET /api/v1/notifications/summary` (badge, tab dots, group counts). Read-only. Defines the `Notification` model. |
| CHAL-401 | `PUT` / `DELETE /api/v1/devices/{device_id}` (push-token registration). First writer: `notification_devices`, `notification_audit_logs`, the transaction + audit helper, user-scoped idempotency. |

## ACs owned by CHAL-399

| AC | Behaviour |
|---|---|
| 0.1 | Badge shows total unread; ≥ 100 renders as `99+` |
| 0.3 | Feed filters by tab (`all` / `activities` / `system`) |
| 0.4 | Groups: Today, This week (the previous 6 days), Earlier — by the device's time zone |
| 0.13 | Feed pages of 20 newest first; pull-to-refresh restarts from the top |
| Card | 45 rows → pages of 20 / 20 / 5; `limit` over max → 400; unknown `tz` → 400; wire never exposes `created_by`, `updated_by`, `source`, `push`, `stack`, `dedupe_key`, `user_id` |
| Fig. 15 | Buttons hidden once `buttons_hidden_at` is set; `is_read` from `read_at` |

## ACs owned by CHAL-401

| AC | Behaviour |
|---|---|
| Create | A new device writes the row and a `create` audit entry in the same transaction |
| New token | Re-registering with a new token updates the same row (no duplicate); the entry records the changed fields, never the token value |
| Refresh | An identical PUT moves `last_registered_at` only; no entry |
| Token moved | A token held by another account removes that row and writes a `delete` entry |
| Unknown delete | `DELETE` of an unknown device → `removed: false`, no entry |
| Rollback | An audit failure rolls the write back (5xx) |
| Validation | `platform=web`, empty token, `device_id` > 128 chars → 400 |
| DoD | Tokens are never logged, audited or echoed |

## Not yet delivered

Mark read / read-all, account-deletion purge, producers (Kafka ingest, Temporal time-driven kinds), push delivery.
