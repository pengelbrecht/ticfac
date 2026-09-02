# ticfac

Execution and orchestration for [ticks](https://github.com/pengelbrecht/ticks): a
reconciler over two durable authorities (the ticks tracker and Git, including
`.ticfac/` run state), a four-operation executor protocol
(start / inspect / cancel / collect), role jobs for review and closeout, and
hosts — the local subprocess executor, Herdr, and Cloudflare.

The architecture is `docs/projects/2026-09-01-ticfac-architecture/SPEC.md` in
the ticks repository. ticfac talks to ticks only through `tk --json` and the
pinned contract bundle under `contracts/` there; it never imports ticks' Go
packages. Migration phases and gates: SPEC §12. Roadmap and state: the
`hzm` project in ticks' tracker.

Status: Phase 1 (reconciler and local subprocess executor) — starting.
