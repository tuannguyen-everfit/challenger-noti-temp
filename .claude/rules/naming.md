# Naming — functions, variables, constants

Read this whenever you add a new function, variable, constant, or struct type. Captures the conventions already established across `internal/features/<feature>/` so new code reads like existing code. Goes hand-in-hand with [layout.md](layout.md) (layer types, file slots) and [http.md](http.md) (wire codes).

> ⚠️ **Names are interface.** A confusing name will be re-read a hundred times. A spent two extra minutes choosing the right one is recovered in a single PR review. Lean on the verb-prefix table in §2 before inventing new wording.

---

## 1. Public surface — Service methods and types

Verbs are short and direct. Read top-to-bottom in a feature's `Service` as a menu of actions.

| Pattern | When | Examples |
|---|---|---|
| `Get` / `GetBy<X>` | single read by primary key | `Get`, `GetByID`, `GetByIDWithSeen` |
| `Find*` | query that may return 0..N rows | `FindManyByAccountIDs`, `FindDistinctChallengeIDsByUser` |
| `List` / `List<Scope>` | enumerate a logical scope | `List`, `ListActive` |
| `Mark*` | one-shot state transition | `MarkTutorialSeen` |
| Domain verbs | single canonical operation | `Finalize`, `Refresh`, `Check`, `EnsureProfile`, `RequestOtp`, `VerifyOtp`, `Me`, `MeActiveChallenges` |

DTO types follow the action: `FinalizeInput`/`FinalizeResult`, `ListInput`/`ListResult`/`ListRow`, `MeInput`/`MeResult`/`MeRow`, `MeActiveChallengesInput`/`MeActiveChallengesResult`. Don't invent `*Request`/`*Response` for Service-layer DTOs — those are reserved for wire DTOs in `handler.go`.

---

## 2. Private helpers — prefix table (HARD CONVENTION)

Same package, lower-case first letter. Pick a prefix from the table; don't invent new ones unless none fit.

| Prefix | Role | Examples |
|---|---|---|
| `validate*` | single guard returning error | `validateListLimit` |
| `check*` | composite guard, multiple branches | `checkChallengeAndRebuild` |
| `parse*` | raw → typed (string→ObjectID, decode, etc.) | `parseAccountIDs`, `DecodeLeaderboardCursor` |
| `build*` | assemble output struct(s) from inputs | `buildListRows`, `buildMeRows`, `buildActiveRow` |
| `compute*` | pure math, no I/O | `computePercentile`, `computeReverseTimeDelta` |
| `collect*` | gather IDs / values into a slice | `collectSampledAccountIDs` |
| `pick*` | select a subset / one item | `pickRandomPeers`, `pickDefaultAvatar` |
| `find*` | locate single item / set inside a feature (private analogue of `Find*`) | `findMatchedActiveChallenges` |
| `intersect*` / `sort*` / `dedupe*` | set / sort / dedupe ops | `intersectByID`, `sortByEndsAtDesc` |
| `translate*` | error mapping at a layer boundary | `translateClientErr`, `translateOtpErr` |
| `ensure*` | idempotent get-or-create | `ensureProfileSnapshot` |
| `issue*` | mint a new artifact (token, code) | `issueTokens` |
| `to*` | DTO seam (domain → wire / wire → domain) | `toFinalizeDetails`, `toProfileSnapshot`, `toItemResponse` |

`assemble*`, `prepare*`, `process*`, `handle*`, `do*`, `gather*` are NOT in the table. If you're tempted to use one, you probably want `build*` (for output) or `collect*` (for gathering).

---

## 3. Booleans and predicates

| Form | When | Examples |
|---|---|---|
| `Is*` / `Has*` (method) | receiver boolean | `Snapshot.IsZero`, `MeRootResult.HasPeak`, `ListResult.HasMore` |
| `matches*` / `after*` / `is*` (free func) | predicate over inputs | `matchesFilter`, `afterCursor` |
| `<noun>HasData` / `<noun>IsEmpty` | predicate over a struct value | `snapshotHasData` |

Don't prefix with `check` for boolean returns — `check*` returns `error`. Boolean predicates are `Is*` / `Has*` / `matches*`.

---

## 4. Domain over infra (LAYER DISCIPLINE)

The Service layer speaks **domain** terms. The Cache / Repo layer can speak **infra** terms. The boundary between them is where translation happens; infra terms must not leak into Service helpers.

