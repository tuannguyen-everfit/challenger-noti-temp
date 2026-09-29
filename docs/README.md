# `docs/` — go-service-template documentation

Layout:

```
docs/
├── architectures/         ← service-wide architecture, scaffolding, cross-cutting design
│   ├── scaffold-design.md
│   └── ...
└── features/              ← per-feature docs, one directory per bounded context
    └── <feature>/
        ├── specs.md
        ├── test-cases.md
        ├── solution-design.md
        └── implementation-plan.md
```

## What goes where

### `architectures/`

Service-wide design that crosses feature boundaries: scaffolding, routing model, observability strategy, deployment shape, library / pattern reviews. One file per topic.

### `features/<feature>/`

Anything **specific to one bounded context** in `internal/features/<feature>/`. One directory per feature. Standard files:

| File | Contents |
|---|---|
| `specs.md` | Product requirements / user stories — what this feature does and why |
| `test-cases.md` | Test scenarios that the feature must satisfy (acceptance criteria) |
| `solution-design.md` | Technical design — data model, API shape, cross-feature interactions |
| `implementation-plan.md` | Phased plan — milestones, dependencies, sequencing |

Other files are fine when needed (e.g. `migration-plan.md`, `rollout.md`); the four above are the minimum.

## Relationship to `.claude/rules/`

- `docs/` describes **what we're building** (feature specs, architecture decisions).
- [`.claude/rules/`](../.claude/rules/) describes **how we build it** (Go conventions, layout, naming, testing).

When a new feature is scaffolded, the code goes in `internal/features/<feature>/` per [.claude/rules/layout.md](../.claude/rules/layout.md) §2 and the docs go in `docs/features/<feature>/` per this README.
