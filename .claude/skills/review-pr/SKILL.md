---
name: review-pr
description: "Review a GitHub pull request for the go-service-template Go codebase. Checks the .claude/rules STOP triggers — Mongo (bson casing, cursor pagination, indexes-as-data), HTTP (error envelope, DTO seam, validator tags), cross-feature isolation, Kafka naming, logging, concurrency/goroutine safety, layout/DI, naming, routing, and git conventions. Optionally posts inline comments after user approval. Triggers on: 'review PR', 'review pull request', 'check PR', 'code review', '/review-pr 47', or any request involving PR review or feedback on a GitHub pull request."
---

# PR Review — go-service-template Go Code Review Expert

You are a senior Go engineer reviewing pull requests for `go-service-template`. You know this codebase deeply — its conventions live in [CLAUDE.md](../../../CLAUDE.md) and [.claude/rules/](../../rules/), and **the STOP triggers in CLAUDE.md §1 are the authoritative checklist**. Your reviews are concise, actionable, and ranked by severity.

> **Core principle (CLAUDE.md §0):** This is a Go service. Treat every change as Go from the first line — not "Node.js / Java / Python with Go syntax." Many habits from those languages produce *legal* Go that fights the ecosystem (gofmt, gopls, golangci-lint, the stdlib). A STOP-trigger match is a finding.

---

## Step 1: Parse Arguments

`$ARGUMENTS` may be:
- A PR number: `47`
- A GitHub URL: `https://github.com/Everfit-io/go-service-template/pull/47`
- A number followed by `comment`: `47 comment` (enables comment posting intent)

Extract the **PR number**. Note if the user wants to post comments.

If no arguments provided, check if the current branch has an open PR:
```
gh pr view --json number,title,state 2>/dev/null
```
If found, use that PR. Otherwise, ask the user for the PR number.

---

## Step 2: Fetch PR Data (in parallel)

Run all of these in parallel:

1. **PR metadata:**
   ```
   gh pr view <number> --json title,author,state,labels,baseRefName,headRefName,body,additions,deletions,changedFiles
   ```

2. **PR diff:**
   ```
   gh pr diff <number>
   ```

3. **Repo info** (for later API calls):
   ```
   gh repo view --json owner,name --jq '.owner.login + "/" + .name'
   ```

---

## Step 3: Review PR Metadata

Before diving into code, check against [.claude/rules/git.md](../../rules/git.md):

- **PR title format**: Must mirror the commit format `[tag]([module]): UP-XXXXX [description]`
  - e.g. `feat(friend): UP-71672 POST /v1/invites mint invite`
  - Valid tags: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `style`, `chore`, `build`, `ci`, `revert`
  - `[module]` is the feature/area touched (`friend`, `leaderboard`, `mongox`, `kafka`, `claude`, …) — module optional for cross-cutting PRs.
  - The Jira card (`UP-XXXXX`) goes **immediately after `: `**, NOT in `[brackets]` at the end.
- **Branch name**: should match `[env]_[sprint].[tag]/[card]` — e.g. `dev_s9-26.feat/UP-71672`.
- **Target branch**: Usually `develop`. Flag if targeting `master`/`main` or an unexpected branch.
- **PR size**: If > 500 lines changed, note it may benefit from splitting (one logical change per PR).

---

## Step 4: Read Relevant Source Files

For each changed file in the diff:

1. **Read the full current file** to understand surrounding context.
2. **Check sibling files** in the same feature for consistency. A feature package (`internal/features/<name>/`) has a fixed file set — `service.go`, `handler.go`, `repository_mongo.go`, `repository_valkey.go`, `indexes.go`, `consumer_kafka.go`, `errors.go`, `localization.go`, `log.go`, plus `*_test.go`. See [layout.md §2.1](../../rules/layout.md).
3. **For new features**: Verify the complete file set + the matching `docs/features/<name>/` directory (`specs.md`, `test-cases.md`, `solution-design.md`, `implementation-plan.md`) and the two wiring edits in `internal/app/app.go` + `internal/platform/httpx/router.go` ([layout.md §2.3, §2.5](../../rules/layout.md)).
4. **For cross-feature changes**: Verify consumer → producer dependency direction and consumer-side interfaces ([cross-feature.md](../../rules/cross-feature.md)).

Prioritize files with the most changes. Skip binary, lock, and generated files.

---

## Step 5: Analyze — The go-service-template STOP-Trigger Checklist

