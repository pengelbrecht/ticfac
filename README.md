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

Status: Phase 1 (reconciler and local subprocess executor) — in progress.

## Operator surfaces (tick glb)

The operator surfaces ticks' pwp deleted from `tk` with no replacement are
either owned here or recorded as deliberately dropped — nothing is left
implicitly missing:

| Surface | Decision | Where / consequence |
|---|---|---|
| factory webhook | **owned** | `ticfac factory webhook` (register by default, `--status`, `--delete`) |
| herdr pane badges | **owned** | `ticfac herd paint` — display-only, TTL-expiring badges over the herdr executor's own attempt records |
| blocked/settled chimes | **owned** | `ticfac herd notify` — once-per-episode, with rate-limit retraction |
| orchestrator guard | **dropped** | tk's guard nudged an agent-run orchestrator pane; ticfac's orchestrator is a program whose liveness is `ticfac status` and whose attention surface is `ticfac watch`. Consequence: a hand-run agent orchestrator in a herdr pane has no watchdog. |
| herdr-ticks plugin install/check | **dropped** | the plugin is ticks' to settle (ticks tick si0); its hooks may be pointed at `ticfac herd paint`/`notify`. Consequence: `herdr plugin install` remains the install path, and a stale plugin's hooks fail until si0 retires or repoints them. |

The same table lives in `ticfac herd --help`, where the consumer (the
plugin, or a person) looks.

## Install

One command, no build:

    curl -fsSL https://raw.githubusercontent.com/pengelbrecht/ticfac/main/install.sh | sh

That URL is the stable install URL: it serves `install.sh` from this
repository's `main`, and the script resolves the latest release, downloads
the archive for your platform (darwin and linux, amd64 and arm64), and
installs `ticfac` and `ticfac-exec-subprocess` side by side into
`~/.local/bin` (override with `INSTALL_DIR`) — side by side because a run
refuses to start unless the local executor sits beside the ticfac that
dispatches it. Then:

    ticfac doctor                    # what a run still needs on this machine
    ticfac skills install ticfac     # the execution skill, the way tk's installs

Releases are cut by pushing a `v*` tag: `.github/workflows/release.yml`
runs goreleaser with `.goreleaser.yaml`, which builds both binaries for
every platform, packs each platform's pair in one archive, and publishes
the archives, `checksums.txt` and release notes generated from the commit
log. The whole distribution is guarded by `internal/release`'s tests — the
platform matrix, the archive naming and the repository the release publishes
to are pinned there, and the installer runs end to end against a fake forge
in the per-tick gate.

Cutting one is one command:

    make release VERSION=vX.Y.Z

run from a merged `main` — the target cuts an annotated tag and pushes it,
and the workflow does the rest. The FIRST release is part of the first merge
to `main`, not an afterthought: until a `v*` tag exists, `releases/latest`
has nothing to resolve to and the stable install URL above answers nothing,
so the one-command install is unreachable.

## Command surface

