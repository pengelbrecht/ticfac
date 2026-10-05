# Herdr Kinds — Capability Matrix

This file is the **per-kind lookup table** for driving a Herdr-managed coding agent as a tick implementer: how to spawn one non-interactively with the model and permission mode you intend, whether its lifecycle state is trustworthy, and how to get back into its native session after the pane is gone. The capability matrix it documents is encoded in this package's `kinds.go` and enforced by `Compile` (`compile.go`); the runner selection that decides you are on Herdr at all (`substrate = herdr | harness | auto | cloud` in `.tick/runners.toml`) lives in `runners-config.md` (published by ticks, pinned here through the vendored contract bundle).

This is ticfac's copy of the reference, lifted from ticks' skill doc (`herdr-kinds.md` in the ticks skill's references, which ticks deleted with its execution surface at epic chz, v0.32.0) at the av8 base, with a **pi** section added from the live verification run of 2026-09-10 (tick gjk — pi 0.85.1, herdr protocol 22 (0.9.0); evidence: `.tick/logs/herd/av8/gjk.RESULT.md`) and every stale pi statement the old copy carried corrected to what that run observed. Since chz this copy is the only one; the claude/codex/opencode sections here are transcriptions of their original live round trips (2026-08-12/13), not re-derivations.

Ticfac's Herdr **pi kind is deleted** (epic 43y, tick uxi): a Herdr agent template spawns the pi CLI, and the pi CLI is not a worker harness any more — pi-durable is, headless through the local subprocess executor or hosted in a cloud container, never in a Herdr pane. The spawner therefore refuses a Herdr profile routed to `kind = "pi"`, even though Herdr itself still advertises that kind. The live observations below are kept where they are history of how the facts were learned; the pi spawn/oracle machinery they describe (`--approve`, `pi --list-models` as a compile-time oracle) is gone from the code and must not be re-added as a Herdr kind. The name `pi` survives as the durable harness's local runner name, which is exactly why the refusal is the mechanism that matters.

A **kind** is Herdr's `--kind` value — the canonical executable it knows how to launch and detect. As of the version verified here, `herdr agent start --help` lists:

```
pi, claude, codex, gemini, cursor, devin, agy, cline, omp, mastracode,
opencode, copilot, kimi, kiro, droid, amp, grok, hermes, kilo, qodercli, maki
```

Three of those — **claude**, **codex** and **opencode** — are verified below as tick implementers. A fourth, pi, was verified 2026-09-10 (tick gjk) and deleted with the pi-CLI worker path (epic 43y, tick uxi): a pi pane was the pi CLI, and the worker harness is pi-durable now. The rest are reachable through the same three-step recipe in [Adding a kind](#adding-a-kind); do not assume a template for a kind nobody has round-tripped.

## The spawn contract

**In a tick run you do not type these calls: `ticfac run-epic`'s Herdr executor does.** It handles startup races and records delivery confirmation; the stricter content round-trip below is what you run by hand when *verifying a new kind*. The loop in [Adding a kind](#adding-a-kind) is exactly this, in a scratch pane.

Herdr does not launch processes out of nowhere. A worker is a **pane running the agent's own CLI**, and Herdr attaches lifecycle tracking to it:

```bash
# 1. Make a pane. Read .result.pane.pane_id from the response.
herdr pane split --current --direction down --cwd "<worktree>" --no-focus

# 2. Start the agent in it. Everything after `--` is the native CLI's argv.
herdr agent start <name> --kind <kind> --pane <pane-id> --timeout 120000 -- <native args>

# 3. Drive it.
herdr agent prompt <name> "<text>" --wait --timeout <ms>
```

Three properties of this contract matter for orchestration:

- **Args after `--` are passed through verbatim.** The `agent_started` response echoes the exact `argv` it executed — always read it back as your proof that the model and permission flags landed, rather than trusting the template.
- **The pane must be at an interactive shell prompt**, and `--cwd` is where model selection meets isolation: point it at the tick's worktree so the agent's own project-instruction discovery (`CLAUDE.md` / `AGENTS.md`) resolves from the worktree, not the controller checkout.
- **`start` succeeding means "the expected agent was detected and is ready for input"** — nothing more. See [The green-start trap](#the-green-start-trap).

Cleanup is symmetric and mandatory: exit the agent, then `herdr pane close <pane-id>` for every pane you created. Never `herdr server stop`; never touch a pane, workspace, or agent you did not create.

That two-step is for panes you made yourself with `herdr pane split` — a scratch pane for verifying a kind, say. A worker spawned through `herdr worktree create` is cleaned up in **one** call instead: `herdr worktree remove --workspace <id>` tears down the worktree, the workspace, its pane, and the running agent together, with no prior exit needed (verified live). Do not do both; see `herdr-runner.md` → *Cleanup* for the ordering rule that governs when either is allowed to run at all.

## Capability matrix

| Kind | Model flag | Full-auto / skip-permissions | Integration (this machine) | `agent_session` kind | Native resume |
|---|---|---|---|---|---|
| `claude` | `--model <alias\|full-name>` | `--permission-mode bypassPermissions` (or `--dangerously-skip-permissions`) | **current (v7)** — `~/.claude/hooks/herdr-agent-state.sh` | `id` (UUID), **not dependable at start** — see below | `claude --resume <id>` |
| `codex` | `-m, --model <MODEL>` | `-a never -s workspace-write` **plus `--add-dir <git-common-dir>` in a linked worktree** (see [codex](#codex)) | **outdated (v6 < v7)** — `~/.codex/herdr-agent-state.sh` | `id` (UUID), appears **after the first prompt** | `codex resume <id>` |
| `opencode` | `-m, --model <provider>/<model>` | `--auto` (see [opencode](#opencode)) | **current (v9)** — `~/.config/opencode/plugins/herdr-agent-state.js` | `id` (`ses_…`, not a UUID), appears **after the first prompt** and **never on a resumed session** | `opencode -s <id>` |

The deleted `pi` row (`--model <provider>/<model>`, full-auto `--approve`, `agent_session` a `~/.pi/agent/sessions/…jsonl` path) went with the kind (epic 43y, tick uxi).

Everything in that table was read from the CLIs' own `--help` and then round-tripped live; flag names drift between releases, so re-verify with `--help` rather than copying this table into a new environment unchecked.

## Model and effort translation

`.tick/runners.toml` specifies a worker along **two dimensions**: `kind` (the runner/harness) and `model` + optional `effort` (the capability). When the resolved profile selects the Herdr executor, those fields are **compiled by the spawner** into one of this file's supported native argv forms. Other executors interpret their own runner names — notably local `kind = "pi"` means the headless pi-durable host and never reaches this table. The config-side rules live in [`runners-config.md`](runners-config.md).

The compilation below, its fixed argv order, and the fail-closed refusal further down are implemented by this package (`kinds.go` / `compile.go`) and called by `ticfac run-epic` before it dials Herdr. The argv Herdr echoes on `agent_started` is the ground truth for what actually landed: read it back rather than trusting the template.

Compiled argv order is fixed: **full-auto template → the kind's computed per-spawn extras → compiled `model`/`effort` flags → `args` verbatim.**

The third position is empty for most kinds. A **computed per-spawn extra** is argv a kind needs that only the *repository* can name, so it is rendered at spawn time from a context the spawner resolves rather than read from `.tick/runners.toml`. Today there are two: codex's `--add-dir <git-common-dir>` below, and claude's `--settings '{"env":{"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS":"1"}}'`, which needs nothing from the repository and is an extra because it is not an approval flag: a claude worker must not background work whether or not it runs full-auto (epic-2jn vqc: a worker backgrounded the gate, ended its turn to wait for it, and sat finished with no report; verified live 2026-09-27 on claude 2.1.283 that herdr passes the JSON intact and the Bash tool loses `run_in_background`). It is declared on the kind row like every other template, lands in the same argv, and is passed through verbatim with the rest; a kind that declares one and cannot render it refuses the spawn rather than starting a worker missing it.

| Kind | `model = "M"` compiles to | `effort = "E"` compiles to | Verified against |
|---|---|---|---|
| `claude` | `--model M` | `--effort E` | `claude --help`: `--effort <level>` — `(low, medium, high, xhigh, max)` |
| `codex` | `-m M` | `-c model_reasoning_effort="E"` | `codex --help`: no reasoning-effort flag exists; effort is a config override via `-c <key=value>` |
| `opencode` | `--model M` (M is a full `<provider>/<model>` id) | **nothing — the dimension does not exist in argv.** A role with `effort` set is a spawn refusal | `opencode --help`: no effort/thinking/variant flag on the interactive command. `opencode run --help` has `--variant <level>` ("provider-specific reasoning effort"), but that is the one-shot subcommand, and passing `--variant` to the TUI is a hard startup failure |

All three rows are round-tripped live (see their sections below) — `claude`/`codex`/`opencode` on 2026-08-12/13. (The pi row — effort as a `:E` suffix on the model id, `--thinking E` when no model was named — went with the kind, epic 43y, tick uxi.)

`opencode` is the first kind with **no effort dimension at all**, and the spawner treats that as a refusal rather than a silent drop: dropping the level would run an `economy` tier at whatever variant the model defaults to (`max`, on the machine verified here), which is precisely the "quietly did the wrong work" outcome the fail-closed rule exists to prevent. Vary an opencode role's tiers by `model`, or set the variant on an agent in opencode's own config (`agent.<name>.variant`) and select it with `args = ["--agent", "<name>"]`.

Worked examples of the compilation:

```text
kind="claude" model="opus"          effort="high"   ->  --model opus --effort high
kind="codex"  model="gpt-5.6-luna"  effort="max"    ->  -m gpt-5.6-luna -c model_reasoning_effort="max"
kind="opencode" model="openai/gpt-5.6-luna"  (no effort)
                                                    ->  --auto --model openai/gpt-5.6-luna
kind="opencode" model="openai/gpt-5.6-luna"  effort="high"
                                                    ->  REFUSED — opencode has no argv form for effort
```

`effort` is optional everywhere. Omitting it means "the kind's own default" — the same reasoning as omitting `model` (see the [green-start trap](#the-green-start-trap) and `runners-config.md`'s authoring rules): codex reads `model_reasoning_effort` from `~/.codex/config.toml`, claude uses its session default.

### Which models each kind accepts

The matrix is **sparse**. A kind is a specific vendor CLI authenticated against a specific account, and it can only run models from the family that CLI serves:

| Kind | Accepts | Examples | Rejects |
|---|---|---|---|
| `claude` | Claude-family only — aliases for the latest model in a family, or full names | `opus`, `sonnet`, `haiku`, `fable`, `claude-fable-5` | any `gpt-*`, `gemini-*`, or `<provider>/<model>` id |
| `codex` | OpenAI models the authenticated Codex account may use | `gpt-5.6-luna`, `gpt-5.6-sol` | Claude/Gemini names; also *plausible-looking* OpenAI names the account is not entitled to |
| `opencode` | Multi-provider, **provider-qualified only** — exactly as `opencode models` prints them | `openai/gpt-5.6-luna`, `openai/gpt-5.6-sol`, `opencode/big-pickle` | bare ids (`gpt-5.6-luna`), claude aliases, a `:<variant>` suffix, and any id this install has no provider credentials for — **none of which produce an error**, see below |

(The deleted `pi` row — cross-provider, provider-qualified, validated against `pi --list-models` at compile time — went with the kind, epic 43y, tick uxi. No compiled kind ships a model oracle any more: a well-formed but non-existent id is the green-start trap's to catch, everywhere.)

Effort levels are sparse too: `claude --effort` accepts `low, medium, high, xhigh, max` (no `off`/`minimal` — those enum values currently name a level no compiled kind accepts); codex's `model_reasoning_effort` is a config string whose accepted set is the model's, not the CLI's (`max` is live on this machine's `~/.codex/config.toml`); `opencode` accepts **no level at all** on the interactive CLI, so every level is refused.

Do not treat any model string here as durable. These are dated observations; the vendor CLIs are the authority, and `.tick/runners.toml`'s `[roles.*]` tables carry the ids a given repo actually runs.

### Fail closed on an impossible cell

**The schema validates shape; the spawner enforces compatibility.** `runners-config.schema.json` cannot enumerate model families — they are open-ended and change between releases — so `kind = "claude"` with `model = "gpt-5.6-luna"` is a perfectly *valid* config document. It is still an impossible cell.

**Rule: before spawning, check the model against the kind's accepted family. On a mismatch, refuse the spawn with a clear message naming the role/tier, the kind, and the model — and never silently reroute** to a kind that would accept the model, never drop the model and fall back to the CLI's default. Both of those turn a config bug into a run that quietly did the wrong work on the wrong vendor.

`ticfac run-epic` enforces this through `Compile`, **before it dials Herdr**: a refusal makes no workspace, worktree, branch or attempt to clean up. The original zero-call observation was verified live against the old helper and Herdr 0.8.0 in 2026-08; current tests pin the ordering in ticfac's own spawner.

```text
.tick/runners.toml [roles.implement.tiers.strong]: kind = "claude" cannot run
model = "gpt-5.6-luna" (claude runs Claude-family models only). Fix the config —
either kind = "codex" for that model, or a Claude model for that kind.
```

This is a stop, matching `runners-config.md`'s rule that a config the orchestrator cannot honour halts the run rather than being guessed around. The cost of *not* stopping is documented right below: an unusable model name starts green, prompts green, and does zero work.

## claude

**Verified template.**

```bash
herdr agent start <name> --kind claude --pane <pane-id> --timeout 120000 \
  -- --model sonnet --permission-mode bypassPermissions
```

- **Model.** `--model` takes an alias for the latest model in a family (`fable`, `opus`, `sonnet`) or a full name (`claude-fable-5`). Prefer the alias — it survives model refreshes, which matters for a template committed to a skill. Effort is a separate axis: `--effort <low|medium|high|xhigh|max>`.
- **Full auto.** `--permission-mode bypassPermissions` is the flag to reach for. It sets the mode for the session and shows up in the pane footer as `⏵⏵ bypass permissions on`. `--dangerously-skip-permissions` is the equivalent blunt form and `--allow-dangerously-skip-permissions` only *enables the option* without turning it on — do not confuse the three. The `--permission-mode` spelling is preferable because the same flag reaches the non-bypass modes (`acceptEdits`, `auto`, `plan`, `manual`, `dontAsk`) when a tick should run gated.
- **Worth knowing.** `--add-dir` widens tool access beyond the worktree; `--session-id <uuid>` lets the orchestrator *choose* the session id up front instead of discovering it afterwards, which is the cheapest way to make the resume story deterministic.

**Live evidence.**

```
$ herdr agent start htg-claude --kind claude --pane w4C:p2 --timeout 120000 \
    -- --model sonnet --permission-mode bypassPermissions
{"result":{"agent":{"agent":"claude","agent_status":"idle","interactive_ready":true,
  "agent_session":{"agent":"claude","kind":"id","source":"herdr:claude",
                   "value":"c13950d3-e722-46f3-87bd-7079658b475d"}},
  "argv":["claude","--model","sonnet","--permission-mode","bypassPermissions"]}}

$ herdr agent prompt htg-claude "Reply with the single word OK" --wait --timeout 120000
{"result":{"agent":{"agent_status":"idle", ...}}}

$ herdr agent read htg-claude   # tail
❯ Reply with the single word OK
⏺ OK
  ⏵⏵ bypass permissions on (shift+tab to cycle)
```

The pane banner confirmed `Sonnet 5 with medium effort` and the worktree cwd, so both halves of the template took effect.

**`agent_session` on the start response is not dependable.** The run above got one; a later run on the same machine (herdr 0.8.0, `--model haiku`) got an `agent_started` response with no `agent_session` key at all, and the id first appeared on the `agent prompt` response. Whether it is present at start appears to depend on how quickly the integration hook reports in, which is a race you do not control. **Capture `agent_session` after the first round-trip for every kind, not just codex** — an orchestrator that reads it only from `agent_started` will intermittently record nothing and lose the cheap resume path.

## codex

**Verified template.**

```bash
herdr agent start <name> --kind codex --pane <pane-id> --timeout 120000 \
  -- -m <model> -a never -s workspace-write --add-dir <git-common-dir>
```

`<git-common-dir>` is `git rev-parse --git-common-dir` resolved from the repo root — never a hardcoded `.git`, because a repo may keep it elsewhere. It is required whenever the worker runs in a **linked worktree**, which is every worker ticfac's Herdr executor starts; see *Full auto* below.

- **Model.** `-m, --model <MODEL>`. Codex resolves the default from `model = "..."` in `~/.codex/config.toml`; when in doubt read that file rather than guessing a model string (see [The green-start trap](#the-green-start-trap) for why guessing is expensive here). Reasoning effort is config, not a flag: `-c model_reasoning_effort="high"`.
- **Full auto.** There is no `--full-auto` flag on the interactive `codex` CLI at the version verified here. Full autonomy is the **pair** `-a never` (never ask for approval; failures are returned to the model) plus `-s workspace-write` (the sandbox the agent may write in). `--dangerously-bypass-approvals-and-sandbox` is the harder form — skips approvals *and* removes the sandbox — and is only appropriate when the surrounding environment is itself sandboxed. For tick implementers in a git worktree, `-a never -s workspace-write` is the right default: the agent is unblocked but the sandbox still bounds the blast radius. Add `--add-dir <dir>` if a tick legitimately needs to write outside its worktree.
- **Linked worktrees: `workspace-write` alone cannot commit** *(observed 2026-08-12, tick s8u)*. A codex worker started with the pair above in a **linked** worktree did its work and then could not commit it — the orchestrator had to commit on its behalf. A linked worktree's `.git` is a *file* pointing at `<git-common-dir>/worktrees/<name>/`, so the index, the refs and the objects a commit writes all live **outside** the directory `workspace-write` bounds. Nothing about it looks like a permission problem from the config side: the spawn is green, the work happens, only the durable step fails.

  The fix is the mechanism named in the bullet above, applied by the spawner rather than left to the tick: **`--add-dir <git-common-dir>`**, where the path comes from `git rev-parse --git-common-dir` at the repo root. Ticfac compiles it into every Herdr codex argv as a [computed per-spawn extra](#model-and-effort-translation) and refuses the spawn if it cannot resolve it — a codex worker that starts without it is a worker that will fail at the last step.

  **Do not reach for `--dangerously-bypass-approvals-and-sandbox` here.** It would also let the commit through, and it is wrong by this document's own rule: it removes the sandbox entirely and is only appropriate inside an already-sandboxed environment. A worktree on a developer's machine is not one. Widening the sandbox by exactly one directory is the proportionate fix.
- **Worth knowing.** `-C, --cd <DIR>` sets the agent's working root independently of the pane's cwd, and `--search` enables web search without per-call approval.

**Live evidence.**

```
$ herdr agent start htg-codex2 --kind codex --pane w4C:p2 --timeout 120000 \
    -- -m gpt-5.6-luna -a never -s workspace-write
{"result":{"agent":{"agent":"codex","agent_status":"idle","interactive_ready":true},
  "argv":["codex","-m","gpt-5.6-luna","-a","never","-s","workspace-write"]}}
  # note: no agent_session on the start response

$ herdr agent prompt htg-codex2 "Reply with the single word OK" --wait --timeout 120000
{"result":{"agent":{"agent_status":"idle",
  "agent_session":{"agent":"codex","kind":"id","source":"herdr:codex",
                   "value":"019ff08d-023c-7f70-b602-b7b798fcd531"}}}}

$ herdr pane read <pane-id>   # tail
│ model:     gpt-5.6-luna max   /model to change           │
› Reply with the single word OK
• OK
```

**The codex session id is not available until after the first prompt.** Any orchestrator that records `agent_session` at spawn time will record nothing for codex. Capture it after the first successful round-trip.

## opencode

**Verified template** *(opencode 1.18.18, herdr 0.8.0, 2026-08-13)*.

```bash
herdr agent start <name> --kind opencode --pane <pane-id> --timeout 120000 \
  -- --auto --model <provider>/<model>
```

- **Model.** `-m, --model <provider>/<model>` — the id must be **provider-qualified**, in the exact spelling `opencode models` prints (`openai/gpt-5.6-luna`, `opencode/big-pickle`). There is no bare-name form.
- **Full auto.** `--auto` — "auto-approve permissions that are not explicitly denied". opencode gates on **per-permission asks**, not a filesystem sandbox, so this single flag is the whole full-auto story; the pane footer reads `Build auto · …` when it took effect. Without it a worker will stall on the default `ask` rules — the built-in `build` agent asks for `external_directory` on `*` (`opencode debug agent build`), which a linked worktree's out-of-tree git metadata trips.
- **No effort dimension.** The interactive command has no `--effort`/`--thinking`/`--variant` flag. `--variant` exists on `opencode run` only, and handing it to the TUI is a **hard failure, not a warning**: yargs prints its usage block and exits, so `herdr agent start` returns `{"error":{"code":"timeout","message":"timed out waiting for agent startup"}}` with the pane back at a shell prompt. Do not route effort into `args`. The variant is set per-agent in opencode's own config (`agent.<name>.variant`) and selected with `--agent <name>`; the footer shows the active one (`… · max`).
- **Linked worktrees need no grant.** Unlike codex, opencode has no sandbox to widen: with `--auto` a worker in a linked worktree commits normally. Verified end to end 2026-08-13 through the then-current `tk herd` helper — a real worker created a file, committed it on `tick/<id>` from `~/.herdr/worktrees/<repo>/tick-<id>`, and collected `ready-to-merge`. The `opencode` kind therefore declares **no computed per-spawn extras**.
- **No one-time startup gates observed.** codex's workspace-trust and hooks-review prompts have no opencode analogue: fresh spawns in never-before-seen directories (a scratch `/tmp` repo and a fresh worktree) went straight to the composer. Nothing had to be approved, so nothing had to be cleared.
- **Worth knowing.** `--agent <name>` selects a configured agent; `--prompt <text>` seeds the first turn; `--mini` is a minimal interface; `--port`/`--hostname` matter only for the server modes. `opencode models` is the authority on ids, `opencode debug config` on what config actually resolved.

**Live evidence.**

```
$ herdr agent start iv9-oc1 --kind opencode --pane w7R:p1 --timeout 120000 \
    -- --model openai/gpt-5.6-luna --auto
{"result":{"agent":{"agent":"opencode","agent_status":"idle","interactive_ready":true,
  "terminal_title_stripped":"OpenCode"},
  "argv":["opencode","--model","openai/gpt-5.6-luna","--auto"]}}
  # note: no agent_session on the start response

$ herdr agent prompt iv9-oc1 "Reply with the single word OK" --wait --timeout 120000
{"result":{"agent":{"agent_status":"done","terminal_title_stripped":"OC | OK",
  "agent_session":{"agent":"opencode","kind":"id","source":"herdr:opencode",
                   "value":"ses_005139d87ffeKwOWE4TWS2ZNwQ"}}}}

$ herdr pane read w7R:p1   # tail
  ┃  Reply with the single word OK
     OK
     ▣  Build · GPT-5.6 Luna · 2.0s
     Build auto · GPT-5.6 Luna OpenAI · max
```

The first prompt landed on the first attempt, and the footer confirms both halves of the template: `auto` for the permission mode, `GPT-5.6 Luna OpenAI` for the model.

**The session id is not available until after the first prompt** — same as codex. It is **not a UUID** (`ses_005139d87ffeKwOWE4TWS2ZNwQ`); treat it as an opaque token. And it is reported **only when the session is created**: after a resume, `agent_session` stays absent on `agent start`, on `agent prompt` and on `api snapshot`, even once the resumed agent has answered a fresh turn. An orchestrator must therefore keep the id it recorded at first spawn rather than re-reading it after a restart.

**Lifecycle state comes entirely from the integration.** `herdr agent explain` reports `screen_detection_skip_reason: full_lifecycle_hook_authority` for opencode with the v9 plugin installed — herdr does not consult screen rules at all, so there is no `rule:`/`evidence:` line to read. That is a stronger position than claude, where a screen rule still won the transitions observed above. It also means an *uninstalled or broken* opencode integration is not a degraded state but an absent one: check `herdr integration status` before trusting `--wait`.

**A bad model id is invisible — the content gate cannot catch it.**

```
$ herdr agent start iv9-oc3 --kind opencode --pane w7R:p1 -- --model openai/gpt-9-imaginary --auto
{"result":{"agent":{"agent_status":"idle","interactive_ready":true}, "argv":[…]}}   # green

$ herdr agent prompt iv9-oc3 "Reply with the single word OK" --wait --timeout 120000
{"result":{"agent":{"agent_status":"done", …}}}                                     # green

$ herdr pane read <pane-id>
  ┃  Reply with the single word OK
     OK                                                        # the gate PASSES
     ▣  Build · DeepSeek V4 Flash Free · 2.8s                  # …on the wrong model
     Build auto · DeepSeek V4 Flash Free OpenCode Zen · max
```

opencode does not reject an id it cannot resolve; it **silently falls back to its own default model** — here a free DeepSeek — and answers correctly. This is the [green-start trap](#the-green-start-trap) with its one cheap defence removed: for codex a bad id surfaces as a visible 400 that the first-round-trip gate catches, and for opencode nothing surfaces at all. The same silent substitution swallows a `:<variant>` suffix and a bare (unqualified) id.

Two consequences: `Compile` makes the kind's family check **strict** — provider-qualified, no `:` — so that whole malformed-id class is refused before a pane exists; and when verifying a kind by hand, **read the pane footer for the model name**, not just the answer.

**Resume is a flag, and the CLI prints it for you.** A single `C-c` exits opencode (claude and codex need two), and the exit screen is the cross-check:

```
Session   OK
Continue  opencode -s ses_005139d87ffeKwOWE4TWS2ZNwQ
```

Restarting in the same pane with `-- --model … --auto -s <id>` replayed the prior turn in the restored transcript, and a fresh prompt continued the same session (both turns visible). Note that the agent name is released the instant the process exits, so the restart needs a fresh `<name>`.

## pi — deleted

Ticfac's Herdr pi kind is **gone** (epic 43y, tick uxi): a Herdr pane running `--kind pi` ran the pi CLI, and that was the last pi-CLI worker path in the tree. The spawner refuses `kind = "pi"` at compile time with a fix naming the durable harness — pi-durable — which runs headless through the local subprocess executor (the local runner table names it `pi`) or hosted in a cloud container (kind `pi-durable`), never in a Herdr pane. `herdr agent` still lists a pi kind; pairing it with the Herdr executor is a config error the run refuses at start.

The history this section used to carry, kept for what it taught: pi had no permission gate (`--approve` covered project-local file trust only, verified live 2026-09-10, tick gjk — `.tick/logs/herd/av8/gjk.RESULT.md`); `pi --list-models` was a real model oracle, and a typo'd pi id passed through as a "custom model id", returned an **empty turn at exit 0** and cost a full worker round — which is why the catalog rung existed; pi's `agent_session` was a `~/.pi/agent/sessions/…jsonl` transcript **path**, not a UUID; its interrupt was Escape (ctrl+c is app.clear), and it needed ctrl+c **twice** to exit. None of that is reachable through this spawner any more, and none of it licenses re-adding the kind: the worker harness is pi-durable, and a durable worker's conversation is resumable by process, not by pane.

## Integrations

`herdr integration status` reports, per kind, whether Herdr's hook/extension is installed in that agent's own config tree:

```
$ herdr integration status
pi: outdated (v5 < v8) (/Users/…/.pi/agent/extensions/herdr-agent-state.ts)
claude: current (v7) (/Users/…/.claude/hooks/herdr-agent-state.sh)
codex: outdated (v6 < v7) (/Users/…/.codex/herdr-agent-state.sh)
copilot: not installed (…)
… (16 kinds listed)
```

The `pi` line is Herdr reporting one of its own integrations, not ticfac advertising a compiled kind; ticfac refuses that pairing as described above. The hook is not always a shell script: opencode's is a **plugin**, `~/.config/opencode/plugins/herdr-agent-state.js` (`current (v9)` as of 2026-08-13). `herdr integration install <kind>` is still the one command.

**Why this matters.** Without an integration, Herdr infers `idle | working | blocked | done` from the terminal — OSC titles and prompt-box rendering. That is a heuristic over a TUI that the vendor is free to restyle in any release. With an integration, the agent's own hooks report lifecycle transitions and session identity to Herdr directly, which is what makes `herdr agent prompt --wait` and `herdr agent wait --until done` safe to build a wave loop on. The `agent_session.source` values observed above (`herdr:claude`, `herdr:codex`, `herdr:opencode`) are the integration reporting session identity through `herdr pane report-agent-session`.

**For a kind ticfac compiles, when status says `outdated`, the fix is `herdr integration install <kind>`** — same command as a fresh install; it upgrades in place. Do this before an epic run, not during one. An outdated integration is not automatically broken (codex at v6 against a v7 expectation still round-tripped cleanly here), but it is the first thing to rule out when lifecycle state goes wrong. Do not install pi's integration for ticfac: that kind is refused, not merely outdated.

`herdr agent explain <name>` tells you which signal actually decided the current state, and is the diagnostic to reach for before believing or disbelieving a status:

```
$ herdr agent explain htg-claude2        $ herdr agent explain htg-codex-r
agent: claude                            agent: codex
state: working                           state: idle
rule: osc_title_working (priority=1100)  rule: osc_title_idle (priority=100)
evidence: "⠐ Count to five slowly"       evidence: "agent-a5c1efa27088b6d37"
```

Note what this shows: on this machine, even with claude's integration `current`, the *winning* signal for these transitions was a screen rule. Integrations raise the floor — they supply session identity and authoritative transitions — but the state you read is whichever signal is currently highest-priority. Treat `explain` as the ground truth about **why** a state is what it is.

## Resume

Herdr exposes each agent's native session identity on `herdr api snapshot` (and on `agent get` / `agent prompt` responses) under `agent_session`:

```bash
herdr api snapshot | jq '.result.snapshot.agents[]
  | select(.name=="<name>") | {name, agent, agent_session}'
```

`agent_session.kind` is `id` for claude, codex and opencode. `value` is the token the resume flag takes for the `id` kinds — a UUID for claude and codex, an opaque `ses_…` string for opencode. All three CLIs also print the resume form on exit, which is a useful cross-check that Herdr's view is the real one:

| Kind | On exit the CLI prints | Resume through Herdr |
|---|---|---|
| `claude` | `Resume this session with: claude --resume <uuid>` | `herdr agent start <n> --kind claude --pane <p> -- --model sonnet --permission-mode bypassPermissions --resume <uuid>` |
| `codex` | `To continue this session, run codex resume <uuid>` | `herdr agent start <n> --kind codex --pane <p> -- resume <uuid> -m <model> -a never -s workspace-write --add-dir <git-common-dir>` |
| `opencode` | `Continue  opencode -s <ses_…>` | `herdr agent start <n> --kind opencode --pane <p> -- --auto --model <provider>/<model> -s <ses_…>` |

(The deleted pi row — `pi --session <uuid>`, exited with ctrl+c **twice** — went with the kind, epic 43y, tick uxi.)

All three were verified live: after `C-c`-ing each agent (twice for claude and codex, **once** for opencode) and restarting it in the same pane under a fresh name with the resume form, `herdr pane read` showed the prior turn (`Reply with the single word OK` → `OK`) replayed in the restored transcript, and for claude the `agent_session.value` was unchanged across the restart.

Note the shape difference. Claude's and opencode's resumes are **flags** that compose with the rest of the spawn template (`--resume <uuid>`, `-s <ses_…>`). Codex's is a **subcommand** — `resume` and its `SESSION_ID` come first in argv, then the ordinary flags (`-m`, `-a`, `-s`, `--add-dir` all exist on `codex resume` too). Beware the collision: `-s` means *sandbox* to codex and *session* to opencode. A manually restarted worker stays in the same linked worktree, so codex needs the same `--add-dir <git-common-dir>` grant. Ticfac's current recovery adopts a still-live pane rather than reconstructing a vanished CLI from this native session id; these forms are the manual/native recovery reference. Both codex and opencode accept a picker when the id is omitted; automation must always pass the id, because a picker is an interactive prompt that will hang the wave.

Two escape hatches worth knowing: `claude --fork-session` resumes into a *new* session id (useful when you want the context but not to mutate the original), and `codex fork --last` is codex's equivalent. `claude --session-id <uuid>` inverts the problem entirely by letting the orchestrator assign the id before the process exists.

## The green-start trap

`herdr agent start` returning `agent_status: idle, interactive_ready: true` means Herdr found the expected agent at a prompt. It does **not** mean the agent can do work. Observed live:

```
$ herdr agent start htg-codex --kind codex --pane w4C:p2 -- -m gpt-5.1-codex -a never -s workspace-write
{"result":{"agent":{"agent_status":"idle","interactive_ready":true}, "argv":[…]}}   # green

$ herdr agent prompt htg-codex "Reply with the single word OK" --wait --timeout 120000
{"result":{"agent":{"agent_status":"idle", …}}}                                     # also green

$ herdr pane read <pane-id>
⚠ Model metadata for `gpt-5.1-codex` not found. Defaulting to fallback metadata…
■ {"type":"error","status":400,"error":{"message":"The 'gpt-5.1-codex' model is not
   supported when using Codex with a ChatGPT account."}}
```

An invalid model name produced a clean start, a clean prompt, and a settled `idle` state — with zero work done. Lifecycle state describes the *terminal*, not the *turn*.

**The trap has a worse form, and opencode has it: an unresolvable model id is not an error there at all.** The CLI silently substitutes its own default model, so the start is green, the prompt is green, *and the gate's expected answer arrives* — from the wrong model. See [opencode](#opencode) for the transcript. Where a content gate closes the trap for claude and codex, only two things close it here: a strict provider-qualified family check before the spawn, and reading the **model name in the pane footer** rather than only the answer.

**pi had a third form, verified 2026-09-10 (tick gjk): the id passes through as a "custom model id"** — a stderr warning, an empty turn, and **exit 0**. No substitution (better than opencode: no turn runs on a different model) and no failure either (worse than codex's visible 400). That observation is history now — the pi kind is deleted and its `pi --list-models` oracle went with it — but the class it names is not: a harness that accepts an unknown model id silently is a harness whose pane footer you must read when verifying by hand.

**Rule when verifying a kind: gate it on the content of its first round-trip, not on lifecycle status.** Send a trivial prompt whose answer you can pattern-match (`Reply with the single word OK`) and read the pane for the answer before adding its template. This is cheap and catches model-name, auth and quota failures that a green start cannot. Ticfac's production dispatch gate is deliberately narrower: it submits the real worker prompt, waits for `working`, and records whether delivery was confirmed; it does not assert a cause or fail a spawn from pane content (`internal/exec/herdr/gate.go`). Do not mistake that delivery acknowledgement for the live content round-trip this verification rule requires.

**A second, unrelated failure hides behind the same green response: the first prompt can simply not land.** Observed on claude and codex in one wave (opencode's first prompt landed first try in every observed spawn, gate included, but do not read one clean kind as immunity) — `herdr agent prompt … --wait --timeout 120000` returned a normal `agent_prompted` with a settled status while the pane still showed an untouched composer (claude's empty `❯`, codex's `› Improve documentation in @filename` placeholder) and no echo of the prompt text. The CLI was still painting its startup UI when herdr, having already seen `interactive_ready: true`, submitted. Re-sending the identical prompt worked immediately for both. Tell the two apart by reading the pane: **no echo of your prompt → re-send it; your prompt echoed with an error or no answer → the green-start trap, kill and fix the routing.** `herdr-runner.md`'s first-round-trip gate carries the same split.

## Adding a kind

This is for a genuinely new worker CLI, not a route for restoring `pi`: pi-durable already owns that worker role outside Herdr, and the pi CLI kind is a deliberate refusal. To extend the matrix to another `--kind` value, run exactly the loop that produced the three entries above — and do not write a row you have not run:

1. **Read the real flags.** `<cli> --help` for the model flag and the approval/permission flag; do not transcribe from memory or from another kind's shape. Note whether resume is a flag or a subcommand.
2. **Round-trip it live.** Split a scratch pane, `herdr agent start` with your candidate template, confirm the echoed `argv`, `herdr agent prompt … --wait`, and **read the pane** for the expected answer (per [the green-start trap](#the-green-start-trap)). Record `herdr agent get` and the `agent_session` from `herdr api snapshot`.
3. **Verify resume.** Exit with `C-c`, restart in the same pane with the resume form, and confirm the prior turn is present in the restored transcript.
4. **Record the integration state.** `herdr integration status` names the kind's hook path and version; install or upgrade with `herdr integration install <kind>` if the wave loop will depend on lifecycle states.
5. **Clean up.** Exit the agent and `herdr pane close <pane-id>` for every pane you created.

Operational notes that bit during verification and will bite again: `herdr agent send-keys` uses **`C-c`**, not `ctrl-c` (the latter errors `unsupported key`); an agent name is released as soon as the process exits, so a restart in the same pane needs a fresh `<name>` — and a second `C-c` to an already-exited agent errors `agent_not_found`, which is how you know one was enough; and `herdr agent prompt --wait` without `--until` matches `idle | done | blocked` and does not track turns, so an already-working agent's current turn can satisfy your wait.

Two more from the opencode round trip (2026-08-13). **Verify the CLI is on the *interactive* shell's PATH, not yours.** `herdr agent start` runs the executable in a pane at an interactive prompt, so what matters is that shell's rc file — opencode installs to `~/.opencode/bin` and is absent from a non-interactive `PATH`, yet spawns cleanly because `~/.zshrc` exports it. Check with `zsh -i -c 'which <cli>'` before concluding a kind needs a full path in its template. And **do the round trip in a workspace you created** (`herdr workspace create --cwd <dir> --label <yours> --no-focus` → use its `root_pane.pane_id` → `herdr workspace close <id>` when done), never `herdr pane split --current`: `--current` resolves to whatever the user is looking at.
