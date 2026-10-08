<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/bo9/attempt-5/u2g`, base `6730309a20094c1ef5d08aace98a782bab82a2a7`, harness `claude` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# u2g: ticfac skills get --full prints the skill's references too

## What changed

`ticfac skills get <name> --full` now prints SKILL.md followed by every
other file in the skill (its `references/` files), each preceded by a
`--- <path> ---` separator line naming it — the same shape `tk skills get
--full` uses, verified by running it against the real `tk` binary on this
host. Without `--full`, `skills get` is unchanged: SKILL.md alone.

`--full --json` emits `ticfac.skills-get.v1` with a `files` array of
`{path, content}` entries (SKILL.md first, then references/ in
`skills.Paths`'s sorted order) instead of the plain `file`/`content` pair
the non-`--full` document carries.

Files: `internal/cli/skills.go` (`newSkillsGetCommand`), new tests in
`internal/cli/skills_test.go`.

## What I ran

- `go build ./...`
- `go test ./internal/cli/ -run 'TestSkillsGet' -v` — the four new tests
  (full text output, order, headers; unchanged non-`--full` output; `--full
  --json`'s `files` array; unknown skill still exits `exitNotFound`), all
  pass.
- `go test ./internal/cli/... -timeout 5m` (GOTEST_PARALLEL=4, GOFLAGS=-p=2)
  — full package, pass.
- `make gate` (GOTEST_PARALLEL=4, GOFLAGS=-p=2) — gofmt clean, `go vet
  ./...` clean, whole-repo short suite green including
  `internal/reconcile` under `-short`.
- Manually diffed `go run ./cmd/ticfac skills get ticfac --full` against
  `tk skills get ticks --full` to confirm the separator format and file
  order match.

## Next

bo9's other items (ba4, g5f, 0ek) are untouched by this change — this tick
only touched `skills get`. Nothing else outstanding for u2g.

```findings v2
[]
```

STATUS: DONE