| Layer | Term it uses |
|---|---|
| Wire (`handler.go`) | `RandomUserItem`, `ListItem`, `MeItem` |
| Service (`service.go`) | `RandomUser` / "peer", `Row`, `Result` |
| Cache (`repository_valkey.go`) | `Member` (ZSET row), `Composite` (score), `MeRootResult` projection |
| Repo (`repository_mongo.go`) | `bson.M`, `Attempt`, raw docs |

| Smell | Why it's wrong | Fix |
|---|---|---|
| `hasZSetData(snap)` in Service | "ZSet" leaks Valkey into the domain layer | `snapshotHasData(snap)` |
| `filterAndCapMembers` in Service | "Members" is Valkey's term | `pickRandomPeers` |
| `parseMongoDoc` in Handler | "MongoDoc" leaks storage into HTTP | The DTO mapper is `to<Type>Response`; let Service hand back domain types |
| `ItemResponse.UserOID` on the wire | "OID" leaks bson into JSON | `user.user_code` / `user.account_id` (hex string) |

Cache-layer types CAN keep infra terms (`MemberScore`, `Members []MemberScore`) — that's the actual ZSET vocabulary. Service receives them and re-names at the boundary (`MemberScore` → `RandomUser`).

---

## 5. Generic-suffix STOP list

Refuse these suffixes on types / functions / files — they signal "I gave up naming this":

- `Partial` — what's missing? Why? Find the real concept.
- `Helper`, `Util`, `Common`, `Manager`, `Handler` (outside the layer name in [layout.md §2.2](layout.md)) — bag-of-functions smell.
- `Data`, `Info`, `Stuff`, `Item` (outside DTO context) — empty of meaning.
- `Out` / `In` suffixes on counts (`activeUsersOut`) — say the unit: `randomPeersPerRow`.
- `Oversample`, `Buffer`, `Tmp` — implementation detail bleeding into the name; describe purpose.

If you can't think of a non-generic name, that's a signal the abstraction itself is wrong. Inline it, OR split the concept until each piece has a real name.

---

## 6. Constants — placement and casing

- **Unexported** when scoped to the package: `defaultListLimit`, `maxActiveChallengeRows`, `randomPeersPerRow`. Tests in the same package can read them.
- **Exported** only when configurable from outside or wired into a config struct: `Config.MaxListLimit`, `Config.MaxWindow`, `ActiveChallengesCap` (only if other packages need it — usually they don't).
- Group cohesive constants in one `const ( ... )` block near the code that uses them, NOT in a top-of-file dump.
- **Prefer concrete units in the name** when the number's unit is non-obvious: `randomPeersFetchSize` (count of items to fetch) beats `randomPeersOversample` (implementation hint).
- Time durations: keep the unit out of the name when the type already carries it — `rebuildingRetryAfter time.Duration` is clearer than `rebuildingRetryAfterSec int`.

---

## 7. Wire codes (cross-link)

`<FEATURE>_<VERB_PAST>` strings + `Code<FeatureVerb>` Go constants live in `localization.go`. Full convention in [http.md §1](http.md) — don't restate it here.

---

## STOP rules

- ✗ Inventing a new verb prefix when the §2 table covers the role. Use `build*` instead of `assemble*` / `prepare*` / `process*`.
- ✗ Infra terms in Service helpers (`hasZSetData`, `filterMembers`). Translate at the layer boundary — see §4.
- ✗ Generic suffixes (`Partial`, `Helper`, `Data`). See §5.
- ✗ Cap / count constants named with implementation-detail suffixes (`*Out`, `*Oversample`). Say what they MEAN, not how they're used internally.
- ✗ Exporting a constant just so tests can read it. Tests sit in the same package — unexported is fine.
- ✗ Reinventing `*Request` / `*Response` types at the Service layer — those names are reserved for wire DTOs in `handler.go`.
- ✗ `parse*` / `build*` returning `error` when the operation has a single guard — that's `validate*`. Inverse: `validate*` should NOT return data; if you need both checks + data, split into `validate*` (error) + `build*` (data).

## References

- [layout.md §2.2](layout.md) — layer naming (`Service`, `Handler`, `Repo`, `Consumer`).
- [http.md §1](http.md) — wire code naming (`<FEATURE>_<VERB_PAST>`).
- [mongo.md §1](mongo.md) — snake_case for `bson:` tags (storage-side naming, separate convention).
- `internal/features/leaderboard/service.go` — current reference for §2's prefix table in use (lives in the upstream challenger-service repo — not in this template).
