<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/bo9/attempt-5-resolve-5-3e6ded19/u2g`, base `76383692f61376d37dcabe40720212a155c5b2dc`, harness `claude` exited 0, 2 work commit(s), 0 uncommitted path(s)._

# u2g — resolve the conflicted merge of attempt 5 into epic/bo9

## The two intents

- **u2g** (my tick, the side being merged in, `95e1c4ae`): `ticfac skills get
  --full` prints the whole embedded bundle — SKILL.md, then every
  `references/` file, each under a header naming its path — so an agent can
  read `references/commands.md`, `holds.md` and `cloud.md` out of the binary
  without installing the skill to disk.
- **The epic side** (`epic/bo9` at the merge base of this job): tick **g5f**
  added `ticfac skills diff`, and the same branch carries tick **rkk**'s
  change that makes the skill *name* optional on `skills get` and `skills
  install` — the binary embeds one skill, so `ticfac skills get` with no
  name acts on `skills.DefaultSkill`.

Both intents are about the same command literal and the same `RunE` body, and
they are complementary: an optional name and a `--full` mode are independent
axes. The resolution is their union — `skills get [name] [--full]`.

## What conflicted, and how I resolved it

One file: `internal/cli/skills.go`, two hunks, both inside
`newSkillsGetCommand`. Everything else merged cleanly, including
`internal/cli/skills_test.go`, which means **both sides' tests are in the
tree** and the resolution has to satisfy both at once.

**Hunk 1 — the `cobra.Command` literal.** Union, line by line:

- `Use` is the epic side's `"get [name]"` (optional name, rkk). u2g's
  `"get <name>"` would have made the name required again and regressed rkk.
- `Short` is u2g's `"print a skill's SKILL.md, or the whole bundle with
  --full"` — the epic side's Short says strictly less, and `--full` has to be
  discoverable from `skills --help`.
- `Long` keeps **both** paragraphs: the epic side's "with no name, print the
  %s skill" (so the literal stays the `fmt.Sprintf` form that interpolates
  `skills.DefaultSkill`) followed by u2g's `--full` paragraph.

**Hunk 2 — the body.** The optional-name resolution (`len(args) > 1` usage
error, `name := skills.DefaultSkill`, the empty-string check) sits *above* the
conflict and merged cleanly, so the union is:

1. that name resolution runs first, unchanged;
2. then u2g's `if !*full { … }` branch — `skills.Read(name, "SKILL.md")`, the
   single-file `--json` document, the bare text, return;
3. then u2g's `skills.Paths(name)` and the per-file loop (already present
   below the conflict, unconflicted) for the `--full` path.

I dropped u2g's `name := args[0]`. It was not a choice between the sides: with
the optional-name block above it, that line both shadowed the resolved `name`
and would not compile as a second `:=` on the same variable — and it is
exactly what made `--full` require a name. Dropping it is what makes
`ticfac skills get --full` with **no** name work on the embedded skill, which
is the union both intents imply and neither side wrote.

No conflict was irreconcilable; nothing was dropped on either side's intent.

## Verification

- `grep -rln '^<<<<<<<\|^=======$\|^>>>>>>>'` over the worktree (excluding
  `.git`): **no markers anywhere**.
- `go build ./...`, `gofmt -l internal/cli/`, `go vet ./internal/cli/`: clean.
- `go test -run 'TestSkills' ./internal/cli/`: **23 tests pass, 0 fail** —
  including both sides' guards, which is the real check on the union:
  - u2g's `TestSkillsGetFullPrintsEveryFile`,
    `TestSkillsGetWithoutFullPrintsOnlySkillMD`,
    `TestSkillsGetFullJSONReportsEveryFile`,
    `TestSkillsGetFullUnknownSkill`;
  - rkk's `TestSkillsGetWithNoNamePrintsTheEmbeddedSkill`;
  - g5f's `skills diff` tests (untouched by the resolution).
- `make gate` (gofmt + `go vet ./...` + the short suite, whole repo): **green**,
  every package `ok`, run at low priority (`GOTEST_PARALLEL=4 GOFLAGS=-p=2
  nice -n 19`) as AGENTS.md requires on this shared host. `internal/reconcile`
  passed in the gate's short pass (17.6s); I did not run its full suite
  locally — this change does not touch that package, and per AGENTS.md its
  full suite is CI's job.

## Commit

`b855b53` on `tick/bo9/attempt-5-resolve-5-3e6ded19/u2g`, source only: one
file, `internal/cli/skills.go`. Nothing under `runs/`, `.ticfac/runs/`, no
build output or caches. Working tree clean.

## Finding: the wave partition, not the text, was wrong

u2g and the epic-side work on `skills get` were scheduled such that two
intents landed on the same ~20 lines of one function — the command literal and
`RunE` of `newSkillsGetCommand`. This is a planning defect, not a modelling
problem: `skills get`'s optional-name change (rkk) and its `--full` mode (u2g)
are both small, both obviously touch that one function, and a wave partition
that put them in sequence rather than in parallel would have produced exactly
the tree I hand back, with no resolve job. Worth noting for epic bo9's
close-out: the per-command granularity of `internal/cli/skills.go` is too
coarse for one file to host two same-wave ticks.

STATUS: DONE
