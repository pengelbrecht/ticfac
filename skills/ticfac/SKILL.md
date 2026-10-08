---
name: ticfac
description: Run, watch, steer and finish epic runs with ticfac — the execution half of the ticks workflow, locally or in the operator's cloud factory. Use when the user asks to run an epic (local, --cloud, --cloud-workers), pick a run config (glm or claude), watch or follow a run, steer a worker, triage findings, clear a held or failed run, land the epic PR, check or wait for the factory deploy, or set ticfac up on another repository or machine. Triggers include "run the epic", "ticfac run", "run it in the cloud", "watch the run", "what is the run doing", "why is it held", "triage findings", "resume the run", "steer the worker", "is the factory deployed", "set up ticfac on this repo".
---

# ticfac — running epics

Boundary with the ticks skill: ticks owns PLANNING — creating ticks and epics,
breaking work down, tick shape and tracker state. ticfac owns EXECUTION —
starting, following, steering, triaging and finishing an epic run. Plan with
`tk`; run with `ticfac`. When the task is "plan this" or "break this down",
use ticks; when it is "run this epic" or "what is the run doing", use ticfac.

Install or update this skill with `ticfac skills install` — the binary
embeds one skill, so naming it (`ticfac skills install ticfac`) is the same
command — the same shape as the ticks skill's `tk skills install ticks`.
Re-run it after upgrading ticfac: it replaces a stamped copy with the new
binary's version.

Detail lives in references/: `commands.md` (every command and the flags
worth knowing), `holds.md` (every stop: who clears it, with what),
`cloud.md` (factory, claude-sub, another repo or machine). Every command
and flag here is in `ticfac <command> --help`; trust --help over memory.

## The loop

1. `ticfac init` — make the repository runnable: writes `.tick/runners.toml`
   (routing and the guessed gate), `.tick/runners.cloud.toml` when the
   substrate includes the cloud, and the close-out rule in `.tick/config.md`
   (`pr` when origin is GitHub, `none` otherwise). Flags answer every
   question: `--substrate local|cloud|both --runner claude|pi --model <m>
   --gate '<cmd>' --closeout pr|none`; `--yes` takes every default. It never
   overwrites a file that exists.
2. `ticfac doctor` — what this machine still lacks for a run, each missing
   line with its fix (tk, herdr, GitHub, git identity, the Jev classifier;
   with `--cloud` or a cloud repo also docker, wrangler and the factory).
   Exit 0 ready, 1 not.
3. Pick the config (below), then start the run — one of three shapes:
   - `ticfac run <epic>` — local: orchestrator and workers on this machine
     (implement workers headless on pi-durable; review and close-out in herdr
     panes when a live herdr is found — `--no-herdr` opts out).
   - `ticfac run <epic> --cloud` — everything in the factory; this machine
     only submits and watches. Pushes the current branch first.
   - `ticfac run <epic> --cloud-workers` — orchestrator, merges and the
     integrated gate HERE, every worker in a factory container. Feed relayed
     to the factory; the factory ends the run if this machine goes quiet.
   The view attaches; Ctrl-C detaches and the run keeps going. Running the
   same command again attaches to a live run or resumes a stopped one.
   `epic-<id>` is accepted wherever an epic id is.
4. Watch it (below). Steer a worker if it is going wrong.
5. Triage what the run left for a person; clear any hold (below).
6. Land: the run keeps the epic PR READY; the merge is a person's unless the
   repository opted in to the run merging its own PR.

## Choosing a config per epic

A repository may declare named run configs — `[configs.<name>]` tables plus
`[configs] default = "<name>"` in `.tick/runners.toml` or the substrate's
override file (`.tick/runners.cloud.toml`). ticfac's own repo declares two
for the cloud: `glm` (default: pi-durable on GLM 5.3 / 5.3-flash, Workers AI)
and `claude` (claude-sub: claude on the operator's subscription, implement
sonnet → opus, review and close-out opus). `ticfac init` writes no
`[configs]`; a repo without them runs its plain role cells.

Precedence, resolved once per run and kept across resumes:
`--config <name>` > the epic's `config:<name>` label > `[configs] default`.
- Set a design choice on the epic: `tk label add <epic> config:claude`.
- `ticfac run <epic> --config claude` works locally and with
  `--cloud-workers`; `--cloud` refuses `--config` — use the label.
- A name no runners file declares is refused, never silently swapped.
- Choose `glm` for routine, well-specified work (cheap, Workers AI). Choose
  `claude` for hard or judgement-heavy epics. A claude config needs at
  least one `CLAUDE_SUB_TOKEN_<LABEL>` secret on the factory; without one
  the run, `ticfac doctor` and the cloud preflight refuse it. When every
  subscription is benched or busy, that one job falls back to Workers AI.
  See references/cloud.md and docs/claude-sub-operator.md.

Worker harness: every implement worker, local and cloud, runs on pi-durable
(`kind = "pi"` cells). The claude CLI is the local frontier rung — local
review, close-out and the top implement tier — and, in the cloud, only via
a claude config.

## Watching and steering

- `ticfac` with no arguments — every local and cloud run, attention first;
  each held or failed run names the one command that clears it. `--all`
  adds history.
- `ticfac watch <epic>` — the live dashboard (enter opens a tick, e the feed).
  `ticfac watch <epic> <tick>` — one pi-durable worker's conversation live.
