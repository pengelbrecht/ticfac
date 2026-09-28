# Tick Run Configuration

## Rules

- Epic integration goes through a PR + CI gate: the orchestrator pushes the epic branch and opens a PR; the epic close-out may not complete until CI is green on that PR. No direct merges of epic branches to the default branch other than the run's own: **the run merges its own PR** once it is ready — the base branch folded in with tk's merge drivers, the integrated gate green on the fold, CI green on the PR head — as a merge commit, then verifies CI on the default branch. This repository opts in for development velocity (decided 2026-09-28; reversible by deleting this sentence): ticfac has no users yet and moves faster when an epic lands the moment it is ready. The product default — the rule `ticfac init` writes for every other repository — is the opposite: the run keeps the PR ready (re-folding, re-gating and re-running CI whenever the base moves) and a person, with their own agent, reviews and merges it.
- ticfac depends on ticks ONLY through `tk --json` (contract in `contracts/tk-json-manifest.json` of the pinned ticks ref) and the pinned contract bundle. Never import a ticks Go package. A fixture break must fail a build here.
- Package management is pnpm only — never npm or yarn. Go stdlib-first.
- **This is a public repository. Nothing operator-specific is ever committed.** No secrets or tokens, and no identifiers tying the repo to one operator: cloud account IDs, workspace IDs, organisation names, personal or work email addresses, real bucket/database names, deployment URLs. This applies to `.tick/` notes and activity as much as to source. Use placeholders; fixtures and tests use example.com addresses.
- Third-party credential tooling is always an optional rung, never a dependency.
- Before you settle, run the whole-repo gate (`make gate`), not only the package you touched. Repo-wide guards run only in a whole-repo pass: every end-to-end test must call `shorttest.EndToEnd(t)` or carry a `short:` doc-comment line saying why the per-tick gate should pay for it (`internal/shorttest`). Pass `-timeout` whenever you run `go test ./internal/reconcile/` directly; it outlives go's 10-minute default.

## Standing orders

Same as ticks (`.tick/config.md` there): library choice within the stack, naming, internal API shape, file layout, test strategy, wave partitioning, discovered bugs → create a tick, base-branch mechanics: **decide and log**. Spending money, credentials and their grade, touching a live external system, removing scope, roadmap changes, architecture posture that outlives the epic, force-pushes: **always ask**.
