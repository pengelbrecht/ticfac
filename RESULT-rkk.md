<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/bo9/attempt-4-resolve-4-6a3da50a/rkk`, base `d9409399e30470105af979071a99510b1d6f6174`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# resolve-conflict — rkk × g5f (epic bo9)

Branch: `tick/bo9/attempt-4-resolve-4-6a3da50a/rkk`
Resolution commit: `301a379` — *Resolve rkk × g5f in the skills surface: optional name and skills diff*

## The two intents

- **rkk (mine, incoming side `4b8b263`)** — `ticfac skills install` and
  `ticfac skills get` default to the one embedded skill when no name is given;
  the named form keeps working, an unknown name is still exit 4; help, SKILL.md
  and README show the short form.
- **g5f (other side, already in `epic/bo9` as `868d88b`)** — the new
  `ticfac skills diff <name>`: report an installed copy that has drifted from
  the binary (missing, unstamped, older version, changed files) and name
  `ticfac skills install <name>` as the fix.

The two are **complementary, not competing**: rkk changes how a name is
resolved, g5f adds a subcommand. Every conflict was textual — both ticks
rewrote the same four prose blocks and appended to the same test file. The
resolution is the union in all four files; nothing was dropped and no side was
chosen over the other.

## What each conflicted file now holds

| File | Resolution |
| --- | --- |
| `internal/cli/skills.go` | The `skills` group's `Long`: rkk's "install with no name" wording **followed by** g5f's `skills diff` paragraph. The code below the conflict (`get [name]`, `install [name]`, `skills.DefaultSkill`, `AddCommand(newSkillsDiffCommand(...))`) had already merged cleanly and carries both ticks' behaviour. |
| `internal/cli/skills_test.go` | Pure append-vs-append: g5f's five `TestSkillsDiff*` tests **and** rkk's three `TestSkillsGet*` tests, both blocks kept, markers removed. No test was rewritten. |
| `skills/ticfac/SKILL.md` | One paragraph merged line by line: rkk's short-form install sentence plus g5f's `ticfac skills diff ticfac` drift sentence. Keeps the literal `ticfac skills install ticfac`, which `TestTheEmbeddedSkillTeachesTheLoopAndStatesItsBoundary` asserts. |
| `skills/ticfac/references/commands.md` | The bullet is `skills list|get|install|diff` (g5f's command set) with rkk's optional-name wording and `skills get`, then g5f's diff/drift sentences. |

### One deliberate non-union

`skills diff` still **requires** a name (`diff <name>`, exactly one arg).
rkk's description scopes the optional name to `install` and `get`, so making
`diff` default too would be new work, not this merge's union. This is the one
judgment call in the resolution worth a second pair of eyes.

## Verification

- No `<<<<<<<`/`=======`/`>>>>>>>` anywhere in the worktree (`grep -rln`, excluding `.git`).
- `go build ./...` clean; `gofmt -l internal/cli` clean.
- `go test -run 'TestSkills|TestTheEmbeddedSkill' ./internal/cli/ ./internal/skills/` — ok.
- `make gate` (gofmt + `go vet ./...` + whole-repo short suite, run at low
  priority with `GOTEST_PARALLEL=4 GOFLAGS=-p=2`) — **passed**, no FAIL lines,
  including `internal/shorttest` and `internal/skills`.
- Commit is source only: four files, no build output, nothing under `runs/`.

## Findings

The wave collision itself, plus two doc gaps that were already on `epic/bo9`
outside every conflict hunk — flagged, not silently fixed, because they are
another tick's to close.

```findings v2
[
  {
    "kind": "proposal",
    "title": "rkk and g5f were dispatched in one wave over one CLI surface",
    "severity": "low",
    "body": "Both ticks own internal/cli/skills.go, internal/cli/skills_test.go, skills/ticfac/SKILL.md and skills/ticfac/references/commands.md: rkk rewrites the install/get prose, g5f adds a sibling subcommand and documents it in the same four blocks. A conflict was certain before either worker started, and the union committed here is what a sequenced partition would have produced on its own. The defect is the wave, not the text — two ticks over one command group belong in different waves, or in one tick.",
    "evidence": "internal/cli/skills.go:43 — the conflict hunk both ticks rewrote"
  },
  {
    "kind": "defect",
    "title": "README still lists the skills group as list|get|install, without diff",
    "severity": "low",
    "body": "g5f added `ticfac skills diff` but never added it to the README command table, so README.md and skills/ticfac/references/commands.md now disagree about the command set. Outside every conflict hunk, so this resolution left it alone.",
    "evidence": "README.md:124 — `ticfac skills list\\|get\\|install`"
  },
  {
    "kind": "defect",
    "title": "commands.md still says --config cannot be used with --cloud",
    "severity": "low",
    "body": "ba4 (closed, merged into epic/bo9 as 3a69386) made `ticfac run <epic> --cloud --config <name>` work, but the skill's reference still documents --config as refused with --cloud. The skill text teaches an agent the opposite of what the binary now does. Outside every conflict hunk, so this resolution left it alone.",
    "evidence": "skills/ticfac/references/commands.md:32 — \"`--config <name>` (not with `--cloud`)\""
  }
]
```

STATUS: DONE_WITH_CONCERNS — confirm `skills diff` was meant to keep requiring a name while `install`/`get` now default to the embedded skill; rkk's description names only those two.
