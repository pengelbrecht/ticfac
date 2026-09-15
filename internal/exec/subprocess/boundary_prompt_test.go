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

// TestTheBoundaryRefusalNamesTheRoleAndThePermittedDestination is tick 54n's
// other half. The prompt now states the exemptions; when the boundary still
// refuses — because the worker wrote a RECORD, not an exempt file — the
// refusal must say WHO was refused and WHERE that output should have gone.
// A refusal that says only "no" is what left the closeout role inventing
// workarounds: five attempts, each told .tick/ was forbidden and nothing
// else, none able to commit its deliverable.
//
// The fixture's `boundary` mode is the shape itself: the worker writes a
// tracker record AND appends to .tick/learnings.md — the learning lands, the
// record is the violation — so the refusal can be checked against the exact
// paths this tick is about.
func TestTheBoundaryRefusalNamesTheRoleAndThePermittedDestination(t *testing.T) {
	t.Parallel()

	for _, role := range []string{"implement-tick", "closeout-epic"} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, fixtureOptions{mode: "boundary"})
			spec := f.spec("run-54n/tick-54n/attempt-1", "54n")
			spec.Role = role
			handle := f.Start(spec)
			f.waitSettled(handle)

			collected := f.collect(handle)
			if collected.Verdict != VerdictBoundaryViolation {
				t.Fatalf("verdict %s (%s), want %s",
					collected.Verdict, collected.Message, VerdictBoundaryViolation)
			}
			if !strings.Contains(collected.Message, role) {
				t.Errorf("the refusal does not name the role %s, so a worker reading it cannot tell "+
					"which job's boundary this is or which role's deliverable is in question: %q",
					role, collected.Message)
			}
			for _, path := range ExemptFromBoundary() {
				if !strings.Contains(collected.Message, path) {
					t.Errorf("the refusal never names %s as a permitted destination, so a worker "+
						"that hit this boundary is left inventing a workaround — the exact "+
						"failure that cost the closeout role five attempts (tick 54n): %q",
						path, collected.Message)
				}
			}
			if prefix := spec.ArtifactPrefix; !strings.Contains(collected.Message, prefix) {
				t.Errorf("the refusal does not say the report belongs under %s: %q", prefix, collected.Message)
			}
		})
	}
}

// TestTheRefusalOffersOnlyDestinationsTheBoundaryPermits guards the drift
// direction the whole tick is about: the refusal's permitted destinations
// are rendered from the boundary's own exemption list, never hand-written a
// second time, so the refusal can never send a worker at a path the check
// would also refuse — which would be the prompt drift of this tick, moved
// from the prompt into the refusal.
func TestTheRefusalOffersOnlyDestinationsTheBoundaryPermits(t *testing.T) {
	t.Parallel()

	msg := BoundaryRefusal("closeout-epic", "runs/r/54n/", []string{".tick/issues/54n.json"})
	for _, path := range ExemptFromBoundary() {
		if OutsideBoundary(path) {
			t.Fatalf("%s is named as permitted but the boundary refuses it: the exemption list "+
				"and the check disagree, which is the drift this file exists to stop", path)
		}
		if !strings.Contains(msg, path) {
			t.Errorf("the refusal %q does not name %s as permitted", msg, path)
		}
	}
	// The learnings destination is the one the closeout role's own
	// instructions point its output at; it must be an exempt path, never a
	// second hard-code the boundary could disagree with.
	if learnings := learningsPath(); learnings != "" {
		var permitted bool
		for _, path := range ExemptFromBoundary() {
			permitted = permitted || path == learnings
		}
		if !permitted {
			t.Errorf("learningsPath() names %s, which is not exempt: the refusal would offer "+
				"a destination the boundary also refuses", learnings)
		}
		if !strings.Contains(msg, learnings) {
			t.Errorf("the refusal %q does not name the learnings destination %s", msg, learnings)
		}
	}
}
