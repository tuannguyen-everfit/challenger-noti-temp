# Notification — test cases (CHAL-399)

All unit tests; integration tests are paused (testing.md §1).

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
