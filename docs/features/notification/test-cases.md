# Notification — test cases

All unit tests; integration tests are paused (testing.md §1). Real-Mongo E2E runs are recorded in the card's PR.

## CHAL-399 — feed + summary

| AC / requirement | Test |
|---|---|
| 45 rows → 20 / 20 / 5, `has_more` t/t/f, last `next_cursor` empty, cursor = last row of the page | `service_test.go::TestListFeed_PagesOf20` |
| Tab filter + user isolation passed to the repo | `TestListFeed_PassesTabAndUser`, `handler_test.go::TestList_PassesUserTabAndCursor`, `TestList_DefaultTabIsAll` |
| Isolation filter always carries `user_id`; tab only when ≠ all; strict `$lt` cursor | `repository_mongo_test.go::TestBuildFeedFilter`, `TestBuildUnreadFilter`, `TestBuildRangeFilter` |
| `limit` over `SVC_PAGINATION_MAX_LIMIT` → 400, repo not called | `TestListFeed_LimitOverMax` |
| `limit=0` / absent → default 20 (clamped to max) | `TestListFeed_ZeroLimitUsesDefault`, `TestListFeed_ZeroLimitUsesDefaultClamped`, `TestList_ZeroLimitPassesThrough` |
| Negative limit / bad tab → 400 | `TestList_NegativeLimit`, `TestList_InvalidTab` |
| Bad cursor (garbage, non-hex id, zero id, zero time) → 400; ms precision | `TestParseFeedCursor`, `TestList_BadCursor` |
| Missing / non-ObjectID / zero `sub` → 401 | `TestList_NoUser`, `TestList_ZeroObjectIDUser`, `TestSummary_NoUser` |
| `items: []` never null; no `next_cursor` when done | `TestListFeed_EmptyItemsNotNil`, `TestList_EmptyItemsArray` |
| Hidden buttons → `[]`; `is_read`; id hex; image / navigate mapped | `TestList_MapsItems` |
| No audit / internal fields on the wire | `TestList_NoAuditFieldsOnWire` |
| Internal errors → 500 envelope, no leak | `TestList_ServiceError` |
| `Cache-Control: private, no-store` | `TestList_CacheControl`, `TestSummary_CacheControl` |
| Unread 2 activities + 1 system → total 3 | `TestSummary_UnreadCounts` |
| Saigon day boundaries (23:30 UTC yesterday = today in +07) | `TestSummary_SaigonBoundaries` |
| Week boundary across DST is local midnight 6 days back | `TestSummary_DSTWeekBoundary` |
| Groups follow `tab`; unread always counts both tabs | `TestSummary_GroupsUseTab_UnreadDoesNot` |
| Repo error propagates | `TestSummary_RepoErrorPropagates`, `TestListFeed_RepoErrorPropagates` |
| `tz`: "" → UTC; IANA ok; `Local` / unknown / offset → 400 | `TestParseTimezone`, `TestSummary_UnknownTZ`, `TestSummary_DefaultTZIsUTC` |
| Response shape | `TestSummary_MapsShape` |
| Routes sit behind Bearer auth | `internal/platform/httpx/router_test.go::TestNewRouter_Notifications_RequireBearer`, smoke probe |
| Boot fails without JWT secrets | `internal/platform/config/config_test.go::TestValidate_Guards` |

## CHAL-401 — devices + audit

The service tests run against `mockRepo`: an in-memory device table that enforces both unique indexes, and a `WithTransaction` that restores devices and audit entries when the callback fails, so rollback is asserted on state.