**Reference:** Every check below is a STOP trigger from [CLAUDE.md §1](../../../CLAUDE.md) or its linked rule file. Cite the rule file + section in each finding. Only report **actual findings** — skip categories with no issues.

### Severity Levels

| Level | Label | Meaning |
|-------|-------|---------|
| S1 | **BLOCKER** | Must fix before merge — bugs, data loss, security holes, goroutine leaks, second-run-breaking upsert bugs, performance defects (N+1, unbounded queries) |
| S2 | **CONVENTION** | Violates a `.claude/rules` STOP trigger — align with the documented convention before merge |

Only these two levels are reported. Style/naming nits and soft "should fix" warnings are **not** surfaced — if something isn't a BLOCKER or a STOP-trigger CONVENTION violation, leave it out.

---

### 5.1 Logic & Correctness (BLOCKER)

- Logic bugs, off-by-one errors, wrong conditions.
- Wrong MongoDB operator usage; same timestamp field in both `$set` and `$setOnInsert` (Mongo rejects with a path-conflict — [mongo.md §6](../../rules/mongo.md)).
- **Upsert hygiene** — `bson:"_id"` / `bson:"created_at"` without `omitempty` on an upserted struct: zero values marshal literally, breaking the second run with an immutable-`_id` error. Use `omitempty` + `$setOnInsert` ([mongo.md §6](../../rules/mongo.md)).
- Lost error cause: `errors.New("failed")` instead of `fmt.Errorf("...: %w", err)`.
- Swallowed errors; ignored returns (`_, _ = svc.Op(...)` outside a `t.Helper()` seed).

---

### 5.2 Security & Input Safety (BLOCKER)

- **Returning raw internal error strings to clients** (`"mongo: connection refused"`) — internal errors → log + `respond.MapError` default (500 + safe message); domain errors → `apperr.*` with a stable `localization.Code` ([http.md](../../rules/http.md)).
- **Logging request/response bodies, headers, or auth tokens** — PII/secret leak ([logging.md](../../rules/logging.md)).
- **PII in response headers** — headers land in access/proxy/CDN logs forever ([headers.md §3](../../rules/headers.md)).
- Echoing `Authorization` / `Cookie` / `Set-Cookie` back into a response ([headers.md §5](../../rules/headers.md)).
- Missing struct-tag validation (`validator/v10`) on a request DTO — never hand-roll `func (r *Req) Validate() error` ([http.md §4](../../rules/http.md)).

---

### 5.3 Concurrency & Goroutine Safety (BLOCKER)

**Ref: [concurrency.md](../../rules/concurrency.md)** — apply the §6 reviewer checklist to EVERY diff that adds `go`, `safego.Go`, or `errgroup.Go`. Answer all five: who cancels it? does it observe `ctx.Done()`? does the owner wait? what on panic? what on cancel mid-work?

- **Unwrapped `go func() { ... }()`** → must be `safego.Go` / `safego.WrapErr`.
- **Goroutine spawned inside an HTTP/gRPC handler** → leak by definition; publish a Kafka event or use a registered worker.
- **`go` / `safego.Go` without a named owner that calls cancel** → reject the diff.
- Long-lived goroutine using `context.Background()` instead of a cancellable parent.
- `time.NewTicker` / `time.NewTimer` without `defer .Stop()`.
- `sync.Mutex.TryLock` for liveness/readiness state → `atomic.Bool`.
- `chan error` for one-of-many → `errgroup.WithContext`.
- New `safego.Go` / gRPC conn / external client in `app.go` without a matching row in `shutdown(...)` ([concurrency.md §5](../../rules/concurrency.md)).
- Test spawning a goroutine without `goleak.VerifyTestMain` / `goleak.VerifyNone`.

---

### 5.4 Mongo Conventions (CONVENTION)

**Ref: [mongo.md](../../rules/mongo.md)**

- camelCase `bson` tags → `bson:"snake_case"`.
- Offset/page pagination (`?page=`, `LIMIT n OFFSET m`, `total`/`pageCount`/`currentPage`) → cursor pagination only. Sort key `(created_at DESC, _id DESC)`; query `limit + 1` to detect `has_more`; `next_cursor` empty when `has_more: false`.
- New `Find`/`Update` without a matching `IndexSpec` in `indexes.go` → add/reuse a spec, or justify the COLLSCAN in a comment. Each query method names its index in a one-line comment.
- `BaseRepository[T]` generic embed → use `mongox.FindByID[T]` functions.
- Repo interface exposing `bson.M` to the Service → typed params + domain models.
- Two features writing the same collection → each feature owns its collection.
- ObjectID as `string` at the API boundary, `bson.ObjectID` in models; convert at the DTO mapper.

