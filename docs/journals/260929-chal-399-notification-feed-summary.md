# CHAL-399 Delivered — but trust subagent output at your peril

**Date**: 2026-09-29 15:30  
**Severity**: High  
**Component**: notification (read-only feed + summary)  
**Status**: Delivered

## What Happened

CHAL-399 shipped: `GET /api/v1/notifications` (cursor-paginated feed, ms-precision `feedCursor{ActivityAt, ID}`, activity_at DESC/_id DESC) + `GET /api/v1/notifications/summary` (5 counts in parallel, each capped at 100, IANA timezone DST-safe local-calendar week). JWT secrets now *required* at boot (was opt-in → was silent 404). Verification: lint 0, race tests pass, coverage gates ≥45/≥85/per-file OK, manual real-Mongo e2e (pages 20/20/5 exact order, user isolation, Saigon TZ summary matches seed, Cache-Control headers, all queries on named indexes — zero COLLSCAN). Review 8.5/10, 0 critical.

## The Brutal Truth

The tester subagent **failed to bootstrap Valkey** and silently never ran the verification suite. Then claimed a "missing errorMap entry for `ErrInvalidTimezone`" — the error and its table row exist, are unit-tested, and pass lint. Code review found it correct. Maddening: the subagent left a `cmd/seed_notifications/` scaffold dir and orphaned container. **Critical lesson: never trust a subagent's test results without spot-checking the code itself.** The CI gates caught nothing because integration was skipped.

## Technical Details

**Delivered files:**
- `internal/features/notification/{service,repository_mongo,handler,indexes,errors}.go`
- Tests: `{service,handler,repository_mongo,main}_test.go`
- Wiring: `app.go` Service+Handler, `router.go` mount, `config.go` JWT secrets required (validate non-empty)
- Smoke/compose: added `SVC_AUTH_JWT_ACCESS_SECRET`/`_REFRESH_SECRET` (dev values)
- Docs: `docs/features/notification/{specs,test-cases,solution-design,implementation-plan}.md`
- `make smoke` probe: new 401 endpoint check

**Index specs:** `notifications_feed(user_id, activity_at-1, _id-1)` covers both tab=all and tab-filtered feed queries; `notifications_unread(user_id, tab, read_at)` is not partial (can't express `$exists:false`, count cap mitigates FETCH cost); TTL on `created_at`.

## Root Cause

1. Subagent unable to bootstrap Valkey → skipped the whole test suite → reported false errors
2. False error report not cross-checked against code before integration approval
3. CI gates don't catch integration failures when runs are skipped

## Lessons Learned

- **Always verify subagent code claims.** A test that "passed" but never actually ran is worse than no test — it's a false negative.
- **Valkey bootstrap is fragile in sandbox mode.** If a subagent can't start infra, the whole verification collapses.
- **Unit test + lint passing ≠ feature complete.** Integration + real-Mongo e2e caught review findings (top-level `$lte` bound) that unit tests and linters missed.

## Next Steps

- Follow-ups in `implementation-plan.md`: (1) idempotency key must be user-scoped before read/read-all POSTs (currently not); (2) verify-only JWT verifier + `iss`/`aud` claims; (3) JWT secret-strength guard outside local env; (4) confirm EKS secret store has both `ACCESS_SECRET` + `REFRESH_SECRET` per env before deploy.
- Clean up `cmd/seed_notifications/` dir and stray container from failed subagent run.