- `ticfac status <epic>` — alive or not (exit 0/1), `--follow` for a table,
  `--json` for the whole status model.
- `ticfac events <epic> --follow` — the JSONL feed as it lands; `--tail N`
  for the last N. A line means "worth looking now", never "done".
- Cloud only: `ticfac cloud logs <epic> -f [--tick <id>]` (container output),
  `ticfac cloud trace <epic> --tick <id>` (model calls),
  `ticfac cloud supervisor <epic>` (is the Workflow alive),
  `ticfac cloud status [<epic>]` (runs, leases, queue).
- `ticfac steer <epic> <tick> <text…>` — a message to a running pi-durable
  worker, placed after its current tool round; acknowledged once durable.
- Stop: `ticfac cloud stop <epic>` (clean) or `--now` (hard). A local run:
  SIGINT the exact pid in `.ticfac/logs/<run-id>/run.pid` (it ends
  cancelled, exit 7) — never `pkill -f` on a shared host.

Never hand-roll a polling loop: `ticfac watch`/`ticfac events --follow`
already block until something happens, and `--json` answers once at the end.

## What resolves itself, what needs a person

The run supervises itself: across these stops it resumes on its own
(recorded as interventions, capped by `run-epic --max-resumes`): a rejected
attempt that left nothing, a worker that asked and was re-dispatched a tier
up, infrastructure deaths, claim width / foreign claims, stale gate
evidence, CI still pending, transient remote and per-run token refusals, a
base fold that brings new ticks or touches what the epic is about. A worker
that stops to ask climbs the tier ladder, then decides under the standing
orders; only an always-ask question (money, credentials, live systems,
scope, force-push) holds.

What needs a person (exit 3, the line names the reason and the command):

| hold | clear it with |
|---|---|
| `finding_untriaged` | `ticfac triage <epic> <key>=absorb\|file\|fixed:<commit>\|discard` (copy the `--run-id` the hold names for a cloud run) |
| `epic_amendment_unconfirmed` | `ticfac amendments <epic>`, then `ticfac amendment <epic> <key> --confirm\|--reject --by <who>` |
| `attempt_needs_human` (always-ask question) | answer on the tick, `ticfac settle <epic> <tick> <attempt> --release <who> --carry-work`, run again |
| `land_review_not_ready` (review bound spent) | fix what the review names on `epic/<id>` and run again (a changed tree gets one more review), or merge/close the PR yourself |
| a failed gate, merge or boundary on an unchanged tree | fix what the line names, run again — it resumes, nothing redone |
| an attempt nobody can address | `ticfac settle <epic> <tick> <attempt> --release <who>` (the line spells it) |

Then resume: `ticfac run <epic>` (local) or `ticfac run <epic> --cloud`. A
held cloud run is SUPERSEDED by that rerun: stopped cleanly, then
resubmitted, and the new run reads the earlier run's final review. Full
list: references/holds.md.

Findings: the run places most itself. A finding enters the running epic
only when the final reviewer calls it blocking, or a worker rates it high
and names the `[A<n>]` done item it breaks while work is under way; the rest
become owned backlog ticks listed in the PR. Past the absorption bound
(default 3 per chain) a finding is backlogged and the run carries on.
`ticfac findings <epic>` lists them; `ticfac triage <epic> <k>=absorb` puts
one into the live epic and the running epic replans around it. The final
review gets two rounds; a NOT READY's blocking findings are absorbed and
re-reviewed; work closed after a READY, or a fold that touches what the
epic is about, is reviewed again before land.

## Exit codes

Every command takes `--json` — one versioned document, the schema named in
it — and exits the table:
0 done, 1 failed, 2 usage,
3 the run ended holding something only a person can move (run-epic, run and
watch alike; the reason class is in the line), 4 a lookup that came back empty,
5 the command ended while the run is still going (detached — nothing is wrong),
6 io, 7 the run was stopped deliberately (cancelled — nothing held, nothing
failed). `ticfac status` exits liveness: 0 alive, 1 not. In the `cloud`,
`factory` and `skills install` family, 3 means not inside a git repository.

## Landing

The epic finishes when its run completes: every tick gated and merged onto
the epic branch, and the PR READY — base folded in, integrated gate green on
the fold, CI green on the PR head, the body written for its reviewer (where
to look first, done items with evidence, review verdict, findings and where
each went, deferred findings). If the base moves before the merge,
`ticfac run <epic>` again re-readies it. A repo whose `.tick/config.md`
Rules say "the run merges its own PR" lets the run merge it and close the
epic tick on the base. `ticfac sweep refs --dry-run` lists ended runs'
leftover refs.

## Another repository or machine

- New machine: install ticfac (`install.sh`, README), copy `~/.ticfacrc`
  (`factory_url`, `factory_token` — one factory, every machine; never print
  or commit it), `ticfac skills install`, then `ticfac doctor` and
  `ticfac factory status`.
- New repository: `ticfac init` (`--substrate both` for cloud), commit the
  `.tick/` files, `ticfac doctor --cloud`. The factory's GitHub App installed
  on "All repositories" already covers it; otherwise add the repo to the
  App's installation. Add `[configs]` to its runners files only if it wants
  a choice of config.
- Factory freshness: merging to main deploys it via CI.
  `ticfac factory wait-deployed <sha>` blocks until it runs your commit;
  never poll `gh run list`.
