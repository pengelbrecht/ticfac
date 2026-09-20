package reconcile

import (
	"strings"
	"testing"
	"time"
)

// Tick m4n: the gate's clock belongs to the SHELL, and it is wall time.
//
// The regression, on epic dha's go gate: reported elapsed of 1m0s, 2m1s, 3m1s,
// 4m2s, 5m2s across heartbeats that were 17, 16, 29 and 18 WALL minutes apart,
// over one `go test` that was started once and never restarted. The same
// sentences carried "last 16m9s ago", which was right.
//
// One line, two clocks. `began` came off time.Now() carrying a monotonic
// reading and so did `now`, so time.Sub measured the MONOTONIC delta — which on
// darwin does not advance while the host is suspended. The feed's own stamps,
// and the output mtime the idle number is measured against, carry no monotonic
// reading and so measured wall. Nothing was re-created and nothing restarted;
// the arithmetic was simply being done in a clock the operator cannot see.

// TestAGateClockIsWallTimeNotAwakeTime pins the property whose absence caused
// the defect: every time the heartbeat reports from is a wall time, so it can
// be compared against the feed lines it appears in.
//
// This cannot be written as a reproduction. Go gives no way to construct a
// time.Time whose monotonic and wall readings disagree — only a suspended host
// does that — so the defect is unreachable in-process and the property is what
// a test can hold. A time carries a monotonic reading exactly when it differs
// from its own Round(0), which is what this asserts.
// short: one short-lived shell in a tempdir; no repository and no run
func TestAGateClockIsWallTimeNotAwakeTime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := startShell(dir, "exit 0", time.Minute, time.Now(), nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.wait()

	if s.startedAt != s.startedAt.Round(0) {
		t.Errorf("the shell recorded its start as %s, which carries a monotonic reading: the heartbeat would "+
			"then report awake time while the feed line around it reports wall, and on a host that suspends "+
			"those disagree without saying so", s.startedAt)
	}
	// The bound keeps its monotonic reading, deliberately: it bounds work, and
	// a gate must not be spent by a lid being shut.
	if s.deadline == s.deadline.Round(0) {
		t.Errorf("the gate's deadline lost its monotonic reading: the bound would then be spent by wall time " +
			"passing on a suspended host, which is the false refusal cy2 is about")
	}
}

// TestAGateClockSurvivesItsWrapperBeingRebuilt is tick m4n's acceptance stated
// as the tick states it: the finish state may be re-derived around a shell that
// is still running, and the gate's age must not restart when it is.
//
// On the code before this tick the start time lived on the gateCommand wrapper,
// so rebuilding the wrapper reset the gate's age to zero — elapsed would read a
// few milliseconds for a gate minutes old.
// short: one short-lived shell in a tempdir; no repository and no run
func TestAGateClockSurvivesItsWrapperBeingRebuilt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	start := time.Now().Add(-4 * time.Minute)
	s, err := startShell(dir, "sleep 30", time.Hour, start, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_, _, _, _ = s.wait()
	}()

	// The wrapper, rebuilt now around the shell that has been running for four
	// minutes — exactly what a re-derived finish leg would do.
	rebuilt := &gateCommand{command: GateCommand{Name: "go"}, shell: s, beat: time.Now().Round(0)}

	age := rebuilt.shell.age(time.Now()).Round(time.Second)
	if age < 3*time.Minute {
		t.Errorf("the rebuilt wrapper reports the gate as %s old; it has been running for four minutes. A clock "+
			"that restarts with the wrapper reports the wrapper's age, not the gate's", age)
	}
	if left := rebuilt.shell.remaining(); left > time.Hour || left < 55*time.Minute {
		t.Errorf("the rebuilt wrapper reports %s of the bound left, which is not the shell's own hour", left)
	}
}

// TestAGatesReportedElapsedMatchesItsOwnFeedLines is the operator's check,
// end to end: "running for X" has to agree with the feed stamps it sits
// between, because subtracting one from the other is exactly what a person
// reading the feed does.
func TestAGatesReportedElapsedMatchesItsOwnFeedLines(t *testing.T) {
	t.Parallel()
	gate := gateOf("sleep 3; test -f README.md")
	f := newFixture(t, fixtureOptions{gate: gate})
	r, result, err := f.run(f.Repo, fixtureOptions{gate: gate, gateHeartbeat: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %s",
			result.Closed, result.State, result.Reason)
	}

	var started time.Time
	beats := 0
	for _, event := range r.Journal() {
		if event.Tick != "a1" {
			continue
		}
		if event.Stage == StageGateStarted {
			started = event.At
			continue
		}
		if event.Stage != StageGateRunning || started.IsZero() {
			continue
		}
		beats++
		reported, ok := reportedElapsed(event.Detail)
		if !ok {
			t.Fatalf("could not read the elapsed out of %q", event.Detail)
		}
		// The feed's own answer to the same question, from the stamps a
		// person reads. One second of slack for the Round(time.Second) the
		// line is printed with.
		actual := event.At.Sub(started)
		if drift := (actual - reported).Abs(); drift > 1500*time.Millisecond {
			t.Errorf("the gate reported %s elapsed on a line stamped %s after it started: the number an operator "+
				"reads and the number they can compute from the feed disagree by %s",
				reported, actual.Round(time.Millisecond), drift.Round(time.Millisecond))
		}
	}
	if beats < 2 {
		t.Fatalf("only %d heartbeats: the fixture is not exercising the cadence", beats)
	}
}

// reportedElapsed reads the duration out of "...has been running for 5m2s on...".
func reportedElapsed(detail string) (time.Duration, bool) {
	const prefix = "has been running for "
	i := strings.Index(detail, prefix)
	if i < 0 {
		return 0, false
	}
	rest := detail[i+len(prefix):]
	j := strings.Index(rest, " on ")
	if j < 0 {
		return 0, false
	}
	d, err := time.ParseDuration(rest[:j])
	return d, err == nil
}
