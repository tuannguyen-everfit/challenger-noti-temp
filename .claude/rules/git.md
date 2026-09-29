# Git — branch naming + commit messages

Read this whenever you create a branch, write a commit message, or open a PR.

## 1. Branch naming

### Development branches

Format: **`[env]_[sprint].[tag]/[card]`**

| Slot | Value | Notes |
|---|---|---|
| `[env]` | `dev` / `stg` / (none) | source environment — `dev` from `develop`, `stg` from `releasing_*`, no prefix from `master` |
| `[sprint]` | e.g. `s48`, `s9_26` | current sprint identifier |
| `[tag]` | `feat` / `fix` / `refactor` / … | change type — see §3 |
| `[card]` | e.g. `UP-14545` | Jira card |

Examples:
- ✓ `dev_s48.feat/UP-14545` — feature branch from `develop`
- ✓ `dev_s9_26.feat/UP-70961` — current PR's branch
- ✓ `stg_s48.fix/UP-14545` — fix branch from a `releasing_*` branch
- ✓ `s48.feat/UP-14545` — branch from `master` (no env prefix)
- ✗ `feat/UP-14545` — missing env + sprint
- ✗ `dev_feat_UP-14545` — wrong separators

### Release branches

Format: **`[prefix]_[version]`**

| Slot | Value | Notes |
|---|---|---|
| `[prefix]` | `releasing` (default) | other prefixes for non-default release envs |
| `[version]` | `v1.0.0` | release version tag |

Examples:
- ✓ `releasing_v1.0.0`
- ✗ `release/v1.0.0` — wrong separator
- ✗ `releasing_1.0.0` — missing `v` prefix

## 2. Commit message format

**`[tag]([module]): [card] [description]`**

| Slot | Value | Notes |
|---|---|---|
| `[tag]` | `feat` / `fix` / `refactor` / … | change type — see §3 |
| `[module]` | e.g. `items`, `watchlist`, `mongox`, `kafka`, `claude` | the package or area touched |
| `[card]` | `UP-XXXXX` | Jira card — every commit traces to a card |
| `[description]` | short, present tense, no period | what changed |

Examples:
- ✓ `feat(items): UP-70961 scaffold go-service-template`
- ✓ `fix(ondemand workout): UP-14545 fix issue mongo transaction error`
- ✓ `refactor(watchlist): UP-71030 KafkaConsumer holds handler func, not *Service`
- ✓ `docs(claude): UP-71030 split CLAUDE.md into per-topic rule files`
- ✗ `feat(items): scaffold go-service-template` — missing card
- ✗ `feat: scaffold go-service-template [UP-70961]` — card belongs after `: `, not in brackets at end
- ✗ `Scaffold go-service-template` — missing tag, module, card

The body (after the title) can be free-form — bullet lists, paragraphs, BREAKING CHANGE notes, etc. The title is the strict-format slot.

## 3. Tags (shared between branch + commit)

Same list applies to BOTH branch `[tag]` and commit `[tag]`.

| Tag | When |
|---|---|
| `feat` | New feature |
| `fix` | Bug fix |
| `refactor` | Behavior-preserving code change (no new feature, no bug fix) |
| `perf` | Performance improvement |
| `docs` | Docs only — no code |
| `test` | Adding or correcting tests |
| `style` | Formatting only (whitespace, semi-colons) |
| `chore` | Routine maintenance — version bumps, dep updates |
| `build` | Build system / external deps (Make, mise, Docker, …) |
| `ci` | CI configs (`.github/workflows`, etc.) |
| `revert` | Revert a previous commit |

## 4. PR title

The PR title should mirror the commit-message format for its lead commit:

- ✓ `feat(items): UP-70961 scaffold go-service-template`
- ✓ `feat: UP-70961 scaffold go-service-template` (module optional for cross-cutting PRs)

Don't restate the body in the title. Don't paste the whole git log.

## 5. GitHub workflows — use `gh` CLI

**Prefer the GitHub CLI (`gh`) over the web UI, `curl`, or raw `https://api.github.com/...`** for every GitHub operation. Same commands work locally, in CI, in Makefile targets, and in AI sessions — the auth is shared (one `gh auth login`) and the output is pipe-friendly.

Install: <https://cli.github.com/>. One-time: `gh auth login`.

### Common commands

| Task | Command |
|---|---|
| Open / view a PR | `gh pr view [N]` (omit `N` to view the PR for current branch) |
| List PRs | `gh pr list --state=open` |
| Diff of a PR | `gh pr diff [N]` |
| Check CI status | `gh pr checks [N]` |
| Create a PR | `gh pr create --title "..." --body "..."` |
| Comment on a PR | `gh pr comment [N] --body "..."` |
| Mark ready / merge | `gh pr ready [N]` / `gh pr merge [N]` |
| Approve / request changes | `gh pr review [N] --approve` / `--request-changes -b "..."` |
| Open / list issues | `gh issue view [N]` / `gh issue list` |
| Repo metadata | `gh repo view` |
| Read API | `gh api repos/OWNER/REPO/...` (use this instead of `curl`) |

### STOP rules

- ✗ `curl https://api.github.com/...` — use `gh api` (auth handled, retries, output pipe-friendly).
- ✗ Pasting a `https://github.com/.../pull/123` URL when you want an action done — call the matching `gh pr` command.
- ✗ Manual browser-click workflows when the action is scriptable. If you'd do it twice, script it with `gh`.
- ✗ Maintaining a personal GitHub token in shell env when `gh auth login` would handle it.

## STOP rules

| Symptom | Why |
|---|---|
| Commit without `[card]` in the description | Every commit traces to a Jira card. No card → no commit. If you're contributing without a card, get one first. |
| Commit message without `[tag]` prefix | `[tag]:` or `[tag]([module]):` is mandatory — feeds release-notes tooling. |
| Card in `[brackets]` at the end of the description instead of right after `:` | Strict format puts card BEFORE the description: `feat(items): UP-XXXXX add feature`. Bracket-at-end is the older/loose style; use the strict form going forward. |
| Branch name not matching `[env]_[sprint].[tag]/[card]` | Random branch names break release/CI tooling. Use the convention. |
| Multiple unrelated changes in one commit | One change = one commit = one card ref. If a PR needs N commits, fine — but each commit is one logical change. |
| Squashing multiple cards into one commit message | Don't. If a single change touches two cards, the cards are probably related — pick the primary one. |
| Auto-commit / auto-push without user's explicit approval | Never. The user reviews staged diffs before git records anything. Applies even in auto mode. |
| Using `--no-verify` to skip hooks | Never. If a hook fails, fix the underlying issue. |

## References

- Current branch (this PR): `dev_s9_26.feat/UP-70961` — exemplary form.
- Initial scaffold commit: `feat: scaffold go-service-template [UP-70961]` — uses the OLDER `[card]` bracket-at-end style. Subsequent commits in this PR also drift from the strict format. The strict format above is the convention **going forward**; existing history is preserved as-is (no rewriting shared branches).
