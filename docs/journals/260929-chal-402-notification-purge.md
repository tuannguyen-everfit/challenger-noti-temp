# CHAL-402 Delivered — erasure is the one write that must not half-happen

**Date**: 2026-09-29 23:30  
**Severity**: Medium  
**Component**: notification (internal account-deletion purge)  
**Status**: Delivered

## What Happened

CHAL-402 shipped `DELETE /api/v1/internal/notifications/users/{user_id}`, called by the account-deletion job:

- It is guarded by `Internal-Secret` (`SVC_NOTIFICATION_INTERNAL_API_SECRET`). It is not mounted when the secret is empty, and boot fails when a set secret is under 32 bytes.
- One transaction does two `deleteMany` calls (notifications, devices) and writes one `purge` audit entry. A repeat call returns `{0, 0}` and writes nothing.

Verification:

- Lint reports 0 issues and the race tests pass. Coverage is 70.1% and the per-file gate is OK.
- A real-Mongo E2E run passed 22 checks: wrong secret, Bearer instead of the secret, a malformed or zero id, 12 + 2 rows purged with count 14, other users untouched, earlier entries kept, the idempotent repeat, the audit-validator rollback, IXSCAN on both deletes, and 404 without the secret.

## The Brutal Truth

The card says `/internal/notifications/users/{user_id}` and `NOTIFICATION_INTERNAL_API_SECRET`. This repo uses `SVC_*`, and challenger mounts its own internal routes under `/api/v1`. Following the card literally would have given the account-deletion job two different path shapes for the same job, so both names were adapted (D19, D20) and documented.

## Technical Details

- **The no-op rule became a function** (`auditIsNoOp`) once a second action needed it: `update` without changes and `purge` with count 0. The `exhaustive` linter forced every action to be listed, which keeps the next action from being forgotten.
- **Wire vs domain.** staticcheck S1016 flagged the field-by-field copy from `PurgeUserResult` to `PurgeUserResponse` because the two structs had the same shape. The domain fields were renamed (`Notifications`, `Devices`) instead of converting, so the wire seam stays explicit.
- **Test double.** The mock now tracks a notifications table, so rollback of the purge is asserted on rows, not on a flag.

## Next Steps

- DevOps sets the secret per env; the account-deletion job sends it together with `everfit-source`.
- BA / legal should confirm that the `purge` entry may keep the `user_id` (Q-G5).
