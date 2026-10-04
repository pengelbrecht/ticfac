# Herdr hosting after pi-durable: the design for local runs

Tick `2q5` (epic `43y`), 2026-10-04. The finding this tick absorbed
(`run-epic-43y/tick-hpk/attempt-9`, promoted from tick `hpk`): after the
durable local host landed on the harness substrate (the subprocess executor,
tick `hpk`), `substrate = "auto"` on a host with a live herdr still resolves
to HERDR — and the herdr profile set every `ticfac run` selects in that case
shipped **executor `herdr` + runner `pi` on every role**, which launches a
herdr agent of *kind* `pi`: the interactive **pi CLI**, the exact worker path
epic 43y's A1 deletes (`jhp`). A local epic run on the herdr substrate was
therefore the one place left where workers did not run on pi-durable.

This document is the design that resolves it, the decision record for why,
and the statement of what each mechanism in the tree now means. The code
half of the tick is the re-cut of `profiles-herdr/` this document specifies,
guarded by the tests named at the end.

## The decision

**Herdr does not host pi-durable workers.** On a herdr host:

- **Implementation work runs on the pi-durable harness**, through the local
  subprocess executor — headless, one Node process per attempt, its
  conversation in the attempt's own SQLite storage, its steer socket, wip
  commits and wall clock owned by the subprocess supervisor (tick `hpk`).
