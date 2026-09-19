package reconcile

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Worker telemetry that cannot tell a working agent from a wedged one
// (tick dh1).
//
// Two attempts of epic ncv, dispatched into the same wave on 2026-09-18
// against the same 3600s bound. 9fc held 433 uncommitted lines at its stop;
// ef7's snapshot was an EMPTY commit. Their observation logs are the same
// three lines — started, one heartbeat at second one, exited at the wall
// clock — and their stall warnings, when a later incarnation finally got
// round to writing them, were the same shape too: "produced nothing durable
// for 43m, branch last moved 43m ago, worktree last changed 43m ago", said
// about both. Everything ticfac kept was a GAP, and a gap cannot answer this
// question: an untouched checkout's newest file is as old as the dispatch,
// which is exactly the number a thinking agent's worktree also shows.

// readLiveness reads the run's liveness record — the surface the per-poll
// measurement is RECORDED on, deliberately not the feed a watcher subscribes
// to (liveness.go).
func readLiveness(t *testing.T, repo, runID string) []LivenessRow {
	t.Helper()
	file, err := os.Open(LivenessPath(repo, runID))
	if err != nil {
		t.Fatalf("read the run's liveness record: %v", err)
	}
	defer file.Close()
	var rows []LivenessRow
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row LivenessRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("decode a liveness row: %v (%q)", err, line)
		}
		if row.SchemaVersion != LivenessSchemaVersion {
			t.Fatalf("a liveness row carries schema_version %d, want %d", row.SchemaVersion, LivenessSchemaVersion)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan the liveness record: %v", err)
	}
	return rows
}

// counted is the file counts one tick's probe rows carry, in order. A row
// whose count is null was taken against a worktree the run could not
// calibrate on, and it is not an answer about what the agent did.
func counted(rows []LivenessRow, tick string) []int {
	var out []int
	for _, row := range rows {
		if row.Kind != LivenessProbe || row.TickID != tick || row.ChangedFiles == nil {
			continue
		}
		out = append(out, *row.ChangedFiles)
	}
	return out
}

// The acceptance's first half: a wedged agent is distinguishable from a
// working one in the recorded observations, and it is distinguishable from
// the probe after the run's first look — not at the wall clock an hour later,
// and not from prose a person has to interpret.
//
// The two runs here are the ncv pair with nothing simulated. `wedged` writes
// nothing at all and never finishes; `busy-a1` writes into its worktree and
// commits nothing until the very end. Both are alive, both have an unmoved
// branch, and under the measurement ticfac had before this tick both produce
// the same account of themselves. The count separates them.
func TestAWedgedAttemptIsDistinguishableFromAWorkingOne(t *testing.T) {
	t.Parallel()

	wedged := newFixture(t, fixtureOptions{mode: "wedged"})
	_, _, err := wedged.run(wedged.Repo, fixtureOptions{mode: "wedged",
		stallWarn: 200 * time.Millisecond, stopAfter: stopAt("a1", StageStallWarned)})
	killedAfter(t, err, "a1", StageStallWarned)

	busy := newFixture(t, fixtureOptions{mode: "busy-a1"})
	_, result, err := busy.run(busy.Repo, fixtureOptions{mode: "busy-a1"})
	if err != nil {
		t.Fatalf("the busy run did not finish: %v", err)
	}
	if len(result.Closed) == 0 {
		t.Fatalf("the busy run closed nothing: %+v", result.Failure)
	}

	wedgedRows := readLiveness(t, wedged.Repo.Dir, "r-fixture")
	// The FIRST probe already answers. The run calibrates on the worktree
	// when the attempt joins the window, so there is no opening interval in
	// which the two attempts still read alike — which is the acceptance's
	// "within one poll interval", and the difference between catching ef7 at
	// 16:52 and never catching it at all.
	for _, row := range wedgedRows {
		if row.Kind != LivenessProbe || row.TickID != "a1" {
			continue
		}
		if row.ChangedFiles == nil {
			t.Errorf("the first probe of the wedged attempt carries no count: the run waited a whole probe "+
				"interval before it could say anything about what the agent had written (%+v)", row)
		}
		break
	}

	wedgedCounts := counted(wedgedRows, "a1")
	busyCounts := counted(readLiveness(t, busy.Repo.Dir, "r-fixture"), "a1")

	// A first look and then a probe against it: the count the record exists
	// for exists at all. Without it the record would be a file of nulls,
	// which is the silence this tick is about wearing a schema.
	if len(wedgedCounts) == 0 {
		t.Fatalf("the wedged attempt's liveness record carries no counted probe at all, so nothing in it can "+
			"answer what the agent was doing (%d rows)", len(wedgedRows))
	}
	if len(busyCounts) == 0 {
		t.Fatal("the working attempt's liveness record carries no counted probe at all")
	}

	// The wedged agent wrote nothing, and EVERY probe says so. Not "the last
	// one": a single zero could be a probe that raced a write, and what ad4
	// needed was the row-by-row account that says the worktree was empty from
	// the first look onward.
	for i, n := range wedgedCounts {
		if n != 0 {
			t.Errorf("probe %d of the wedged attempt counted %d files written: the worker writes nothing at all, "+
				"so a non-zero count means the measurement is counting the checkout rather than the work", i, n)
		}
	}

	// And the working one is not zero — at some probe, while it was still
	// alive and had committed NOTHING, the run could already see it producing.
	// That is the whole distinction: same gaps, same empty branch, different
	// answer.
	produced := false
	for _, n := range busyCounts {
		if n > 0 {
			produced = true
		}
	}
	if !produced {
		t.Errorf("every probe of the working attempt counted zero files written (%v): an agent writing into its "+
			"worktree reads exactly like the wedged one, which is the defect", busyCounts)
	}
}

