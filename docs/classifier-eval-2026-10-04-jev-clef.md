# Jev vs Clef vs Clef-flash on tick classification (tick r3y, 2026-10-04)

The evaluation half of tick `r3y` (switch tick classification from Jev to
Clef-flash). It asks the three Workers AI decision models the question ticfac
asks in production, on the ticks whose outcomes tick `ms9` (Jev for tier start)
labelled, and compares them on cost, speed, agreement and predictive value.

**Bottom line.** Keep Jev as the default for now. Clef-flash is not a drop-in
swap: the production reader refuses its response, and its distributions are
so flat that the declared `mass_threshold = 0.75` would route no tick dear.
It is no faster end to end and costs about 1.7x Jev per call, though both
costs are negligible. On predictive value the three are the same, and the
value is nil: the dear-mass score has no signal for "needed a stronger tier"
under any of them (AUC 0.37 to 0.42). The routing rule, not the classifier,
is what needs to change. Details and recommendation are at the end.

## Models

| model | Workers AI id | answers as | list price |
|---|---|---|---|
| Jev (TypeSafe) | `typesafe/jev` | `jev-1.13.0` | $0.042 / M input tokens, output free (wne) |
| Clef (Cloudflare, 27B, Apache-2.0) | `@cf/cloudflare/clef` | `clef` | $0.24 / M input tokens (model catalog) |
| Clef-flash (Cloudflare, 9B, Apache-2.0) | `@cf/cloudflare/clef-flash` | `clef-flash` | $0.09 / M input tokens (model catalog) |

The ids come from the account's Workers AI catalog (`ai/models/search`).
Jev is a partner model and is not listed there. All three take the same
`POST …/ai/run` body, `{"model", "input": {"state", "questions"}}`. The
criteria objects (`what` / `not_for` / `examples`) are accepted unchanged.

## Method

- **Tick set: 127 role-less implement ticks.** This is ms9's 108 (the
  9pd/dha/ncv 49-set minus its 3 role ticks, plus 2jn 45, 6in 8 and hn6 9),
  plus 19 hn6 ticks first classified after ms9. That adds up to every
  `classify-tick` decision on any `origin/epic/*` branch: 61 decisions on 29
  distinct ticks, all on hn6 except `bib` on 6in.