- **The frontier rung keeps herdr panes**: review-epic, closeout-epic, and
  the on-demand judgement jobs (resolve-conflict, plan-repair) run as
  herdr agents of kind `claude` — the claude CLI, the operator's local-only
  exception to "one harness" (operator decision, 2026-10-04: "KEEP the
  claude CLI as the local frontier rung … every other local and all cloud
  workers run on pi-durable").

The substrate enum and the probe are unchanged: `substrate = "auto"` still
resolves to herdr when the probes answer, and `ticfac run` still selects the
embedded herdr set (`--profiles herdr`) when it finds a live herdr. What
changes is what that set *routes*: the set is now split per role instead of
uniform, because the profile — not the substrate — is the one field that
names an executor (tick `9sz`), and dispatch routes on the profile
(`internal/cli/executor.go`'s factory switches on `Profile.Executor`, per
dispatch).

Concretely, `profiles-herdr/` after this tick:

| role | executor | runner | meaning |
|---|---|---|---|
| implement-tick | `local-subprocess` | `pi` | the pi-durable Node harness, headless (tick `hpk`'s runner table entry) |
| review-epic | `herdr` | `claude` | the frontier rung, in a pane the operator attends |
| closeout-epic | `herdr` | `claude` | the frontier rung, in a pane |
| resolve-conflict | `herdr` | `claude` | on-demand, routed at the ceiling — frontier |
| plan-repair | `herdr` | `claude` | on-demand, routed at the ceiling — frontier |

The roles table (`.tick/runners.toml`) is untouched and still routes as it
did: `[roles.implement]` kind `pi` + GLM 5.3 for strong work (now meaning
the durable host, because the profile that carries it names the subprocess
executor), `[roles.review]`/`[roles.closeout]` claude + opus, and the local
overlay's implement ladder (`strong` → `frontier`) climbs from GLM on the
durable host to the claude CLI headless — still the same exception, since
the exception is the *CLI*, not the pane.

## Why herdr does not host pi-durable (the rejected alternative)

The finding named a herdr-side mechanism: "a herdr agent kind that shells
the Node entry with per-attempt arguments". It is rejected, on four counts:

1. **A herdr agent template is static, and the durable host's interface is
   per-attempt.** The local host (`harness/src/local/main.ts`) is driven by
   `worker.json` — the attempt's storage path, worktree, branch, report,
   checker, steer socket, wall deadline (`internal/exec/subprocess/workerconfig.go`) —
   written by the executor *per attempt*, before any runner process exists.
   herdr's `--kind` values are canonical executables herdr knows how to
   launch and detect (`internal/runconfig/herdr-kinds.md`); there is no kind
   whose canonical executable is `node` running this repository's harness
   entry. Adding one is a change to herdr itself, plus operator config
   outside this repository — and even then the per-attempt arguments would
   need this repository's herdr executor to render per-attempt placeholders
   into `Options.Args`, which today are host configuration resolved once
   per role.
2. **herdr's lifecycle machinery has nothing to say about a Node process.**
   herdr detects `idle | working | blocked | done` through per-CLI
   integrations (`herdr integration status`) and screen rules. A
   pi-durable host in a pane would need its own integration to be
   addressable at all — another herdr-side artefact to build, install and
   keep current, for a host whose lifecycle the subprocess supervisor
   already owns precisely (it is the process's parent).
3. **The whole durable contract would have to be rebuilt against panes.**
   Wip checkpoints after every tool round, the steer socket, the wall
   clock, the stuck watch, collect's three-check result contract, dispose
   and cancel: all live in the subprocess executor today. A herdr-hosted
   durable worker would re-implement that contract through herdr's agent
   API to arrive at behaviour that already exists.
4. **A pane buys nothing the harness does not already give.** The point of
   hosting a worker in a pane is operator visibility; pi-durable's local
   host is headless by design and already has the surface for that —
   `ticfac watch` over the harness's own committed state and steer socket
   (tick `y03`). A pane would duplicate that with a worse fit: a scrolling
   log where `watch` shows the committed conversation.

The cost of the rejected alternative is therefore paid twice — once in
herdr (an upstream change this repository cannot make, in agent
configuration its operator owns) and once here (a second implementation of
the durable contract) — to reach behaviour the tree already has.

## What "substrate = herdr" means now

The pinned contract (`contracts/runners-config-contract.json`) describes the
enum: `herdr` — workers are herdr panes/worktrees. That sentence describes
what the substrate *authorises*; which workers actually ride panes is the
profile set's, per role, and has been since tick `9sz` gave each set its own
executor field — a set could always have named a mix. The herdr set was
uniform only because every role happened to be an interactive CLI when it
was cut. Two statements follow, and both are now true:

- A run on the herdr substrate dispatches **through herdr for every role
  whose profile names herdr** — the frontier rung above — and through the
  local subprocess executor for the rest. Mixed executors within one run
  are already supported end to end: dispatch routes on `Profile.Executor`
  per dispatch, the attempt marker records the executor it ran on, and a
  later leg re-resolves the profile from the set naming the *recorded*
  executor (`internal/reconcile/dispatch.go`, `profileForRecordedExecutor`,
  from epic `6in`'s "herdr attempt, local settle" fix).
- A run that wants **no panes at all** does not need this design to say so:
  `ticfac run --no-herdr`, or `$TICKS_SUBSTRATE = "harness"` for the
  orchestrator, dispatches every role with the local set — implement on
  the durable host, frontier on the headless claude CLI. That is the pin
  the acceptance's real local run *could* have used; after this tick it
  does not need to, because `auto` on a herdr host now routes implementation
  onto the durable host anyway.

The acceptance item this serves is A1 — "every worker, local and cloud,
runs on pi-durable" — with the one exception the operator recorded: the
claude CLI as the local frontier rung. The frontier rung is the *CLI*; a
frontier worker in a herdr pane and a frontier worker headless are the same
rung. Which of the two a frontier job takes is the profile set's: on the
herdr set the roles an operator attends (review, closeout, the on-demand
judgement jobs) keep panes; the implement ladder's frontier tier runs
headless, because implement is one role and its executor does not change
with its tier.

## What jhp still deletes

Tick `jhp` (epic step 9) deletes "the pi-CLI worker path": `image/worker.sh`'s
pi-CLI harness path, the pi CLI in the container image, and the
herdr/subprocess pi-CLI profiles. After this tick's re-cut there is no such
profile left to delete in the herdr set — every role ships either the
durable host (`local-subprocess` + `pi`) or an interactive claude CLI, and
the guard test below holds exactly that. What remains for `jhp` is the
image side, the `TICFAC_RUNNER_ARGV` escape hatch that still makes the pi
CLI reachable on the subprocess executor, and the real local + cloud runs
that prove the whole posture.

## The guards

The design is pinned by tests, not just written down (the rule
`decisions/README.md` states for prose without a check):

- `internal/profile/herdr_embedded_test.go` —
  `TestTheEmbeddedHerdrSetResolvesEveryRole` pins the split: implement on
  the durable host's executor, the frontier rung on herdr with the claude
  CLI, the on-demand roles with it.
- `internal/profile/herdr_embedded_test.go` —
  `TestNoEmbeddedProfilePairsTheHerdrExecutorWithThePiCLI` refuses the
  deleted pairing in any embedded set a herdr run can select: on the
  herdr executor a runner names a herdr agent *kind*, and kind `pi` is the
  pi CLI. (The same name on the local-subprocess executor is a different
  thing — the durable Node harness — so the guard refuses the *pairing*,
  not the name.) This is the test that failed at this tick's base, five
  roles over, demonstrating the finding.
- `internal/profile/repair_test.go` — `TestTheHerdrProfileSetShipsTheRepairJob`
  pins the on-demand repair job to the frontier rung on the herdr set.

A future packaging change (an embedded harness bundle, a herdr kind for
the durable host that actually exists) changes this document with the
profiles, not after them.
