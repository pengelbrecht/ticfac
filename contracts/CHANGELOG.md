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

## 1.2.0

MINOR: six fields added. `worker-boot-contract.json` gains `boot_arg`,
`boot_command`, `boot_marker`, `boot_prompt_begin`, `boot_prompt_end`,
`finish_arg` and `finish_command` — the pi-durable worker host's half of the
per-tick worker contract (epic 43y, tick pom; docs/spikes/
n0b-round2-pi-durable.md, "The worker contract on pi-durable"). The host
runs `--boot` as its environment's first command, submits the prompt it
prints between the prompt markers to a durable conversation, and runs
`--finish` with the conversation's outcome once it settles; the boot's faults
keep the all-in-one's exit classes (2-8, 13-15) and the finish decides the
same 9/10/11 from the same git facts. An unchanged consumer is still
correct: the all-in-one default runs exactly as before, and the new args
are additive. All three readers follow in the same change — `image/worker.sh`
answers the args and prints the markers, `internal/sandboximage`
(`WorkerBootArg`, `WorkerFinishArg` and the markers) and
`cloudflare/src/worker-boot.ts` (`WORKER_BOOT_ARG`, `WORKER_FINISH_ARG` and
the markers) assert them.

---

## 1.1.0

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
