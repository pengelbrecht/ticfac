package subprocess

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// The supervisor: the process that owns one runner, pushes its work on a
// timer, enforces the wall clock and records that it settled.
//
// It is a child of `start` and it OUTLIVES it. That is the point — `start`
// returns a handle immediately, and everything after that is durable state
// this process maintains, so a controller that was killed mid-job comes back
// to a job that is still running rather than to a hole.
//
// It is Go rather than a generated shell script for one reason: Appendix A #5
// says durability is a TIMER, and a timer implemented twice — once for the
// real host and once for the test — is two timers that can disagree. `pusher`
// below is the one implementation, driven by a real clock in the supervisor
// and by the fixture's clock in the invariants suite.

// DefaultPushInterval is how often in-progress work reaches origin.
//
// contracts/lifecycle-invariants.json pins push_interval_ms at 60000 and
// requires it to stay UNDER the reconciler's poll cadence: a job's work must
// reach origin more often than the reconciler looks, or a reconcile settles an
// attempt from a durable layer that is older than the answer it is about.
const DefaultPushInterval = 60 * time.Second

// pusher is the durability timer. Its whole job is to make "is a push due?" a
// question with ONE answer, asked from a clock rather than from the runner's
// good intentions.
type pusher struct {
	interval time.Duration
	last     time.Time
	now      func() time.Time

	// enabled is Appendix A #5's guard. With it off, nothing reaches origin
	// until the job pushes at exit — which is exactly what a killed job never
	// does, and is why the guard has a negative control.
	enabled bool

	push func() error
}

func (p *pusher) due() bool {
	if !p.enabled {
		return false
	}
	return p.now().Sub(p.last) >= p.interval
}

// maybePush returns what happened, in the vocabulary the lifecycle harness
// uses: `pushed`, `not_due`, or `refused_revoked` when the credential this
// push would spend is already gone.
func (p *pusher) maybePush(credentialLive bool) string {
	if !p.due() {
		return "not_due"
	}
	if !credentialLive {
		return "refused_revoked"
	}
	if err := p.push(); err != nil {
		return "push_failed"
	}
	p.last = p.now()
	return "pushed"
}

