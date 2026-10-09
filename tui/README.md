# The terminal property suite (tick z7w)

Bombadil (`@antithesishq/bombadil`, the terminal driver) drives the REAL
ticfac binary over a real pty and holds the screens to the properties the
tick names — the outside half of the CLI's testing, where `make gate`'s
in-process tests cannot see: the pane, the resizes, the scrollback, the
frame timing.

```
pnpm test            the honest half: the real binary passes every property
pnpm run test:seeded the seeded half: every property fails against a
                     deliberately broken program — a property that cannot
                     fail pins nothing
pnpm run test:all    both, in order
```

`make bombadil` and `make bombadil-seeded` are the same two runs from the
repository root. CI runs `pnpm test:all` in its own job (`bombadil`).

## What the properties say

`specs/overview.spec.ts` — the bare `ticfac` overview, over a fixture world
of registries and feeds:

- **a stale or orphaned run never shows as live**: none of the world's
  ten-days-silent, gone-checkout (#86) and unclaimed runs renders a row whose
  state word is `running`;
- **every held or failed run shows the one command that clears it**;
- **history collapses into one line** (#87): the finished runs are absent as
  rows and the one summary line says so;
- **the listing answers and ends**: the program exits with the table's own
  code (0 — attention is data, not a failure).

`specs/watch.spec.ts` — the watch dashboard (`ticfac watch epic-hld`), over
generated resizes. The properties are pointed at the dashboard the 2026-10
redesign drew (docs/design/watch-redesign-2026-10.md): the frame's marker is
the key-hints footer the design's layout ends with
(`[enter] details  [e] all events  [q] quit`), drawn on every frame — the
redesign removed the tick table whose `TICK` header the suite used before
(tick vii) — so a state that shows the marker is a state a person is reading
the dashboard in:

- **the dashboard groups the ticks by state, and each group's rows keep the
  plan's order**: the group headers (NOW / DONE / UP NEXT / HELD) are part of
  the property — a dashboard that dropped them for one flat table fails here
  — and within a group the ids appear in plan order (u4l's P1, re-pointed:
  grouping by state is the redesign's own rule, so plan order across groups
  is no longer true). The property makes two claims, and each has its own
  seeded program: `rows-reordered.sh` breaks the rows within a group,
  `groups-reordered.sh` the order of the groups themselves, so neither
  claim rests on the other's proof;
- **needs-you carries the hold**: never `Needs you: nothing` over a run that
  ended holding — the redesign spells the answer with a leading capital — and
  every word of the clearing command on screen (u4l's P2; a wrap is by
  design);
- **never a fabricated `$0.00`** for the world's unmeasured spend (u4l's P3);
- **the frame fits the pane** at every generated size (u4l's P4);
- **the first frame appears within its bound** (#112): five seconds from
  spawn to the dashboard's own marker.

## How it runs

`run-suite.mjs` builds the ticfac binary (`go build`; `TICFAC_BIN` overrides),
builds the fixture world (`build-world.mjs`: a checkout that knows five runs,
a machine registry that knows two more — one whose checkout was deleted after
it claimed — and a stub `tk` that answers the tracker reads with a fixed
graph), writes SUT wrappers that bake the world's environment (HOME with no
factory in it, `TICFAC_REGISTRY_DIR` at the fixture registry, PATH with the
stub tk first), and drives each specification through Bombadil.

The suite is hermetic: the binary reads only the fixture world. No real
factory, no real tracker, no operator state. The wrapper quiets the
terminal's echo (`stty -echo`) — the driver types and clicks at a program
that never reads stdin, and echo is the terminal's artifact, not the
program's answer.

The seeded programs under `seeded/` are deliberately broken screens — one
per property, and one per claim where a property makes two: the
grouped-order property's rows and its groups each have their own. The
seeded half runs the same specifications against each and REQUIRES the
violation, which is the non-vacuity proof for the screen-reading oracle:
it is the same shape the gate's
`TestAgentJSONPropertiesCatchSeededBugs` holds for the in-process half.

## What the suite found, on the tree as it stood

Two real defects, both fixed with the suite:

1. **The live watch ignored resizes**: `watchLive` read the pane's size once,
   at start, and drew every later frame for a pane that no longer existed —
   after a resize the header scrolled away and the needs-you line with it
   (hn6's deliverable called the renderer "resize-aware"; it was not). The
   loop now re-reads the size every frame, starts the frame over from home
   on a change, and answers `SIGWINCH` at once.
2. The watch's stream path replayed a hold a RESUME had answered as the
   current alarm (see the CLI half: `internal/cli/agentjson_pbt_test.go`'s
   P5, which caught it first, and its fix in `watch.go`).

Known non-defects, for whoever reads a violation here:

- On a pty that does not ANSWER the terminal colourscheme query (`script`,
  plain ptys, CI), the watch's first frame waits out the query's ~5s
  timeout before drawing; Bombadil's emulator answers, so the suite's
  first-frame bound measures the real render. The bound stays 5s.
- The Cloudflare-Workflow orphan rule (#87's phantom "running" cloud runs)
  needs the operator's Cloudflare credentials and cannot run hermetically;
  the suite holds the LOCAL half (dead pidfiles, gone checkouts, silent
  feeds) and the repo's own unit tests pin the cloud seam.

## Layout

```
build-world.mjs   the fixture world builder (registries, feeds, stub tk)
run-suite.mjs     the runner: build, world, wrappers, drive, verdicts
specs/*.spec.ts   the specifications (Bombadil reads them)
seeded/*.sh       the deliberately broken programs, one per property
                  (and one per claim where a property makes two)
trace-frame.mjs   print the last screen state a trace recorded (debugging)
```

`specs/world.json` is generated by every run (gitignored): the world's own
facts, which the specifications' oracles read.
