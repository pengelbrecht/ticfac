package subprocess

import (
	"fmt"
	"os"
	"strings"
)

// The signal-death relaunch (tick 3c2, epic 43y): a DURABLE runner killed by
// a signal — an operator's kill, an OOM — is relaunched in-attempt, bounded,
// before anything settles.
//
// Before this, a killed runner settled its attempt failed whatever the kill
// was: nudgeDue refuses every non-zero exit ("the runner failed"), and a
// death by signal is the -1 Go reports for one. The attempt's durable
// storage — the conversation runLocalWorker's harness.resume() is built to
// continue from, the same requestId making the replay idempotent ("the same
// argv replayed after a crash must find the same one again",
// harness/src/local/worker-host.ts) — was abandoned for it, and the run paid
// a full redispatch with a fresh conversation. The cloud rung already had the
// recovery: the WorkerAgent host's own storage resumes a conversation whose
// process was lost mid-tool. This is the local rung's.
//
// The bound is the ATTEMPT's, like the nudge's: a runner that keeps dying by
// signals is not one more relaunches will save, and the wall clock remains
// the outer bound either way.

// MaxRelaunches bounds how often one attempt replays a runner process that
// died by a signal. The nudge and the relaunch are separate spends for the
// same reason the nudge and the pushback are: a worker that died by a kill
// and then ended its turn early is owed both.
const MaxRelaunches = 2

// relaunchDetailPrefix opens the observation a relaunch is recorded as. A
// relaunch is a `started` observation — a runner process did start — the
// same shape a nudge takes, so a reader of the feed reads one vocabulary.
const relaunchDetailPrefix = "relaunched: "

// IsRelaunch reports whether an observation records a signal-death relaunch,
// for a reader (the run's feed) that surfaces them.
func IsRelaunch(o Observation) bool {
	return o.Kind == ObsStarted && strings.HasPrefix(o.Detail, relaunchDetailPrefix)
}

// RelaunchDetail is the observation a relaunch is recorded as: relaunch n of
// MaxRelaunches, the death, and the recovery — the same sentence every
// executor-side relaunch takes, so the feed reads one shape.
func RelaunchDetail(n int, runner, death string) string {
	return fmt.Sprintf("%srelaunch %d of %d: the %s runner was killed mid-run by %s, and the same argv is "+
		"replayed so the relaunched process resumes its own conversation from the attempt storage — the killed "+
		"turn continues, mid-tool, rather than the attempt paying a fresh one",
		relaunchDetailPrefix, n, MaxRelaunches, runner, death)
}

// signalDeath reports whether an exit code is a death by signal: Go reports a
// process terminated by a signal as -1, and every exit a runner chose itself —
// 0, a usage error, a container's no-work exit — as 0 or more.
func signalDeath(code int) bool { return code < 0 }

// relaunchDue is the supervisor's decision after a runner process died:
// replay the same argv, or leave the exit to the ordinary ladders. Every
// condition is either a reason the kill was DELIBERATE — the wall clock and
// the stuck watch leave their own durable marks, a cancel its record, and
// the stuck re-prompt its stuckRestart flag, checked where it lives, in the
// supervisor loop — or a bound already spent, or a runner whose conversation
// does not outlive its process at all, for which a replay is a whole fresh
// run rather than a resume and the redispatch ladder is the honest recovery.
func relaunchDue(st *store, record *attemptRecord, code, relaunched int) (bool, string) {
	switch {
	case !signalDeath(code):
		return false, fmt.Sprintf("the runner exited with %d by itself, not by a signal", code)
	case record.SteerSock == "":
		return false, "the runner's conversation does not survive its process"
	case relaunched >= MaxRelaunches:
		return false, "the relaunches are spent"
	case st.wallClockExceeded():
		return false, "the wall clock stopped it"
	case st.stuckStopped():
		return false, "the supervisor stopped it as stuck"
	}
	if _, cancelled := st.cancelled(); cancelled {
		return false, "the attempt is cancelled"
	}
	if _, err := os.Stat(record.Worktree); err != nil {
		return false, "the worktree is gone"
	}
	return true, ""
}

// deathWord names how the runner's last process died, for the observation
// and log line a relaunch is recorded as. A death by signal is the only exit
// that reaches a relaunch, and the name is evidence the exit code cannot
// carry; a life whose signal could not be read still died by one.
func deathWord(life *runnerLife) string {
	if life != nil {
		if sig, ok := life.deathSignal(); ok {
			return fmt.Sprintf("%s (signal %d)", sig, int(sig))
		}
	}
	return "a signal"
}
