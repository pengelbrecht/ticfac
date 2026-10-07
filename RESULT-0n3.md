<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/ilz/attempt-4/0n3`, base `72a61b88a0c051599141746d3e703e56db20d733`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# Re-review of epic ilz (round 2), as integrated

The epic said it would ship the operator documentation v5t left out: a guide to
the `claude-sub` rung whose every claim is checked against
`cloudflare/src/claude-sub.ts`, `cloudflare/src/sandbox-executor.ts` and
`internal/reconcile/runconfig_select.go` (A1), a README link to it (A2), a
green `make gate` (A3), and a run whose own workers rode the subscription rung
(A4).

## What I could read, and what I could not

The checkout this review runs in holds a single synthetic commit (`72a61b8`,
`git rev-list --count HEAD` = 1) and no other refs. The epic's base
(`b99b5b0`), the integration head (`e834273`) and the round-1 head (`86dbb60`)
are not in the object store, so **no boundary diff was available to me**: I
read the integrated tree as it stands, plus the run's own records on the run
branch (`.ticfac/runs/run_4d92bab25a7341d3bba01691779168a0/`) and the tracker
(`.tick/issues/`). The epic's deliverable is two files, both read in full, so
the missing diff costs this review little — but it is why nothing below is
stated as "the diff adds".

## A1 — the guide, claim by claim

`docs/claude-sub-operator.md` (144 lines) covers all six topics A1 names, and I
checked each load-bearing claim against the cited source:

| guide | claim | source | verdict |
| --- | --- | --- | --- |
| 24-26 | a line break pasted inside the token is tolerated | `claude-sub.ts:175-179` (`normalizeToken` strips `\s+`) | correct |
| 30-32 | one secret per subscription, `LABEL` is `^[A-Z0-9_]{1,32}$` | `claude-sub.ts:115`, `:157-165` | correct |
| 53-55 | secret names are read at every lease, nothing to redeploy | `ClaudeSubPool.lease` → `subscriptionLabels(this.env)`, `claude-sub.ts:447-453` | correct |
| 57-59 | 401/403 benches 24h; rotating does not clear it; unbench after rotating | `claude-sub.ts:113`, `:208-210`, `:494-508` | correct |
| 66-71 | lease at boot, keyed on the attempt's job id, cap `CLAUDE_SUB_MAX_CONCURRENT` default 2, fewest-leases wins | `sandbox-executor.ts:1825-1846`, `claude-sub.ts:100`, `:182-185`, `:337-350` | correct |
| 72-80 | sticky per job id, benched or not, **only within `LEASE_TTL_MS` (2h) of when the lease was first taken**; a stale lease is treated as new | `claude-sub.ts:107`, `:331-334` (`now - held.at < LEASE_TTL_MS`, and `at` is never refreshed on reuse) | correct |
| 81-86 | quota test: unified status `rejected`, **or** a per-window `-status` (excluding `overage`), **or** no unified status at all with a parseable reset | `claude-sub.ts:212-227` | correct |
| 86-89 | quota bench = reset, else retry-after, else **60 seconds**; the 1s is only a floor | `claude-sub.ts:228`, `:231` | correct |
| 89-91 | non-quota 429 benches until **retry-after when the answer carries one**, else 60s | `claude-sub.ts:236-240` | correct |
| 94-98 | the job keeps its own 429; the next lease is the one routed around the bench | `ClaudeSubProxy.fetch`, `claude-sub.ts:552-597` (a retry is a new attempt, so a new job id: `run-…/tick-X/attempt-N`) | correct |
| 99-104 | `none`/`exhausted`/`busy` each step this one job down to the deployment's standing Workers AI pair, never a wait | `claude-sub.ts:144-152`, `:351-357`; `sandbox-executor.ts:1847-1858` | correct |
| 108-112 | two configs declared, `glm` default, `claude` = implement sonnet→opus, review and close-out opus | `.tick/runners.cloud.toml` `[configs] default = "glm"`, `[configs.claude.*]` | correct |
| 114-122 | `--config` > the epic's `config:` label > the `[configs]` default | `runconfig_select.go:216-227` | correct |
| 122-123 | a run keeps its config across a resume; a differing `--config` is refused | `runconfig_select.go:204-213` | correct |
| 123-128 | a rung-riding config with no subscription on the factory is refused at run start, at doctor and at the submission preflight | `runconfig_select.go:278-300`; `internal/cli/doctor.go:435-440`; `internal/cli/cloud_harness_preflight.go:184-188` | correct |
| 132-139 | `GET /api/claude-sub`, `POST /api/claude-sub/unbench/<LABEL>`, behind the operator bearer | `index.ts:1813-1815`, `claude-sub.ts:509-548`; `auth.ts:448-465` does not exempt the path, so `authenticateFactoryRequest` (`index.ts:1529-1541`) gates it | correct |
| 140-143 | `/api/deployment` carries `claude_sub_labels`, and that is what `factory status`/`doctor` read | `index.ts:258-263`; `internal/factory/status.go:482-489`, `internal/factory/deploy.go:455-469`, `internal/cli/doctor.go:620-629` | correct |

The guide also honours the epic's secrecy constraint:
`grep -nE "https?://|[0-9a-f]{32}|workers\.dev|\.com" docs/claude-sub-operator.md`
returns nothing — no URL, no account id, no token.

### The round-1 blocking finding is fully fixed

Tick 5gm carried four sub-claims; all four are now right in the tree
(rows 86-89, 89-91, 72-80 and 81-86 above). In particular the 60-second
default is now stated as the default with the 1s named as the floor it is, the
throttle arm's `retry-after` is named, the 2-hour TTL bounds the stickiness
claim it used to state unconditionally, and both omitted quota arms (the
header-absent-with-reset case and the `overage` exclusion) are now in the text.
Nothing in 5gm's fix overreached: the operator commands, which round 1 found
correct, are unchanged.

## A2 — the README link

`README.md:277-280` links `docs/claude-sub-operator.md` from the factory/cloud
section, immediately after the deploy-secrets paragraph. The target exists.

## A3 — the gate

The integrated gate ran on the final integration sha and passed:
`evidence/gate-5gm-3-go.json` (`gofmt` + `go vet ./...` + `go test -short
-timeout 45m -parallel 12 ./...`, exit 0) and `evidence/gate-5gm-3-ts.json`
(`pnpm lint && pnpm contracts:check && tsc --noEmit`, exit 0), both with
`source_sha e834273` and `result: pass`. I re-ran the cheap half here
(`gofmt -l . | grep -v '^contracts/'` → empty); I did not re-run the full suite,
because this epic changes no Go and no TypeScript and the host is shared.

## A4 — did the run's own workers ride the rung

I cannot confirm this from the tree, and I am saying so rather than implying it.
What the run branch shows is consistent with the rung: every attempt resolved a
claude alias — `56z`/`5gm` on `sonnet` at tier economy, `yaz` on `opus`
(`attempts/1.json`, `2.json`, `3.json`) — which under the cloud substrate can
only come from `[configs.claude]`, since every non-config cloud cell is pi on
GLM. But the fact A4 actually asks for, "the claude-sub lease, not a step-down",
is a Worker log line (`claude_sub: "benched"` / `"stepped_down"`,
`claude-sub.ts:578` and `sandbox-executor.ts:1847`) and is not persisted to the
run branch or to any evidence record. A step-down would have overridden
harness/model inside the boot, after the attempt record was written, so the
`sonnet`/`opus` values above do not settle it. This is a fact about the run, not
about the integrated tree, and no change to the tree could fix it — it is not a
reason to hold the epic. Whoever closes out should read it off the factory's
logs (or `GET /api/claude-sub`) rather than off these records.

## The tests

This is a documentation epic, and no test in this repository can assert that
prose matches source — the gate's two commands passed without executing a line
of the change. I take that as the honest state rather than a defect: the check
A1 actually asks for is a reading, which is what rounds 1 and 2 performed.

What is worth saying is that the guide is not resting on unverified source
behaviour. Every number the corrected failover section now states is pinned by
an existing suite, `cloudflare/test/claude-sub.test.ts`: the quota reset arm
(:72), the allowed-429-is-throttling arm (:85), the throttle arm at both
`THROTTLE_COOLDOWN_MS` and `retry-after: 7` (:102-111), the 24-hour auth bench
(:113), the cap and `busy`/`exhausted` reasons (:200-224), and the TTL (:236).
Two gaps: that suite runs in CI's `typescript` job and not in the per-tick gate
(`.github/workflows/ci.yml:409-419` — a deliberate 82s-vs-3s split), so a
docs-only epic PR never ran it; and no case asserts the one behaviour the
guide's corrected claim 3 newly promises an operator — the same job id asking
again *after* the TTL and being handed a different subscription. `:236` covers
the leak-expiry half with a job that never re-asks. I file that as a low
finding below.

## The findings round 1 left behind

My tick's notes list no deferred findings, and the three non-blocking findings
of round 1 were promoted to owned backlog ticks by the run itself. I looked at
each and judge none of them a reason this epic is not ready:

- **hbw** — "the repository has one Worker and one wrangler config"
  (`docs/claude-sub-operator.md:37-38`) is false: `cloudflare/staging/wrangler.toml`
  exists and binds `CLAUDE_SUB_POOL` too. But it is an aside justifying the
  absent `-c` flag, and the instruction it justifies is right: run from
  `cloudflare/`, no `-c`, and wrangler reads the production config. An operator
  following the guide does the correct thing. Stays backlog.
- **gv3** — `claude-sub.ts:30-32` still says "Production does none of these
  (only cloudflare/staging does)" while `cloudflare/wrangler.toml:166` binds
  the pool and `index.ts:2006` exports the proxy. A stale docstring in a file
  this epic did not set out to change. Stays backlog.
- **55q** — `LEASE_TTL_MS` (2h) being shorter than a worker's 8h
  `wall_seconds` is a real design question about the pool, not about the guide,
  and the guide now describes the 2 hours accurately. Stays backlog.

## My own findings

Two, both backlog, neither a reason to hold the epic: the guide names the
factory's per-subscription cap but not the repository's own per-tier width, and
the stale-lease promise is untested. Both are in the block below.

```findings v2
[
  {
    "kind": "defect",
    "title": "claude-sub guide names only the factory's concurrency cap, not the config's",
    "severity": "medium",
    "body": "The guide's failover section tells an operator the width of the rung is CLAUDE_SUB_MAX_CONCURRENT, default 2 (docs/claude-sub-operator.md:70). That is the factory's per-subscription lease cap, and it is stated correctly, but it is not what bounds how many claude workers this repository runs at once: .tick/runners.cloud.toml also declares [configs.claude.tier_policy.concurrency] economy = 2, strong = 1, which TierPolicy.WaveWidth (internal/runconfig/tierpolicy.go:533) narrows the wave to. An operator asking the guide why only one opus worker is running is sent to the wrong number, in the wrong place, with no hint the second cap exists. One sentence in the failover or the config-selection section would close it.",
    "evidence": "docs/claude-sub-operator.md:68-71 against .tick/runners.cloud.toml ([configs.claude.tier_policy.concurrency]) and internal/runconfig/tierpolicy.go:515-545"
  },
  {
    "kind": "defect",
    "title": "No test pins the stale-lease reassignment the guide now promises",
    "severity": "low",
    "body": "The guide's corrected stickiness claim (docs/claude-sub-operator.md:76-80) promises an operator that a job whose lease goes stale past LEASE_TTL_MS is treated as new on its next ask and may be handed a different subscription. ClaudeSubPoolCore.lease does that (claude-sub.ts:331-334), but no case asserts it: claude-sub.test.ts:184 covers stickiness inside the TTL, and :236 covers the leak expiry with a job (\"leaked\") that never asks again. A case that leases a job, advances the clock past LEASE_TTL_MS and re-asks with the first subscription benched would pin the sentence the guide now stakes the operator's expectation on.",
    "evidence": "cloudflare/test/claude-sub.test.ts:184-243 against cloudflare/src/claude-sub.ts:329-334"
  }
]
```

## Judgement

The epic set out to write one operator document whose every claim survives a
reading against three named source files, link it, and keep the gate green. The
document does, the link is there, and the gate passed on the integrated head.
The one blocking finding of round 1 is fixed completely rather than narrowly,
and the three claims it named now read exactly as `classifyAnswer` and
`ClaudeSubPoolCore.lease` behave. The two findings I add are an omission and a
test gap, not errors an operator would act on wrongly. A4 is a fact about the
run that this tree cannot carry either way, and the close-out should read it off
the factory.

REVIEW-VERDICT: READY

STATUS: DONE