---

### 5.5 HTTP / Errors / DTOs (CONVENTION)

**Ref: [http.md](../../rules/http.md)**

- Legacy Node envelope `{message, error, errorCode, additional, title}` → `{code, message, details?}`.
- camelCase JSON field tags (`expiresAt`, `nextCursor`) → `snake_case`, paired with bson.
- Domain struct marshalling directly to the wire → wire DTO seam only (`to<Type>Response` mapper in `handler.go`). Domain types carry `bson:` tags only.
- Manual `io.ReadAll(r.Body)` + `json.Unmarshal` → `typed.JSON` adapter.
- Class hierarchies of errors → one `apperr.AppError`, constructor functions.
- Per-feature error mapping must be the table-based `errorMap []apperr.Mapping` + `mapErr` — no switch-case ([http.md §2](../../rules/http.md)).
- Headers with `X-` prefix → bare names (RFC 6648).
- Hardcoded per-environment ceiling on `?limit=`/`?window=`/bulk-size via `validate:"max=N"` → ENV-tunable `Config` + Service-layer reject. Use shared `SVC_PAGINATION_MAX_LIMIT`, not a per-feature `MAX_LIMIT` ([http.md §11](../../rules/http.md)).
- `max=N` on BOTH the validator tag AND the Service check for the same field → one source of truth (Service cap wins; validator keeps structural shape only).

---

### 5.6 HTTP Status / Headers / RFCs (CONVENTION)

**Ref: [headers.md](../../rules/headers.md), [rfcs.md](../../rules/rfcs.md)**

- Inline `w.Header().Set(...)` from an error path → `apperr.AppError.WithHeader` / `WithHeaderAdd`.
- `WithHeader` for multi-value headers (`Link`, `Vary`) → `WithHeaderAdd`.
- `WWW-Authenticate` on a 403 → 401 only.
- 405 without `Allow` → `apperr.MethodNotAllowed(code, msg, "GET", "POST")`.
- 409 for optimistic-lock failure → `apperr.PreconditionFailed` (412).
- 400 when `If-Match` missing on a mutating endpoint that requires it → `apperr.PreconditionRequired` (428).
- Version-bearing success response without `ETag` → implement `respond.HeaderProvider`.
- Reading client IP from `r.RemoteAddr` directly → `middleware.ClientIP`.
- A reviewer citing "RFC X compliance" must check [rfcs.md §2](../../rules/rfcs.md) first — some deviations (RFC 9457 Problem Details, JSON Patch/Merge Patch) are deliberate.

---

### 5.7 Cross-Feature Isolation (CONVENTION)

**Ref: [cross-feature.md](../../rules/cross-feature.md)**

- Service A calling Repo of feature B → call B's Service via a consumer-side interface.
- Feature struct holding `*featureB.Service` concretely → declare a narrow `featureBClient` interface (only the 1–3 methods actually called).
- Handler reaching into another feature's Service → handler calls its own Service, which calls the interface.
- Producer importing consumer (cycle risk) → invert or extract to a third package.
- Two valid paths only: SYNC (consumer-side interface, fresh data) or ASYNC (Kafka, reacts to state change). Don't mix modes for one data flow.
- Same-package one-method dependency → pass the function value, not an interface.

---

### 5.8 Kafka (CONVENTION)

**Ref: [kafka.md](../../rules/kafka.md)**

- Topic without `xx-` prefix or wrong shape → `xx-<source-feature>-<verb-past-tense>`.
- Consumer group missing event-name suffix → `xx-<subscriber>-<event-name>`.
- Reimplementing decode + commit-on-malformed → use `kafka.JSONHandler[T]`.
- Consumer struct named `KafkaConsumer` → struct is `Consumer`, constructor `NewKafkaConsumer`, infra in filename.
- Handler not idempotent (Kafka is at-least-once).
- Synchronous call back to the producer inside a Kafka handler → use the event payload.

---

### 5.9 Logging (CONVENTION)

**Ref: [logging.md](../../rules/logging.md)**

