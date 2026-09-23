package reconcile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The WAVE COMPOSITION (the decision, logged — tick 01u).
//
// Epic av8's wave 4 dispatched four ticks together, and three of them
// independently rewrote the same function and the same status list. The
// overlap was discovered at the MERGE GATE — after every worker had run,
// either as conflicts a person had to resolve by hand or as work handed to
// another worker, because two additions to one file are a union in INTENT,
// not in text. `.tick/learnings.md` already carried the rule; what was missing
// was the check that applies it BEFORE dispatch.
//
// THE SHAPE CHOSEN — a tick DECLARES the files it expects to touch, and the
// composition that cannot merge is REFUSED at dispatch:
//
//   - The declaration rides the same weakly typed field the tier override
//     (tick 5eq) rides: a `touch:` label. The tracker stores labels as
//     opaque strings and cannot validate them, so a malformed one is refused
//     LOUDLY, naming the tick and the label, exactly as a tier label is.
//     A declaration is a repo-relative path; one ending in `/` declares every
//     file under that directory.
//
//   - The composition is REFUSED, not silently deferred. The tracker owns
//     the waves: a reconciler that moved a tick to the next wave on its own
//     would make a planning decision the graph never recorded, and a silent
//     deferral of work nobody asked for defers is the quiet shape of every
//     failure this package refuses. The loud refusal names both ticks and
//     the file and sends the fix to the only place it can be made — the
//     ticks: re-wave one of them in the tracker, or undeclare the file on
//     one. One file, one owner per wave.
//
//   - A tick that DECLARES nothing is checked for nothing. Inferring overlap
//     from prose is the weaker half of the same idea and was not chosen: a
//     guess that wrongly refuses a wave is a dispatch nobody can audit, and
//     a guess that wrongly passes one is the wave-4 failure back again.
//
//   - The declaration has a second half, AFTER the fact: the dispatch marker
//     carries it, and the merge holds the worker to it — a file the attempt
//     touched that the declaration does not name is a refusal with its own
//     reason, never a silent merge. A tick that touches a file it did not
//     declare is the boundary violation of the declaration, and it is
//     reported the way the artifact boundary (tick p6b) is: detected,
//     recorded, refused.
//
// Wave WIDTH is declared policy ([tier_policy.concurrency], tick 5eq); wave
// COMPOSITION is the same kind of decision and this is where it lives, read
// at admission so the refusal happens at DISPATCH — when it costs nothing,
// before any worker spends a dollar or an hour.

// touchLabelPrefix is the label namespace file declarations ride in, the way
// tier overrides ride "tier:". A label is a weakly typed field the tracker
// cannot validate, so everything about a touch: label is checked here.
const touchLabelPrefix = "touch:"

// parseTouchLabels reads one tick's labels for the files it declares. It
// returns the declarations normalised and ordered, and one message per label
// it refused — never a silent skip of a label that looks wrong, because the
// tracker is where the label was written and here is the only place its
// meaning can surface.
//
// A declaration is a REPO-RELATIVE path: "./" prefixes are normalised away,
// and an empty path, an absolute path or a ".." element is refused, because a
// declaration that names nothing, or names something outside the repository,
// checks nothing and reads as though it did.
func parseTouchLabels(tick string, labels []string) (files, refused []string) {
	seen := map[string]bool{}
	for _, raw := range labels {
		declared, ok := strings.CutPrefix(raw, touchLabelPrefix)
		if !ok {
			continue // another namespace's label: not ours to read
		}
		declared = strings.TrimSpace(declared)
		declared = strings.TrimPrefix(declared, "./")
		switch {
		case declared == "":
			refused = append(refused, fmt.Sprintf("tick %s carries label %q, which declares no file: a touch: label needs a repo-relative path", tick, raw))
			continue
		case strings.HasPrefix(declared, "/"):
			refused = append(refused, fmt.Sprintf("tick %s carries label %q, which declares an absolute path: a declaration is repo-relative, and a path outside the repository checks nothing", tick, raw))
			continue
		}
		for _, part := range strings.Split(declared, "/") {
			if part == ".." {
				refused = append(refused, fmt.Sprintf("tick %s carries label %q, which declares a path leaving the repository: a declaration is repo-relative", tick, raw))
				declared = ""
				break
			}
		}
		if declared == "" || seen[declared] {
			continue
		}
		seen[declared] = true
		files = append(files, declared)
	}
	sort.Strings(files)
	return files, refused
}

// touchCovers reports whether one changed file is inside a tick's declaration.
// A declaration is an exact path, or — ending in "/" — every file under that
// directory. Both halves of the declaration are mechanical on purpose: the
// composition check and the merge's after-the-fact check read the same rule
// or they disagree about the same tick with nothing failing.
func touchCovers(declared []string, file string) bool {
	for _, path := range declared {
		if path == file {
			return true
		}
		if strings.HasSuffix(path, "/") && strings.HasPrefix(file, path) {
			return true
		}
	}
	return false
}