// The acceptance's second half, and the regression guard for the thing that
// silently did not happen: the stall warning fires for an attempt whose
// worktree has not changed.
//
// The warning already existed. What ef7 proved is that nothing tested the
// case it was written for in its purest form — an attempt that never commits
// AND never writes, so that both measured facts are frozen at the checkout.
// Every stall test before this one used `hang`, which commits first: its
// branch moves, and the gap it measures afterwards is a gap on a worktree
// that was written to at least once.
func TestTheStallWarningFiresForAWorktreeThatNeverChanged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "wedged"})
	r, _, err := f.run(f.Repo, fixtureOptions{mode: "wedged",
		stallWarn: 200 * time.Millisecond, stopAfter: stopAt("a1", StageStallWarned)})
	killedAfter(t, err, "a1", StageStallWarned)

	var warning *Event
	for _, event := range r.Journal() {
		if event.Stage == StageStallWarned && event.Tick == "a1" {
			copied := event
			warning = &copied
		}
	}
	if warning == nil {
		t.Fatal("no stall warning for an attempt that has never written a byte: the warning that exists and does " +
			"not fire is worse than the warning that does not exist")
	}

	// And the line SAYS which kind of quiet it found. This is the sentence
	// that would have answered ad4 in one read: ef7 and 9fc got warnings of
	// the same shape, and only one of them had written nothing.
	if !strings.Contains(warning.Detail, "written NOTHING") {
		t.Errorf("the stall warning does not say that the attempt has produced nothing at all, so it reads the "+
			"same for a wedged agent and for one thinking hard: %q", warning.Detail)
	}
}