Every command runs on one cobra tree, styled by [fang](https://github.com/charmbracelet/fang)
(tick nwj) — and every operator-facing text is DERIVED from that tree rather
than maintained beside it:

- `ticfac --help`, and `ticfac help <command>`, render the styled help from
  the tree; a command's flags live on the same declarations its body parses,
  so `ticfac <command> --help` lists exactly what the command accepts.
- An unknown command or flag is a styled refusal with exit 2, not a usage
  dump repeated on every error. The styling is stripped for anything that is
  not a terminal — a pipe, a test buffer, CI — so scripts and tests see
  exactly the words.
- `--version` reports this build (the same value `ticfac version` carries
  beside the contract bundle).
- `ticfac completion bash|zsh|fish` writes the shell completion, generated
  from the tree.
- The hidden `ticfac man` renders the whole tree as man pages (mango),
  writing roff to stdout — the same surface a terminal reads, in the format
  `man` presents.
- Every command takes `--json` (tick 8v3): ONE versioned document on stdout —
  the schema named inside it (`ticfac.<command>.v1`, beside the older
  `ticfac.status.v1` and `ticfac.job-status.v1`), the command's prose on
  stderr, and — where the answer is an outcome of the work — a `state` word
  the exit code agrees with. A live stream (`--follow`, the dashboard) is
  not one document and says so.
- A bare `ticfac` is the overview (2qz): every run this checkout and the
  factory know, attention first — every run held or failed names its reason
  and the one command that clears it. Runs that need nobody — their epic is
  closed, a later run of the epic superseded them, or they finished more than
  a week ago — are one summary line; `--all` lists them, and `--json` emits
  the versioned overview model with every run, one status model each, the
  history ones flagged with the reason. `ticfac --help` remains the place a
  person reads the whole tree.

| command | what it does |
|---|---|
| `ticfac` (no arguments) | the overview: every local and cloud run, attention first, every held or failed run with its reason and the one command that clears it, history (closed epics, superseded runs, runs finished over a week ago) collapsed into one line (`--all` lists it, `--json`); another project's cloud runs are listed from their own record and feed alone — this repo's records, tracker and PR are never read for them, and their rows name no command |
| `ticfac run <epic-id>` | the one command for a local run: start the epic in the background — into herdr panes with the embedded herdr profile set when a live herdr is detected — and attach the live view; run it again to attach to a live run or resume a stopped one, Ctrl-C detaches without stopping the run, and the epic id is accepted everywhere (also spelled `epic-<id>`); with `--cloud` the same verbs, view and triage drive your cloud factory: submit the epic, attach the same live view, run it again to attach to this project's run or resume a finished or frozen one; with `--cloud-workers` the orchestrator runs on this machine and every worker in the factory (the factory records the run and hands this machine its credential — nothing to export; the reconciler, merges and the integrated gate run here) |
| `ticfac run-epic <epic-id>` | run one epic through the reconciler — the foreground form scripts drive; `ticfac run` is this command, started detached with the defaults decided |
| `ticfac init` | make this repository ready to run an epic: routing, the guessed gate, the guessed close-out rule — `pr` when origin names a GitHub repository, `none` otherwise; `--closeout` overrides (refuses to overwrite) |
| `ticfac doctor` | say what a run still needs on this machine, each missing thing with its fix |
| `ticfac settle <epic-id> <tick-id> <attempt>` | release an attempt nobody can address |
| `ticfac findings <epic-id>` | list the worker findings drafted for triage |
| `ticfac finding <epic-id> <key>` | triage one drafted finding |
| `ticfac triage <epic-id> [<key-prefix>=<decision>...]` | settle every untriaged finding — absorb / file / fixed / discard — interactively or by short key prefix (`--json` for agents) |
| `ticfac status <run-id>` | is the run alive, and when did it last say anything; an epic id with no run here answers the run the factory holds for this checkout's project |
| `ticfac events <run-id>` | a run's event feed: what it did, as it does it (`--follow` subscribes) |
| `ticfac watch <run-id>` | the whole epic as a live dashboard, redrawn in place — attention first, the lifecycle as a progress bar with elapsed, ETA and the health verdict, one fixed row per tick with its pipeline, the live workers with their activity, CI and honest cost, and the event feed shrunk to a two-line tail. When stdin is a terminal too, the dashboard answers the keys: `j`/`k` (or the arrows) move a cursor over the tick rows, `enter` opens that tick's story — every try with its tier, outcome and the run's own reason and next step, the report summary and its diff stats, the gate evidence, the findings — `e` opens the whole event feed (`j`/`k` scroll it), and `esc` or `q` comes back down to the dashboard; on the dashboard, `q` or Ctrl-C ends the watch the way SIGINT does. On a pipe, plain lines one per event, byte for byte as before — and on both paths, it says, to a human, when a run ends holding something for one, and it exits the ended run's own class: 0 done, 1 failed, 3 holding for a person, 7 cancelled |
| `ticfac version` | report this build and the contract bundle it serves |
| `ticfac skills list\|get\|install` | the agent skills embedded in this binary — `ticfac skills install ticfac` is the one command, installing the execution skill into the same `.claude/skills/` / `.agents/skills/` directories `tk skills install ticks` does |
| `ticfac factory deploy\|setup\|status\|dashboard\|webhook\|wait-deployed` | put and run the ticks cloud factory in your own Cloudflare account; `factory wait-deployed <sha>` blocks until the factory runs a commit containing `<sha>` (0 live, 1 the deploy or CI that would carry it failed, 4 no such commit, 5 timed out) — a deploy skipped or cancelled because a newer main commit superseded it is normal and is waited through |
| `ticfac herd paint\|notify` | the herdr operator surfaces ticfac owns: badge panes, chime on blocks |
| `ticfac cloud run\|stop\|status\|logs\|trace\|supervisor\|branch` | the expert half of running epics in your cloud factory (`ticfac run <epic> --cloud` is the everyday surface); `cloud branch` is the container-side write a sandbox's entrypoint uses to record the branch it created — it authenticates with the run's own token, never the operator's config |
| `ticfac sandbox image\|toolchain\|model\|substrate\|setup\|environment\|worker-prompt` | the sandbox image's boot questions, ported from tk (46x): the image a checkout's `[sandbox]` table declares, its extra toolchain pins, the model its routing resolves for a boot, the substrate a run dispatches through, the idempotent warm step, the `[environment.commands]` pre-flight and the per-tick worker prompt — the run scripts call these after their clone so no shell ever parses runners.toml |

Exit codes are the contract a script branches on — every command's process
code is one of this table, the same words in `ticfac --help` and in every
`--json` document's `state` field:

| code | name | meaning |
|---|---|---|
| `0` | done | the command did its work |
| `1` | failed | a failure that is not a usage mistake — a refused action, an unreadable store, a run that stopped over a repair another run can make (the refusal names the reason class), a run whose own terminal line says it failed (watch, run: the line names what did not pass) |
| `2` | usage | a malformed invocation: wrong flags, wrong argument count, a refusal to guess |
| `3` | held | the run ended holding something only a person can move (run-epic, run, watch): the reason class is the refusal's reason or the wait kind in the line and the --json document — e.g. finding_untriaged, merge; in the cloud, factory and skills family this code keeps tk's meaning, not inside a git repository |
| `4` | missing | a lookup that honestly came back empty: a missing epic, a missing tick |
| `5` | running | the command ended while the run is still in flight: `ticfac run` detached with the run going, a watch interrupted on a live run — the work continues, nothing is wrong |
| `6` | io | an unreadable local file the command needs |
| `7` | cancelled | a run that was stopped deliberately before it finished (run-epic, run, watch): its own terminal line names the stop and why — the work is neither done nor failed, and nothing is held for a person |

Two documented exceptions: `ticfac status` exits the run's liveness answer
(0 alive, 1 not) rather than the command's own success — the question it
exists to answer — and a run-epic killed by a signal exits the shell's
convention (130/143), not the table. The signal's RUN still classifies:
a person's Ctrl-C (SIGINT) writes its run_died line led by the cancelled
word, so a watch of that run answers the cancelled class (7), never the
failed class — a SIGTERM eviction stays a death (1).

The section is pinned by a test (`internal/cli/readme_test.go`): a command
added to the tree fails the suite until this table names it, so the README
cannot lag the tree the way it once did.

## Contracts

`contracts/` is the **cross-language contract bundle ticfac authors** (tick
4i8): `contracts/bundle.json` carries ticfac's own version and changelog, and
every contract describing a ticfac format is edited and re-cut here. Two files
in it — `tk-json-manifest.json` and `tracker-layout.json` — describe ticks'
own formats and are a **vendored, pinned copy** of ticks bundle 7.0.0, fetched
from `pengelbrecht/ticks` at the commit recorded in `contracts.pin.json`.
Everything is verified offline by digest on every test run, the ticks-owned
files also against GitHub at the pinned ref in CI, and every file in the bundle
has a Go reader in `internal/contracts/parity` and a TypeScript reader in
`cloudflare/test`. `CONTRACTS.md` says how that works, how to cut a bundle
version, and how to adopt a new ticks release. Never edit a ticks-owned fixture
here — a ticfac-owned one is edited here and re-cut in the same commit.

## image

`image/` is the sandbox image's build context: the container a cloud run
boots in either role. **ticfac authors it** (tick r6w). It was ticks'
`cloud/sandbox`, vendored here and pinned by digest until ticks became
tracker-only (ticks epic chz); the tree and the suite that runs its scripts
now live only here, so the image that ships and the image that is tested are
one tree. Edit `image/` directly. `internal/sandboximage` runs the scripts
against stub harnesses and real git (end-to-end: `make test` and CI run it, the
`-short` gate runs its text checks), and checks that the tree on disk is the
tree `embedded.go` ships. `ticfac factory deploy` builds the image from the
copy embedded in the binary; nothing fetches it from ticks.

## Development

```
go build ./...                    # the ticfac binary
make test-short                   # the short suite: seconds, and what a tick's gate runs
make test                         # the full suite, end-to-end included (-timeout 45m)
go run ./cmd/contracts check      # verify the bundle, offline
```

`-short` means something here (tick miu). `internal/reconcile`,
`internal/exec/herdr` and `internal/runstate` build real git repositories and
spawn real worker processes per test; under `-short` their harness constructors
skip, so the short suite is the readers, the parsers, the drift guards and the
negative controls, and it finishes in seconds. Everything skipped there runs in
`make test`, which CI runs on every push and pull request beside
`make test-short`, and which epic close-out runs before a merge. The discipline
is enforced by `internal/shorttest`'s guard rather than remembered.

The full suite also carries one test that needs the OTHER toolchain:
`internal/exec/cloudflaresandbox`'s end-to-end adoption test (tick 6gr) drives
the REAL factory Worker — `cloudflare/src`'s deployed fetch handler, the
gateway's authorization, the dispatch lease, the adoption machinery — in real
workerd through miniflare, on a real local port, with the real Go executor on
the far side. The harness is `cloudflare/test/go-door-harness/`; it needs
`cloudflare/node_modules` (`pnpm install` there) and skips — naming that
remedy — where they are absent, so a checkout with only Go installed still
gets the short suite in seconds and a clear sentence saying what the long one
adds. CI installs the workspace in the go job for exactly this reason.

**Which side a new test belongs on.** "End-to-end" is not the same claim as
"not worth a tick's time", so the second is decided by name rather than
inferred from the first. A test in one of those three packages must do one of
four things, and the guard fails it otherwise:

| | what it means |
|---|---|
| build the harness | the default — skipped in the gate, run by `make test` and close-out |
| `shorttest.EndToEnd(t)` | same, said explicitly; needed when the harness is only built inside a subtest closure |
| `short:` doc-comment line | cheap: no repository, no subprocess, so the gate pays for it |
| `shorttest.LoadBearing(t)` + a `gate:` line | end-to-end, and it **stays in the gate** |

The last is the exception, and the bar is narrow: the test is the only proof
that a path the run depends on works, and a regression in it would be
expensive and silent. Today those are `internal/reconcile/supervise_test.go`'s
three supervision tests (16.2s) — the continuation loop is what now keeps an
autonomous run alive across resumable stops, and nothing else proves it — and
`internal/release`'s install test — the tick-4yb acceptance is a machine that
never built ticfac installing it with one command and running `ticfac doctor`,
and a broken installer ships silently broken releases.
"This test is important" is not the bar; nearly every test is important, which
is how a fast gate becomes a slow one. So the exception is bounded rather than
argued: each `gate:` line carries a measured cost, the costs are summed against
`shorttest.Budget` (30s), and the guard prints the spend on every run. Admitting
a new one means measuring it against what is left, or raising a number in a diff
a reviewer can see.

`make test` still pins `-timeout 45m`: the full `internal/reconcile` suite runs
600-620s, past `go test`'s default 10-minute per-package timeout. Use these
targets (or pass `-timeout` yourself) rather than a bare `go test ./...`.

## Deploying the factory

`ticfac factory deploy` installs or upgrades the factory in the operator's
own Cloudflare account, from the bundle embedded in the exact binary running
the deploy — the version pin rides the repository (D16). `ticfac factory
setup` is the first-run walk: it climbs the credential ladder one rung at a
time (wrangler, a deployment, a GitHub credential, model access) and verifies
every rung against the live service before storing it; `ticfac factory status`
re-checks all of them live.

**CI is the normal way the factory is deployed.**
`.github/workflows/deploy-factory.yml` runs the same installer for every
commit on main that CI passed and that changes something the factory ships
(the Worker bundle, the image, or the Go cross-compiled into it — diffed
against the commit the factory actually runs), for every `v*` tag, and on
`workflow_dispatch` (which is also how a failed CI deploy is retried). Deploys
serialize; before deploying it asks the factory whether a run is live and
waits for it, up to 60 minutes, then deploys anyway. The job summary records
the commit, the Worker version and the image digest; `ticfac factory status`
(and `ticfac doctor`) ask the factory for the same facts (`GET
/api/deployment`) and say when this machine's `~/.ticfacrc` remembers an
older deploy. A local `ticfac factory
deploy` is the fallback: the first install, a token rotation, or CI being
unable to deploy.

Configure `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID` and
`TICFAC_FACTORY_TOKEN` (the `factory_token` from `~/.ticfacrc`) as repository
secrets to enable it. A repository without them skips the deploy with a
warning naming what is missing — the factory is the operator's opt-in, not a
service this repository runs.


## Following the factory from a phone

The deployed factory serves `/status` — a mobile-first, read-only page an
operator signs into with the factory token (typed into a form, kept in an
HttpOnly cookie, never in a URL) and installs to a phone home screen as a
PWA. Every run the factory can see is listed attention first, the same view a
bare `ticfac` prints: cloud runs live from the factory's own records, local
runs from the status snapshots they push when opted in —
`ticfac run-epic --status-push` per run, or once for the machine by setting
`TICFAC_STATUS_PUSH=1` (the flag always wins; needs a configured factory;
always best-effort). A local run pauses when its laptop sleeps, so the page
marks an old local row PAUSED/STALE and says why rather than looking stuck.
The same pushes drive the factory's Telegram alerts — a run needing a person (with the
reason and the one command that clears it), an epic done, a run failed — one
message per stop, deduplicated and rate-limited.
