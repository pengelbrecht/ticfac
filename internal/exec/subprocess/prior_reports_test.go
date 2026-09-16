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
