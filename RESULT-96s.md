<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ex6/attempt-3/96s`, base `1f44e5959b4359cd149ebd7a0edfde084c01e3c5`, harness `pi-durable` exited 0, 0 work commit(s), 1 uncommitted path(s)._

_ticks-worker: a carried attempt — its base `1f44e5959b4359cd149ebd7a0edfde084c01e3c5` is the head of the work it continued, which was cut from `304cca6250414b27584d43678a2b38a4f914e7c2`; its work commits are counted from the carried head._

# RESULT — tick 96s (attempt 3) — runners.local.toml header note delivered as a protected_change

## Summary

STATUS: DONE. Tick 96s's whole deliverable is the one-paragraph note in `.tick/runners.local.toml`'s header explaining why that file declares no `[roles.review]`/`[roles.closeout]` overlay (the blessed local claude exception of tick j6o lives in `.tick/runners.toml`; the behaviour is already enforced by guard tests, so the note is documentation only). `.tick/runners.local.toml` is a protected `.tick/` path no worker may write — attempts 14 and 18 of this tick ended BLOCKED over exactly that, and tick j6o's attempt 32 was rejected for the same edit. This attempt's instructions give the delivery channel for precisely this case: do not write the file, do not answer BLOCKED, and report a v2 finding carrying the exact change as its `protected_change` key, which the run applies itself onto the epic branch after close-out as a labelled commit. So this attempt commits nothing and changes no code: the composed, verified change is in the findings block below, and the tick closes when the run applies it.

## What the note says (for the person reviewing the epic PR)

One comment block inserted into `.tick/runners.local.toml` after the last header paragraph (line 9, "(claude opus), capped there."), before `version = 2` (line 10). Everything else in the file is byte-identical:

```diff
 # attempt: attempt 1 -> strong (pi on glm-5.3), after a failure -> frontier
 # (claude opus), capped there.
+#
+# Deliberately no [roles.review] or [roles.closeout] cells here (tick 96s):
+# the blessed local claude exception lives in .tick/runners.toml — its
+# [roles.review], [roles.review.tiers.frontier] and [roles.closeout] cells
+# name the claude CLI on opus for local final reviews and close-outs (the
+# 2026-10-04/05 decisions, tick j6o), and an overlay cell in this file
+# would move those jobs off the blessed decision. So the cells below
+# concern the implement ladder only: the frontier rung and the
+# [tier_policy] that climbs it (and sets the ceiling the on-demand
+# judgement jobs route at, through the review cell). A local run's review
+# and close-out therefore keep .tick/runners.toml's cells exactly as
+# written, and the claude CLI appears locally only in those cells and on
+# implement's frontier rung. Guards:
+# TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells and
+# TestThisRepositorysLocalImplementTiersRouteOnTheOneHarness
+# (internal/reconcile/routing_check_test.go).
 version = 2
