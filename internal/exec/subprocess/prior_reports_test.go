package subprocess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestARedispatchedPromptNamesItsPredecessors is tick nvn's rendering half.
//
// A re-dispatched attempt used to start blind: its prompt named the tick and
// the repository, never the reports of the attempts before it — which, since
// tick 35h, survive as report.md beside their attempt records. In the ticks
// pwp run, close-out attempts 11 to 15 each re-derived the same impasse and
// each surfaced a different subset of findings, because none could see what
// the last had concluded.
//
// This test reads the text a worker is handed, not the data behind it: a
// worker cannot consult PriorReports; it can only read its prompt.
func TestARedispatchedPromptNamesItsPredecessors(t *testing.T) {
	t.Parallel()

	record := &attemptRecord{
		TickID:  "abc",
		Branch:  "ticfac/run-r/tick-abc/attempt-3",
		BaseSHA: "0123456789abcdef0123456789abcdef01234567",
		Repo:    "/tmp/repo",
		// Deliberately NOT newest first: the render owns the order, because
		// "newest first" is a property of the prompt, not of whoever gathered.
		PriorReports: []PriorReport{
			{Attempt: 1, Path: "/state/r-fixture/abc/1/repo/aa/report.md",
				Status: StatusBlocked, Detail: "the factory webhook refuses the new hook"},
			{Attempt: 2, Path: "/state/r-fixture/abc/2/repo/bb/report.md",
				Status: StatusDoneWithConcerns, Detail: "hook merged, tests red on one host"},
		},
	}
	spec := &JobSpec{Role: "implement-tick", ArtifactPrefix: "runs/r/abc"}

	prompt := renderPrompt(record, spec)

	_, section, found := strings.Cut(prompt, "## Prior attempts")
	if !found {
		t.Fatal("a prompt for a re-dispatched attempt carries no Prior attempts section")
	}

	// The paths — a predecessor is no use to a worker it cannot be sent to.
	for _, prior := range record.PriorReports {
		if !strings.Contains(section, prior.Path) {
			t.Errorf("the Prior attempts section does not name attempt %d's report (%s)", prior.Attempt, prior.Path)
		}
	}

	// The status lines, in the same shape the worker's own report ends with.
	if !strings.Contains(section, "STATUS: "+StatusBlocked+" — the factory webhook refuses the new hook") {
		t.Errorf("the section does not carry attempt 1's status line with its detail:\n%s", section)
	}
	if !strings.Contains(section, "STATUS: "+StatusDoneWithConcerns+" — hook merged, tests red on one host") {
		t.Errorf("the section does not carry attempt 2's status line with its detail:\n%s", section)
	}

	// NEWEST FIRST, whatever order the reports arrived in: the most recent
	// attempt is the one a worker pressed for time must read first.
	var first string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- attempt") {
			first = line
			break
		}
	}
	if !strings.Contains(first, "attempt 2") {
		t.Errorf("the section does not render newest first; its first entry is %q", first)
	}

	// The framing the tick exists for: prior analysis to CHECK, never
	// instructions to follow — a predecessor can be wrong, and an attempt
	// that trusts it inherits the error.
	for _, phrase := range []string{"verify, not instructions to follow", "A predecessor can be wrong"} {
		if !strings.Contains(section, phrase) {
			t.Errorf("the section never says %q: without the framing, a predecessor's mistake is inherited, not checked", phrase)
		}
	}
}

// TestAPredecessorOfAnotherRunNamesItsRun is tick n4h's rendering half.
//
// Attempt numbers count a RUN's dispatches, so attempt 1 of a previous run
// and attempt 1 of this one are different attempts with the same number: a
// worker handed a cross-run predecessor must be told WHICH RUN it ran under,
// or "attempt 1" reads as this run's own first try — analysis of a different
// dispatch wearing the identity of another.
func TestAPredecessorOfAnotherRunNamesItsRun(t *testing.T) {
	t.Parallel()

	section := PriorReportsSection([]PriorReport{
		{Run: "r-first", Attempt: 1, Path: "/state/r-first/a1/1/report.md",
			Status: StatusBlocked, Dispatched: "2026-09-01T00:00:00Z"},
	})
	if !strings.Contains(section, "run r-first attempt 1") {
		t.Errorf("a predecessor of another run is not named with its run:\n%s", section)
	}
	// This run's own predecessors keep the shorter label: the prompt already
	// names the worker's own run in its job line, so an unnamed run IS this
	// one, and the named one is distinguishable from it precisely because the
	// other is unnamed.
	same := PriorReportsSection([]PriorReport{
		{Attempt: 1, Path: "/state/r-second/a1/1/report.md", Status: StatusBlocked,
			Dispatched: "2026-09-02T00:00:00Z"},
	})
	if !strings.Contains(same, "- attempt 1") {
		t.Errorf("this run's own predecessor lost its in-run label:\n%s", same)
	}
	if strings.Contains(same, "run r-second") {
		t.Errorf("this run's own predecessor is labelled with a run the worker never heard of:\n%s", same)
	}
}

