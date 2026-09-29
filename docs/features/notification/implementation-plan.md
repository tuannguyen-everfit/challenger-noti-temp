# Notification — implementation plan

Card order (each card lands its own PR):

1. **CHAL-399** — feed + summary (read-only), `Notification` model, indexes, auth fail-fast. Plan: [plans/260929-1458-chal-399-notification-feed-summary](../../../plans/260929-1458-chal-399-notification-feed-summary/plan.md). ✅
2. Devices — push-token registration.
3. Read / read-all — first writer; adds `log.go`, `localization.go`, audit entries, `currentSchemaVersion`.
4. Account-deletion purge (hard delete + audit entry).
5. Producers — Kafka ingest (N4, N6, N1 deferred) and Temporal time-driven kinds (N2, N3, N5, N7, N8), push delivery.

Follow-ups raised by CHAL-399:

- Verify-only access-token verifier (this service should not need the refresh secret) + `iss` / `aud` checks.
- challenger-service solution-design §12.2: drop "replaces created_by" from the `Source` comment.
- Confluence §2.2: `Stack.Actors []Actor` → `[]StackActor`.
- JWT secret strength guard outside `local` (≥ 32 bytes, reject dev values) — `config.validate` only checks non-empty today.
- Before the read / read-all POSTs land: the Idempotency middleware runs before `BearerAuth` and keys by `Idempotency-Key` only — scope the cache key by user (or move it inside the authed group) or a replayed key returns another user's cached response.
