# Contract bundle changelog

The bundle in this directory is versioned so a consumer can pin it by exact
value: `cloudflare/contracts.pin.json` here does, by exact version. This file
is the other half of that pin — the version says *which bytes*, the entry below
says *what changed and who has to follow*.

Since 1.0.0 this bundle is **ticfac's**: ticfac authors it, and the version
series starts here. Versions 2.1.1–6.3.0 were ticks' bundle — the same
fixtures at the bytes ticfac took over — and their changelog history lives in
the ticks repository. 1.0.0 is a MAJOR restart of the series on purpose: a new
owner renumbers, so a version string can never silently mean two different
owners' cuts.

## The rule

**Every change to a file in `contracts/` bumps `version` in `bundle.json` and
adds an entry here, in the same commit.** Both halves are enforced:
`internal/contracts` (Go) and `cloudflare/scripts/contracts.mjs` (TypeScript)
each re-hash the fixtures and refuse a manifest that does not match, each
refuses a version with no entry here, and each refuses a manifest re-cut at a
version `version_digests` already records (see below).

Versioning is semver over *consumer obligation*, not over file size:

| bump | when |
|---|---|
| MAJOR | a rule changed, or a fixture was removed or renamed — an unchanged consumer is now **wrong** |
| MINOR | a contract or case was added — an unchanged consumer is still correct but no longer complete |
| PATCH | comment, formatting or ordering only — no consumer has anything to do |

Do **not** re-cut the digests without bumping the version — you cannot.
`bundle.json`'s `version_digests` records a sha256 over each version's own
`version` + `digests` the first time that version is cut, and never rewrites
it, so a re-cut at an unchanged version is refused by `contracts.Verify` (Go)
and by `verifyBundle` (TypeScript). That was the one change a pinned consumer
could not see, which is precisely why it is the one the version exists to make
loud.

## How to cut a bump

1. Edit the fixture, and every implementation of the rule, in one commit
   (`contracts/README.md` — a one-sided edit is what these files exist to catch).
   Two files are exceptions: `tk-json-manifest.json` and `tracker-layout.json`
   are ticks' and are never edited here — move the pin in
   `contracts.pin.json` and run `go run ./cmd/contracts sync` instead.
2. Bump `version` in `contracts/bundle.json`.
3. Add the entry below.
4. Refresh `files` and `digests` in `contracts/bundle.json` to match the files
   on disk (sha256, lower-case hex), and record the new version's entry in
   `version_digests`.
5. Set `bundleVersion` in `cloudflare/contracts.pin.json` to the new version.

---

## 1.5.0

