# Tick Run Configuration

## Rules

- Epic integration goes through a PR + CI gate: the orchestrator pushes the epic branch and opens a PR; the epic close-out may not complete until CI is green on that PR. No direct merges of epic branches to the default branch.
- ticfac depends on ticks ONLY through `tk --json` (contract in `contracts/tk-json-manifest.json` of the pinned ticks ref) and the pinned contract bundle. Never import a ticks Go package. A fixture break must fail a build here.
- Package management is pnpm only — never npm or yarn. Go stdlib-first.
- **This is a public repository. Nothing operator-specific is ever committed.** No secrets or tokens, and no identifiers tying the repo to one operator: cloud account IDs, workspace IDs, organisation names, personal or work email addresses, real bucket/database names, deployment URLs. This applies to `.tick/` notes and activity as much as to source. Use placeholders; fixtures and tests use example.com addresses.
- Third-party credential tooling is always an optional rung, never a dependency.

## Standing orders

Same as ticks (`.tick/config.md` there): library choice within the stack, naming, internal API shape, file layout, test strategy, wave partitioning, discovered bugs → create a tick, base-branch mechanics: **decide and log**. Spending money, credentials and their grade, touching a live external system, removing scope, roadmap changes, architecture posture that outlives the epic, force-pushes: **always ask**.
