package runstate

import (
	"fmt"
	"maps"
	"path/filepath"

	"github.com/pengelbrecht/ticfac/internal/tempdir"
)

// One push per step (tick f61).
//
// Every write here used to be its own commit AND its own push. A push is a
// round trip to the forge, and on GitHub it is also a CI run and a share of
// a rate the forge asks a repository to keep under six a minute: a run's
// claim, note, close and the checkpoints around them came to roughly a
// dozen pushes per tick, and two live runs on one repository regularly
// went past the rate (docs/analysis/github-failures.md).
//
// A HELD store keeps writing exactly the commits it always wrote — one per
// record, in order, each with its own message and its own guard — but chains
// them locally and sends the chain as ONE push when the step is released.
// The commits are kept separate, not squashed, because the checkpoint's
// HISTORY is read back: a resumed run finds a rejection's reason and a
// settled start's refusal in the intermediate checkpoints
// (CheckpointHistory), and one commit per step would erase them.
//
// # What a held step keeps, and why it is safe
//
// Batching W1..Wn into one push changes which states a crash can leave on
// origin: per record they were {}, {W1}, {W1,W2}, ... {W1..Wn}; held they are
// {} and {W1..Wn}. Both of those were already reachable, so a resume already
// reads them. A held step therefore adds no crash state — PROVIDED nothing
// outside the run acts on a held record before it lands. That proviso is
// what decides which writes may be held:
//
//   - A sha-guarded UPDATE of the checkpoint may be held. Its answer gates no
//     effect in the step: the reconciler stops on a lost checkpoint guard
//     whenever it learns of it, and a held step that learns of it at release
//     lands NOTHING (a HeldConflict), because every later record in the step
//     was written on the strength of the checkpoint that lost.
//   - A tracker record (StageChanges) may be held, except where its caller
//     says it must be visible at once (the reconciler's claim, which must be
//     on origin before the work it claims starts).
//   - A CREATE is never held. create_if_absent's answer is the proof an
//     effect has not happened yet — a dispatch marker, a decision, evidence —
//     and the caller acts on it immediately. It lands at once, carrying
//     everything held before it in the same push; if its own guard is lost
//     it answers its conflict exactly as it would alone, and the records held
//     before it land without it (they would have landed before it was ever
//     attempted).
//   - Every other update lands at once the same way, for the same reason: a
//     caller may branch on its conflict.
//   - A terminal checkpoint lands at once: the run tag is placed on the
//     commit that carries it.
//   - Reading origin again (Fetch) inside a step re-reads origin and keeps
//     the held records on top of it: the chain is rebased onto origin's new
//     head under the same guards a release would re-examine, and a lost
//     guard is the step's HeldConflict there and then.
//
// The reconciler holds a step only where nothing between its writes acts
// outside the run, and releases it before anything does.

// TreeChange is one path a change set writes or removes: the tracker's
// records, handed to a held store so they ride in the step's push. A
// change set is unguarded — the path carries this writer's blob, last writer
// wins per path — which is the tracker writer's own rule (reconcile's
// trackerTree.treeOn).
type TreeChange struct {
	Path    string
	Mode    string
	Blob    string
	Removed bool
}

// HeldConflict is a held record whose guard was lost by the time the step
// was released. Nothing of the step landed.
type HeldConflict struct {
	Path    string
	Outcome Outcome
	Message string
}

func (c *HeldConflict) Error() string {
	return fmt.Sprintf("runstate: the held step's %q lost its guard on %s (%s): another writer advanced the run "+
		"under this one, so nothing of the step landed", c.Message, c.Path, c.Outcome)
}

// heldWrite is one record chained locally and not yet on origin.
type heldWrite struct {
	message string

	// A store record: one path, under its guard. guard is the blob origin
	// held at the path when the chain began ("" = absent), which is what the
	// per-record write would have compared against.
	path   string
	blob   string
	update bool
	guard  string

	// A change set (the tracker's): unguarded.
	changes []TreeChange

	onLanded func(commit string)
	commit   string
}

// Hold starts a step: until Release, writes that may be held are chained
// locally and not pushed.
func (s *Store) Hold() { s.holding = true }

// Holding reports whether a step is held.
func (s *Store) Holding() bool { return s.holding }

// Pending is how many records are chained and not yet on origin.
func (s *Store) Pending() int { return len(s.held) }

// Sends counts the pushes this store made to the branch — one per step, or
// one per record outside a step.
func (s *Store) Sends() int { return s.sends }

// Release ends the step and lands what it held, as one push.
func (s *Store) Release() error {
	_, err := s.flush(nil)
	s.holding = false
	return err
}