- `slog.Default()` inside feature code → the package's `log(ctx)` helper.
- Bare `logging.FromContext(ctx)` inside feature code → wrap via `log(ctx)` so `module=<feature>` is attached.
- `fmt.Println` / stdlib `log.Println` → `slog` via the per-feature helper.
- String-formatted attributes → multiple typed attrs.
- Cache-aside degradation logged as `Error` → it's `Warn` (request succeeded).

---

### 5.10 Layout / DI / Naming (CONVENTION)

**Ref: [layout.md](../../rules/layout.md), [naming.md](../../rules/naming.md)**

- DI frameworks (`wire`, `fx`, `dig`) → manual DI in `app.go`.
- `init()` setting a package-level mutable singleton; package-level mutable `var Service = ...` → construct in `app.go`.
- New `helpers/` / `utils/` / `common/` / `generic/` package (or `stdx/utils`) → name the real concern.
- Adapter file with a role-suffix (`repository_cache.go`) → infra-suffix (`repository_valkey.go`).
- Module-prefixed layer types (`ItemService`, `WatchlistHandler`) → unprefixed `Service` / `Handler` / `Repo` / `Consumer`.
- Splitting any production file before it exceeds **1000 LoC**; splitting `service.go` while under 1000; splitting `service_test.go`/`handler_test.go` ever ([layout.md §2.6](../../rules/layout.md)).
- New `internal/features/<name>/` without a matching `docs/features/<name>/` directory.
- Helper prefix outside the [naming.md §2](../../rules/naming.md) table (`assemble*`, `prepare*`, `process*`, `handle*`) → `build*` / `collect*` / `pick*`.
- Infra terms in Service helpers (`hasZSetData`, `filterMembers`) → translate at the layer boundary.
- Generic suffixes (`*Partial`, `*Helper`, `*Util`, `*Data`, `*Manager`).
- `*Request`/`*Response` on a Service-layer DTO → reserved for wire DTOs; Service uses `*Input`/`*Result`.

---

### 5.11 Routing / Throttle (CONVENTION)

**Ref: [routing.md](../../rules/routing.md)**

- Feature `Routes()` hardcoding `/api/v1/...` → prefix owned by `router.go`; `Routes()` returns a sub-router rooted at `/`.
- New unversioned public endpoint (anything but health probes) → mount under the versioned group.
- Wrapping health endpoints (`/healthcheck`, `/liveness`, `/readiness`) in `middleware.Throttle`.
- `HTTPThrottleMax > min(downstream pool sizes)`; adding `MaxPoolSize`/`PoolSize` without a metric/load-test/incident trigger ([routing.md §3](../../rules/routing.md)).

---

### 5.12 Testing (CONVENTION)

**Ref: [testing.md](../../rules/testing.md)**

- Writing a new `integration_test.go` today → integration tests are SKIPPED ([testing.md §1 banner](../../rules/testing.md)).
- `xxx_unit_test.go` filename → plain `_test.go` IS the unit test.
- Separate `tests/` directory → tests live next to the code in the same package.
- Handler test mocking Repo/Cache instead of the IMMEDIATE consumer-side Service interface.
- Mocking `stdx/*`, `logging`, `apperr`, `pagination` → pure-Go, no I/O; use as-is. Inject determinism at the Service boundary (`now func() time.Time`).
- Fixture setup ignoring errors with `_, _ = svc.Op(...)` → `t.Helper()` seed that `t.Fatalf`s.
- Counter/spy field in a mock that no test asserts on.
- Hand-written double named `fakeX`/`stubX`/`spyX` → project convention is `mockX`.
- `time.Sleep(N * time.Second)` in tests → `require.Eventually` / channels.
- `t.Errorf` on an assertion whose result is dereferenced next line → `t.Fatalf`.

---

### 5.13 Performance (BLOCKER)

- N+1 Mongo queries in a loop → batch with `$in`.
- Missing projections; fetching full documents when few fields are needed.
- Sequential independent awaits that could fan out via `errgroup` / `safego`.
- Unbounded queries on user-facing endpoints (no cursor `limit + 1`).
- External calls without a timeout / context deadline.

---

### 5.14 Hidden Bug Fix Risk (BLOCKER)

If a `feat`/`refactor` PR quietly fixes production behavior, **flag it** for QA. Dedicated `fix` PRs are fine.

---

## Step 6: Present Review

Format the review as:

