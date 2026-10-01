<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/hn6/attempt-7/lh4`, base `adb77b9d75020f74e179b6953f489dbd320d67c6`, harness `pi` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# lh4 — dashboard golden's try outcomes and one reason the derivation cannot produce

The drift the tick names was reproduced at the base first, then fixed: the
guard rule landed BEFORE the fixture moved, and it failed on exactly the four
values the tick lists, on the dashboard golden alone (the other two goldens'
tries already agree with the derivation, and still do).

## What changed

**`contracts/status-model.json` (dashboard golden), four values** — each
provable from the document alone, since the gates array is the complete
evidence inventory and the refusal lines sit in the recent tail, so they are
the newest:

- 46x try 1 (attempt 4, not current): outcome `rejected` → `gate-failed`.
  The array carries the fail record `gate-46x-4-go` naming its attempt, and
  `tryOutcome` reads the evidence before the census for a non-current try.
- 46x try 1: reason `gofmt drifted in two files` → `attempt 4 of 46x struck
  out: gofmt drifted in two files` — the newest refusal line's detail, whole,
  exactly what `decorateTries` copies (cut at 160 leaves a 55-char line
  whole).
- 823 try 1 (attempt 2, not current): outcome `gate-failed` → `dispatched`.
  No record for 823#2 in the array and nothing standing → the census branch
  never answers and the derivation says dispatched.
- v7z try 1 (attempt 5, current): outcome `reported` → `dispatched`. No
  record for v7z#5 and no standing worker; `reported` is not one of the two
  states the checkpoint branch maps, so the derivation too answers
  dispatched.

**`internal/statusmodel/contract_test.go` — the guard extends to the try
vocabulary** (`goldenTryOutcomesAgreeWithTheEvidence`, wired into
`TestEveryGoldenAgreesWithThePipelineDerivation` for every golden and tick):

- A try's OUTCOME is demanded of every try: the rule runs `tryOutcome`'s own
  production branch over the document's own inputs — the checkpoint's word is
  the tick's state, the evidence is what the gates array carries for the
  (tick, attempt), the census is the workers panel. The array is the complete
  inventory and the panel is the census, so nothing is under-demanded.
- A refused try's REASON is demanded where the document's tail view of the
  feed (liveness.last_event, else the newest match scanning the recent tail
  backwards) carries a rejected/gate_failed line for the pair — the tail
  holds the feed's newest lines, so a refusal line the tail shows IS the
  newest one for its pair, the one `reasonLine` answers — and stays silent
  where the tail shows none, because a refusal older than the tail could
  still exist. The cut is `decorateTries`' own `cutToWordBoundary` called
  directly, not a re-spelled copy.

**`cloudflare/test/phone-page.test.ts`** — the one consumer pinning the moved
values: 823's attempts cell renders `○✓` now (a dispatch the records state
and no evidence answers), not `✗✓`; the test's wording follows ("refused",
not "rejected"). 46x's `✗●` row is unchanged — gate-failed draws the same ✗.

**Bundle cut 1.2.4 (PATCH), one commit, per `contracts/README.md`**: fixture
+ guard + consumer together; `version` bumped; the status-model digest
refreshed; `version_digests["1.2.4"]` recorded; the CHANGELOG entry written;
`cloudflare/contracts.pin.json`'s `bundleVersion` moved in the same commit.
The root `contracts.pin.json` (ticks' bundle) is untouched.

## What I ran

- `go test ./internal/statusmodel/ -run TestEveryGoldenAgreesWithThePipelineDerivation`
  — at the base, FAIL with exactly the four drifts above; after the fix, ok.
  The rule reads all three goldens; the other two pass unchanged.
- `go test ./internal/statusmodel/ ./internal/contracts/... ./internal/cli/
  -count=1` — ok (the parity shape tests, the bundle verifiers and the CLI
  renderers against the new bytes).
- `make gate` — exit 0, 44 packages ok, gofmt and `go vet` clean.
- cloudflare: `pnpm contracts:check` (bundle 1.2.4 verifies), 
  `pnpm contracts:test`, `pnpm operator-strings`, `pnpm lint` (Biome clean),
  `tsc --noEmit`, and the full `vitest run` — 1624 tests, 0 failures (the
  phone-page and status-model suites exercise the corrected golden).

## What the next tick has to know

- The agreement guard now covers the tries' outcome and reason for EVERY
  golden: a future golden edit must keep each try consistent with its own
  gates array, its workers panel and the refusal lines its tail carries, or
  `TestEveryGoldenAgreesWithThePipelineDerivation` names the value.
- The reason demand is one-sided on purpose: where the document's tail shows
  a refusal line, the reason must be that line's detail cut at 160; where it
  shows none, the rule stays silent (a refusal older than the tail could
  still exist). Demanding null there would refuse honest goldens.
- Bundle is 1.2.4. A consumer still pinned to 1.2.3 is not wrong, only
  behind: the cut is a PATCH over corrected golden values, schema and rules
  unchanged.

```findings v2
[]
```

STATUS: DONE