// Abandon ends the step and discards what it held, as a process that died
// before its release would. Nothing of it reaches origin.
func (s *Store) Abandon() {
	s.dropHeld()
	s.holding = false
}

// StageChanges chains a change set (the tracker's records) onto the step and
// answers the commit that carries it. held=false — or no step held — lands
// it at once, with everything held before it; the answer is then the commit
// on origin. onLanded, if given, is told the commit that carries the change
// set on origin once it lands, which after a rebuild is not the one first
// answered.
func (s *Store) StageChanges(message string, changes []TreeChange, held bool, onLanded func(commit string)) (string, error) {
	if len(changes) == 0 {
		return "", nil
	}
	if err := s.ensureFetched(); err != nil {
		return "", err
	}
	w := &heldWrite{message: message, changes: changes, onLanded: onLanded}
	if err := s.chain(w); err != nil {
		return "", err
	}
	if s.holding && held {
		return w.commit, nil
	}
	if _, err := s.flush(nil); err != nil {
		return "", err
	}
	return w.commit, nil
}

// guardFor is what origin held at a path when the chain began: the guard a
// per-record write would have compared against.
func (s *Store) guardFor(path string) string {
	if s.baseView != nil {
		return s.baseView[path]
	}
	return s.view[path]
}

// chain commits one write on top of the chain (or origin's head, for the
// first) and makes it this writer's view.
func (s *Store) chain(w *heldWrite) error {
	if len(s.held) == 0 {
		s.baseView = maps.Clone(s.view)
	}
	parent := s.head
	if s.tip != "" {
		parent = s.tip
	}
	commit, err := s.build(parent, w)
	if err != nil {
		if len(s.held) == 0 {
			s.baseView = nil
		}
		return err
	}
	w.commit, s.tip = commit, commit
	s.held = append(s.held, w)
	if w.path != "" {
		s.view[w.path] = w.blob
	}
	return nil
}

func (s *Store) build(parent string, w *heldWrite) (string, error) {
	if w.path != "" {
		return s.git.commitWithFile(parent, w.path, w.blob, w.message)
	}
	return s.git.commitWithChanges(parent, w.changes, w.message)
}

// flush pushes the chain, as one push, under the compare-and-swap every record
// in it was written against. subject is the write that asked for the flush
// (a create, or an update that may not be held); its outcome is the answer.
// It is the per-record push of before, generalised to a chain: a chain of one
// is exactly that push.
func (s *Store) flush(subject *heldWrite) (Outcome, error) {
	if len(s.held) == 0 {
		return "", nil
	}
	var lost Outcome
	base := s.head
	// pushed is every chain head this flush has sent, and uncertain says
	// whether any send failed in transit — a push whose effect on origin
	// nobody heard back about. Together they are how a lost acknowledgement
	// is told apart from a foreign write (tick o82).
	var pushed []string
	uncertain := false
	for try := 0; try < maxContendedPushes; try++ {
		tip := s.tip
		pushed = append(pushed, tip)
		_, stderr, tries, pushErr := s.git.tryCounted(nil, nil, "push",
			"--force-with-lease="+s.branchRef()+":"+base,
			s.remote, tip+":"+s.branchRef())
		if tries > 1 {
			uncertain = true
		}
		if pushErr == nil {
			s.head = tip
			return s.landHeld(subject, lost), nil
		}
		if !refusedPush(stderr) {
			s.dropHeld()
			return "", pushErr
		}

		// The lease is on the branch ref, which is coarser than the per-path
		// guards. Re-examine THOSE against origin: a genuine loss is the
		// contract's typed conflict, and a ref that merely moved for other
		// paths is a chain to rebuild.
		freshHead, freshView, err := s.peek()
		if err != nil {
			s.dropHeld()
			return "", err
		}
		// Before either: is what moved the ref THIS writer? A push that landed
		// and whose acknowledgement was lost leaves origin at this writer's
		// own chain, and the retry of it is refused against a head that is its
		// own write (epic-gvc, 2026-09-25).
		own, err := s.ownWrite(freshHead, pushed, uncertain)
		if err != nil {
			s.dropHeld()
			return "", err
		}
		if own {
			s.head, s.view = freshHead, freshView
			return s.landHeld(subject, lost), nil
		}
		if !s.guardOff {
			kept := s.held[:0:0]
			for _, w := range s.held {
				outcome := guardLost(w, freshView)
				if outcome == "" {
					kept = append(kept, w)
					continue
				}
				if w == subject {
					// The write that asked for the flush lost its own guard:
					// it answers its conflict, exactly as it would alone, and
					// what was held before it lands without it.
					lost = outcome
					continue
				}
				// A held record lost its guard, and every record after it in
				// the step was written on the strength of it: none of it lands.
				// The writer's view stays what it fetched, exactly as a
				// refused record's always has — only a fetch refreshes it.
				s.dropHeld()
				return "", &HeldConflict{Path: w.path, Outcome: outcome, Message: w.message}
			}
			s.held = kept
			if len(kept) == 0 {
				// The subject alone, refused: what a single record's refusal
				// always was, view and head untouched.
				s.dropHeld()
				return lost, nil
			}
		}
		// Every guard still holds, so the ref moved for other paths. Take the
		// whole fresh view with the new base, and rebuild the chain on it.
		s.head, base = freshHead, freshHead
		s.view, s.baseView = freshView, maps.Clone(freshView)
		held := s.held
		s.held, s.tip = nil, ""
		for _, w := range held {
			if err := s.chain(w); err != nil {
				s.dropHeld()
				return "", err
			}
		}
	}
	s.dropHeld()
	return "", fmt.Errorf("runstate: a step of records on %s: origin's branch moved under this writer %d times running; "+
		"that is an operational problem, not a conflict to spin on", s.branch, maxContendedPushes)
}

