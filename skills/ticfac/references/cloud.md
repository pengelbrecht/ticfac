# The cloud factory, claude-sub, and other repos and machines

## The three run shapes

| | orchestrator | workers | gate, merges | needs on this machine |
|---|---|---|---|---|
| `ticfac run <epic>` | here | here (pi-durable subprocess; claude CLI for the frontier rung; herdr panes for review/close-out when herdr is live) | here | tk, the harnesses, git identity |
| `ticfac run <epic> --cloud` | factory container | factory containers | factory | `~/.ticfacrc` and push access; the current branch is pushed for you |
| `ticfac run <epic> --cloud-workers` | here | factory containers (the cells of `.tick/runners.cloud.toml`) | here | the checkout, tk, `~/.ticfacrc`; stay awake — the factory ends a run whose machine goes quiet |

Cloud workers run on pi-durable as WorkerAgents (the conversation in the
factory's Durable Object, the tools in a container); `ticfac watch <epic>
<tick>` and `ticfac steer <epic> <tick> <text…>` reach them from any
machine. A local run can still appear on the factory's phone page
(`/status`) and Telegram alerts with `run-epic --status-push` or
`TICFAC_STATUS_PUSH=1`.

## Named configs and claude-sub

`[configs.<name>.roles.*]` tables in the runners files are alternative
routings; a config is routing only (gate, substrate and findings routes stay
the repository's). Selection: `--config` > the epic's `config:<name>` label
> `[configs] default`. `--cloud` takes no `--config`: label the epic
(`tk label add <epic> config:claude`) or use `--cloud-workers`. An epic's
`config:` label on a substrate whose files declare no configs is noted and
ignored.

claude-sub is the cloud's claude on the operator's own subscription:
- A subscription is a factory Worker secret `CLAUDE_SUB_TOKEN_<LABEL>`
  (token from `claude setup-token`), added with `wrangler secret put` from
  `cloudflare/` — no redeploy. Rotate by putting it again; delete with
  `wrangler secret delete`.
- Reached only through a config that routes `claude` on `sonnet`/`opus`.
  No token configured → that config is refused at run start, by
  `ticfac doctor` and at submission.
- Leases are per job and sticky; at most 2 jobs per subscription by
  default. A quota answer benches the subscription until its reset; a
  job's mid-run quota stop is redispatched at the same tier, not climbed.
  No free subscription → that one job runs on the Workers AI pair.
- Pool state: the factory's `GET /api/claude-sub` (labels, leases, benches);
  `ticfac factory status` shows the configured labels.
- Full operator guide: docs/claude-sub-operator.md in the ticfac repo.

Rule of thumb: `glm` (Workers AI, cheap) by default; `claude`
for epics where judgement dominates and retries on GLM would cost more
than they save. Local runs already use the claude CLI for review, close-out
and the top implement tier.

## The factory

- One factory per operator, in their own Cloudflare account. `ticfac factory
  setup` walks the credentials once; `ticfac factory status` re-checks them
  live; `ticfac factory dashboard` watches it read-only.
- CI deploys it from main (`deploy-factory.yml`) after green CI on a commit
  that changes what it ships. To know your merge is live:
  `ticfac factory wait-deployed <merge-sha>`. A local `ticfac factory deploy`
  is the fallback only.
- A deploy never takes a running container: each run keeps the image it
  started on.

## Another machine

1. Install ticfac and its executor side by side (README's `install.sh`).
2. Copy `~/.ticfacrc` from a machine that has it — `factory_url` and
   `factory_token` are shared by every machine of the operator (the file is
   0600 and the only copy of the token; never print, paste or commit it).
   Do not re-run `ticfac factory setup` to get there: the token exists
   only in that file, and re-walking the GitHub App rung registers a new App.
3. `ticfac factory status` should show the factory configured and live;
   `ticfac doctor --cloud` names anything else this machine lacks.
4. In each repo: `ticfac skills install ticfac` (and `tk skills install ticks`).

## Another repository

1. `ticfac init --substrate both` (or `local` / `cloud`), review and commit
   `.tick/runners.toml`, `.tick/runners.cloud.toml`, `.tick/config.md`.
2. GitHub: the factory's GitHub App installed on "All repositories" covers
   every repository of that owner, including new ones; otherwise add the
   repository to the App's installation. Each run gets its own token for
   its one repository.
3. `ticfac doctor --cloud`, then `ticfac run <epic>` / `--cloud`.
4. Optional: copy a `[configs]` block (e.g. ticfac's own `glm`/`claude` in
   `.tick/runners.cloud.toml`) to offer a choice of routing.
5. A repo's own sandbox needs (image, toolchain pins, setup commands) go in
   its `[sandbox]` table; `ticfac sandbox image` and `ticfac sandbox toolchain`
   print what it resolves to.
