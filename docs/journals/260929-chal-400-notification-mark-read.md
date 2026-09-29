# CHAL-400 Delivered — the first cross-service call, proven against the real server

**Date**: 2026-09-30 00:30  
**Severity**: Medium  
**Component**: notification (mark-read / read-all) + `infra/challengerclient`  
**Status**: Delivered

## What Happened

CHAL-400 shipped two endpoints:

- `POST /api/v1/notifications/{id}/read`. The first read sets `read_at` + `read_action`, any action hides the buttons, and navigate is resolved at read time.
- `POST /api/v1/notifications/read-all`. It marks both tabs read, leaves buttons alone, and writes one entry with a count.

It also adds the first cross-service client, `internal/infra/challengerclient`:

- It covers `GetChallenge` (5-min cache) and `CheckChallengeAccess` (live).
- Each call has a per-attempt deadline and retries on the transient codes.
- `ErrorInfo` is mapped to sentinels.
- It runs on the byte-for-byte `proto/challenger/internalv1` mirror, pinned in `proto/challenger/VERSION`.

Verification:

- Lint reports 0 issues and the race tests pass (the client tests run over bufconn with goleak). The per-file gate is OK.
- Total coverage is 54.8%. Generated `.pb.go` is counted, as in challenger; without it the total is 72.4%.
- An E2E run passed 32 checks against the **real challenger-service** in the `grpc` role, built from the pinned commit and fed seeded challenges, profiles and a whitelist. It covered:
  - All five navigate outcomes, 404 ownership, and `read_all` rejected as a tap action.
  - Read-all 7 → 1 entry, with IXSCAN on `notifications_unread`.
  - Caller metadata visible in challenger's access log.
  - Challenger down → 503 + `Retry-After` with the read kept, while a public card still resolves from the cache.
  - A wrong secret → 500.

## The Brutal Truth

A fake server would have passed everything. The value of the real one was proving the wire and the error contract end to end (`ErrorInfo` reasons, `deleted` vs `NOT_FOUND`, the whitelist collation path). The only surprise was seeding: challenger's `profiles` has a unique `user_code`, so a test profile without one collides on `null`.

## Technical Details

- **No network inside a transaction.** The read commits first (Mongo transaction + audit), then navigate is resolved (D23). A challenger outage therefore can't hold a transaction open or be re-run by the driver's transient retry. The user's tap is kept, and the client's retry writes no second entry because nothing changes.
- **Public vs private.** Every card goes through the cached `GetChallenge`; only non-public challenges pay for the live `CheckChallengeAccess`, whose `ended` wins over the cached `ends_at` (D24). An unknown status is treated like private, so a newer server can't open a private challenge by accident.
- **500 vs 503.** Only `ErrUnavailable` becomes 503. A rejected secret is config drift and must page as an error, not hide behind "retry later" (D25).
- **Stacking.** This branch sits on CHAL-402, which sits on CHAL-401, because all three cards edit the same service, handler and docs, and sibling branches would conflict on merge.

## Next Steps

- CI mirror check and the DevOps env values (see `implementation-plan.md`).
- Producers: `currentSchemaVersion`, Kafka ingest, Temporal lifecycle, push.
