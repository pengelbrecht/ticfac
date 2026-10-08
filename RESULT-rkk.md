<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/bo9/attempt-4/rkk`, base `7333bc9e8e4effc5dd9d2a867275e1d36c58afb3`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

> **BOUNDARY VIOLATION ATTEMPTED.** This agent tried to write tracker state, which the
> orchestrator owns. The container refused it, so nothing under `.tick/`
> should have reached this branch — but the attempt is reported rather than
> silently cleaned, because a model that ignored an explicit instruction is
> something a human has to see. What it did:
>
> - the agent ran `tk --json show rkk`

# rkk: ticfac skills install/get default to the ticfac skill when no name is given

## What changed

- `internal/skills/skills.go`: added `DefaultSkill = "ticfac"`, the name
  `skills install`/`skills get` fall back to when no name is given.
- `internal/cli/skills.go`:
  - `skills get [name]` — with zero args, reads `skills.DefaultSkill`;
    with one, uses it as before (empty string still refused); more than
    one is a new usage error ("at most one skill name is accepted").
  - `skills install [name]` — same shape: zero args installs the
    embedded skill, one names it explicitly, more than one is a usage
    error. `cmd.Args` enforces the argument count (was "exactly one").
  - `Use` strings (`get [name]`, `install [name]`) and the `Long` help
    text on both commands, and on the `skills` group itself, now state
    the short form.
- `skills/ticfac/SKILL.md` and `skills/ticfac/references/commands.md`:
  updated to teach `ticfac skills install` / `ticfac skills get` as the
  short form, keeping `ticfac skills install ticfac` stated explicitly
  once (the embedded skill's own boundary test greps for that exact
  phrase in `SKILL.md`).
- `README.md`: the install walkthrough and the command table now show
  the short form.
- `embedded.go`: updated the doc comment on `SkillsFS` to the short form.
- `internal/cli/skills_test.go`: new tests —
  `TestSkillsInstallWithNoNameInstallsTheEmbeddedSkill`,
  `TestSkillsInstallTooManyArgs`,
  `TestSkillsGetWithNoNamePrintsTheEmbeddedSkill` (asserts the no-name
  and `ticfac` outputs are byte-identical),
  `TestSkillsGetUnknownSkill`, `TestSkillsGetTooManyArgs`. Existing
  tests (install refusals, the JSON report, the upgrade path, the named
  form) were left as is — they still cover the explicit-name path
  unchanged.

The named form keeps working exactly as before, including the
not-found exit code (4) for a name the binary does not embed; only the
argument *count* check changed (0 or 1 accepted, was exactly 1).

## What I ran

- `go build ./...` — clean.
- `go test ./internal/cli/... -run 'TestSkills' -v` — all pass,
  including the five new tests.
- `go test ./internal/cli/...` (full package) — pass, 40s.
- `GOTEST_PARALLEL=4 GOFLAGS=-p=2 make gate` — green: gofmt clean, go
  vet clean, `go test -short` green across every package (reconcile
  included, 8.5s under `-short`).
- Manual smoke test: `go run ./cmd/ticfac skills install --dir <tmp>`
  with no name installed the ticfac skill's tree; `go run ./cmd/ticfac
  skills get` with no name printed the same SKILL.md `skills get
  ticfac` does.

## Next tick

Nothing outstanding from this one. No findings.

```findings v2
[]
```

STATUS: DONE
