# ticfac watch, redesigned (epic, 2026-10-08)

Operator feedback on the hn6 dashboard: "frankly i don't think the tui is very
intuitive/clear". The screen answers questions in ticfac's vocabulary (tiers,
gates, pushes, shas, unlabelled stage dots) instead of the operator's.

## The three questions, in this order

1. Does it need me? (first line, always; a box with the exact command when yes)
2. Is it healthy, and how far along? (one line: health, N of M done, ETA)
3. What is happening right now? (each running worker, in words, with a live
   one-line excerpt of what it is doing)

## Principles

- Group ticks by state (NOW / DONE / UP NEXT / HELD), never by plan order.
- Words over symbols: a symbol that needs a legend is replaced by a word.
- Exceptions only: retries, an escalated model, a stall, a failure appear
  inline when they happen; TIER and ATTEMPTS columns go.
- Mechanics (pushes, shas, gate internals, tier names) live behind a key.
- One epic-level track with a "you are here" marker replaces the dot strip.

## Target layout (120 columns; must also fit 80x24)

```
 order feed v1  (9g5)                        claude · cloud · running 55m
 ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
 Needs you: nothing                    ● healthy · 6 of 18 done · ~8h left

 Building ──── Reviewing ──── Closing out ──── PR & CI ──── Merged
    ▲ here (wave 1 of 6)

 NOW
   tgi  order-feed service skeleton       writing code     19m   "adding the /feed handler"
   5az  smoke validation vs kofoed dumps  writing code      8m   "running pytest"
   v1g  safe intraday kanpla pulls        testing           32m  gate running

 DONE (3)
   gzt  sum same-key kanpla lines         merged           22m
   rtk  order-feed contract v1            merged           34m
   ww2  kitchen_order_feed dbt view       merged           45m

 UP NEXT (12)   s3r kanpla-etl /sync/intraday · wmd paired report capture · …
                then: final review → close-out

 ─ latest ─────────────────────────────────────────────────────────────────
 13:08  v1g  merged into the epic, now testing
 13:02  ww2  finished and merged
                                   [enter] details  [e] all events  [q] quit
```

Status words for a tick: waiting (blocked by X) · up next · claimed · writing
code · testing · merged · reviewing · closing out · waiting for CI · held:
<reason> · failed: <reason> · done. A retry reads "writing code (attempt 2, on
opus)". Cost: shown only when metered; a claude-sub run shows the
subscription's window use ("MAX1 · 34% of 5h") instead of "$0.00".

## Verification

Golden frames rendered through a real terminal emulator at 80x24 and 120x40
for: a fresh run, a busy wave, a held tick (needs-you box), an ended landed
run, an ended failed run. Each frame is checked against the three questions.

The five `watch-*-120x40.png` screenshots in this directory are captured from
the STYLED output — `capture.sh` renders each scenario's frame with the
terminal's own style set and freeze turns it into a PNG — so they show the
dashboard's colour accents: green done, amber in progress, red failed/held,
dim pending/mechanics, cyan identities and hints (tick cl7; the first capture
fed the pipeline plain text and came out monochrome, and
`TestCommittedScreenshotsShowTheColourAccents` now reads the committed PNGs
back so that cannot happen silently again). The captures use no window
chrome: window controls would bring their own red, yellow and green, and the
colour on screen must be the frame's own.
