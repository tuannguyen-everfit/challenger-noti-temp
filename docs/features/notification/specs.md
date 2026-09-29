# Notification — specs

**Source of truth:** Confluence page 3886907646 (Notification solution design + ACs). Mirror: challenger-service `docs/features/notification/spec.md`. When the two disagree, Confluence wins.

## What this service owns

The in-app notification centre for challenger users: a feed of cards in two tabs (`activities`, `system`) and the unread badge / tab dots / date-group counts.

## Delivered so far

| Card | Scope |
|---|---|
| CHAL-399 | `GET /api/v1/notifications` (cursor feed) + `GET /api/v1/notifications/summary` (badge, tab dots, group counts). Read-only. Defines the `Notification` model. |

## ACs owned by CHAL-399

| AC | Behaviour |
|---|---|
| 0.1 | Badge shows total unread; ≥ 100 renders as `99+` |
| 0.3 | Feed filters by tab (`all` / `activities` / `system`) |
| 0.4 | Groups: Today, This week (the previous 6 days), Earlier — by the device's time zone |
| 0.13 | Feed pages of 20 newest first; pull-to-refresh restarts from the top |
| Card | 45 rows → pages of 20 / 20 / 5; `limit` over max → 400; unknown `tz` → 400; wire never exposes `created_by`, `updated_by`, `source`, `push`, `stack`, `dedupe_key`, `user_id` |
| Fig. 15 | Buttons hidden once `buttons_hidden_at` is set; `is_read` from `read_at` |

## Not yet delivered

Mark read / read-all, device registration, account-deletion purge, audit log, producers (Kafka ingest, Temporal time-driven kinds), push delivery.
