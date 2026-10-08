# ticfac commands

Every command ticfac carries, what it is for, and the flags worth knowing.
`ticfac <command> --help` is the authority; every command also takes
`--json` (one versioned document, `ticfac.<command>.v1`) and `--repo <dir>`
where it reads a checkout.

## Setting up

- `ticfac init` — make a repository runnable. `--substrate local|cloud|both`,
  `--runner claude|pi`, `--model <m>`, `--gate '<cmd>'`, `--closeout pr|none`,
  `--yes`. Refuses to overwrite an existing file.
- `ticfac doctor` — what a run still needs on this machine; `--cloud` adds
  the cloud checks whatever the repo declares. Exit 0 ready, 1 missing.
- `ticfac skills list|get|install` — the skills embedded in this binary.
  `ticfac skills install` installs into `.claude/skills/` and
  `.agents/skills/` (the binary embeds one skill, so the name is optional —
  `ticfac skills install ticfac` names it explicitly and still works);
  `--dir <the skill's own dir>`, `--force` to take over an unstamped dir.
  `ticfac skills get` prints its SKILL.md; `ticfac skills list` prints the
  version each ships with.
- `ticfac version` — this build and the contract bundle it serves.
- `ticfac completion bash|zsh|fish` — shell completion.

## Running

- `ticfac run <epic>` — start in the background and attach; again to
  attach or resume. `--cloud` (all in the factory), `--cloud-workers`
  (orchestrator here, workers in the factory), `--config <name>` (not with
  `--cloud`), `--no-herdr`, `--profiles <dir|herdr>`, `--wall <seconds>`.
- `ticfac run-epic <epic>` — the foreground reconciler `ticfac run` starts;
  what scripts drive. Notable: `--config`, `--tier <name>` (pin a tier),
  `--supervise` (default true), `--max-resumes` (12),
  `--absorption-depth` (default 3), `--stuck-after` (900s, nudge then stop
  a silent worker), `--stall-warn`, `--status-push` (or
  `TICFAC_STATUS_PUSH=1`) to show a local run on the factory's phone page.

## Watching

- `ticfac` — the overview, attention first; `--all` includes history.
- `ticfac status <epic>` — liveness (exit 0 alive / 1 not); `--follow`,
  `--interval`; `--json` is the full status model (ticks, tiers, waits with
  their unblock command, CI, cost).
- `ticfac watch <epic>` — the dashboard; keys j/k, enter, e, q. On a pipe,
  one line per event. Exits with the run's class (0/1/3/5/7).
  `ticfac watch <epic> <tick>` — one pi-durable worker live; `--attempt`.
- `ticfac events <epic>` — the JSONL feed. `--follow`, `--tail N`,
  `--from <cursor>`, `--from-start`.
- `ticfac steer <epic> <tick> <text…>` — speak to a running pi-durable
  worker (local or cloud); `--attempt`, `--request-id` for idempotent retry.
- `ticfac herd paint|notify` — herdr pane badges and chimes; display-only,
  safe from a hook.

## Deciding

- `ticfac findings <epic>` — the worker findings and their triage state.
- `ticfac triage <epic> [<key-prefix>=<decision>...]` — absorb, file,
  `fixed:<commit>`, discard; interactive without decisions; `--by <who>`,
  `--run-id` for a run stored under another id (cloud runs).
- `ticfac finding <epic> <key>` — one decision by full key:
  `--promote-as <tick>`, `--discard`, `--fixed-as <commit>`, `--by`.
- `ticfac amendments <epic>` — worker notes on the epic's own record that
  wait for the operator; `ticfac amendment <epic> <key-prefix> --confirm`
  or `--reject`, `--by <who>`.
- `ticfac settle <epic> <tick> <attempt> --release <who>` — release an
  attempt nobody can address, or one held over a rejected attempt's work;
  `--carry-work` bases the next attempt on its commits; `--run-id` for a
  cloud run. The hold line prints the exact command — copy it.

## Cloud (the expert half; `ticfac run --cloud` is the everyday one)

- `ticfac cloud run <epic>` — push and submit; `--queue`, `--max-cost`,
  `--max-wall-clock`, `--notify`.
- `ticfac cloud status [<run|epic>]` — runs, leases, queue; or one run.
- `ticfac cloud stop <run|epic>` — clean stop; `--now` revokes the run's
  credential and skips close-out.
- `ticfac cloud logs <run|epic>` — container output; `-f`, `--tail N`,
  `--tick <id>` for one worker container.
- `ticfac cloud trace <run|epic>` — the model calls; `--tick`, `--tools`,
  `--call N`, `--cache`.
- `ticfac cloud supervisor <run|epic>` — whether the Workflow is alive;
  `--steps N`.
- `ticfac cloud branch <name>` — container-side only (a sandbox recording a
  branch it created); never an operator command.

## Factory

- `ticfac factory status` — what is configured and whether it works, live;
  `--offline`, `--check` (nonzero on a rejected credential).
- `ticfac factory wait-deployed <sha>` — block until the factory runs a
  commit containing `<sha>`: 0 live, 1 deploy/CI failed, 4 no such commit,
  5 `--timeout` (90m). A superseded deploy is normal; it keeps waiting.
- `ticfac factory dashboard` — live read-only board of the factory.
- `ticfac factory deploy` — local deploy; the fallback only (first install,
  token rotation, CI unable). CI deploys main.
- `ticfac factory setup` — first-run credential walk: deployment, the
  factory's GitHub App (or device flow / PAT), AI Gateway.
- `ticfac factory webhook` — point Telegram at the factory (`--status`,
  `--delete`).

## Housekeeping and container-side

- `ticfac sweep refs` — delete origin refs of ended runs; `--dry-run`,
  `--grace-days` (14). Never touches `epic/*`, main or tags.
- `ticfac sandbox image|toolchain|model|substrate|setup|environment|worker-prompt`
  — what the repo's `[sandbox]` table declares; the image scripts call
  these. `ticfac sandbox substrate` is handy to see what a run resolves.