// refreshHeld is Fetch inside a held step: origin re-read, and the chain
// rebased onto what it now holds, so the writer sees origin's records AND its
// own. Nothing is pushed.
func (s *Store) refreshHeld() error {
	freshHead, freshView, err := s.peek()
	if err != nil {
		return err
	}
	s.fetched = true
	if freshHead == s.head {
		return nil
	}
	if !s.guardOff {
		for _, w := range s.held {
			if outcome := guardLost(w, freshView); outcome != "" {
				// The step is lost; the fetch the caller asked for still
				// happened, so the view is origin's.
				s.dropHeld()
				s.head, s.view = freshHead, freshView
				return &HeldConflict{Path: w.path, Outcome: outcome, Message: w.message}
			}
		}
	}
	held := s.held
	s.head, s.view = freshHead, freshView
	s.held, s.tip, s.baseView = nil, "", nil
	for _, w := range held {
		if err := s.chain(w); err != nil {
			s.dropHeld()
			return err
		}
	}
	return nil
}

// guardLost is the conflict a held record's guard answers against a fresh
// view of origin, or "" when it still holds. A change set has no guard.
func guardLost(w *heldWrite, fresh map[string]string) Outcome {
	if w.path == "" {
		return ""
	}
	current, exists := fresh[w.path]
	if w.update {
		if current != w.guard {
			return ConflictStaleSHA
		}
		return ""
	}
	if exists {
		return ConflictExists
	}
	return ""
}

// landHeld records that the chain is on origin: every record in it exists
// now, and each change set is told the commit that carries it.
func (s *Store) landHeld(subject *heldWrite, lost Outcome) Outcome {
	held := s.held
	s.held, s.tip, s.baseView = nil, "", nil
	s.sends++
	for _, w := range held {
		if w.path != "" {
			s.pushes++
		}
	}
	for _, w := range held {
		if w.onLanded != nil {
			w.onLanded(w.commit)
		}
	}
	switch {
	case lost != "":
		return lost
	case subject == nil:
		return ""
	case subject.update:
		return Updated
	default:
		return Created
	}
}

// dropHeld discards the chain, and with it this writer's belief in its own
// held records: the view goes back to what origin held when the chain began.
func (s *Store) dropHeld() {
	if s.baseView != nil {
		s.view = s.baseView
	}
	s.held, s.tip, s.baseView = nil, "", nil
}

// readHead is the commit this writer's reads are answered from: its own chain
// when it holds one, origin's head as last seen otherwise.
func (s *Store) readHead() string {
	if s.tip != "" {
		return s.tip
	}
	return s.head
}

// commitWithChanges builds a commit that is `base` plus a change set, in a
// throwaway index, the way commitWithFile does for one record.
func (g *git) commitWithChanges(base string, changes []TreeChange, message string) (string, error) {
	indexDir, removeIndex, err := tempdir.Make("ticfac-index-")
	if err != nil {
		return "", err
	}
	defer removeIndex()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(indexDir, "index")}
	if base != "" {
		if _, err := g.runWith(env, "read-tree", base); err != nil {
			return "", err
		}
	}
	for _, c := range changes {
		if c.Removed {
			if _, err := g.runWith(env, "update-index", "--force-remove", "--", c.Path); err != nil {
				return "", err
			}
			continue
		}
		if _, err := g.runWith(env, "update-index", "--add", "--cacheinfo", c.Mode+","+c.Blob+","+c.Path); err != nil {
			return "", err
		}
	}
	tree, err := g.runWith(env, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree}
	if base != "" {
		args = append(args, "-p", base)
	}
	args = append(args, "-m", message)
	return g.run(args...)
}