- **The request is the original one.** For the 29 ticks with a recorded
  decision, the title, description and acceptance criteria are the ones the
  first recorded decision sent. For the other 98, they are the tick text ms9
  re-asked with (ms9's `ticks-input.jsonl`). The body is built by the
  production client (`jev.Client.Classify`, one tick per call, run-epic's
  state and question). Only the body's model id is swapped, by an HTTP
  transport (`benchmarks/classifiers/main.go`), so the model is the only
  variable.
- **Two passes.** Pass 1 ran the three models at the same time, each one call
  at a time. Pass 2 ran one model after another, so its latency is
  isolated, and comparing it with pass 1 measures answer stability. The
  latency is the wall time of the HTTPS round trip from the operator's Mac.
- **Outcomes are ms9's labels, unchanged**, so the AUCs compare directly
  with ms9's 0.42. Of the 127 ticks, 96 have a known outcome:
  - FAILED (10 ticks): a cheap start that failed and then escalated, meaning
    a wall-clock or stuck kill, a work rejection, or a tier escalation.
  - HARD (32 ticks): FAILED, or any attempt with more than 60 minutes of
    work.
  - Process failures (merge conflicts, holds, the absorption bound, start
    failures) do not count.
- **The 28 hn6 ticks have no outcome label.** Their histories (16 run feeds:
  6 local logs and 10 from `ticfac events`) are dominated by cloud
  infrastructure failures. These are an unreachable dispatch door,
  `missing-result` with no push, `lease_lost`, and an "escalation" after an
  operational `no-commits`. ms9 treated such histories as unknown, and they
  are left unknown here, so hn6 contributes to the agreement and latency
  figures only.
- **Dear mass** is P(design) + P(diagnosis), renormalised over the five
  work types (Clef's probabilities carry four decimals and sum to 1 ± 1e-4).
- **The AUC is the probability that a random positive outranks a random
  negative**, with ties counted as half. The 95% intervals come from 2,000
  bootstrap resamples of the 96 ticks.

## Results

### Calls: failures, latency, cost

| model | answered | refused / no answer | median | p95 | max | mean input tokens | list cost / call |
|---|---|---|---|---|---|---|---|
| Jev | 254/254 | 0 | 276 ms | 366 ms | 812 ms | 1,431 | $0.000060 |
| Clef-flash | 254/254 | 0 | 281 ms | 522 ms | 844 ms | 1,152 | $0.000104 |
| Clef | 254/254 | 0 | 517 ms | 932 ms | 1,429 ms | 1,152 | $0.000276 |

The latencies are isolated (pass 2). Pass 1 (concurrent) gave the same
medians within 20 ms. The max column is the larger of the two passes.

- **Nothing failed or refused** in 762 calls. **But the production reader
  rejects every Clef answer.** Clef answers one level shallower than Jev:
  `result -> {model, answers, usage}`, where Jev answers
  `result -> {state, result: {model, answers, usage}}`. `internal/jev`'s
  `parseResponse` reads only `result.result.answers`, so pointed at Clef it
  returns `Unavailable` ("the response carries no result.result.answers")
  on 254 of 254 calls. A run would then quietly start every tick at the
  policy default. The harness reads the shallow shape itself and labels
  each answer with the shape it came in. r3y's acceptance criterion
  "verify the answer shape is identical" fails as stated, and the reader
  must accept both shapes.
- **Latency.** The blog's ~39 ms (Clef-flash) against ~524 ms (Jev) is model
  time. End to end through `ai/run`, Clef-flash and Jev are the same at the
  median (about 280 ms), and Clef-flash has the longer tail. Clef is about
  1.9x slower than both. Classification runs once per tick, so none of this
  matters to a run.
- **Cost.** `usage.cost_usd` is 0 on every response from all three models.
  The figures in the table are list price times reported input tokens. Clef's
  tokenizer counts about 20% fewer tokens for the same request. At about
  $0.0001 per tick, the swap is a cost question for nobody.

### What the models answer

| model | argmax over 127: mech / trans / constr / diag / design | median top prob | median `confidence` | mean dear mass | mass > 0.75 |
|---|---|---|---|---|---|
| Jev | 27 / 6 / 53 / 12 / 29 | 0.78 | 0.72 | 0.31 | 27/127 |
| Clef-flash | 34 / 8 / 32 / 15 / 38 | 0.37 | 0.07 | 0.37 | **0/127** |
| Clef | 13 / 16 / 76 / 12 / 10 | 0.46 | 0.14 | 0.27 | 1/127 |

The two Clefs give **much flatter distributions**. The highest dear mass
Clef-flash gives any of the 127 ticks is 0.74 (`w1c`; `19l` is 0.74 too). A Clef `confidence`
is not on Jev's scale either: a 0.54 top probability comes back with
confidence 0.20. Every threshold measured on Jev (`mass_threshold = 0.75` in
`.tick/runners.local.toml`, and 7l1's cloud ladder) is therefore meaningless
under Clef. Swapping the model without re-measuring the threshold turns
dear starts off.

### Agreement and distribution similarity

| pair | n | argmax agree | Cohen's κ | mean JS div. | mean \|Δ mass\| | Spearman (mass) |
|---|---|---|---|---|---|---|
| Jev vs Clef-flash | 127 | 78 (61%) | 0.49 | 0.212 | 0.245 | 0.80 |
| Jev vs Clef | 127 | 81 (64%) | 0.48 | 0.170 | 0.194 | 0.85 |
| Clef vs Clef-flash | 127 | 65 (51%) | 0.37 | 0.067 | 0.122 | 0.82 |
| Jev today vs Jev ms9 (2026-09-30) | 108 | 105 (97%) | 0.96 | 0.002 | 0.012 | 1.00 |
| Jev today vs Jev as recorded in the runs | 29 | 28 (97%) | 0.95 | 0.002 | 0.013 | 0.99 |

The JS divergence uses log base 2 and runs from 0 to 1.

- **The models agree moderately on the top work type** (κ about 0.5). On
  dear mass they agree more: the rank correlation is 0.80 to 0.85, so they
  largely order the ticks the same way and differ mostly in how sharp they
  are. The two Clefs have close distributions (JS 0.07) but split argmaxes,
  because flat distributions tip on small differences.
- **Stability.** Clef and Clef-flash are **deterministic**: their two passes
  were identical on 127 of 127 ticks (Δ mass 0.000). Jev drifts slightly:
  3 of 127 argmaxes flipped, with mean |Δ mass| 0.014 and a maximum of
  0.08, matching ms9's 0.011 and 0.09. Jev's answers have also not moved
  since ms9 or since the runs recorded them.

### Predictive value: dear mass vs "needed a stronger tier"

N = 96 ticks with known outcomes: 32 HARD, 10 FAILED.

| score | AUC → HARD (95% CI) | AUC → FAILED (95% CI) | dear at > 0.75 | FAILED caught at > 0.75 |
|---|---|---|---|---|
| **ms9 Jev (published)** | **0.42** (0.30–0.55) | **0.45** (0.22–0.69) | 22 | 3/10 |
| Jev, today | 0.42 (0.30–0.54) | 0.45 (0.23–0.69) | 24 | 3/10 |
| Clef-flash | 0.42 (0.30–0.55) | 0.43 (0.19–0.70) | 0 | 0/10 |
| Clef | 0.37 (0.25–0.49) | 0.40 (0.18–0.65) | 1 | 0/10 |
| mean of the three | 0.40 (0.28–0.53) | 0.41 (0.19–0.66) | 4 | 1/10 |
| *for context:* request length (chars) | 0.48 (0.35–0.61) | 0.52 (0.32–0.70) | — | — |
| *for context:* P(construction), Jev / Clef-flash / Clef | 0.67 / 0.65 / 0.67 | 0.58 / 0.64 / 0.57 | — | — |

AUC 0.5 means no signal, and an AUC below 0.5 means the score points the
wrong way.

- **None of the three models makes dear mass predictive.** Clef's AUC on
  HARD is the lowest, and its interval (0.25 to 0.49) lies entirely below
  0.5: under Clef, more dear mass goes with *less* hardness. The reason is
  the one ms9 found. The ticks that burned an hour (`46x`, `dz1`, `nu9`,
  `ef7`, `i1r` among the FAILED) are big construction ticks at near-zero
  dear mass, and every model agrees they are construction or mechanical.
- **Matched on the dear share, the models catch the same ticks.** Picking
  each model's threshold so that 24 of the 96 start dear (Jev 0.77,
  Clef-flash 0.50, Clef 0.43), each catches 3 of 10 FAILED. All three
  catch `19l` and `9fc`. Jev's third catch is `k7p`, while both Clefs'
  is `823`. On HARD, Jev and Clef-flash each catch 7 of 32 and Clef
  catches 5. Clef-flash at 0.50 is the like-for-like replacement for Jev
  at 0.75. No model buys a meaningful catch the others miss.
- **P(construction) is the one score with a hint of signal for HARD**
  (0.65 to 0.67, with lower bounds 0.52 to 0.55). Treat it as a hypothesis,
  not a finding. It was found after looking at several scores, the effect
  is weak, it does not reach FAILED reliably, and it is the mirror of ms9's
  observation that long ticks are construction ticks. A size proxy
  (request length, or Jev's input tokens) has no signal either.

## Limits

- **N is small.** 96 labelled ticks with 10 FAILED: a FAILED AUC's 95%
  interval is about ±0.25 wide, so nothing here can separate the three
  models on FAILED. The HARD intervals are about ±0.12.
- **No tick has run on a cheap tier with an outcome we can use.** "Hard at
  strong" is not "would have failed cheap" (ms9's caveat, still true). The
  hn6 runs since 2026-09-30 do start at `economy`, but their failures so far
  are operational, so they are left unlabelled.
- **The questions are ms9's.** For 98 ticks the request is today's tick
  text, not the text at dispatch time. The 29 recorded requests show no
  material difference: Jev today agrees with Jev as recorded on 28 of 29.
- **One criteria text, tuned on Jev.** The work-type criteria
  (`internal/jev/wire.go`) were written and checked against Jev's answers
  (wne). Clef may do better with criteria written for it. That was not
  tried.
- **Latency is from one Mac.** It was measured on 2026-10-04. The
  orchestrator container's route goes through the factory gateway and was
  not measured.

## Recommendation

1. **Keep Jev as the default and do not swap to Clef-flash as a drop-in.**
   As shipped, a swap makes every classification unavailable (the response
   shape). Fixed, it routes nothing dear at the declared 0.75 (the flat
   distributions). It brings no latency gain end to end and no predictive
   gain.
2. **Do r3y's implementation half anyway, so the model can be chosen**, with
   two requirements this evaluation adds:
   - The reader must accept both `result.result.answers` and
     `result.answers`.
   - `mass_threshold` must be declared per model. On this set, Clef-flash
     at 0.50 and Clef at 0.43 give the same dear share as Jev at 0.75 and
     the same number of FAILED catches.

   Clef has real advantages that are not about accuracy, and they are what
   would justify a later switch. It is open weights, a first-party Workers
   AI model rather than a partner in limited early access, and
   deterministic, which suits axiom 1 (a re-derivation reaches the same
   dispatch). It is also the route to Cloudflare's RL fine-tuning on our own
   outcomes. If we switch, Clef-flash at 0.50 is the like-for-like choice.
   Clef costs 2.7x more than Clef-flash and adds 240 ms for nothing measured
   here.
3. **The routing rule should change, not the classifier.** Under all three
   models, P(design) + P(diagnosis) does not predict which ticks need a
   stronger tier. Today it buys about 3 catches out of 10 by spending a dear
   start on about a quarter of ticks. The escalate-after-failure ladder is
   still what rescues the costly ticks. The next step is cheap:
   - Add a size/effort question (for example a Choice of
     small / medium / large change, or the files and subsystems touched) to
     the same call, and record it beside the work type.
   - Re-run this evaluation once the economy-start runs on hn6 and later
     epics have produced real "failed cheap" outcomes.

   Until then the 0.75 rule can stay, because it is cheap and it does catch
   the design ticks. Nothing should be built on top of it.
4. **For an RL-tuned router** (Cloudflare's fine-tuning on AI Gateway
   traffic), the data we would need is, per tick:
   - the exact request (state and questions, which the decision record
     already keeps);
   - the starting tier;
   - every attempt's tier, verdict and work time;
   - whether each failure was work or process.

   We would need it on the order of hundreds of ticks with *cheap-start*
   outcomes. Today we have 96 labelled ticks, none of them cheap-start, and
   only 10 work failures.

## Reproducing

```sh
# one JSON tick per line: {id, title, description, acceptance_criteria, role}
go run ./benchmarks/classifiers -model typesafe/jev               < ticks.jsonl > jev.jsonl
go run ./benchmarks/classifiers -model @cf/cloudflare/clef-flash  < ticks.jsonl > clef-flash.jsonl
go run ./benchmarks/classifiers -model @cf/cloudflare/clef        < ticks.jsonl > clef.jsonl
```

The credential is the one run-epic uses: `TICFAC_JEV_*`, or `~/.ticfacrc`'s
Cloudflare token and the gateway URL's account. Nothing is printed.
`benchmarks/classifiers/2026-10-04-jev-clef.jsonl` holds this run's per-tick
results: each model's distribution, confidence, latency and input tokens
(pass 1), with ms9's HARD and FAILED labels (null where unknown). Every
table above can be recomputed from that file.