MINOR: `status-model.json` gains two goldens, `dashboard_degraded` and
`dashboard_stopped` (tick h7w, epic hn6 — A5/rule 8, "two renderers, they
cannot disagree"). The phone page (i1r) and the terminal watch are two
renderers of one model, and until now every golden answered "is it healthy"
with `healthy`, so the two states a person most needs to read the same way
— something is wrong but the run is going, and the run is not going — had
no fixture a cross-renderer test could render. The wording this pins is the
verdict's own: a degraded summary already carries its `degraded: ` prefix
(internal/statusmodel/verdict.go's degradedCause), so a renderer that
prefixes the state again reads "degraded: degraded: …", and a stopped run
whose probe said nothing has an EMPTY summary, which no renderer may spell
"stopped: ". Both goldens are the dashboard golden's own run with only the
health verdict (and, for the stopped one, the liveness answer, its waits and
its census) changed, so every derivation rule the agreement guard runs —
durations, elapsed, try outcomes, the CI cell — still holds at the golden's
own stamps. Consumers: no field changed shape, so an unchanged reader is
still correct — the cloudflare pin bumps in the same commit.

## 1.4.0

MINOR: `status-model.json` gains a per-tick `duplicate_of` (tick gmo, epic
hn6). The dashboard answers for the EPIC, and an epic is worked by many runs:
when a run promotes the same finding twice, the later promotion is closed as
the earlier one's duplicate (internal/reconcile/dupes.go's own note), and
that closure is a fact about the epic's shape no renderer could see — a tick
was work, and now it is the same work twice. The new field carries the tick
the work belongs to, read off the tracker's own note and closed_reason; null
for every tick that is its own work. With it comes a rule the schema cannot
pin and `checked_beyond_schema` now states: the progress counts count the
epic's REAL ticks — a duplicate is excluded from `total` and `closed` —
while its row still stands, dimmed, because rows never move. Every golden
carries the field at null; the dashboard golden is otherwise untouched.
Consumers: no existing field changed shape, so an unchanged reader is still
correct — the cloudflare pin bumps in the same commit.

## 1.3.0

MINOR: `status-model.json` gains a fourth golden, `status_model_refused_last_try`
(tick lkq). Every golden carried `next_step` null on every try — tick 378's
anchor stops at the reason on purpose (the dashboard golden's one refusal was
superseded by a live try, and no next step is derivable from it), so the field
the first-use bugs are about had zero fixture coverage: the phone page (A5),
z7w's properties and any future golden test had no document that shows the
field rendering, and c2u had to shape a next step in test code to pin the tick
view's "— next:" line. The new golden is a snapshot of epic yoh mid-waves where
tick 78v's last try was rejected by its worker, the work it committed was never
merged, and the run parked the tick for a person: its reason is the rejection
line's own detail, and its next step is the settle command the document's own
`run_held` line produces — the same sentence the model's attention entry carries
as the unblock command, so the field is derivable from the golden's own bytes
and not decorative. It also carries the shapes around it a renderer meets: the
superseded first try the records state and no evidence answers, a closed
neighbour whose gate backs its duration, the workers panel empty because the
attempt was torn down, the held-for-person wait and attention entry, and the
verdict still healthy because a hold that needs a person is attention, not
degradation. The dashboard golden and its anchor are untouched. Two bindings
join the fixture: `TestEveryGoldenAgreesWithThePipelineDerivation` runs its
rules over the new golden (placement included — a next step only on the last
try of a refusal), and a new anchor in `internal/statusmodel` pins the golden
exists, is admitted, round-trips, and keeps its refused last try populated and
derivable. No consumer has anything to do: the schema is unchanged, and a
reader that never heard of the golden stays correct.

## 1.2.4

PATCH: four values in `status-model.json`'s `dashboard` golden, corrected to
what the wave-2 derivations actually produce — the same fixture-drift class
1.2.3 removed from durations, elapsed, progress and gates, still live in the
try vocabulary the agreement guard did not read (tick lh4): 46x's superseded
try carried outcome `rejected` beside the gates array's own fail record
naming its attempt (`gate-46x-4-go`), where `tryOutcome` reads the evidence
before the census for a non-current try and answers `gate-failed`, and its
reason carried the refusal line's tail (`gofmt drifted in two files`) where
`decorateTries` copies the line's detail whole ("attempt 4 of 46x struck out:
gofmt drifted in two files"); 823's and v7z's tries carried outcomes
`gate-failed` and `reported` beside an evidence array that names no record
for either attempt and a census that stands none of them behind, where the
derivation answers `dispatched`. The schema, the rules and every other byte
are unchanged, and every corrected value remains schema-admitted. The
agreement guard now also reads the try outcomes and reasons: a try's outcome
is `tryOutcome`'s own branch run over the document's inputs (the checkpoint's
word, the gates array's evidence, the census — the array is the complete
evidence inventory, so the outcome is demanded of every try), and a refused
try's reason is the newest refusal line's detail, cut at 160 the way
`decorateTries` cuts it, demanded where the document's tail carries the line.
The phone page's own test follows the one glyph the correction moves: 823's
first try renders ○, the mark of a dispatch the records state and no evidence
answers, not the ✗ of a refusal. No consumer has anything to do.

---

## 1.2.3

PATCH: values in three `status-model.json` goldens, corrected to what the
wave-2 derivations actually produce — the same fixture-drift class 1.2.2
removed from two pipeline cells, still live in values the agreement guard
did not read (tick fq0): `status_model_running_wave`'s nwj and 6dh carried
`duration_seconds` null beside stamps that measure 2898 (nwj's try stamp to
its gate record's finish) and 4912 (6dh's, to generated_at, open), and 6dh
carried `elapsed_seconds` 4892 where generated_at minus its own dispatch is
4912 — the workers panel's 4892 the same drift; `status_model_completed_awaiting_merge`
counted 14 closed ticks and 3 waves beside a waves list that lists none of
them, where `buildWaves` counts progress in the same loop that lays the
waves out and answers an empty tracker with null waves and null counters —
so its `waves` list is now null, not `[]`, and its progress null; the
`dashboard` golden's v7z carried `elapsed_seconds` null beside a reported
state with a stamped current try (2400 to generated_at), and its gates array
backed only 46x of its closed ticks — gate records for 060 and 823 are now
in it, pass records whose finished stamps are the closes 060's 2940 and
823's 2070 measure; and the running wave's progress now counts the four
ticks its waves list carries. The schema, the rules and every other byte
are unchanged, and every corrected value remains schema-admitted. The
agreement guard now also reads the values: duration, elapsed (tick and
worker), progress (with the wave states it counts) and gates (sorted, and
backing the closed ticks' durations), each derivable from the document
alone. No consumer has anything to do.

---

## 1.2.2

PATCH: two values in `status-model.json`'s `status_model_running_wave` golden,
corrected to what the wave-2 pipeline derivation (tick 3gk) actually produces —
the same fixture-drift class 1.1.1 removed from the `dashboard` golden, still
live in the wave-1 golden (tick oro): nwj read state `closed` with a closed try
beside an all-pending pipeline cell, and 6dh read state `dispatched` beside the
same, where a dispatch marker alone makes claim done and work active. nwj's
cell is now all four stages `done` and 6dh's is claim `done`, work `active`,
gate and merged `pending`. The schema, the rules and every other byte are
unchanged, and both corrected values remain schema-admitted. The agreement
guard that 1.1.1's tick bound to the `dashboard` golden alone now reads every
golden the contract carries, with the state agreements the two contradictions
name (`TestEveryGoldenAgreesWithThePipelineDerivation`) — a golden is a
rendering fixture, and no golden the contract carries is outside the rule that
it states only values the derivation produces. No consumer has anything to do.

---

## 1.2.1

PATCH: the fold of tick 378's attempt into epic/hn6 — two parallel cuts
become one, again, for the same reason as 1.2.0. Tick 378 had branched from
the epic's 1.1.0 and corrected two values in `status-model.json`'s
`dashboard` golden at 1.1.1 (full text below); the epic folded main at
1.2.0 (full text below); and each ledger entry binds bytes the other cut
never saw, so the union cannot be re-cut at either — a version string must
never mean two different sets of bytes. Both bindings stay in
`version_digests`, and the union re-cuts here. It is the next PATCH over
1.2.0, not the next MINOR: the fold's half is already in 1.2.0, so all this
adds to it is the 1.1.1 corrections, and both corrected values remain
schema-admitted. A consumer pinned to 1.2.0 has nothing to do; a consumer
pinned to 1.1.1 adopts the fold's `worker-boot-contract.json` half by moving
here — the 1.2.0 entry says what that costs.

---

## 1.2.0

MINOR: the fold of main into epic/hn6 — two parallel cuts of 1.1.0 become one.
Main and the epic had each bumped 1.0.1 to 1.1.0 with DIFFERENT bytes: main's
cut gave `worker-boot-contract.json` `env.work_base` / `TICKS_WORK_BASE_SHA`
(#158, hn6 run_3f034e68 — full text under 1.1.0), and the epic's cut gave
`status-model.json` the dashboard vocabulary (hn6 wave 1, tick r5i — full text
under 1.1.0). Both halves are additive and unrelated, so the union is still
a MINOR bump — but it cannot be re-cut at 1.1.0: a version string must never
mean two different sets of bytes, and `version_digests` already binds 1.1.0,
once per side. The fold therefore re-cuts both halves here, at the next MINOR
version. An unchanged consumer is still correct but no longer complete —
twice over. The ledger holds one binding per version and never rewrites an
entry, so `version_digests["1.1.0"]` keeps the epic's cut, the entry the
fold's surviving line had already written
(db0bbc96b5753a17c58e18dd1d5b26b3c8de9b90c30e46c9bc836b023778a616); main's
own cut of the same version,
b3c50e1c412e3d8b903fb671f701b6dd5717d4225e6c8254e7c8c939988b3eab, is
recorded here instead. A consumer pinned to either 1.1.0 adopts both halves
by moving to this version.

---

## 1.1.1

PATCH: two values in `status-model.json`'s `dashboard` golden, corrected to
what the wave-2 pipeline derivation (tick 3gk) actually produces — the
fixture is a rendering fixture, not a Build output, and it had drifted into
illustration values no Build can emit (tick 378): the closeout tick's ci
stage read `active` beside the golden's own red `ci.state` (a red CI is that
stage's `failed`), and a superseded try carried a `next_step` while a later
try stood (a next step is stated on the last try of a refusal only). The
schema, the rules and every other byte are unchanged, and both corrected
values remain schema-admitted. No consumer has anything to do.

---

## 1.1.0

Cut twice, in parallel, from the same 1.0.1 bytes: main and epic/hn6 each
bumped the bundle to 1.1.0 without knowing of the other, and the fold above
re-cut the pair as 1.2.0. Both cuts were MINOR; both texts follow verbatim.

The epic's cut (hn6 wave 1, tick r5i):

MINOR: `status-model.json` grows the dashboard vocabulary — additive fields
within schema_version 1, which does not move (the phone page's snapshot parser
refuses any other version, and every reader of this contract lives in this
repository). Epic hn6, wave 1 (tick r5i): the per-tick pipeline cell
(`pipeline`, with the stage list per role documented in the contract and
spelled in Go as PipelineImplement/PipelineReview/PipelineCloseout),
`parent_tick_id`, `duration_seconds`, per-tick `findings` and `report`,
per-try `tier`/`reason`/`next_step`, per-worker `handle` and `activity`,
`health.verdict`, per-source `cost.lines` (usd MUST be null when metered is
false — pinned by an anyOf and refused by a new negative), and the top-level
`recent` and `epic_title`. One new golden, `dashboard`, carries
every new field populated — it is the fixture the wave-3 renderers and the
phone page test against — and three new negatives refuse an unknown pipeline
stage, an unmetered line with a number, and an unknown verdict state.
An unchanged consumer is still correct but no longer complete.

Main's cut (#158, hn6 run_3f034e68):

MINOR: one environment variable added. `worker-boot-contract.json` gains
`env.work_base`, `TICKS_WORK_BASE_SHA`: for a CARRIED attempt, the base the
carried work was cut from (epic hn6, run_3f034e68). A carried attempt boots at
the released attempt's head, so a worker that finds the carried work complete
and adds nothing had no work commits of its own and the container exited
no-work (10), settling a finished tick as failed. With the work base the
container measures the carried work too and exits 0. The control plane
(`worker-boot.ts`) sets it when the dispatch door's optional `work_base_sha`
carries one; the container (`image/worker.sh`) reads it. An unchanged consumer
is still correct: absent, the container behaves exactly as before.

---

## 1.0.1

PATCH: one description, no rule. `status-model.json`'s epic `phase`
description now says the merge is a person's *by default* and that a
repository may opt in to the run merging its own ready PR — the wording main's
#92 gave it. The merge of main into epic/6in carried that edit in without
re-cutting this bundle, so the digests no longer matched; this is the re-cut.
No consumer has anything to do.

---

## 1.0.0

MAJOR by ownership, MINOR by content: ticfac takes the bundle over.

Since ticks 7.0.0 (epic chz, "ticks becomes tracker-only"), ticks' bundle
carries only the two contracts that describe ticks' own formats:
`tk-json-manifest.json` and `tracker-layout.json`. Those two are vendored from
a pinned ticks release and re-vendored at the 7.0.0 bytes here. Everything else
in this bundle describes a **ticfac** format and is authored here from now on.
Consumers have three things to do:

- **Re-vendor the two ticks files** at the 7.0.0 bytes. `tk-json-manifest.json`
  changes words, not commands: the wave-width gate and its exit 8 left tk with
  the runner (epic chz), `dispatch.now` is uncapped by tk and the orchestrator
  caps it itself, `max_parallel` is always 0 and `free` always -1, and the
  `sandbox` command is gone rather than merely unpublishable. `tracker-layout.json`
  changes readers' names and where the worker boot banner is pinned (it moved
  to `worker-boot-contract.json`, which ticks no longer ships — ticfac owns it).
- **`status-model.json` joins the bundle**, moved from
  `internal/statusmodel/contract.json`: the 6dh status model fixture gained a
  second reader (i1r, the factory's phone status page), which is the condition
  the fixture was waiting for. The Go readers moved with it
  (`internal/contracts/parity/status_model_test.go`); the TypeScript reader is
  new (`cloudflare/test/status-model.test.ts`).
- **`runners-config-contract.json` gains the `[findings]` table** (the
  `[findings.route."owner/name"]` allowlist from ticfac #85), which had been
  covered only by ticfac's own config tests because the file was still vendored
  from a ticks commit that predated it. With ticfac authoring the contract, the
  upstream check for it is gone and the table is pinned with the rest.

The other twelve fixtures carry across at their ticks 6.3.0 bytes, unchanged.

## 6.3.0 and earlier

Cut by ticks, in the ticks repository. The bytes are the ones ticfac took
over; the history is ticks' to tell.