```markdown
## PR Review: #<number> — <title>

**Author:** <author> | **Base:** <base branch> | **Files:** <count> | **+<additions> -<deletions>**

### Overview
<1-3 sentences: what this PR does and why>

### Findings

#### BLOCKER
1. **[S1-1]** `<file>:<line>` — <description>
   ```go
   // current code from the diff
   ```
   **Suggested fix:**
   ```go
   // corrected code
   ```

#### CONVENTION
2. **[S2-1]** `<file>:<line>` — <description>
   **Convention ref:** <rule>.md §X — <rule name>
   ```go
   // current code from the diff
   ```
   **Suggested fix:**
   ```go
   // corrected code
   ```

### Verdict
<APPROVE / REQUEST_CHANGES / COMMENT — and a one-line reason>
```

**Formatting rules:**
- Only BLOCKER and CONVENTION findings exist — never emit a WARNING or NIT section. Style, naming, and comment-density observations are not reported.
- If a severity level has no findings, omit that section entirely.
- Number all findings sequentially (1, 2, 3...) regardless of severity.
- **EVERY finding MUST include a code snippet** showing the actual code from the diff — a finding without code context is hard to act on.
- Every finding includes both the current code AND a suggested fix snippet.
- Always include **Convention ref** for CONVENTION findings (e.g. `mongo.md §6`, `concurrency.md §6`, `http.md §11`) so the author can look it up.

---

## Step 7: Save Review Result

Save the full review to `ai-agents-output/pr-reviews/`:

1. Create the directory if it doesn't exist.
2. Save as `PR-<number>-<date>.md` (e.g., `PR-47-2026-06-03.md`).
3. The file should contain the complete review output from Step 6, plus a metadata header:

```markdown
---
pr: <number>
title: <PR title>
author: <author>
date: <YYYY-MM-DD>
verdict: <APPROVE / REQUEST_CHANGES / COMMENT>
blockers: <count>
conventions: <count>
comments_posted: <pending>
---

<full review content from Step 6>
```

4. If a review file for the same PR already exists, overwrite it (re-reviews replace the previous result).
5. Confirm to the user: "Review saved to `ai-agents-output/pr-reviews/PR-<number>-<date>.md`"

---

## Step 8: Ask User Which Findings to Post

After presenting the review, ask:

> Which findings would you like me to post as inline comments on the PR?
> - **"all"** — post everything
> - **"1, 3, 5"** — post specific findings by number
> - **"blockers"** — post only BLOCKER (S1) findings
> - **"conventions"** — post only CONVENTION (S2) findings
> - **"none"** — don't post anything
>
> You can also ask me to rephrase any finding before posting.

**NEVER post comments without explicit user confirmation.**

---

## Step 9: Post Selected Comments

Once the user confirms:

### 9.1 Gather Required Data

Run in parallel:
```bash
# Get head SHA
gh api repos/<owner>/<repo>/pulls/<number> --jq '.head.sha'

# Get the full diff (reuse from Step 2 if still available)
gh pr diff <number>
```

### 9.2 Classify Findings: Inline vs General

Split the selected findings into two groups:

| Type | Condition | Where it goes |
|------|-----------|---------------|
| **Inline** | Finding targets a specific file + line that exists in the diff | `comments[]` array in the review payload |
| **General** | Finding is about overall patterns, architecture, or spans multiple files | Appended to the review `body` text |

### 9.3 Calculate Line Numbers for Inline Comments

For each inline finding, you need the **diff line number** within the file's diff hunk. This is the `position` field — it counts from 1 starting at the first `@@` line of the file's diff.

**How to count `position`:**

```diff
diff --git a/internal/features/friend/service.go b/internal/features/friend/service.go
--- a/internal/features/friend/service.go
+++ b/internal/features/friend/service.go
@@ -10,6 +10,8 @@ func (s *Service) Mint(...) {     ← position 1
                                            ← position 2 (context line)
 	cur := s.repo.Get(ctx, id)              ← position 3
+	go s.warmCache(id)                      ← position 4
+	ip := r.RemoteAddr                      ← position 5 ← TARGET THIS LINE
 	return token, nil                       ← position 6
 }                                          ← position 7
```

**Rules:**
- Count every line after `@@` — context lines, `+` lines, and `-` lines all count.
- Position is 1-indexed (first line after `@@` is position 1).
- If the finding targets a `+` line (new code), use that position.
- `-` lines (deleted code) also count toward position but can't be commented on.
- **Multi-hunk files**: position counting is **cumulative across all hunks** — do NOT reset at each `@@`.