```

Every claim in the note was checked against the current tree: `.tick/runners.toml`'s `[roles.review]`, `[roles.review.tiers.frontier]` and `[roles.closeout]` all name `kind = "claude", model = "opus"`; `.tick/runners.local.toml` declares no review or closeout cell and its own cells are exactly the implement frontier rung and the `[tier_policy]`; the blessed table in `TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells` confirms the claude CLI appears locally only in those cells and on implement's frontier rung.

## What I ran

- `go test ./internal/reconcile/ -run 'TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells|TestThisRepositorysLocalImplementTiersRouteOnTheOneHarness' -count=1 -timeout 5m` → ok (both guards pass; the behaviour the note documents is already enforced).
- `make gate` → exit 0, twice (a full pass, then a second pass re-checked for FAIL lines: none). Attempt 14's one-time unattributed gate failure did not reproduce; nothing re-filed for it.
- `git status` → clean tree apart from this report file; no commits made by this attempt.

There is no new test and no code change: the deliverable is prose in a protected file, and the guards that enforce the behaviour it documents already exist and are named in the note itself.

## What the next tick has to know

- Nothing on this branch moved from its base. The delivery is the `protected_change` finding below; if the epic branch's `.tick/runners.local.toml` has moved since this attempt's base, merge the note into its header rather than replacing the file blindly (called out in the finding's body too).
- Once the note lands, do not "fix" it by adding a review/closeout cell to `runners.local.toml`: the blessed-cells sweep fails any such cell by design, which is exactly what the note tells a reader.
- Attempt 14's harness boundary-guard defect (checkout file holding git's stderr, disarming the tk shim) is STILL LIVE in this container — re-filed below with fresh evidence so dedup keeps it visible; it is outside this tick's scope.
- Attempt 14's "deliverable writable by no actor a run can dispatch" finding is superseded by this attempt's delivery: the `protected_change` channel is the writer.

```findings v2
[{"kind": "proposal", "title": "Tick 96s's header note for .tick/runners.local.toml, composed and verified", "severity": "low", "body": "The whole of tick 96s's deliverable is the comment paragraph in .tick/runners.local.toml's header explaining why that file declares no [roles.review]/[roles.closeout] overlay (the blessed local claude exception of tick j6o); the behaviour is already enforced by the guard tests, so it is documentation only. The file is a protected .tick/ path no worker may write, so this finding carries the exact change as protected_change: the whole new file, the note inserted in the header before 'version = 2', everything else byte-identical to this tree. The run applies it onto the epic branch after close-out as the labelled commit and the tick closes on it; if the epic branch's copy of the file has moved since this attempt's base, merge the note into its header rather than replacing the file blindly. This supersedes attempt 14's 'deliverable writable by no actor' finding: the protected_change channel is the writer.", "evidence": ".tick/runners.local.toml (insertion before line 10, 'version = 2'); TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells and TestThisRepositorysLocalImplementTiersRouteOnTheOneHarness (internal/reconcile/routing_check_test.go:350, :288) pass on this tree; make gate exit 0", "protected_change": {"path": ".tick/runners.local.toml", "content": "# Overrides for runs on THIS machine — herdr and harness substrates (tick\n# 5uo): merged over .tick/runners.toml. The cloud never reads this file.\n#\n# The operator, 2026-09-23: \"if a tick is complex you can consider using a\n# claude executor locally. It should be significantly stronger than pi/glm\" —\n# local claude bills to the operator's Max subscription. So a local run's\n# implement ladder starts on GLM 5.3 and climbs to claude after one failed\n# attempt: attempt 1 -> strong (pi on glm-5.3), after a failure -> frontier\n# (claude opus), capped there.\n#\n# Deliberately no [roles.review] or [roles.closeout] cells here (tick 96s):\n# the blessed local claude exception lives in .tick/runners.toml — its\n# [roles.review], [roles.review.tiers.frontier] and [roles.closeout] cells\n# name the claude CLI on opus for local final reviews and close-outs (the\n# 2026-10-04/05 decisions, tick j6o), and an overlay cell in this file\n# would move those jobs off the blessed decision. So the cells below\n# concern the implement ladder only: the frontier rung and the\n# [tier_policy] that climbs it (and sets the ceiling the on-demand\n# judgement jobs route at, through the review cell). A local run's review\n# and close-out therefore keep .tick/runners.toml's cells exactly as\n# written, and the claude CLI appears locally only in those cells and on\n# implement's frontier rung. Guards:\n# TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells and\n# TestThisRepositorysLocalImplementTiersRouteOnTheOneHarness\n# (internal/reconcile/routing_check_test.go).\nversion = 2\n\n[roles.implement.tiers.frontier]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\nargs = []\n\n[tier_policy]\ndefault = \"strong\"\nceiling = \"frontier\"\nstep = 1\n# No tick starts on the classifier (operator decision 2026-10-04): every\n# implementation tick starts at strong (GLM 5.3) and climbs to frontier\n# (claude opus) after a failed attempt. The dear-mass rule tick ms9 set here\n# (dear_work_types = design + diagnosis, dear_tier = frontier,\n# mass_threshold = 0.75) is switched off. ms9's own history table already\n# showed the mass caught 3 of 10 work failures at every threshold and missed\n# the big construction ticks; docs/classifier-eval-2026-10-04-jev-clef.md\n# (tick r3y, PR #206) then measured dear mass against outcomes at AUC\n# 0.37-0.42 for Jev, Clef and Clef-flash — no model makes the rule\n# predictive. Classification still runs and is recorded on the run branch,\n# as data for a later question (size/effort, or fine-tuning on outcomes).\n# `ticfac status` reports the escalation rate the ladder now carries alone.\n"}}, {"kind": "defect", "title": "Harness guard writes git's stderr into its checkout file, disarming the tk shim", "severity": "medium", "body": "First filed by attempt 14 of this same tick; re-filed because it is still live in this container (fresh evidence below) and dedup should keep it visible. boundary-guard.ts resolves the guard's checkout companion with 'git rev-parse --path-format=absolute --git-common-dir || true' through a helper that captures the command's merged output, so when the resolution runs before/outside the checkout the failure's stderr line lands in the checkout file as if it were a path. The shim's 'different tracker' clause then matches for every non-read call and passes reads AND writes to the real tk, silently: layer 1 of the boundary is off exactly where it exists to fire, and the ledger never records the attempt. The pre-commit hook and the collect's .tick/ diff check still hold, so this is defense-in-depth lost, not an open write path. Observed live in this container: /work/repo.guard/checkout holds exactly 'fatal: not a git repository (or any of the parent directories): .git' and the ledger is empty.", "evidence": "harness/src/env/boundary-guard.ts:166-170 (the resolution), :171 (writeTextFile of checkout.output.trim()); harness/src/env/factory-sandbox.ts:798 (onOutput accumulates the merged stream); live: /work/repo.guard/checkout content and empty /work/repo.guard/attempts"}]
```

STATUS: DONE — no commits: the deliverable is a change to the protected file `.tick/runners.local.toml`, delivered verbatim as the `protected_change` finding above for the run to apply onto the epic branch; the behaviour the note documents is already enforced and green on this tree (guards pass, `make gate` exit 0).