// TestPriorReportsOrderNewestFirstAcrossRuns: the section's order across
// runs cannot come from attempt numbers — they are a run's own count, and
// attempt 9 of an older run is not newer than attempt 1 of this one. The
// dispatch times order the list (tick n4h): dated predecessors newest
// first, whatever run issued them; an undated record — one that cannot be
// read — is the least fresh thing the list can say and sorts after every
// dated entry; a list with no dates at all falls back to attempt numbers,
// which is the order of every list the seam ever handed over before this.
func TestPriorReportsOrderNewestFirstAcrossRuns(t *testing.T) {
	t.Parallel()

	section := PriorReportsSection([]PriorReport{
		// Deliberately in the wrong order on every key except the truth:
		// the oldest run's attempt 9 arrived first, and its number is the
		// biggest — sorting by attempt would put it first, and it is the
		// OLDEST analysis in the list.
		{Run: "r-old", Attempt: 9, Path: "/state/r-old/a1/9/report.md", Status: StatusDone,
			Dispatched: "2026-07-01T00:00:00Z"},
		{Run: "r-first", Attempt: 2, Path: "/state/r-first/a1/2/report.md", Status: StatusDone,
			Dispatched: "2026-08-01T00:00:00Z"},
		{Attempt: 1, Path: "/state/r-second/a1/1/report.md", Status: StatusBlocked,
			Dispatched: "2026-09-01T00:00:00Z"},
		// A predecessor whose record does not read is undated: it sorts after
		// every dated entry, never between them by its attempt number.
		{Run: "r-first", Attempt: 3, Path: "/state/r-first/a1/3/report.md", Status: StatusDone},
	})

	want := []string{
		"- attempt 1 — ",
		"- run r-first attempt 2 — ",
		"- run r-old attempt 9 — ",
		"- run r-first attempt 3 — ",
	}
	var got []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			got = append(got, strings.TrimSpace(line))
		}
	}
	if len(got) != len(want) {
		t.Fatalf("the section renders %d predecessors, want %d:\n%s", len(got), len(want), section)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("entry %d is %q, want it to start %q — the section is not newest first across runs:\n%s",
				i, got[i], want[i], section)
		}
	}
}

// TestAFirstAttemptsPromptNamesNoPredecessors guards the other direction: a
// first attempt has nothing to inherit, and a prompt that printed the section
// header over an empty list would teach every worker to skip a section it
// later needs.
func TestAFirstAttemptsPromptNamesNoPredecessors(t *testing.T) {
	t.Parallel()

	record := &attemptRecord{
		TickID:  "abc",
		Branch:  "ticfac/run-r/tick-abc/attempt-1",
		BaseSHA: "0123456789abcdef0123456789abcdef01234567",
	}
	prompt := renderPrompt(record, &JobSpec{ArtifactPrefix: "runs/r/abc"})
	if strings.Contains(prompt, "Prior attempts") {
		t.Errorf("a first attempt's prompt carries a Prior attempts section:\n%s", prompt)
	}
}

// TestAPredecessorThatSaidNothingIsNamedAsSuch: a predecessor that settled
// without a recognisable status line still has a report worth reading, and
// rendering it as an empty verdict would read as "it concluded nothing" —
// which is a different fact from "nobody can say what it concluded".
func TestAPredecessorThatSaidNothingIsNamedAsSuch(t *testing.T) {
	t.Parallel()

	section := PriorReportsSection([]PriorReport{{Attempt: 1, Path: "/state/report.md"}})
	if !strings.Contains(section, "attempt 1") || !strings.Contains(section, "no recognisable status line") {
		t.Errorf("a predecessor without a status line is not named as such:\n%s", section)
	}
}

// TestStartRendersThePredecessorsIntoThePromptFile is the executor-level
// wiring: the dispatch hands the reports to Options, and the prompt the
// runner is actually handed — prompt.md beside the attempt record — carries
// them. A section that exists only in renderPrompt's signature and never
// reaches the file is a section no worker ever saw.
func TestStartRendersThePredecessorsIntoThePromptFile(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{attempt: 2})
	prior := []PriorReport{{Attempt: 1, Path: filepath.Join(f.StateDir, "prior-report.md"), Status: StatusBlocked}}
	f.Executor.opts.PriorReports = prior

	handle := f.Start(f.spec("run-nvn/tick-abc/attempt-2", "abc"))
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(local.State, filePrompt))
	if err != nil {
		t.Fatalf("the executor wrote no prompt for the attempt: %v", err)
	}
	if !strings.Contains(string(raw), prior[0].Path) {
		t.Errorf("the prompt the runner was handed does not name the predecessor's report:\n%s", string(raw))
	}
}
