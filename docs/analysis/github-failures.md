# GitHub failures across ticfac runs: what we hit, why, and what to do

Status: decision doc, 2026-10-05. Read-only analysis. No code changed.

## Headline

Most of what looked like "GitHub failing" was this host losing its network.
Of the 267 remote-retry events in every run feed so far, 251 (94%) were DNS,
unreachable-network, timeout or reset errors on the operator's Mac. They were
clustered on 09-22 to 09-28 and mostly stopped after #94/#97 (bounded
transport) and #119 (IPv4 fallback). The failures that really were GitHub's
are few, but they still happen and they all have one cause.

**Cause: ticfac uses GitHub as a transactional database.** Every tracker
record is its own commit and its own `git push` (claim, note, close, create,
blocked-by), plus integrations, gate evidence and base folds. Nothing paces
those pushes across the runs writing to one repository. Measured:

- At least 2,197 pushes to the integration branches came from run feeds, and
  `published` alone undercounts them by about 1.6x. With two local runs live,
  GitHub's own event feed shows 71 to 74 pushes an hour to `epic/*`.
- **11% of pushes were the 7th or later in their trailing minute.** GitHub's
  documented recommended maximum is **6 pushes per minute per repository**
  ([repository limits](https://docs.github.com/en/repositories/creating-and-managing-repositories/repository-limits)).
  The peak was 17 pushes in 60 s, and 36 separate minutes had more than 6
  push events.
- **The bare `! [remote rejected] <sha> -> <ref> (failed)` rejections track
  cross-run concurrency.** 7 of the 8 local ones came within 5 s of the
  *other* live run's push to the same repository, against 3% of all pushes
  (n=452, hn6 and 43y overlapping). Before this they were treated as random
  GitHub blips.
- **Merges to main synchronise the runs.** At 10:24:20 on 10-05, hn6 and 43y
  folded the same main commit 140 ms apart and pushed together. 43y's push
  failed `(failed)`.
- Every tracker push to `epic/**` touches `.tick/`, which CI does not ignore,
  so each one starts a CI run. epic/hn6 has 591 CI runs, 506 of them (86%)
  cancelled. epic/43y has 210, 187 of them (89%) cancelled.

Ref count is **not** a cause today. Origin has 762 refs: 295
`ticfac/run-*`, 214 `refs/pull`, 68 `tick/*`, 31 `refs/ticfac/start`.
GitHub's branch limit is 5,000. No rate-limit, 429, secondary-limit or abuse
response appears anywhere in the feeds or container logs.

**Systemic fix, in order:** (1) one push per reconcile step instead of per
record (**f61** — one commit and one push per step), (2) one paced, jittered
push queue per repository shared by every writer on a host (**new: rlp**),
(3) stop routing a run's token at other repositories (**new: gy9**), then
hygiene: retire ended runs' refs (**6is** — retire ended runs' branches) and
move the per-attempt hot path off GitHub (**ujr** — spike on Cloudflare
Artifacts as run-scoped git).

## Evidence

Sources:

- 11 local epic feeds and 6 local `run_*` feeds (`.ticfac/logs/*/events.jsonl`),
  09-15 to 10-05.
- All 24 cloud runs on this repository (`ticfac events <run>`), 09-23 to 10-04.
- Cloud container logs for the six largest runs.
- `git ls-remote origin`.
- GitHub's repository event feed (the last 300 events, about 19 h).
- `gh run list` per epic branch.
- 212 merged PRs.

Every remote git call goes through one wrapper (`runstate.ClassifyRemote`,
`Reconciler.remoteRetry`), which writes `remote_retried` and
`remote_exhausted` lines. That is why these counts are complete for the
orchestrator's own git. Worker pushes (attempt branches, wip refs) are not in
the feeds.

### Every failure, by category

Counts are feed events. One failing operation produces one event per attempt.

| Category | Retry events | Bound exhausted | Run halted for a person | Where in the flow | Status |
|---|---|---|---|---|---|
| **Host DNS / network unreachable** (`Could not resolve host`, `Network is unreachable`, `port 22 timed out`) | 181 | 52 | 0 after #53/enj (auto-resumed 21x: yoh 7, xte 4, gvc 3, wne 3, 6in 2, d51a 2) | ls-remote 160, fetch 33, push 40. Every phase. | Host-side. Ended by #119 (IPv4 fallback). Nothing since 09-28. |
| **Host connection reset / timeout / silent hang** | 17 | 1 | 2 (ncv 09-18 fetch reset before enj; 6in 09-28 30-min checkpoint push with empty stderr) | fetch, push | Fixed: enj, #94/#97 (transport bound), tracked in **art**. |
| **`[remote rejected] … (failed)`** (GitHub accepted the pack, ref did not move) | 8 local + 1 cloud | 0 | 1 (cloud run_911b 09-30 12:12, claim push, before #153) | Leased pushes to `epic/<id>` (claim, create, close, base fold), 1 to a worker attempt branch | Retried as transient since #153. **Root cause open: cross-run push concurrency (below).** Also seen twice on 10-05 by the operator's own manual pushes. |
| **GitHub storage fault** (`remote: error: unable to write file … fatal error in commit_refs` → `(failure)`) | 0 | 0 | 1 (epic-2jn 09-27 15:04, checkpoint push) | push | Transient since #69. |
| **HTTPS 403, App token, same repository** (`Permission to …/ticfac.git denied to ticfac[bot]`) | 4 | 0 | 2 (run_0f64 09-29, gate-evidence and classification pushes, 47 and 22 min into a boot) | Leased pushes to `epic/hn6` | Token lifecycle (fresh-token propagation, fallback to an expired boot token). #140 + #153: named, retried 4x, fresh token per retry. **All recovered since.** |
| **HTTPS 403, App token, other repository** (`Permission to …/ticks.git denied`) | 3 | 1 | 0 (finding backlogged instead) | Cloud run_5c7c 10-04 02:18: pushed a routed finding straight to `ticks` `main` | **Deterministic, not a blip:** the per-run token is minted for one repository (`github-app.ts`, `repositories: [repo]`). Retried 4x for nothing. **Gap → gy9.** |
| **ssh key refused** (`Permission denied (publickey)`) | 0 | 0 | 2 (yoh 09-24 14:34, 14:37, during a DNS storm) | fetch | ssh-agent blip. Retried a small bound since jsz/#62. |
| **Non-fast-forward / lease race on origin** | 0 | 0 | 1 (yoh 09-24 14:36, unleased `prepare integration branch` push during the same storm) | push | Lost-ack writes (o82/#62). No recurrence. |
| **Local ref lock / D/F conflicts** (`cannot lock ref refs/ticfac/peek/...`) | 0 | 0 | 2 (9pd 09-16) | fetch into private refs | Local, not GitHub. Fixed by private peek namespaces. |
| **Branch listing** ("more than 2000 branches" with 281 on origin) | — | — | progress `unknown` on every cloud run past 100 branches | Factory reading `matching-refs` | Fixed #149. GitHub ignores `page`/`per_page` there. |
| **Missing workflow trigger** (no PushEvent for a merge) | — | — | deploy never shipped #178 | Merge to main | GitHub's. Watchdog dispatch #184. |
| **PR CI never runs on a "conflicting" epic PR** | — | — | close-out `closeout_ci_absent` for an hour (09-23) | `pull_request` events | Push-triggered CI on `epic/**` (#38). Still open: **b6u**, **ciw**. |
| **REST unreachable at close-out** (`api.github.com … network is unreachable`) | — | — | 1 rejection (xte 09-23) | check-runs read | Host network. |
| **Rate limits / 429 / secondary / abuse** | **0** | 0 | 0 | — | Never observed. |

### Push volume against GitHub's guidance

Push events are `published` + `integrated` + `base_refreshed` that moved the
branch. Local and cloud runs on this repository are combined and deduplicated.

- Total: 2,197 (2,279 before deduplication and dropping no-op folds). `published` is 1,706; per run, notes ~50%, claims ~20%,
  creates/blocked-by ~20%, closes ~15%.
- Single run: peak 13 in one minute (hn6) and 46 in five minutes (hn6). Cloud
  runs peak at 12/min (run_5c7c).
- Pushes that were the 7th or later in their trailing 60 s: 244 (11.1%).
  Sliding-window max: 17.
- GitHub's event feed (it samples and has gaps, so this is a lower bound):
  max 11 pushes to `epic/*` in one minute; 71 and 74 an hour at 10-05 10:00
  and 11:00. That count includes 46 by `ticfac[bot]` (cloud) and the rest by
  the operator's own credential (local runs and agents).

### The `(failed)` rejections line up with the other run

| Failure | Run | Nearest push activity by the *other* live run |
|---|---|---|
| 10-04 18:35:05 | 43y | 1.4 s |
| 10-04 18:35:19 | hn6 | 0.8 s |
| 10-04 18:36:08 (worker attempt branch) | hn6 | 0.7 s |
| 10-04 18:52:00 | hn6 | 3.5 s |
| 10-04 18:52:10 (same push, retry 2) | hn6 | 1.4 s |
| 10-04 18:52:24 | 43y | 1.6 s |
| 10-04 18:52:55 | 43y | 0.6 s |
| 10-05 10:24:32 | 43y | 11 s in the run feed. GitHub logged hn6's base-fold push at 10:24:25 and 43y's at 10:24:21; both folded main c7e11fe3 within 140 ms. |

Within 5 s: 7 of 8 failures, against 3% of all pushes. The two runs write
**different refs** (`epic/hn6`, `epic/43y`), so this is not a same-ref lease
race. It is contention at the repository level. GitHub moves every ref of a
repository through one replicated, per-repository update path, and its
documented throttling is per repository too. The mechanism inside GitHub is
an inference. The correlation is measured: with p = 0.03, 7 or more
coincidences in 8 has a binomial probability of about 10⁻¹⁰. The one cloud
`(failed)` (run_911b, 09-30 12:12) came 37 s after a burst of
take-over/claim/dispatch pushes, with workers pushing attempt branches at
the same time, which the feed does not show.

### What the per-record design costs GitHub

- `durableTracker.write` (`internal/reconcile/tracker.go`) does a sync (fetch)
  then a push **for every record**. So each claim, note or close is two round
  trips to GitHub, and one absorbed finding (create + blocked-by + note) is
  six.
- Every one of those pushes touches `.tick/`, so it starts a CI workflow run on
  `epic/**`. `ci.yml` ignores `.ticfac/**` but deliberately not `.tick/**`. The
  next push cancels the run (86–89% cancelled on hn6/43y). This is Actions
  load, event load and noise, and it is how a close-out's CI gets cancelled
  under it (**5ob**).
- Merges to main make every live run fold and push at the same moment.

### What it does not cost (hypotheses rejected)

- **Rate limits / abuse detection:** zero 429, "secondary rate limit" or abuse
  responses. The REST reads (check-runs, PRs) never failed except on a dead
  network. Moving from PAT to App, or the reverse, would not change any
  observed failure.
- **Ref count:** 762 refs, 0 failures attributable. The one "branch limit"
  failure was our own pagination bug (#149). Refs still grow without bound:
  2jn alone left 69 branches, and nothing deletes a finished run's job
  branches. That is 6is's hygiene case, not a failure cause.
- **403 as disguised throttling:** the same-repository 403s fit token lifecycle
  (one came 30 s after boot, one 70 min after boot on a 1-hour token) and
  stopped after #140. The cross-repository 403 is deterministic.

## Decision: systemic solutions, ranked by impact / effort

| # | Solution | Removes | Impact | Effort | Covered by |
|---|---|---|---|---|---|
| 1 | **One commit and one push per reconcile step**, not per record. A step's claim+note, create+blocked-by+note, note+close, evidence+close become one push. | An estimated 2–3x fewer pushes (pairs and triples dominate the feeds), the matching sync fetches, and the matching CI runs. Brings a single run under 6/min. | High | Medium. Its durability invariants have to be restated first, as the tick says. | **f61** (open, under t8u). Promote it: its case was test speed, and this doc adds GitHub contention as the stronger reason. |
| 2 | **One push queue per repository per host**, shared by every run, the operator's `ticfac` commands and worker pushes. A lock in the git common dir, a token bucket under 6 pushes/min, and full-jitter backoff on `(failed)`. Base folds after a main merge are jittered. Push count and GitHub errors are reported per run in `status` and the feed. | The cross-run coincidence that 7 of 8 `(failed)` rejections share, and the synchronised fold bursts. | High for `(failed)` | Low–medium. Local mirror of what **ef7** did for the cloud (one serialised publisher per repository in a Durable Object). | **New: rlp** |
| 3 | **A run never pushes to a repository its token was not minted for.** Mint the target repository's token through the token door (App rung), or classify a cross-repository 403 as terminal at once and backlog the finding. | The deterministic 403s on routed findings, and the 4 retries a known refusal currently spends. | Medium | Low | **New: gy9** |
| 4 | **Do not start CI for tracker-only pushes.** Either ignore `.tick/**` on `epic/**` pushes, with the close-out dispatching CI on its head the way `dispatchSilentCI` already does, or let (1) shrink the count. | About 500 cancelled workflow runs per epic. | Medium | Low | **5ob**, **ciw**, **b6u** (open). Decide there; no new tick. |
| 5 | **Retire ended runs' refs** by rule. | Unbounded ref growth (762 now; 2jn left 69 branches). | Low today, needed eventually | Low–medium | **6is** (open), tyv (wip refs, done) |
| 6 | **Move the per-attempt hot path off GitHub** (worker attempt/landing/wip branches in a run-scoped Artifacts repository; push events instead of polling). | Worker pushes and most of the ref growth. It does **not** remove tracker pushes to `epic/*`, which are what contends. | Medium | High (spike) | **ujr** (open) |
| 7 | **Host network**: wait out an outage longer than the 14 s retry bound, rather than spending supervision continuations. | 53 exhausted bounds, 21 auto-resumes. | Low now (quiet since #119) | Low | Not filed. Revisit if it recurs. **art** covers the hang half. |

Not recommended: switching between PAT and GitHub App for rate-limit headroom
(no limit was ever hit), and raising retry counts on `(failed)` (it already
recovers in-run; the fix is to stop causing it).

### Order

1. **rlp.** It is the cheapest thing that stops the `(failed)` class, and it
   adds the push counter that measures everything after it.
2. **gy9.** Small and deterministic.
3. **f61.** Biggest reduction, after its invariants are written down.
4. Settle 5ob / ciw for tracker-push CI.
5. 6is.
6. ujr, on its own merits.

## Ticks

- Existing: **f61** (one commit and one push per step), **6is** (retire ended
  runs' branches), **ujr** (Cloudflare Artifacts spike), **art** (a hung push
  stalls a run), **5ob** (checkpoints cancel close-out CI), **ciw** /
  **b6u** (GitHub stops PR CI on tracker-file conflicts), **ef7** (cloud
  serialised publisher, done), **dm6** (GitHub App rung).
- Filed by this analysis:
  - **rlp** — one paced, jittered push queue per repository on a host, with
    per-run push and GitHub-error counts.
  - **gy9** — a run's token pushes only to the repository it was minted for.
