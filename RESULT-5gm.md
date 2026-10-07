<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ilz/attempt-3/5gm`, base `45f9309c88fea055271167cfc81191ada53f6b60`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# RESULT-5gm

## What changed

`docs/claude-sub-operator.md`, "How failover behaves" section, corrected
against `cloudflare/src/claude-sub.ts`:

1. **Sticky lease bullet.** Was: "a job that already holds a lease gets the
   same subscription back on every later ask, benched or not... A running
   job never switches subscription." Now states the reuse is bounded by
   `LEASE_TTL_MS` (2 hours, `ClaudeSubPoolCore.lease`): a lease held past
   that age is treated as gone, and the job's next ask may be given a
   different subscription.
2. **Quota-rejection bench fallback.** Was: "falling back to `retry-after`,
   or one second out if neither is usable" — one second is only
   `classifyAnswer`'s floor (`Math.max(until, now + 1000)`), not the
   fallback; the real fallback (`now + THROTTLE_COOLDOWN_MS`) is 60 seconds.
   Fixed to say 60 seconds, with the one-second floor called out separately.
3. **Non-quota 429 bench.** Was: "benches the subscription only briefly (60
   seconds)" unconditionally. `classifyAnswer`'s throttle arm returns
   `retryAt ?? now + THROTTLE_COOLDOWN_MS`, so an answer carrying
   `retry-after` benches for that instead, which can exceed a minute. Fixed
   to say "until `retry-after` if the answer carries one, else 60 seconds."
4. **Quota-test description.** Also tightened the sentence describing what
   counts as a quota rejection: added the `(unified === "" && reset !==
   null)` case (no unified-status header but a parseable reset still counts
   as quota) and the `/overage/`-window exclusion, both present in
   `classifyAnswer` but previously unstated.

## What I ran

- `GOTEST_PARALLEL=4 GOFLAGS=-p=2 make gate` — gofmt clean, `go vet ./...`
  clean, `go test -short ./...` all pass (full output captured in this
  session; no failures, no skips affecting this change).
- Docs-only change: no Go or TypeScript source touched, no new test surface
  to add. Grepped for any test referencing `claude-sub-operator.md` — none
  exists, so the gate above is the full check available.

## Next tick / reviewer note

This tick closes the blocking finding the epic `ilz` final review (decision
2, run `yaz`) named. No other part of the doc or `claude-sub.ts` was
touched — scope was the three misstated numbers (and the one related
quota-test omission in the same bullet) called out in the finding body.

```findings v2
[]
```

STATUS: DONE
