# CHAL-401 Delivered — the first writer, and a smoke check that lied

**Date**: 2026-09-29 22:30  
**Severity**: Medium  
**Component**: notification (devices + audit log + transaction helper)  
**Status**: Delivered

## What Happened

CHAL-401 shipped `PUT` / `DELETE /api/v1/devices/{device_id}` and the write-side infrastructure every later card reuses:

- `mongox.Client.WithTransaction`, which wraps the driver's `Session.WithTransaction`.
- The `notification_audit_logs` collection and its four indexes.
- `Service.runAudited` + `recordAudit`, which handle the allow-list, token masking, skipping no-ops, the request_id / trace_id stamps and duplicate `audit_key` detection.
- `notification_devices` with unique `(user_id, device_id)` and unique `token`.

It also closes the CHAL-399 follow-up: Idempotency now runs after `BearerAuth`, with the key scoped to user + method + path.

Verification:

- Lint reports 0 issues, the race tests pass and total coverage is 69.5%. The per-file gate is OK, and `make smoke` is OK, including the integration gate.
- A throwaway real-Mongo E2E run passed 34 checks. It covered create, refresh, new token, a token moving between accounts, delete, validation, cross-user idempotency, and rollback. The rollback check forced audit inserts to fail with a collection validator, so the device write had to roll back.

## The Brutal Truth

The first E2E run printed **PASS** for checks that never ran. In bash, `"$(mq "…{a: 1, b: 2}…")"` gets brace-expanded into two command substitutions. Both mongosh calls fail with a SyntaxError and print nothing, and `"" == ""` passes.

The fix was a `check_q` helper that takes the query as one quoted argument and treats an empty result as a failure. Only the second run counts.

## Technical Details

- **Order matters inside the transaction.** The unique token index rejects the upsert unless the old holder is deleted first. The unit mock enforces both unique indexes, so eviction-after-upsert fails the tests. It also snapshots devices and audit entries in `WithTransaction`, so rollback is asserted on state rather than on a flag.
- **A refresh-only PUT** (nothing changed) goes through `TouchDevice` instead: `last_registered_at` moves, `updated_at` stays, and no audit entry is written.
- **Token masking** lives in the allow-list (`auditFieldMasked`). Even a caller that passes the token value gets `{changed: true}` stored.
- **mongox coverage.** `client.go` fell below the integration gate's 30% once `WithTransaction` landed. The fix was an offline unit test: a transaction with no operations never contacts the server. It uses goleak via a new `main_test.go`.
- **Audit TTL** is a const equal to `retention` (D13), not the card's env. Same crashloop reasoning as D10.

## Lessons Learned

- In a shell check, an empty result on both sides is not a pass. Assert on non-empty output.
- A rollback test needs a mock that can actually roll back. A counter on "tx aborted" would pass even if the data write had happened.

## Next Steps

- CHAL-400 (read / read-all) and CHAL-402 (purge) stack on this branch and reuse `runAudited` / `recordAudit`.
- Follow-ups are in `implementation-plan.md`: the stale-token cleanup + FCM `UNREGISTERED` path, and BA confirmation for keeping the evicted owner's `user_id`.