// Supervise runs one attempt to settlement. It is the body of the hidden
// `supervise` subcommand and never returns before the runner has.
func Supervise(stateDir string) error {
	st := newStore(stateDir)
	record, err := st.readAttempt()
	if err != nil {
		return fmt.Errorf("supervise %s: %w", stateDir, err)
	}

	log, err := os.OpenFile(st.path(fileRunnerLog), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer log.Close()

	now := func() string { return time.Now().UTC().Format(time.RFC3339) }
	note := func(format string, args ...any) {
		fmt.Fprintf(log, "[ticfac %s] %s\n", now(), fmt.Sprintf(format, args...))
	}

	if len(record.RunnerArgv) == 0 {
		return fmt.Errorf("supervise %s: the attempt record names no runner argv", stateDir)
	}

	// A signal aimed at the supervisor takes the runner with it. Cancel kills
	// both groups itself; this is for every other way a supervisor is asked to
	// stop, because a runner whose supervisor is gone is a process nothing is
	// left to collect.
	//
	// It is registered BEFORE the runner exists, not after it is recorded: the
	// window between those two points is one where a TERM would kill this
	// process by default, and the whole point of handling it is that a stopped
	// supervisor settles its attempt rather than leaving one nobody can. The
	// channel buffers one, so a signal that arrives during startup is handled
	// by the loop below rather than lost.
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGTERM, syscall.SIGINT)

	// The SOURCE GRADE is applied here, at the one moment the runner process
	// does not exist yet. For a read-only grade the environment it is about to
	// inherit is stripped of every source credential and its git config is
	// pinned so that no push resolves to a remote — see grade.go. Doing it at
	// launch rather than in the runner's instructions is the difference
	// between a boundary the issuer keeps and one the model is asked to.
	box := sandboxFor(os.Environ(), record, readRemotes(record.Worktree))

	runner := exec.Command(record.RunnerArgv[0], record.RunnerArgv[1:]...)
	runner.Dir = record.Worktree
	runner.Env = append(box.Env, record.RunnerEnv...)
	runner.Stdout = log
	runner.Stderr = log
	runner.SysProcAttr = newProcessGroup()

	// observe is the inspect cursor: a lost observation is a fact a second
	// process can never read back, so a failed append is written to the runner
	// log — the one durable place left — rather than dropped.
	observe := func(kind, detail string) {
		if err := st.observe(Observation{At: now(), Kind: kind, Detail: detail}); err != nil {
			note("the %s observation could not be appended (%v); it is only here: %s", kind, err, detail)
		}
	}

	if err := runner.Start(); err != nil {
		note("the runner could not be started: %v", err)
		observe(ObsExited, "the runner could not be started: "+err.Error())
		_ = atomicWrite(st.path(fileRunnerExit), []byte("127\n"), 0o644)
		return err
	}
	runnerPID := runner.Process.Pid
	if err := atomicWrite(st.path(fileRunnerPID), []byte(strconv.Itoa(runnerPID)+"\n"), 0o644); err != nil {
		note("the runner pid file could not be written (%v); cancel reaches this runner through the "+
			"observation log's pid instead", err)
	}
	observe(ObsStarted, fmt.Sprintf("%s runner, pid %d, worktree %s", record.Runner, runnerPID, record.Worktree))
	observe(ObsCredentialIssued, box.note(record))
	note("started %s (pid %d) on %s", record.Runner, runnerPID, record.Branch)

	push := &pusher{
		interval: time.Duration(record.PushInterval) * time.Second,
		last:     time.Now(),
		now:      time.Now,
		enabled:  record.PushOnTimer && record.PushInterval > 0 && record.canPush(),
		push:     func() error { return pushBranch(record.Worktree, record.Remote, record.Branch) },
	}
	if push.interval <= 0 {
		push.interval = DefaultPushInterval
	}

	waited := make(chan int, 1)
	go func() {
		err := runner.Wait()
		code := 0
		if err != nil {
			code = 1
			var exitErr *exec.ExitError
			if ok := asExitError(err, &exitErr); ok {
				code = exitErr.ExitCode()
			}
		}
		waited <- code
	}()

	ticker := time.NewTicker(pushTick(push.interval))
	defer ticker.Stop()

	var wall <-chan time.Time
	if record.WallSeconds > 0 {
		timer := time.NewTimer(time.Duration(record.WallSeconds) * time.Second)
		defer timer.Stop()
		wall = timer.C
	}

	code := 0
	for done := false; !done; {
		select {
		case code = <-waited:
			done = true

		case <-ticker.C:
			switch outcome := push.maybePush(st.credentialLive()); outcome {
			case "pushed":
				_ = atomicWrite(st.path(fileLastPush), []byte(now()+"\n"), 0o644)
				observe(ObsHeartbeat, "pushed "+record.Branch+" to "+record.Remote)
			case "refused_revoked":
				note("the credential is revoked; nothing is pushed")
			case "push_failed":
				note("the timed push of %s failed; the next tick tries again", record.Branch)
			}

		case <-wall:
			note("wall clock of %ds exceeded; stopping the runner", record.WallSeconds)
			_ = atomicWrite(st.path(fileWallExceeded), []byte(now()+"\n"), 0o644)
			stopTree(runnerPID)

		case sig := <-stopping:
			note("supervisor received %s; stopping the runner", sig)
			stopTree(runnerPID)
			// A stop is not an exit — but an attempt nobody settles is an
			// attempt nobody CAN settle. Returning here left no runner.exit
			// and no live pid, which inspect reads as `lost`; the reconciler
			// refuses a handle it cannot address, marks the tick rejected,
			// and — because the branch may already carry timer-pushed commits
			// — re-adopts and re-refuses the same attempt on every restart
			// after that, forever. So the stop settles the attempt as FAILED,
			// with the signal in the exit code the way a shell reports one.
			// Which stop it was is still the cancel record's to say: a cancel
			// writes that before it signals, and inspect reads it first.
			code := stopExitCode(sig)
			observe(ObsExited, fmt.Sprintf(
				"the supervisor was stopped by %s and settled the attempt as failed with %d; "+
					"a stop is not a completion, and an unsettled attempt is one nobody can ever settle", sig, code))
			note("settled as failed (%d) after %s", code, sig)
			return atomicWrite(st.path(fileRunnerExit), []byte(strconv.Itoa(code)+"\n"), 0o644)
		}
	}

	// The final push happens BEFORE settlement is recorded, so that "settled"
	// always means durability was attempted rather than merely that a process
	// is gone.
	push.last = time.Time{}
	if outcome := push.maybePush(st.credentialLive()); outcome == "pushed" {
		_ = atomicWrite(st.path(fileLastPush), []byte(now()+"\n"), 0o644)
	}

	observe(ObsExited, fmt.Sprintf(
		"the %s runner exited with %d; completion is read from the branch and the report, never from this",
		record.Runner, code))
	note("the runner exited with %d", code)
	return atomicWrite(st.path(fileRunnerExit), []byte(strconv.Itoa(code)+"\n"), 0o644)
}

// stopExitCode is the exit code a stopped attempt settles with: the shell's
// 128+signal, so 143 reads as SIGTERM and 130 as SIGINT to anybody who looks
// at runner.exit later.
func stopExitCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok && s > 0 {
		return 128 + int(s)
	}
	return 128
}

// pushTick is how often the timer is ASKED, which is half the interval: a
// ticker that fires exactly on the interval misses it by a scheduling hair and
// pushes at twice the period, which is a durability window twice the size the
// contract pins.
func pushTick(interval time.Duration) time.Duration {
	if half := interval / 2; half > 100*time.Millisecond {
		return half
	}
	return 100 * time.Millisecond
}

// canPush is one HALF of the source grade: whether this supervisor's own timed
// push runs. The other half — whether the RUNNER can push — is not a question
// this timer answers, and used to be answered by nothing at all; it is
// enforced at launch in grade.go, where a read-only attempt's process is built
// without the credentials or the git configuration a push needs.
func (r *attemptRecord) canPush() bool {
	return r.Remote != "" && !r.readOnly()
}

// stopTree stops a runner and everything it started: TERM, a moment to write
// what it has, then KILL.
func stopTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = signalGroup(pid, sigTerm())
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = signalGroup(pid, sigKill())
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}