| AC / requirement | Test |
|---|---|
| New device → row (`created_*`, `updated_*`, `last_registered_at` = now, actor user/api) + one `create` entry with `request_id`, `trace_id` | `service_test.go::TestRegisterDevice_CreatesRowAndAudit` |
| New token → same row, `updated_at` moves, `created_at` kept; one `update` entry `{app_version: {from, to}, token: {changed: true}}`; no token bytes in any entry | `TestRegisterDevice_NewTokenUpdatesSameRow` |
| Platform change recorded as `{from, to}` | `TestRegisterDevice_PlatformChangeRecorded` |
| Identical PUT → `last_registered_at` only, `updated_at` unchanged, no entry | `TestRegisterDevice_IdenticalPutOnlyTouches` |
| Token held by another account → that row deleted before the upsert; `delete` entry (owner = evicted user, actor = caller) + `create` | `TestRegisterDevice_TokenHeldByAnotherAccountIsEvicted` |
| Token moved from the caller's old install → old row removed | `TestRegisterDevice_TokenMovedFromCallersOtherDevice` |
| Audit failure → error, rows untouched (eviction rolled back too) | `TestRegisterDevice_AuditFailureRollsBack`, `TestRemoveDevice_AuditFailureRollsBack` |
| Repo errors propagate, no entry | `TestRegisterDevice_RepoErrorPropagates`, `TestRemoveDevice_RepoErrorPropagates` |
| DELETE existing → removed + `delete` entry | `TestRemoveDevice_DeletesAndAudits` |
| DELETE unknown / another user's device → `false`, nothing removed, no entry | `TestRemoveDevice_UnknownDeviceIsNoOp` |
| Allow-list drops unknown fields; `token` is masked whatever the caller passes | `TestPickAuditChanges` |
| Update with no allow-listed change writes nothing | `TestRecordAudit_SkipsNoOpUpdate` |
| Duplicate `audit_key` = already recorded → success | `TestRunAudited_DuplicateAuditKeyIsSuccess` |
| Upsert doc: `created_*` only in `$setOnInsert`; empty `app_version` is `$unset`; filter on `(user_id, device_id)` | `repository_mongo_test.go::TestBuildDeviceUpsert`, `TestBuildDeviceKeyFilter` |
| Stored `changes` shape (`""` kept, nil / false omitted) | `TestFieldChange_BSON` |
| PUT maps input; token never on the wire; `Cache-Control` | `handler_test.go::TestRegisterDevice_PassesInputAndHidesToken`, `TestRegisterDevice_AppVersionOptional` |
| `platform=web`, missing / empty token, `device_id` > 128, unknown field, no body → 400, service not called | `TestRegisterDevice_InvalidRequest`, `TestRemoveDevice_InvalidRequest` |
| No caller → 401; service error → 500 without leaking | `TestRegisterDevice_NoUser`, `TestRemoveDevice_NoUser`, `TestRegisterDevice_ServiceError`, `TestRemoveDevice_ServiceError` |
| DELETE → `{removed}` | `TestRemoveDevice_ReportsRemoved` |
| `/devices` behind Bearer; Idempotency never runs before auth | `internal/platform/httpx/router_test.go::TestNewRouter_Devices_RequireBearer`, `TestNewRouter_IdempotencyRunsAfterBearer` |
| Replay key scoped by user + method + path | `middleware/idempotency_test.go::TestIdempotency_KeyScopedByUserMethodAndPath` |
| `request_id` reachable from ctx | `middleware/requestid_test.go::TestRequestID_StampsContext`, `TestRequestIDFromContext_Absent` |
| Transaction helper runs fn with a session ctx and returns fn's error | `internal/infra/mongox/client_test.go::TestWithTransaction_RunsFnWithSessionContext`, `TestWithTransaction_ReturnsFnError` (offline client) |

## CHAL-402 — internal purge

| AC / requirement | Test |
|---|---|
| 12 notifications + 2 devices → `{12, 2}`; one `purge` entry (entity `user`, count 14, actor `service/<caller>/internal_api`); earlier entries kept; other user untouched | `service_test.go::TestPurgeUser_DeletesBothAndAudits` |
| Second call → `{0, 0}`, no second entry | `TestPurgeUser_SecondCallIsNoOp` |
| Audit failure → nothing deleted | `TestPurgeUser_AuditFailureDeletesNothing` |
| Repo errors propagate and roll back | `TestPurgeUser_RepoErrorPropagates` |
| A purge that deleted nothing writes no entry | `TestRecordAudit_SkipsEmptyPurge` |
| `user_id` + `everfit-source` passed through; zero counts on the wire | `handler_test.go::TestPurgeUser_PassesUserAndCaller`, `TestPurgeUser_CallerOptional` |
| Non-hex / short / zero `user_id`, caller > 64 → 400, service not called | `TestPurgeUser_InvalidRequest` |
| Service error → 500 without leaking | `TestPurgeUser_ServiceError` |
| Secret unset → 404; missing / wrong secret → 401; right secret reaches the handler without Bearer | `router_test.go::TestNewRouter_InternalPurge_NotMountedWithoutSecret`, `TestNewRouter_InternalPurge_RequiresSecretNotBearer` |
| Secret from env; empty by default; < 32 bytes fails boot | `config_test.go::TestLoad_Notification_InternalSecret`, `TestValidate_Guards` |
