<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ozw/attempt-7/7px`, base `8b79c6e76e6672fadf40cb7654b35ba7fe6f95c3`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# tick 7px — the committed watch screenshots predate the epic

## What changed

One commit, `e673bb7` on `tick/ozw/attempt-7/7px` (base `8b79c6e`, the integrated epic tree):
the five PNGs in `docs/design/watch-redesign-2026-10/` re-captured in place. All five changed
(busy 79%, held 76%, landed 71% rewritten; fresh and failed a few hundred bytes of anti-aliasing
drift) — confirming the committed set was rendered from the pre-epic frames, exactly as the tick
describes. The new set renders from the current pinned goldens:

- held's health line now reads `● paused · needs you · 1 of 5 done` (the line [A1] removed is gone
  from the committed evidence, no longer in green under the needs-you box);
- busy's status column carries the short spellings (`writing code`, `running pytest`,
  `gate running`), not the long note spelling;
- all five show the [A4] blank-line group spacing and the [A3] colour accents the guard asserts.

No source, test or tracker file changed. Working tree clean after the commit.

## What I ran

- `sh docs/design/watch-redesign-2026-10/capture.sh` with output at the repository directory, so
  the script's own assertions ran: `TestDashboardScreenshotSources` (each styled source asserted
  equal to the pinned plain golden before any PNG is written) and, in place,
  `TestCommittedScreenshotsShowTheColourAccents` — `ok github.com/pengelbrecht/ticfac/internal/cli`.
  `freeze` was not on PATH on this host; per the repo's no-global-install rule I installed it into a
  scratch prefix (`GOBIN=/tmp/scratch/bin go install github.com/charmbracelet/freeze@latest`) and put
  that on PATH for the capture only. Nothing machine-wide was touched.
- `go test ./internal/cli -run TestCommittedScreenshotsShowTheColourAccents -count=1` → ok, and the
  source test re-run with `-screenshot-dir` → ok; stripping the SGR from the held source matches the
  pinned golden line for line, the only delta being the 14 blank padding rows the source adds to
  carry the terminal's full 40 rows (the behaviour the test documents).
- `make gate` (whole-repo, standing order) with `GOTEST_PARALLEL=4 GOFLAGS=-p=2` for the shared
  host → exit 0, 50 packages ok, no FAIL lines.

## What the next tick has to know

- The committed evidence the c1d review (decision 7) called blocking is now current as of
  `e673bb7`; re-verifying is reading the held PNG or re-running the guard test — no new capture
  needed unless the frames change again.
- The PNGs go stale the moment any merge changes the pinned goldens or the rendering. The fix is
  mechanical: `capture.sh` on the integrated tree, which needs `freeze` on PATH — it is NOT
  preinstalled on this host (see the finding below).
- This was a docs-only change; the per-tick gate's touched-Go scope is empty by construction, so
  `make gate` is the whole verification surface for this tick beyond the capture's own tests.

```findings v2
[
  {
    "kind": "proposal",
    "title": "Pin the freeze version capture.sh installs, or the PNGs drift with tool updates",
    "severity": "low",
    "body": "capture.sh's not-on-PATH message recommends `go install github.com/charmbracelet/freeze@latest`, unpinned. The .ansi capture sources are asserted against the pinned goldens, but the PNG rendering itself (glyph shaping, spacing) depends on the freeze version, so a re-capture on a newer freeze produces PNG diffs a reviewer cannot attribute to the dashboard rather than to the tool. Pin the version (or add freeze as a tool dependency) so a capture's diff only ever reflects frame changes.",
    "evidence": "docs/design/watch-redesign-2026-10/capture.sh:21 (the @latest recommendation)"
  }
]
```

STATUS: DONE
