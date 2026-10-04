<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-3/tt6`, base `f3b1cf67479dc2b4fd8f61101474311130198885`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# tt6 — A failed cloud run's page tells the person to resume it locally

## What changed

The TS stop builder (`stopsFromStatusDoc`, `cloudflare/src/notify.ts:87`) and the
shared classifier (`classifyStatusDoc`, `cloudflare/src/status.ts:536`) both
hardcoded the failed run's clearing command as `ticfac run-epic <epic>` — the
LOCAL foreground resume — ignoring the doc's `host`. A failed **cloud** run
therefore paged the person with `run-epic`, which restarts the epic locally on
whatever machine reads the page, exactly the move the Go model's
`statusmodel.ResumeCommand` (tick gtk) exists to prevent.

Fix:

- Added `resumeCommand(host, epicID)` to `cloudflare/src/status.ts` — the
  reader's mirror of `statusmodel.ResumeCommand`: host `cloud` →
  `ticfac run <epic> --cloud`, anything else → `ticfac run-epic <epic>`.
- `stopsFromStatusDoc`'s failed stop and `classifyStatusDoc`'s failed case now
  spell the resume through it. Both instances of the seam are fixed in this one
  change — the phone page's row for a failed cloud run (`classifyStatusDoc` →
  `cloudRow`) had the identical defect and asserted it in
  `phone-page.test.ts:302`.

Tests (written first; all reproduced the defect before the fix):

- `status-alerts.test.ts`: a failed **local** doc still pages
  `ticfac run-epic 2jn`; a failed **cloud** doc pages `ticfac run 2jn --cloud`
  (pure vocabulary); `notifyRunEnded`'s cloud ending message carries
  `clear with: <code>ticfac run ko8 --cloud</code>`; and a new pure test asserts
  `stopsFromStatusDoc` and `classifyStatusDoc` answer the same failed doc with
  the same command for both hosts — one model, two renderers, no disagreement
  (epic hn6 rule 8).
- `phone-page.test.ts`: the failed cloud row now reads
  `clear with: ticfac run ko8 --cloud` (was the wrong `run-epic`).

No Go changes; the Go model already spells the resume by host.

## What I ran

- `pnpm exec vitest run test/status-alerts.test.ts test/phone-page.test.ts` —
  new tests failed before the fix (2 + 1 failures, exactly the hardcoded
  strings), 33/33 pass after.
- `pnpm exec vitest run` (full TS suite) — 71 files, 1684/1684 pass. The
  trailing uncaught workerd exception after the green summary is the known
  stderr noise already tracked as issue `heu`; pre-existing, untouched.
- `make gate` (gofmt, vet, Go short suite) — green.
- `make ts-gate` (biome lint, contracts check, tsc) — green after
  `biome check --write` on the four touched files.

Committed on `tick/hn6/attempt-3/tt6`: `6ba54fd` (on base `f3b1cf67`).

## What the next tick has to know

- **Dedup keys did not change**: the failed stop's key stays
  `terminal:failed:<reason>` — it never included the command. An already-notified
  stop will not re-page with the corrected command; it self-corrects on the next
  fresh episode. No migration needed.
- `resumeCommand` in `cloudflare/src/status.ts` is now the one TS spelling of the
  resume, mirroring Go's `statusmodel.ResumeCommand`. Any new TS surface naming a
  failed run's clearing command should call it, not format the command itself
  (same rule the Go side guards with `TestEveryClearingCommandInTheModelIsSpelledOnce`).
- Local-doc behavior is unchanged everywhere: local docs still get the
  foreground `run-epic` form; the push door only ever stores host `local`, so
  cloud docs reach the alerts only through `notifyRunEnded` (run-workflow
  finalize) and through the phone page's `cloudStatusDoc`.

```findings v2
[]
```

Nothing outside this tick: the identical defect in `classifyStatusDoc` (the
phone page's failed cloud row) was the same seam — the TS spelling of the resume
ignoring `doc.host` — so it is repaired in this change rather than filed, per the
repo's rule that a repair names every implementation of the seam; the workerd
stderr noise on a green vitest run is already tracked as issue `heu`.

STATUS: DONE