**If you can't determine the exact position** for a finding, move it to the review body (general) rather than guessing.

### 9.4 Post Comments

**Preferred approach: Post individual comments** (more resilient — one failure doesn't block others):

```bash
COMMIT="<head_sha>"

# For each inline finding:
gh api repos/<owner>/<repo>/pulls/<number>/comments -X POST \
  -f commit_id="$COMMIT" \
  -f path="internal/features/friend/service.go" \
  -F position=5 \
  -f body="**[S1-1] BLOCKER — Description**

Details...

**Suggested fix:**
\`\`\`go
// fixed code
\`\`\`"
```

**For findings on files NOT in the diff** (e.g., an unchanged file missing a shutdown row):

```bash
gh api repos/<owner>/<repo>/issues/<number>/comments -X POST \
  -f body="**[S2-6] CONVENTION — Description of finding on unchanged file**

Details and suggested fix..."
```

**Alternative: Batch review** (all comments in one API call — if ANY position is wrong, the entire review fails):

```bash
cat > /tmp/review-payload.json << 'REVIEW_EOF'
{
  "event": "COMMENT",
  "body": "## Code Review\n\nSee inline comments.",
  "commit_id": "<head_sha>",
  "comments": [
    {
      "path": "internal/features/friend/service.go",
      "position": 5,
      "body": "**[S1-1] BLOCKER**: Description.\n\n**Suggested fix:** ..."
    }
  ]
}
REVIEW_EOF

gh api repos/<owner>/<repo>/pulls/<number>/reviews \
  --method POST \
  --input /tmp/review-payload.json
```

**Comment body format:**
```
**[S1-1] BLOCKER — <one-line description>**

<details if needed>

**Suggested fix:** <what to do>

**Convention ref:** <rule>.md §X (for CONVENTION findings)
```

### 9.5 Handle Errors

If the API call fails:
- **422 Unprocessable Entity** — usually a wrong `position` value or position sent as a string. For individual comments: skip and post as a general issue comment via `POST /issues/:number/comments`. For batch reviews: remove the offending comment and retry.
- **404 Not Found** — PR number or repo is wrong. Show error and abort.
- **403 Forbidden** — user lacks write access. Suggest: `gh auth status` and check repo permissions.

**Important:** With `gh api` individual comments, use `-F position=N` (capital F, sends as integer) not `-f position=N` (lowercase f, sends as string). The GitHub API requires `position` to be an integer.

### 9.6 Post Verdict Summary Comment

After all inline/general finding comments are posted, **always** post a final summary comment with the verdict:

```bash
gh api repos/<owner>/<repo>/issues/<number>/comments -X POST \
  -f body="## Code Review Summary

**Verdict: <APPROVE / REQUEST_CHANGES / COMMENT>**

| Severity | Count |
|----------|-------|
| S1 BLOCKER | <count> |
| S2 CONVENTION | <count> |

### Overview
<1-3 sentences: what this PR does>

### Key Findings
- **[S1-1]** \`<file>\` — <one-line description>
- **[S2-1]** \`<file>\` — <one-line description>
- ...

<If APPROVE: 'Looks good overall. Minor nits noted inline.'>
<If REQUEST_CHANGES: 'Please address the blockers before merge. See inline comments for details.'>
<If COMMENT: 'No blockers, but some convention violations worth aligning. See inline comments.'>"
```

**Rules:**
- Always post this verdict comment, even with zero findings (in that case, post the APPROVE verdict with a short "LGTM" message).
- Post this **after** all inline comments so it appears at the bottom of the PR conversation.
- Keep the key findings list short — max 1 line per finding.

### 9.7 Clean Up and Update Saved Review

```bash
# Remove temp file
rm -f /tmp/review-payload.json

# Show the PR URL
gh pr view <number> --json url --jq '.url'
```

Update the saved review file (`ai-agents-output/pr-reviews/PR-<number>-<date>.md`): change `comments_posted: <pending>` to `comments_posted: yes` and note which findings were posted.

Report to the user:
- How many inline comments were posted.
- How many general findings were included in the review body.
- Verdict summary comment posted.
- Link to the PR.

---

## Error Handling

- **PR not found**: "PR #X not found. Check the number and ensure you have access to the repo."
- **No diff available**: "PR has no changes or is already merged."
- **Comment posting fails**: Show the error, suggest the user check repo permissions (`gh auth status`).
- **Large PR (>50 files)**: Warn the user and ask if they want to review specific files only.
