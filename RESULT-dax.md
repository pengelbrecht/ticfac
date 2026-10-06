<!-- ticks-worker: container facts, prepended after the harness exited. The
agent's report, including its STATUS line, is unchanged below. -->

_ticks-worker: branch `tick/umq/attempt-1-repair-1-55821ea9/dax`, base `ce2a5541071740f6136a4b70221842df27f52318`, harness `pi-durable` exited 0, 1 work commit(s), 0 uncommitted path(s)._

# Repair report — tick dax, integrated gate `gate-dax-1-go`

Branch: `tick/umq/attempt-1-repair-1-55821ea9/dax` — repair commit `4f742e41d`
("Liveness in exec/subprocess: existence is not life (tick 8ct)"), one new
commit on top of the integration head; no amends, no rebases. Source only.

## The failing check

Evidence `.ticfac/runs/run_7445005ff17447c3ae351f5ba99ea01d/evidence/gate-dax-1-go.json`
(result **fail**, exit 1) is the Go half of the gate:

    gofmt -l . | grep -v '^contracts/' | (! grep .) && go vet ./... \
        && go test -short -timeout 45m -parallel 12 ./...

red on three tests in `internal/exec/subprocess` (all process-group behavior
tests):

- `TestInterruptingAHungToolKillsItsGroupAndSaresTheRunner` —
  `interrupt_test.go:144: the tool's own child 80056 outlived the interrupt`
- `TestAStoppedSupervisorInterruptsTheRunnersDetachedTools` —
  `stop_detached_test.go:99: the detached tool's group 83127 survived the supervisor's stop …`
- `TestACancelInterruptsTheRunnersDetachedTools` —
  `stop_detached_test.go:126: the detached tool's group 83207 survived the cancel …`

The TS half (`gate-dax-1-ts.json`) passed, exit 0. The merged work — the merge
is `b50e45a26` ("Merge branch 'ticfac/run-…/tick-dax/attempt-1' into
epic/umq") — is the 0.x cut-over the tick `.tick/issues/dax.json` describes,
and it never touches these tests; its deletions are all intact in the tree
(`internal/factory/rollout_held.go` gone, `rollout_active_grace_period` named
only by a ban-list test, the rollout waits gone from `deploy-factory.yml`,
the `max_instances` mirror checks gone) and I changed none of it. The same
three tests were red at base too (finding `94a4781f…`, promoted to backlog
tick 8ct, "Subprocess interrupt tests fail at base in Linux containers").

## Root cause — diagnosed by experiment, not by reading the message

The process-group kills **work**. This container's PID 1 is
`sleep infinity` (the sandbox control server), never an init: it reaps
nobody, so every member a group kill ends reparents to PID 1 and stays a
ZOMBIE forever. A zombie answers signal 0, and both liveness oracles the
package used — the test helper `gone` (`kill(pid,0)==ESRCH`) and production
`groupAlive` (`kill(-pgid,0)`) — read the corpses as live. Reproduced
directly in this container: after `kill -9 -<pgid>`, `kill -0 -<pgid>` still
answers alive while `ps` shows the members `Z`, `PPID 1`. The pids the tests
printed in the evidence (e.g. 9597) were `State: Z, PPid: 1, PGid: their own
tool group` — dead, unreaped, and misread. The repository had already met and
fixed this exact disease in `internal/reconcile` (tick 58z, "Existence is
therefore not life"); this package never got the answer.

## The repair — the tree, not the gate

The gate's command is untouched. What changed is the package's notion of
liveness, brought in line with the repo's own established fix:

- `internal/exec/subprocess/zombie_linux.go` (new): `processZombie`,
  `groupZombie`, `procState`, `deadByProcState`, `parseProcStatState` —
  /proc answers whether a pid, or a group's members, are still RUNNING
  rather than merely still in the table. Answering dead for a corpse is the
  one dead answer with no pid-reuse risk (tick rmc): a zombie's number is
  still taken by the corpse itself, so no stranger can hold it yet.
- `internal/exec/subprocess/zombie_other.go` (new): the no-`/proc`
  platforms (macOS and the rest) — their PID 1 reaps in milliseconds, so
  signal 0 alone separates living from dead there; behavior unchanged.
- `internal/exec/subprocess/process_unix.go`: `processAlive` and
  `groupAlive` ask signal 0 for the existence question and the zombie answer
  for the life question. Production also improves: `killUntilGone` no longer
  burns its whole persistence window re-signalling groups it has already
  killed (the two stop tests dropped from ~2.3s to ~0.3s).
- `internal/exec/subprocess/interrupt_test.go`: `gone` asks `processAlive`
  inverted — the same liveness question the rest of the package asks —
  instead of existence alone.
- `internal/exec/subprocess/zombie_linux_test.go` (new): four regression
  tests that reproduce the container's condition deterministically on any
  Linux host (a killed child nobody reaps is not alive; a group of corpses
  with its number still taken is not alive; a live member keeps its group
  alive though its leader is dead; the /proc state-letter table), each with
  its `short:` line as `internal/shorttest` requires.

## Verification, run over the repaired tree

- The gate's exact command over the committed tree: **exit 0** — gofmt clean
  (`gofmt -l .` with the contracts exclusion), `go vet ./...` clean, and
  `go test -short -timeout 45m -parallel 12 ./...` green, 46 packages `ok`,
  no FAIL lines (including `internal/exec/subprocess` and
  `internal/reconcile`).
- The three originally failing tests pass, re-run with `-count=3` for
  stability, alongside the new regression tests.
- Cross-platform check: the package builds for `GOOS=darwin`, `freebsd` and
  `windows`, exercising both the `zombie_other.go` stub path and
  `process_other.go`.
- The gate's own configuration was not edited, and nothing was weakened: the
  assertions the three tests make are the ones they always made — the tool
  groups really are dead when they now read as dead.

Nothing outside source was committed: no build output, no caches, nothing
under the run's `runs/` artifact prefix. The tick records were left to the
maintainers' bookkeeping; the repair is named for 8ct in the code it closes.

## Findings

Nothing new found outside the repair itself: the one defect behind this
failure is the one the tick's own attempt already reported and the run
already triaged — finding `94a4781f…`, promoted to backlog tick 8ct — and
this repair closes it rather than re-reporting it.

```findings v2
[]
```

STATUS: DONE_WITH_CONCERNS — the gate is green over commit 4f742e41d (zombie-liveness fix in internal/exec/subprocess, mirroring tick 58z); worth a double-check: processAlive/groupAlive are production answers also used by the legacy pre-locks paths (cancel.go, inspect.go, leftovers.go), where a zombie now reads dead instead of alive — reasoned safe (a zombie's number is still its own, so no pid-reuse hazard) and gate-verified, but a maintainer may want to glance at those paths; and backlog tick 8ct still reads open in the tracker though this repair closes what it describes.