// checkWaveComposition refuses a plan whose waves cannot merge: two ticks of
// one wave that declare the same file. It runs at DISPATCH — at admission,
// before any tick is claimed, started or paid for — so a composition that
// would surface as merge-gate conflicts after four workers ran surfaces here
// instead, naming both ticks and the file.
//
// A run-level refusal rather than a per-tick one, because no ONE tick is at
// fault: the overlap is a fact about the pair, and the fix is at the ticks.
func (r *Reconciler) checkWaveComposition(plan []planEntry) *Refusal {
	// The declared file → the tick that owns it this wave. One file, one
	// owner per wave; the second tick to want it is the overlap.
	owner := map[int]map[string]string{}
	for _, entry := range plan {
		files, refused := parseTouchLabels(entry.TickID, entry.Labels)
		if len(refused) > 0 {
			// The loud refusal, in the tier label's shape: a label is the one
			// place a typo can ever surface, and this is it.
			return r.refuse(RefusedTouchLabel, entry.TickID, "%s", strings.Join(refused, "; "))
		}
		if len(files) == 0 {
			continue
		}
		wave, known := owner[entry.Wave]
		if !known {
			wave = map[string]string{}
			owner[entry.Wave] = wave
		}
		for _, file := range files {
			if first, taken := wave[file]; taken {
				return r.refuse(RefusedWaveOverlap, "",
					"wave %d carries two ticks that declare the same file: %s and %s both declare %s — "+
						"a wave that cannot merge is refused at dispatch, where it costs nothing, rather than "+
						"discovered at the gate after the workers have run (two additions to one file are a union "+
						"in intent, not in text; one file has one owner per wave). Re-wave one of the ticks in the "+
						"tracker, or undeclare the file on one of them",
					entry.Wave, first, entry.TickID, file)
			}
			wave[file] = entry.TickID
		}
	}
	return nil
}

// recordWaveCompositionDecision writes the composition rule into the journal
// at admission, where an operator reads it while the run can still be
// cancelled cheaply — the decision and its reasoning, stated once for any
// plan that carries a declaration, not only for the ones it refuses.
//
// It follows recordTierPolicy's convention (the other run-level policy line)
// so a person reading the run finds the two planning decisions beside each
// other.
func (r *Reconciler) recordWaveCompositionDecision(plan []planEntry) {
	declaring := 0
	for _, entry := range plan {
		if files, _ := parseTouchLabels(entry.TickID, entry.Labels); len(files) > 0 {
			declaring++
		}
	}
	if declaring == 0 {
		return
	}
	r.record("", StagePolicyStated,
		"wave composition is checked at dispatch: %d tick(s) declare the files they expect to touch with "+
			"touch: labels, two ticks of one wave declaring the same file are REFUSED before anything is "+
			"dispatched, and an attempt that touches an undeclared file is refused at the merge — the rule "+
			"applied at dispatch where it costs nothing rather than at the gate where it costs the wave",
		declaring)
}

// undeclaredTouch is the declaration's other half: the files this attempt
// changed that its tick's declaration does not name. It is asked of the diff
// the MERGE is about to act on — the collected head against the dispatched
// base — so it runs after the head is durable on origin and before anything
// merges it. A tick that declared nothing is checked for nothing (the
// declaration is opt-in); a tick that declared something is held to it.
func (r *Reconciler) undeclaredTouch(marker attemptHandle, head string) ([]string, error) {
	if len(marker.Touch) == 0 || marker.BaseSHA == "" {
		return nil, nil
	}
	out, _, err := r.git.try("", "diff", "--name-only", marker.BaseSHA, head)
	if err != nil {
		// An operational failure, not a verdict: the same reads the merge
		// needs are the ones this check needs, and a diff that cannot be
		// read stops the run rather than merging an unchecked one.
		return nil, fmt.Errorf(
			"read the files %s touched between %s and %s to check its touch: declaration: %w",
			r.attemptName(marker.TickID, marker.Attempt), short(marker.BaseSHA), short(head), err)
	}
	var undeclared []string
	for _, file := range strings.Split(strings.TrimSpace(out), "\n") {
		if file == "" || touchCovers(marker.Touch, file) {
			continue
		}
		undeclared = append(undeclared, file)
	}
	return undeclared, nil
}

// checkDeclaredTouch refuses the merge of an attempt that touched files its
// declaration does not name. It is the reporting half of a boundary the
// declaration draws: like the artifact boundary (tick p6b) it fails closed,
// records the rejection durably, and keeps the branch — the refusal is what a
// person reads next, and the branch is where they read it from.
func (r *Reconciler) checkDeclaredTouch(marker attemptHandle, head string) error {
	undeclared, err := r.undeclaredTouch(marker, head)
	if err != nil {
		return err
	}
	if len(undeclared) == 0 {
		return nil
	}
	if err := r.rejectDurably(marker, subprocess.VerdictReadyToMerge,
		"the attempt touched files its declaration does not name"); err != nil {
		return err
	}
	r.record(marker.TickID, StageRejected,
		"touched %s, which the tick's touch: declaration (%s) does not name",
		strings.Join(undeclared, ", "), strings.Join(marker.Touch, ", "))
	return r.refuse(RefusedUndeclaredTouch, marker.TickID,
		"%s touched %s, which its touch: declaration does not name (%s): a tick that declares the "+
			"files it expects to touch is held to the declaration, and an undeclared file is reported here — at "+
			"the merge, before it reaches the integration branch — rather than discovered at the next tick's "+
			"gate. Fix the declaration or the scope of the tick, and run the epic again",
		r.attemptName(marker.TickID, marker.Attempt), strings.Join(undeclared, ", "), strings.Join(marker.Touch, ", "))
}
