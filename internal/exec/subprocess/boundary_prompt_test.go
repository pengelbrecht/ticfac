package subprocess

import (
	"strings"
	"testing"
)

// TestPromptStatesTheBoundaryItIsJudgedAgainst pins the fix for tick 54n.
//
// The prompt told every worker "do not write under .tick/ or .ticfac/", flatly.
// OutsideBoundary exempted .tick/learnings.md, .tick/config.md and
// .tick/runners.toml. The two drifted, and the closeout role — whose own
// instructions tell it to compact what was learned into .tick/learnings.md —
// believed the flat prohibition and reported it could not do its job. Five
// attempts of one tick, none able to commit anything, each one re-deriving the
// same impasse.
//
// A worker obeys what it is TOLD the boundary is. So a path the enforcement
// lets through must be named as permitted in the text the worker reads.
func TestPromptStatesTheBoundaryItIsJudgedAgainst(t *testing.T) {
	t.Parallel()

	exempt := ExemptFromBoundary()
	if len(exempt) == 0 {
		t.Fatal("no exemptions to state: if the boundary really is absolute, delete this test with that fact")
	}

	for _, path := range exempt {
		if OutsideBoundary(path) {
			t.Errorf("%s is listed as exempt but OutsideBoundary refuses it: "+
				"the list and the check disagree, which is the drift this test exists to stop", path)
		}
	}
}

// TestExemptPathsAreInsideAProtectedPrefix guards the other direction: an
// exemption only means something for a path the boundary would otherwise
// refuse. An entry that is outside every protected prefix is dead weight that
// makes the prompt's permission list longer and less credible.
func TestExemptPathsAreInsideAProtectedPrefix(t *testing.T) {
	t.Parallel()

	for _, path := range ExemptFromBoundary() {
		var covered bool
		for _, prefix := range protectedPrefixes {
			if strings.HasPrefix(path, prefix) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("%s is exempt from a boundary that never covered it; drop it from the list", path)
		}
	}
}

// TestRenderedPromptNamesEveryExemption is the test that would actually have
// caught tick 54n: it reads the text a worker is handed, not the data behind it.
// A worker cannot consult ExemptFromBoundary(); it can only read its prompt.
func TestRenderedPromptNamesEveryExemption(t *testing.T) {
	t.Parallel()

	record := &attemptRecord{
		TickID:  "abc",
		Branch:  "ticfac/run-r/tick-abc/attempt-1",
		BaseSHA: "0123456789abcdef0123456789abcdef01234567",
		Repo:    "/tmp/repo",
	}
	spec := &JobSpec{ArtifactPrefix: "runs/r/abc"}

	prompt := renderPrompt(record, spec)

	// Look only at the Boundaries section. A bare Contains over the whole
	// prompt is not enough: .tick/learnings.md already appears earlier as a
	// file to READ, so a whole-prompt check passes while the boundary text
	// still forbids writing it — which is exactly the case that cost tick 54n.
	_, boundaries, found := strings.Cut(prompt, "## Boundaries")
	if !found {
		t.Fatal("the prompt has no Boundaries section to state the exemptions in")
	}

	for _, path := range ExemptFromBoundary() {
		if !strings.Contains(boundaries, path) {
			t.Errorf("the prompt's Boundaries section never names %s as writable, but the boundary lets it through.\n"+
				"A worker obeys the text it is handed: the closeout role read a flat "+
				"'do not write under .tick/' and reported it could not compact learnings "+
				"into .tick/learnings.md, which it was separately instructed to do. See tick 54n.", path)
		}
	}
}
