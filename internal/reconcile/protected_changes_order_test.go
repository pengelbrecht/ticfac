package reconcile

import (
	"slices"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// Epic ex6's tgx (2026-10-08): three findings proposed changes to the SAME
// protected file — 2pn's append (the harness gate cell) and q6z's and 2p3's
// whole-file replacements, each composed from a base before the cell existed.
// Applied in findings-key order, the append landed first and both
// replacements then wiped it, so the branch the PR merged lost the gate the
// run had just declared. The run must apply a file's content replacements
// before its appends, so the append lands on the content that finally stands.

// The incident, end to end: three ticks report protected changes to
// .tick/runners.toml — an append and two whole-file contents whose keys sort
// after it, exactly the order that lost the cell. The run completes, each
// finding is fixed by its own commit, and the file on the epic branch carries
// the last content's edit AND the appended cell.
func TestProtectedChangesApplyContentsBeforeAppendsOnTheSameFile(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	opts := fixtureOptions{mode: "protected_change_order"}
	f := newFixture(t, opts)
	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}

	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	findings, err := store.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 3 {
		t.Fatalf("%d finding draft(s), want the three protected changes: %+v", len(findings), findings)
	}
	// The scenario is the incident's: the append's key sorts first, so the
	// naive key order applies it before the replacements that erase it. If
	// the titles change and this flips, the test no longer reproduces the
	// bug — pick titles whose keys sort this way again.
	if findings[0].ProtectedAppend == "" || findings[1].ProtectedContent == "" || findings[2].ProtectedContent == "" {
		t.Fatalf("the drafts are not append-then-contents in key order: %+v", findings)
	}
	for _, finding := range findings {
		if finding.Status != runstate.FindingFixed || !sha40.MatchString(finding.FixedAs) {
			t.Errorf("finding %s is %s fixed as %q, want fixed by its own commit", finding.Key[:8],
				finding.Status, finding.FixedAs)
		}
	}

	branch := r.IntegrationBranch()
	mustRun(t, f.Repo.Dir, "git", "fetch", "--quiet", "origin", branch)
	file := mustRun(t, f.Repo.Dir, "git", "show", "origin/"+branch+":.tick/runners.toml")
	for _, want := range []string{
		// The appended cell survived both whole-file replacements.
		"[testing.commands.lint]",
		"test -f work-a1.txt",
		// The LAST content replacement stands: key order among contents kept.
		"# whole-file replacement proposed by b1",
		// The base the contents carried is intact.
		"tree = { command = \"test -f README.md",
	} {
		if !strings.Contains(file, want) {
			t.Errorf("the epic branch's runners.toml lost %q — an append wiped by a later content replacement:\n%s",
				want, file)
		}
	}
	if strings.Contains(file, "# whole-file replacement proposed by a2") {
		t.Errorf("the earlier content replacement outlived the later one:\n%s", file)
	}
}

// short: pure functions; no processes.
func TestOrderForApplicationPutsContentsBeforeAppends(t *testing.T) {
	t.Parallel()
	const file = ".tick/runners.toml"
	appendFinding := runstate.Finding{Key: "7", ProtectedPath: file, ProtectedAppend: "[testing.commands.lint]"}
	contentEarly := runstate.Finding{Key: "4", ProtectedPath: file, ProtectedContent: "version = 2\n"}
	contentLate := runstate.Finding{Key: "5", ProtectedPath: file, ProtectedContent: "version = 3\n"}
	unrelatedAppend := runstate.Finding{Key: "9", ProtectedPath: ".tick/runners.cloud.toml", ProtectedAppend: "[configs.claude]"}
	unrelatedContent := runstate.Finding{Key: "6", ProtectedPath: ".tick/runners.cloud.toml", ProtectedContent: "x = 1\n"}
	noChange := runstate.Finding{Key: "1"}

	// The incident's exact shape: the append's key sorts first, the two
	// contents after. The append must come out last, on the content that
	// finally stands; within each group the key order is kept.
	got := orderForApplication([]runstate.Finding{appendFinding, contentEarly, contentLate})
	want := []runstate.Finding{contentEarly, contentLate, appendFinding}
	if !slices.Equal(got, want) {
		t.Errorf("orderForApplication(append, content, content) = %v, want %v", keysOf(got), keysOf(want))
	}

	// Every path gets the same rule, and findings that carry no change stay
	// in place: one stable pass over the findings serves them all. On each
	// path the contents still precede that path's append, in key order.
	got = orderForApplication([]runstate.Finding{appendFinding, noChange, contentEarly, unrelatedAppend, contentLate, unrelatedContent})
	want = []runstate.Finding{noChange, contentEarly, contentLate, unrelatedContent, appendFinding, unrelatedAppend}
	if !slices.Equal(got, want) {
		t.Errorf("orderForApplication(mixed) = %v, want %v", keysOf(got), keysOf(want))
	}
	if got := orderForApplication(nil); len(got) != 0 {
		t.Errorf("orderForApplication(nil) = %v, want none", keysOf(got))
	}
}

func keysOf(findings []runstate.Finding) []string {
	keys := make([]string, len(findings))
	for i, finding := range findings {
		keys[i] = finding.Key
	}
	return keys
}