// The other half of dh1's evidence, and the reason ef7's warning never landed
// in the incarnation that dispatched it.
//
// The warning is evaluated in addressOnce, so only a POLL can write it — and
// pollWindow does not run while finishTick does. On epic ncv ef7 became
// eligible at 17:06:29; the run's last poll was at 17:02:50, after which it
// spent the whole time inside vyg's collect, integrate and gate (17:03:24 to
// 17:10:08), then refused vyg at 17:10:19, which STOPS the run. ef7 was never
// once polled while it was eligible, and the run walked away from it without
// a word. A resumed run warned at its first poll 28 minutes later.
//
// The fixture is that sequence: a1 works and a2 wedges under a declared width
// of two, a1's gate takes long enough that a2 crosses its threshold entirely
// inside it, and the gate then REFUSES — so the run stops with a2 live and
// has exactly one remaining chance to say what it last saw.
//
// # What tick 9pz changed about it, and what it did not
//
// The gap this test was written around is GONE at the source. The finish is no
// longer a blocking call the loop disappears into: it is a state machine the
// loop advances one step per round, so pollWindow runs through a gate and the
// warning lands from a poll WHILE a1 is being gated — 28 minutes earlier than
// ncv managed, and with the whole of a2's remaining wall clock still to spend.
// That is what dh1 wanted, and it is why the assertion below is now "the
// warning landed inside the finish" rather than "the warning landed only when
// the run walked away".
//
// What has NOT changed, and is still asserted, is the last look itself: the run
// stopping is the moment its account of a live attempt stops being added to, so
// that account is made current before it walks away, and the reader is never
// told an attempt is out there before being told what it was last seen doing.
func TestAnAbandonedAttemptIsMeasuredBeforeTheRunWalksAway(t *testing.T) {
	t.Parallel()
	// The gate takes five seconds and then refuses. Both numbers matter: the
	// wait is where a2's threshold is crossed with no poll running, and the
	// refusal is what makes this the run's last look rather than merely a
	// late one.
	slowRefusingGate := `version = 2

[orchestration]
max_parallel = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "sleep 5; exit 3", description = "refuses, slowly" }
`
	f := newFixture(t, fixtureOptions{gate: slowRefusingGate, mode: "wedged-a2"})
	r, result, err := f.run(f.Repo, fixtureOptions{gate: slowRefusingGate, mode: "wedged-a2",
		stallWarn: 3 * time.Second})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 0 {
		t.Fatalf("closed %v behind a gate that refuses: %+v", result.Closed, result.Failure)
	}

	settled, warned, abandoned := -1, -1, -1
	for i, event := range r.Journal() {
		switch {
		case event.Stage == StageWaiting && event.Tick == "a1" && strings.Contains(event.Detail, "settled as"):
			if settled < 0 {
				settled = i
			}
		case event.Stage == StageStallWarned && event.Tick == "a2":
			if warned < 0 {
				warned = i
			}
		case event.Stage == StageWaiting && event.Tick == "a2" && strings.Contains(event.Detail, "still running"):
			abandoned = i
		}
	}
	if abandoned < 0 {
		t.Fatalf("a2 was never announced as abandoned, so this run is not the shape the test is about: %v",
			stagesOf(r.Journal(), "a2"))
	}
	if warned < 0 {
		t.Fatal("the run stopped on another tick's refusal with a wedged attempt live and said NOTHING about it: " +
			"a2 had been eligible for a stall warning for the whole of a1's gate, and the only place left to " +
			"write one was the moment the run walked away")
	}
	if settled < 0 {
		t.Fatalf("a1 never settled, so a2 never waited through a finish at all: %v", stagesOf(r.Journal(), "a1"))
	}
	if warned < settled {
		t.Errorf("a2's stall warning landed at %d, before a1 even settled at %d: it is not the warning this test "+
			"is about — the one that has to cross a threshold while another tick is being finished", warned, settled)
	}
	if warned > abandoned {
		t.Errorf("a2 was announced abandoned at %d and only measured at %d: the reader is told the attempt is out "+
			"there before being told what it was last seen doing", abandoned, warned)
	}
}

// stagesOf is the stages one tick passed through, for a failure message that
// has to say what the run did instead.
func stagesOf(journal []Event, tick string) []string {
	out := []string{}
	for _, event := range journal {
		if event.Tick == tick {
			out = append(out, event.Stage)
		}
	}
	return out
}
