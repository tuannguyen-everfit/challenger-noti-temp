# Notification — implementation plan

Card order (each card lands its own PR):

1. **CHAL-399** — feed + summary (read-only), `Notification` model, indexes, auth fail-fast. Plan: [plans/260929-1458-chal-399-notification-feed-summary](../../../plans/260929-1458-chal-399-notification-feed-summary/plan.md). ✅
2. **CHAL-401** — devices (push-token registration) + the shared audit / transaction helper + user-scoped idempotency. ✅
3. **CHAL-400** — read / read-all; adds `log.go`, `localization.go`, notification audit entries and the challenger gRPC client (`internal/infra/challengerclient` + the `proto/challenger/internalv1` mirror). ✅ (`currentSchemaVersion` moves to the producers card: nothing here inserts rows.)
4. **CHAL-402** — account-deletion purge (hard delete + `purge` entry). ✅
5. Producers — Kafka ingest (N4, N6, N1 deferred) and Temporal time-driven kinds (N2, N3, N5, N7, N8), push delivery.

Follow-ups raised by CHAL-399:

- Verify-only access-token verifier (this service should not need the refresh secret) + `iss` / `aud` checks.
- challenger-service solution-design §12.2: drop "replaces created_by" from the `Source` comment.
- Confluence §2.2: `Stack.Actors []Actor` → `[]StackActor`.
- JWT secret strength guard outside `local` (≥ 32 bytes, reject dev values) — `config.validate` only checks non-empty today.
- ~~Idempotency before `BearerAuth`, global key~~ — fixed in CHAL-401 (D15).

Follow-ups raised by CHAL-401:

- Stale-token cleanup job (reads `last_registered_at`) and FCM `UNREGISTERED` → `DeleteDevice` + `delete` entry land with push delivery.
- Confirm with BA that keeping the evicted owner's `user_id` on the eviction entry is fine (same question as upstream Q-G5 for purge).

Follow-ups raised by CHAL-402:

- DevOps: add `SVC_NOTIFICATION_INTERNAL_API_SECRET` (≥ 32 bytes) per env and give the account-deletion job the same value + `everfit-source`.
- Confirm with BA / legal that the `purge` entry may keep the purged `user_id` (upstream Q-G5).

Follow-ups raised by CHAL-400:

- CI: run `make proto-mirror-check` against a challenger-service checkout at the pinned commit (needs read access to that repo in CI).
- DevOps: `SVC_CHALLENGER_GRPC_ADDR` (`dns:///challenger-internal-grpc.<ns>.svc.cluster.local:7992`) + `SVC_CHALLENGER_GRPC_SECRET` (= challenger's `CHALLENGER_GRPC_INTERNAL_SECRET`) per env.
- Circuit breaker (upstream §5.2) is for the worker fan-out; the API path relies on retries + 503 today.

