# Stops, holds and how each one clears

A run that stops writes one terminal line: its reason class, a message, and
— when a person is needed — the one command that moves it on. `ticfac` (the
overview), `ticfac watch` and `ticfac status <epic> --json` (`waits_on`,
with its `unblock_command`) all name the same command. Copy it rather
than reconstructing it: for a cloud run it carries `--run-id run_<hex>`.

Exit 3 = held for a person. Exit 1 = failed (fix what the line names, run
again). Exit 7 = cancelled on purpose. Exit 5 = still running.

## Resolves itself (supervised continuation — no action)

`run-epic --supervise` (on by default, also under `ticfac run`) resumes
across these by itself, records each as an intervention, and stops after
`--max-resumes` (12) continuations or on a second identical stop over an
unchanged tree:

- `collect_failed` — an attempt rejected with nothing lost; redispatched.
- `attempt_blocked_redispatched` — a worker asked; re-dispatched one tier
  up, or at the ceiling told to decide under the standing orders.
- `rejected_attempt_redispatched` — rejected work disposed of by class.
- `infrastructure_redispatched` — boot died on the gateway/origin; same tier
  again. A claude-sub job that hit its quota mid-job is treated this way too.
- `claim_width`, `foreign_claim`, `claim_holder_unknown` — someone else
  holds the slot or the tick; resumes once it frees.
- `stale_evidence` — the branch moved under a gate; re-derived.
- `closeout_ci_pending`, `closeout_ci_absent`, `land_ci_pending` — CI wait
  ran out; waits again (re-triggering a missing workflow run).
- `closeout_dispatched_over_red_ci` — the repair job fixes CI, a new
  close-out follows.
- `base_fold_replan` — a deferred fold brought new ticks; planned next.
- `land_fold_review` — a fold of the base touched what the epic is about
  after a READY review; reviewed again before land.
- `no_capacity`, transient remote failures, a per-run GitHub token refused
  (a fresh token is minted next incarnation).

Also self-handled, never a stop: a finding past the absorption bound
(backlogged, named in the PR), a NOT READY whose blocking findings can be
absorbed (fix ticks + a re-review), a NOT READY caused only by the base
moving (does not spend the review bound), work closed after a READY review
(reviewed once more before close-out), a silent worker (nudged in its own
session after `--stuck-after`, then stopped).

## Needs a person

| reason | what it means | clear it |
|---|---|---|
| `finding_untriaged` | close-out will not hand over with an untriaged finding | `ticfac triage <epic> <key>=absorb\|file\|fixed:<commit>\|discard` (with the `--run-id` the line names) |
| `epic_amendment_unconfirmed` | a worker wrote to the epic's own record | `ticfac amendments <epic>`, then `ticfac amendment <epic> <key> --confirm\|--reject --by <who>` |
| `attempt_needs_human` | an always-ask question (money, credentials, live systems, scope, force-push), or asked again after being told to decide | answer on the tick (`tk note`), then `ticfac settle <epic> <tick> <n> --release <who> --carry-work`, run again |
| `role_answer_needs_human` | a review/close-out asked an always-ask question | same as above for the role tick |
| `land_review_not_ready` | the final review is still NOT READY after its rounds, tree unchanged | push the fix to `epic/<id>` and run again (one more review), or merge or close the PR yourself |
| `rejected_attempt_carries_work` | a rejected attempt left unmerged commits | read its branch, then `ticfac settle ... --release <who>` (add `--carry-work` to keep the work) |
| `attempt_unaddressed`, `held`, `wiped` | nobody can say whether the attempt runs | `ticfac settle <epic> <tick> <n> --release <who>` (refuses a live attempt) |
| `gate_failed`, `merge_failed`, `boundary_violation`, `undeclared_file_touched` | the work does not pass, integrate or stay in scope, tree unchanged | fix what the line names on the epic branch or the tick, run again |
| `base_refresh_conflict` | the base does not fold into the epic branch | resolve the conflict on `epic/<id>`, run again |
| `epic_absent` | the submitted commit has no `.tick/issues/<epic>.json` | push/commit the epic on the branch you submit from |
| `tier_label_unrecognised`, `touch_label_invalid`, `wall_label_invalid`, `wave_composition_conflict` | a tick label is wrong or two ticks of a wave touch one file | fix the label / the ticks with `tk`, run again |
| `tick_blocked_open`, `closeout_children_open` | an open blocker or child outside the run's plan | close or re-scope it with `tk`, run again |
| `epic_nothing_to_dispatch` | nothing open, no close-out, no review to act on | decide with `tk` what the epic still needs |
| `closeout_*` (forge absent, PR unmet, CI failed) and `land_*` (push refused, base CI failed, epic close) | the PR + CI rule cannot be met | fix the credential / CI job the line names, run again |
| lease taken | another run holds the project's dispatch lease | let it finish or `ticfac cloud stop` it |

## Resuming

- Local: `ticfac run <epic>` (or `ticfac run-epic <epic>` in the
  foreground). It resumes from the recorded state; nothing that passed is
  redone.
- Cloud: `ticfac run <epic> --cloud`. If the live run is holding, this
  SUPERSEDES it — a clean stop, a bounded wait for the factory to release
  the run and its lease, then a new submission and an attach. The new run
  (new run id) reads the earlier run's review rounds and final review.
- `--cloud-workers`: `ticfac run <epic> --cloud-workers` re-attaches,
  re-collects the credential for a dead local process, or starts anew.
- A run keeps its first-selected config; a resume with a different
  `--config` is refused.
