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

	runner := exec.Command(record.RunnerArgv[0], record.RunnerArgv[1:]...)
	runner.Dir = record.Worktree
	runner.Env = append(os.Environ(), record.RunnerEnv...)
	runner.Stdout = log
	runner.Stderr = log
	runner.SysProcAttr = newProcessGroup()

	if err := runner.Start(); err != nil {
		note("the runner could not be started: %v", err)
		_ = st.observe(Observation{At: now(), Kind: ObsExited, Detail: "the runner could not be started: " + err.Error()})
		_ = atomicWrite(st.path(fileRunnerExit), []byte("127\n"), 0o644)
		return err
	}
	runnerPID := runner.Process.Pid
	_ = atomicWrite(st.path(fileRunnerPID), []byte(strconv.Itoa(runnerPID)+"\n"), 0o644)
	_ = st.observe(Observation{
		At:     now(),
		Kind:   ObsStarted,
		Detail: fmt.Sprintf("%s runner, pid %d, worktree %s", record.Runner, runnerPID, record.Worktree),
	})
	note("started %s (pid %d) on %s", record.Runner, runnerPID, record.Branch)

	// A signal aimed at the supervisor takes the runner with it. Cancel kills
	// both groups itself; this is for every other way a supervisor is asked to
	// stop, because a runner whose supervisor is gone is a process nothing is
	// left to collect.
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGTERM, syscall.SIGINT)

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
				_ = st.observe(Observation{At: now(), Kind: ObsHeartbeat, Detail: "pushed " + record.Branch + " to " + record.Remote})
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
			// Nothing is recorded as settled here: the durable fact about a
			// cancellation is the cancel record, and a stop is not an exit.
			return nil
		}
	}

	// The final push happens BEFORE settlement is recorded, so that "settled"
	// always means durability was attempted rather than merely that a process
	// is gone.
	push.last = time.Time{}
	if outcome := push.maybePush(st.credentialLive()); outcome == "pushed" {
		_ = atomicWrite(st.path(fileLastPush), []byte(now()+"\n"), 0o644)
	}

	_ = st.observe(Observation{
		At:     now(),
		Kind:   ObsExited,
		Detail: fmt.Sprintf("the %s runner exited with %d; completion is read from the branch and the report, never from this", record.Runner, code),
	})
	note("the runner exited with %d", code)
	return atomicWrite(st.path(fileRunnerExit), []byte(strconv.Itoa(code)+"\n"), 0o644)
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

// canPush is the SOURCE GRADE, enforced by the issuer. A read-only grade means
// this executor issues nothing that can advance a ref — it is not a rule the
// runner is asked to respect.
func (r *attemptRecord) canPush() bool {
	return r.Remote != "" && r.SourceGrade == "write"
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
