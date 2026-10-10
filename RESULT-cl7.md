<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ozw/attempt-2/cl7`, base `45ee0e0480c278e342f4ad66d6616c0d31077dd0`, harness `pi-durable` exited 0, 3 work commit(s), 0 uncommitted path(s)._

# RESULT-cl7 — Colour accents in the new dashboard, and screenshots that show them

## Verdict

DONE. The diagnosis: **the view never dropped its colour accents — the capture ran without colour.** The five committed screenshots are re-captured in colour from the styled output, with a committed capture path that cannot silently lose the accents again, and a gate guard that reads the committed PNGs back.

## Diagnosis (the "find whether" half)

- The renderer has the accents and tests them: `TestWatchColourGridPerState` (tick 5ba, `internal/cli/watch_colour_test.go`) passes on the tree — green merged/healthy, amber writing-code/testing, red failed and needs-you, cyan ids and hints, dim timestamps and duplicates, bold headers.
- The committed PNGs were monochrome by construction: their content matches the *plain* (identity-styled) golden frames (`internal/cli/testdata/watch_scenario_*_120.txt`) pixel-for-pixel in layout, and the content region carries **zero** pixels of any hue (I decoded all five: every pixel outside the capture's own window controls is grey or the background tint). The only chroma in the old files was the capture tool's window controls (washed red/yellow/green circles at the top) — one old PNG's lone "green" pixel cluster was a traffic light, not the dashboard.
- Corroborating detail: the old PNGs' grey levels match the *luminances* of the palette's own colors (dim text ≈ (75,73,85) ≈ bright-black #4B4B4B; white text ≈ (196,196,196) ≈ #C5C8C6), i.e. a render that had colour and lost/greyscaled it. The capture recipe was never committed (the ymf run's records name only "vhs or freeze"), so it could not be re-run — that gap is what let the monochrome capture through unreviewed.

## What changed (3 commits on tick/ozw/attempt-2/cl7)

1. `0f14d34` — the re-capture and its machinery:
   - `docs/design/watch-redesign-2026-10/*.png` (5 files): re-captured in colour, 1230×851, from the five scenarios' frames rendered with `ansiWatchStyles` at 120×40. Green done, amber in progress, red failed/held (whole needs-you box, announcement included), dim mechanics, cyan ids/hints are all visible.
   - `docs/design/watch-redesign-2026-10/capture.sh` (new, executable): the committed capture path — runs the source writer (below), then `freeze --language ansi` per scenario with fixed flags, then re-runs the guard to verify. Requires `go` + `freeze` on PATH (`go install github.com/charmbracelet/freeze@latest`). No window chrome, deliberately: window controls would carry their own red/yellow/green and make the colour guard vacuous.
   - `internal/cli/watch_screenshots_test.go` (new):
     - `TestDashboardScreenshotSources` (flag-gated via `-screenshot-dir`, skipped by the gate) writes each scenario's styled frame as the capture's source, asserted to be the pinned plain golden with colour on — the capture cannot ship a layout the goldens do not pin.
     - `screenshotSource` re-spells the two SGR sequences the image pipeline misreads (same words, same widths, same meaning): faint `\x1b[2m` → `\x1b[90m` (the dim accent *is* a dark grey on a dark terminal; the pipeline draws no faintness), and a bare bold arriving after a colour (the needs-you announcement's red+bold, adjacent or with the box border between) → one combined `\x1b[1;31m` (per-span tokenizers drop the carried fill; cumulative terminals read both spellings identically). Unit-tested exact in `TestScreenshotSourceRespellsForTheCapture`.
     - `TestCommittedScreenshotsShowTheColourAccents` decodes the five committed PNGs and asserts, per scenario, the hue families the frame's own accents must show (fresh: green+cyan; busy: amber+green+cyan; held: red+amber+green+cyan; landed: green+cyan; failed: red+green+cyan) — read by hue family with saturation/brightness floors, not by one rendering's RGB. **This test failed on all five old PNGs before the re-capture and passes now** — the red→green the acceptance asked for.
     - `TestHueFamilyOfRefusesMonochrome` holds the guard's oracle to itself: the old captures' washed greys and the background refuse; the palette's accents accept.
   - `docs/design/watch-redesign-2026-10.md`: the Verification section now states where the screenshots come from and why they show colour.
2. `75121be` — capture.sh: self-verification only when capturing in place (the guard reads the committed PNGs); custom output dirs are created.
3. `08faa33` — `internal/cli/watch_colour_test.go`: the HELD group's own row's status word asserted **red** on the styled output — "red failed/held" was the one tick-named accent without an in-process assertion (failed was covered; the needs-you box was, but a held *row* was not).

Production rendering code is untouched: the view was never at fault, and real terminals already accumulate the nested red+bold the pipeline mishandles.

## What I ran (foreground, output read)

- `go test -run 'TestWatchColour|TestDashboardScenario' ./internal/cli/` — pass (baseline; the view's colour tests were already green, which is half the diagnosis).
- `go test -run 'TestCommittedScreenshotsShowTheColourAccents' ./internal/cli/` — **FAIL on all five old PNGs** (the reproducing assertion), then pass after the re-capture.
- `make gate` — green (whole-repo short suite; no FAIL lines).
- `TICFAC_GATE_TOUCHED_BASE=45ee0e0 TICFAC_GATE_TOUCHED_HEAD=08faa33 go run ./cmd/gate-touched -timeout 45m -parallel 4 -leave-to-ci .../internal/reconcile` — green: full non-short suites of `internal/cli` (88s) and `cmd/ticfac` (no test files). No origin/main is fetched in this worktree, so the pair is exported explicitly over the tick's own base and head.
- internal/cli's `-short` wall with `-count=1` (this tick adds tests there): **73.2s** at `-parallel 4`.
- `capture.sh` end-to-end twice: in place (writes + guard verifies), and to a custom dir (creates it, skips the in-place verification). Two consecutive captures are **byte-identical** to each other and to the committed PNGs — the artifact is reproducible, not a lucky frame.

## What the next tick has to know

- The screenshots' provenance is now in the repo: `capture.sh` + the `-screenshot-dir` source writer. To re-capture (e.g. after a layout tick changes the frames), install freeze in a scratch prefix, run `docs/design/watch-redesign-2026-10/capture.sh`, commit the five PNGs. The guard test will refuse a monochrome or stale capture.
- The guard asserts hue *families*, not exact RGB: it survives a pipeline or theme change that keeps the accents recognizable, and it deliberately tolerates freeze's specific palette. If a future change moves an accent's hue outside its family (e.g. amber into green), the guard is the thing that fires.
- freeze quirks the capture source re-spells (kept out of production code on purpose): SGR 2 (faint) is ignored, and a bare SGR 1 after a colour drops the fill. If a future renderer adds new nested bold-over-colour accents, `screenshotSource`'s regex already covers colour-then-bold generally; anything else nested needs its own re-spelling, and `TestDashboardScreenshotSources` will catch it via the stale-sequence check.
- The new PNGs are 1230×851 (~118–240KB each, ~870KB total) vs the old ~33KB paletted files; filenames and the five-scenario set are unchanged, so nothing referencing them moved.

## Findings

```findings v2
[
  {
    "kind": "defect",
    "title": "freeze's ANSI renderer ignores SGR 2 and drops fills when bold follows",
    "severity": "low",
    "body": "charmbracelet/freeze v0.2.2's CSI dispatcher has no case for SGR 2 (faint) — dim text renders at full brightness — and every SGR sequence starts a fresh span, so a bare bold arriving after a colour sequence renders the text unstyled (red+bold came out white). Terminals accumulate attributes, so both are render-only defects; the repo's capture source re-spells both (faint -> 90, colour-then-bold -> combined 1;3X) rather than depending on a fix. Filing upstream in case the ANSI path gets tightened.",
    "target": "charmbracelet/freeze",
    "evidence": "ansi.go CsiDispatch: case 1 adds an empty tspan (the font-weight attr is commented out); no case 2; palette map in the same file"
  }
]
```

STATUS: DONE
