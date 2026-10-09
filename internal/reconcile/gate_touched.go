package reconcile

import (
	"strings"

	"github.com/pengelbrecht/ticfac/internal/gatescope"
)

// What the gate tells its commands about the tick they are gating.
//
// The declared checks of `[testing.commands]` run over the MERGED tree, and
// until tick r1f that was all they knew: a check could not see WHICH files
// the tick being gated changed, so the only suites a gate could afford to
// run were the whole repository's short ones. The cost of that blindness was
// tick dz1's regression — a pinned-version change broke four internal/
// reconcile end-to-end tests, the short suite skipped every one of them, the
// gate stayed green, and the break surfaced at the epic's close-out in CI's
// full suite, costing a repair and two ~30-minute CI cycles.
//
// So the gate now exports, into every declared command's environment, the
// two commits whose diff is the tick's own change:
//
//	TICFAC_GATE_TOUCHED_BASE   the integration branch's head at the merge
//	TICFAC_GATE_TOUCHED_HEAD   the attempt's head the merge carried in
//
// `git diff --name-only $BASE...$HEAD` — three dots, the same shape the
// status model reads a merged attempt's diff with (mergedAttemptDiff) — is
// the files the TICK changed, never the branch's other ticks and never the
// run's own records under .ticfac/. Both names are ALWAYS exported, an empty
// value meaning "this gate has no touched diff to offer" (the fold at an
// epic's close-out, whose full suite is the CI the close-out already waits
// for on the PR head; an adopted attempt whose merge this run cannot name),
// so a command can tell the gate's "nothing to diff" apart from a bare
// invocation by a person, which this repository's own check answers by
// diffing the working branch against origin/main instead.
//
// The names are gatescope's (the package that implements this repository's
// own touched-packages check), imported here so the writer and the reader
// spell them once.
//
// A pair that cannot be derived is exported empty rather than refusing: the
// checks this feeds are ADDITIONAL coverage over a gate that already runs,
// and turning a failed `git log` into a refused tick would be a false
// refusal of innocent work — the exposure cy2 spent this epic removing. What
// an empty pair costs is stated in the gate command's own output ("this gate
// names no touched diff"), which is evidence a person reads.

// merge.TouchedBase and merge.TouchedHead are carried on the merge for the
// same reason GateSHA is: the gate is a verdict about the merge, and the
// merge is the one place that knows which commit it merged onto and which it
// carried in. Each construction site states its own pair; the sites that
// gate a tree whose diff they cannot name state that by leaving both empty.

// touchedPair is the pair for a merge this run is about to gate: a merge's
// own two sides when sha is one, and — for a head that already CARRIES the
// tick (an adopted attempt, or a repair found already on the branch) — the
// sides of the tick's own merge, found by the needle every attempt merge
// names. No pair at all answers empty.
func (g *repoGit) touchedPair(sha, needle string) (base, head string) {
	if base, head, ok := g.mergeSides(sha); ok {
		return base, head
	}
	// Not a merge: the tick's own merge is in the history below this head.
	// The needle is the run's own naming — a foreign run that merged this
	// attempt names a different run id, so its merge is not found and the
	// pair is empty: that tree was gated by the run that merged it.
	out, err := g.run("", "log", sha, "--merges", "--fixed-strings", "--grep="+needle,
		"--format=%H%x1f%B%x1e")
	if err != nil {
		return "", ""
	}
	found, ok := MergeNamingAttempt(out, needle)
	if !ok {
		return "", ""
	}
	base, head, ok = g.mergeSides(found)
	if !ok {
		return "", ""
	}
	return base, head
}

// mergeSides is the first two parents of a commit: the integration branch's
// head at the merge and the attempt the merge carried in. An octopus merge
// answers its first two, which is the honest reading of a shape the run never
// makes.
func (g *repoGit) mergeSides(sha string) (base, head string, ok bool) {
	out, err := g.run("", "log", "-1", "--format=%P", sha)
	if err != nil {
		return "", "", false
	}
	sides := strings.Fields(out)
	if len(sides) < 2 {
		return "", "", false
	}
	return sides[0], sides[1], true
}

// repairTouched is the pair a REPAIRED tree's gate exports: the tick's own
// base against the repair's head — the union, deliberately. A re-gate that
// diffed only the repair's delta would be a gate that stops covering the
// tick's exposure the moment a repair lands: the repair fixes the ts check,
// the re-gate runs nothing but the repair's one file, and the tick closes
// over a tree whose broken Go package nobody ran. Diffing from the tick's
// base re-covers everything the tick — and every tick merged since — changed,
// which is the repaired tree's whole delta from the last gated one.
//
// When the failed gate's own pair is empty (the tick was adopted, or gated
// at a landing), there is no tick base to union from, and the repair's own
// sides are still a true statement about what changed since the last gate
// over this tree.
func (r *Reconciler) repairTouched(marker attemptHandle, merged merge, repairMerge string) (base, head string) {
	if merged.TouchedBase != "" {
		return merged.TouchedBase, repairMerge
	}
	needle := AttemptMergeNeedle(r.runID, marker.TickID, marker.Attempt)
	return r.git.touchedPair(repairMerge, needle)
}

// MergeNamingAttempt finds the newest merge commit whose message names the
// attempt in one line, out of a `git log --format=%H%x1f%B%x1e` listing.
//
// The needle is matched as a LINE, never as a substring: a substring would
// read attempt 1's number off attempt 10's merge — "ticfac run r: tick a1
// attempt 1" is the opening of "ticfac run r: tick a1 attempt 10" — and
// answer a tick with another dispatch's work. A line is the needle whole
// (the plain merge ends its message there) or the needle followed by a
// space (the resolve spellings carry the resolution's story after it).
//
// The listing must be newest-first, which is `git log`'s own order: when
// more than one merge matches the grep, the newest is the one a re-run of
// this attempt is most likely to be re-gating.
//
// This is the reader half of AttemptMergeNeedle's contract — the words live
// beside the minting in integrate.go, and the line-exact reading lives here
// so the status model's drill-in (statusmodel.mergedAttemptDiff) reads the
// same words through the same rule rather than a copy of it.
func MergeNamingAttempt(out, needle string) (string, bool) {
	for _, record := range strings.Split(out, "\x1e") {
		sha, body, ok := strings.Cut(record, "\x1f")
		if !ok {
			continue
		}
		sha = strings.TrimSpace(sha)
		if sha == "" {
			continue
		}
		for _, line := range strings.Split(body, "\n") {
			if line == needle || strings.HasPrefix(line, needle+" ") {
				return sha, true
			}
		}
	}
	return "", false
}

// gateTouchedEnv is the exported pair a declared gate command runs under:
// always both names, so a command can tell an empty diff from no gate at all.
func gateTouchedEnv(merged merge) []string {
	return []string{
		gatescope.EnvBase + "=" + merged.TouchedBase,
		gatescope.EnvHead + "=" + merged.TouchedHead,
	}
}
