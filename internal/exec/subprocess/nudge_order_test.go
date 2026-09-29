package subprocess

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A nudge a reader never sees (hol, main's CI at 7fce53c5:
// TestAResolveJobThatEndsItsTurnEarlyIsNudgedAndTheRunCompletes, "the feed
// never said the resolve job was nudged"). The run completed — the nudge
// worked — but the observation that announces it never reached the reader,
// by either of two orderings:
//
//   - the WRITER: the supervisor announced a nudge only after the nudged
//     runner had started (and after an fsynced pid write). A runner that
//     reports at once could write its report first, and an inspect in that
//     moment answered `succeeded` — terminal, so the reader stops polling —
//     without the announcement, which then landed where nobody reads.
//   - the READER: statusOf read the observation stream BEFORE the evidence
//     the state is decided from. An announcement and a report both written
//     between the two reads gave a terminal status without the announcement.
//
// Each test forces its interleaving at a seam, so each fails every time on
// the old order.

// superviseHeldAnnouncementArg is the helper mode: Supervise, with every
// nudged runner's start held — before the observation that follows it — until
// the runner has written its report and the test has created the release
// file beside the attempt's state.
const superviseHeldAnnouncementArg = "__supervise_held_announcement__"

const heldAnnouncementRelease = "release-announcement"

func superviseHeldAnnouncement(args []string) int {
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	state := fs.String("state", "", "")
	if err := fs.Parse(args); err != nil || *state == "" {
		fmt.Fprintf(os.Stderr, "supervise helper: --state is required\n")
		return 2
	}
	runnerStarted = func(env []string) {
		nudged := false
		for _, kv := range env {
			if strings.HasPrefix(kv, EnvNudge+"=") {
				nudged = true
			}
		}
		if !nudged {
			return
		}
		release := filepath.Join(*state, heldAnnouncementRelease)
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(release); err == nil {
				return
			}
		}
	}
	if err := Supervise(*state); err != nil {
		fmt.Fprintf(os.Stderr, "supervise helper: %v\n", err)
		return 1
	}
	return 0
}

// pollLikeTheRun addresses the handle the way the reconciler's await does —
// resuming the stream at each answer's cursor — until it is terminal, and
// answers every observation it was handed.
func pollLikeTheRun(t *testing.T, f *fixture, handle *JobHandle) []Observation {
	t.Helper()
	var seen []Observation
	cursor := ""
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		status, err := f.Executor.Inspect(handle, cursor)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, status.Observations...)
		if status.Terminal {
			return seen
		}
		if status.Cursor != nil {
			cursor = *status.Cursor
		}
	}
	t.Fatal("the attempt never read terminal")
	return nil
}

func TestANudgeIsAnnouncedBeforeTheNudgedRunnerCanReport(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "stop_early",
		supervisorArgv: []string{os.Args[0], superviseHeldAnnouncementArg}})
	handle := f.Start(f.spec("run-hol/tick-nnn/resolve-1", "nnn"))
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}

	seen := pollLikeTheRun(t, f, handle)
	if err := os.WriteFile(filepath.Join(local.State, heldAnnouncementRelease), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.waitSettled(handle)

	if got := nudges(seen); len(got) != 1 {
		t.Fatalf("the reader was handed %d nudge observations before the attempt read terminal, want 1: "+
			"the nudged runner reported before its nudge was announced\n%s", len(got), formatObservations(seen))
	}
}

func TestAnObservationWrittenBeforeTheEvidenceReachesTheReader(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "slow_report"})
	handle := f.Start(f.spec("run-hol/tick-ooo/resolve-1", "ooo"))
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}
	st := f.store(handle)

	// Between the two reads of the first inspect: the supervisor's
	// announcement, and then the report — in that order, as a supervisor
	// writes them.
	fired := false
	statusBetweenReads = func() {
		if fired {
			return
		}
		fired = true
		if err := st.observe(Observation{At: "2026-09-29T00:00:00Z", Kind: ObsStarted,
			Detail: nudgeDetailPrefix + "nudge 1 of 2: announced between the reads"}); err != nil {
			t.Error(err)
		}
		if err := os.MkdirAll(filepath.Dir(local.ResultPath), 0o755); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(local.ResultPath, []byte("# ooo\n\nSTATUS: DONE\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { statusBetweenReads = nil })

	seen := pollLikeTheRun(t, f, handle)
	statusBetweenReads = nil
	f.waitSettled(handle)
	if !fired {
		t.Fatal("the seam never fired")
	}
	if got := nudges(seen); len(got) != 1 {
		t.Fatalf("the reader was handed %d nudge observations, want 1: an observation written before the "+
			"evidence that made the attempt terminal was dropped\n%s", len(got), formatObservations(seen))
	}
}
