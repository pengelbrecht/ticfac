package runstate

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxContendedPushes bounds the rebuild-on-a-lost-lease loop.
//
// A lost lease is not a conflict: the branch ref moved for some OTHER path, and
// the per-path guard the contract states still holds. The commit is rebuilt on
// the new head and pushed again. That is bounded, because a ref moving forever
// under a writer is an operational problem to report, not to spin on.
const maxContendedPushes = 8

// Options configures a Store.
type Options struct {
	// Repo is a git repository the store may run in. The store never reads or
	// writes a working tree: it builds commits with plumbing over the fetched
	// origin commit. The one local ref it moves is Branch, and only from
	// CommitLocal, so the repository must not have Branch checked out.
	Repo string
	// Remote is the remote holding the durable authority. Default "origin".
	Remote string
	// Branch is the EpicRun integration branch — the churn branch the run's
	// records land on, one commit per write.
	Branch string
	// RunID identifies the run, and names its directory and its tag.
	RunID string

	AuthorName  string
	AuthorEmail string

	// Now stamps records that carry no time of their own. Default time.Now.
	Now func() time.Time
}

// Store reads and writes one run's `.ticfac/` records against origin.
//
// It is not safe for concurrent use by several goroutines; it IS safe against
// other processes, which is the point — the guard is a compare-and-swap on a
// shared ref, not a lock any of them could lose.
type Store struct {
	git    *git
	remote string
	branch string
	runID  string
	now    func() time.Time

	// view is this writer's fetched view of origin: path -> blob sha. Nothing
	// refreshes it but a fetch, an observe, or this writer's own successful
	// push of that one path. It is the only thing a sha guard may compare
	// against, and it is stale by construction — which is what makes a stale
	// writer's update refusable.
	view    map[string]string
	head    string
	fetched bool
	pushes  int

	// guardOff disables the compare-and-swap for the negative control. A CAS
	// that has stopped guarding does not raise: it lets a second reconciler
	// dispatch the same attempt, and the run pays for both jobs.
	guardOff bool
}

// Open prepares a store. It makes no network call: a writer that has not
// fetched has no view of origin, and the contract requires that to be visible
// rather than papered over.
func Open(o Options) (*Store, error) {
	if o.Repo == "" {
		return nil, fmt.Errorf("runstate: no repository")
	}
	if o.Branch == "" {
		return nil, fmt.Errorf("runstate: no integration branch: run state lands on the EpicRun integration branch")
	}
	if err := checkSegment("run id", o.RunID); err != nil {
		return nil, fmt.Errorf("runstate: %w", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("runstate: git is not on the path: %w", err)
	}
	s := &Store{
		remote: or(o.Remote, "origin"),
		branch: o.Branch,
		runID:  o.RunID,
		now:    o.Now,
		view:   map[string]string{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.git = newGit(o.Repo, or(o.AuthorName, "ticfac"), or(o.AuthorEmail, "ticfac@example.com"))
	if _, err := s.git.run("rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("runstate: %s is not a git repository: %w", o.Repo, err)
	}
	return s, nil
}

// RunID is the run this store reads and writes.
func (s *Store) RunID() string { return s.runID }

// Head is origin's branch commit as this writer last saw it.
func (s *Store) Head() string { return s.head }

// Pushes counts the writes this store landed on origin. Durable means pushed,
// so this is the count of records that exist.
func (s *Store) Pushes() int { return s.pushes }

// ----------------------------------------------------------- the CAS ---
//
// The five operations of contracts/ticfac-run-state.json's `cas.fake.ops`,
// against real git. The fake models them in memory so the sequences are
// executable in any language; these are the same rules over a repository.

// Fetch refreshes this writer's view of origin.
func (s *Store) Fetch() (Outcome, error) {
	head, view, err := s.peek()
	if err != nil {
		return "", err
	}
	s.head, s.view, s.fetched = head, view, true
	return Fetched, nil
}

// Observe is a poll that learns nothing: it refreshes the view and writes
// nothing. Checkpoint on state change, not on observation — at ten checkpoints
// an hour the cost is negligible, and at one commit per poll it is not.
func (s *Store) Observe() (Outcome, error) {
	if _, err := s.Fetch(); err != nil {
		return "", err
	}
	return NoChange, nil
}

// CommitLocal writes into the local repository only. Origin is untouched, so
// the record does not exist: durable means pushed. It moves the local branch
// ref, which is why the store must not be pointed at a repository with that
// branch checked out.
func (s *Store) CommitLocal(path string, content []byte) (Outcome, error) {
	if err := checkPath(path); err != nil {
		return "", err
	}
	base, err := s.localHead()
	if err != nil {
		return "", err
	}
	blob, err := s.git.writeBlob(content)
	if err != nil {
		return "", err
	}
	commit, err := s.git.commitWithFile(base, path, blob, s.message("write", path))
	if err != nil {
		return "", err
	}
	if _, err := s.git.run("update-ref", s.branchRef(), commit); err != nil {
		return "", err
	}
	return LocalOnly, nil
}

// CreateIfAbsent commits and pushes in one operation, guarded on the path being
// absent from origin. The file's existence is the proof the effect already
// happened, so ConflictExists means another reconciler already did it and this
// one must not.
func (s *Store) CreateIfAbsent(path string, content []byte) (Outcome, error) {
	return s.put(path, content, false)
}

// UpdateIfSHA commits and pushes in one operation, guarded on origin's sha for
// the path equalling the one this writer fetched. ConflictStaleSHA means the
// writer's view of the run is stale: re-fetch and reconcile from what is
// actually there, never retry blindly.
func (s *Store) UpdateIfSHA(path string, content []byte) (Outcome, error) {
	return s.put(path, content, true)
}

func (s *Store) put(path string, content []byte, update bool) (Outcome, error) {
	if err := checkPath(path); err != nil {
		return "", err
	}
	if !update && !s.fetched {
		// A create needs origin's head to build on, and the guard is then
		// evaluated against what that fetch found.
		if _, err := s.Fetch(); err != nil {
			return "", err
		}
	}
	if !s.guardOff {
		_, haveBase := s.view[path]
		switch {
		case update && !haveBase:
			// A writer that never read origin cannot compare against it.
			return ConflictMissingBase, nil
		case !update && haveBase:
			return ConflictExists, nil
		}
	}
	if !s.fetched {
		// Only reachable with the guard off, and the base still has to be
		// origin's.
		if _, err := s.Fetch(); err != nil {
			return "", err
		}
	}

	blob, err := s.git.writeBlob(content)
	if err != nil {
		return "", err
	}
	verb := "create"
	if update {
		verb = "update"
	}

	base := s.head
	for try := 0; try < maxContendedPushes; try++ {
		commit, err := s.git.commitWithFile(base, path, blob, s.message(verb, path))
		if err != nil {
			return "", err
		}
		_, stderr, pushErr := s.git.try(nil, nil, "push",
			"--force-with-lease="+s.branchRef()+":"+base,
			s.remote, commit+":"+s.branchRef())
		if pushErr == nil {
			// A successful push refreshes the WRITER's view of the path it
			// wrote, and no other actor's.
			s.head = commit
			s.view[path] = blob
			s.pushes++
			if update {
				return Updated, nil
			}
			return Created, nil
		}
		if !refusedPush(stderr) {
			return "", pushErr
		}

		// The lease is on the branch ref, which is coarser than the per-path
		// guard. Re-examine THAT guard against origin: a genuine loss is the
		// contract's typed conflict, and a ref that merely moved for another
		// path is a commit to rebuild.
		freshHead, freshView, err := s.peek()
		if err != nil {
			return "", err
		}
		if !s.guardOff {
			if update {
				if freshView[path] != s.view[path] {
					return ConflictStaleSHA, nil
				}
			} else if _, exists := freshView[path]; exists {
				return ConflictExists, nil
			}
		}
		// The guard still holds, so the ref moved for some other path. Take
		// the whole fresh view with the new base: a writer that kept the old
		// one would know a NEWER ref and an OLDER set of paths, and its next
		// create would sail past a lease that has nothing to say about paths.
		s.head, s.view, base = freshHead, freshView, freshHead
	}
	return "", fmt.Errorf("runstate: %s on %s: origin's %s moved under this writer %d times running; "+
		"that is an operational problem, not a conflict to spin on",
		verb, path, s.branch, maxContendedPushes)
}

// peek reads origin without touching this writer's view. Refreshing the view
// here would hand a stale writer a fresh base it never fetched, which is
// exactly the overwrite the guard exists to refuse.
func (s *Store) peek() (head string, view map[string]string, err error) {
	if _, err := s.git.run("fetch", s.remote, s.branch); err != nil {
		return "", nil, fmt.Errorf("runstate: fetch %s %s: %w", s.remote, s.branch, err)
	}
	head, err = s.git.run("rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", nil, err
	}
	view, err = s.git.lsTree(head, Root)
	if err != nil {
		return "", nil, err
	}
	return head, view, nil
}

// Read returns the content of a record as this writer last fetched it, and
// whether it is there at all.
func (s *Store) Read(path string) ([]byte, bool, error) {
	sha, ok := s.view[path]
	if !ok {
		return nil, false, nil
	}
	content, err := s.git.catFile(sha)
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

func (s *Store) message(verb, path string) string {
	return fmt.Sprintf("ticfac run %s: %s %s", s.runID, verb, path)
}

// branchRef is the integration branch as a full ref name. Origin's copy and
// this repository's copy have the same name and are emphatically not the same
// thing: the guard is against the first, and CommitLocal moves the second.
func (s *Store) branchRef() string { return refFor(s.branch) }

func (s *Store) localHead() (string, error) {
	out, _, err := s.git.try(nil, nil, "rev-parse", "--verify", "--quiet", s.branchRef())
	if err == nil && out != "" {
		return out, nil
	}
	// No local branch yet: a local commit still has to build on something, and
	// origin as last fetched is the only base this store knows.
	if s.head == "" {
		if _, err := s.Fetch(); err != nil {
			return "", err
		}
	}
	return s.head, nil
}

func refFor(branch string) string {
	if strings.HasPrefix(branch, "refs/") {
		return branch
	}
	return "refs/heads/" + branch
}

func checkPath(path string) error {
	if !strings.HasPrefix(path, Root+"/") {
		return fmt.Errorf("runstate: %q is outside %s/, which is the only place this store writes", path, Root)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." || segment == "" {
			return fmt.Errorf("runstate: %q is not a path this store will write", path)
		}
	}
	return nil
}

// -------------------------------------------------------- the records ---

// PutCheckpoint writes the run's checkpoint, and only on a state change.
//
// The sequence is assigned by the store unless the caller sets it, and a
// sequence that did not move is refused: a checkpoint whose sequence did not
// move is an observation, and an observation writes nothing. The first write is
// a create; every later one is guarded on the sha this writer fetched, so a
// stale writer is refused rather than overwriting whoever advanced the run.
//
// A terminal state also places the run tag.
func (s *Store) PutCheckpoint(c Checkpoint) (Outcome, error) {
	if err := s.ensureFetched(); err != nil {
		return "", err
	}
	previous, exists, err := s.Checkpoint()
	if err != nil {
		return "", err
	}

	c.SchemaVersion = SchemaVersion
	if c.RunID == "" {
		c.RunID = s.runID
	}
	if c.RunID != s.runID {
		return "", fmt.Errorf("runstate: checkpoint names run %q in run %q's directory", c.RunID, s.runID)
	}
	if exists {
		if !c.isStateChangeFrom(*previous) {
			// An observation. Nothing is written — but a run that is already
			// terminal still gets its tag, so a restart cannot leave the
			// history unreachable.
			if previous.State.Terminal() {
				if err := s.ensureRunTag(); err != nil {
					return "", err
				}
			}
			return NoChange, nil
		}
		switch {
		case c.Sequence == 0:
			c.Sequence = previous.Sequence + 1
		case c.Sequence <= previous.Sequence:
			return "", fmt.Errorf("runstate: checkpoint sequence %d does not move past %d; "+
				"a checkpoint whose sequence did not move is an observation", c.Sequence, previous.Sequence)
		}
	} else if c.Sequence == 0 {
		c.Sequence = 1
	}
	if c.UpdatedAt == "" {
		c.UpdatedAt = s.stamp()
	}
	if err := c.Validate(); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	content, err := encodeRecord(c)
	if err != nil {
		return "", err
	}

	path := CheckpointPath(s.runID)
	var outcome Outcome
	if exists {
		outcome, err = s.UpdateIfSHA(path, content)
	} else {
		outcome, err = s.CreateIfAbsent(path, content)
	}
	if err != nil {
		return "", err
	}
	if outcome.EffectPermitted() && c.State.Terminal() {
		if err := s.ensureRunTag(); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// isStateChangeFrom reports whether this checkpoint says something new. What a
// checkpoint claims is the run's state, why, and where each tick stands; a
// record differing only in its timestamp is the poll the contract refuses to
// commit.
func (c Checkpoint) isStateChangeFrom(previous Checkpoint) bool {
	if c.State != previous.State || c.Reason != previous.Reason || len(c.Ticks) != len(previous.Ticks) {
		return true
	}
	for i := range c.Ticks {
		if c.Ticks[i] != previous.Ticks[i] {
			return true
		}
	}
	return false
}

// PutAttempt writes the dispatch marker. Its existence IS the idempotency
// marker: ConflictExists means another reconciler already dispatched this
// attempt, and the caller must NOT start a job.
func (s *Store) PutAttempt(a Attempt) (Outcome, error) {
	a.SchemaVersion = SchemaVersion
	if err := checkIndex("attempt", a.Attempt); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := a.Validate(); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := s.checkRun(a.Provenance); err != nil {
		return "", err
	}
	content, err := encodeRecord(a)
	if err != nil {
		return "", err
	}
	return s.CreateIfAbsent(AttemptPath(s.runID, a.Attempt), content)
}

// PutDecision writes one validated role-job exchange. Create-if-absent: a
// decision is a thing a model was paid for once, and a replay after a restart
// must not overwrite it.
func (s *Store) PutDecision(d Decision) (Outcome, error) {
	d.SchemaVersion = SchemaVersion
	if err := checkIndex("decision", d.Decision); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := d.Validate(); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := s.checkRun(d.Provenance); err != nil {
		return "", err
	}
	content, err := encodeRecord(d)
	if err != nil {
		return "", err
	}
	return s.CreateIfAbsent(DecisionPath(s.runID, d.Decision), content)
}

// PutEvidence places one evidence record, at the key it names itself.
// Create-if-absent: evidence is a record of something that already happened,
// and a record that can be overwritten is not evidence.
func (s *Store) PutEvidence(e Evidence) (Outcome, error) {
	e.SchemaVersion = SchemaVersion
	if err := e.Validate(); err != nil {
		return "", fmt.Errorf("runstate: %w", err)
	}
	if err := s.checkRun(e.Provenance); err != nil {
		return "", err
	}
	content, err := encodeRecord(e)
	if err != nil {
		return "", err
	}
	return s.CreateIfAbsent(EvidencePath(s.runID, e.Key), content)
}

func (s *Store) checkRun(p Provenance) error {
	if p.RunID != s.runID {
		return fmt.Errorf("runstate: a record whose provenance names run %q cannot live in run %q's directory",
			p.RunID, s.runID)
	}
	return nil
}

func (s *Store) stamp() string { return s.now().UTC().Format(time.RFC3339) }

func (s *Store) ensureFetched() error {
	if s.fetched {
		return nil
	}
	_, err := s.Fetch()
	return err
}

// --------------------------------------------------------- the reader ---
//
// What a restarted reconciler reads: recovery is a fetch, and then this.

// Checkpoint returns the run's checkpoint as this writer last fetched it.
func (s *Store) Checkpoint() (*Checkpoint, bool, error) {
	var c Checkpoint
	ok, err := s.load(CheckpointPath(s.runID), &c)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &c, true, nil
}

// Attempt returns one dispatch marker.
func (s *Store) Attempt(n int) (*Attempt, bool, error) {
	if err := checkIndex("attempt", n); err != nil {
		return nil, false, fmt.Errorf("runstate: %w", err)
	}
	var a Attempt
	ok, err := s.load(AttemptPath(s.runID, n), &a)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &a, true, nil
}

// Decision returns one role-job exchange.
func (s *Store) Decision(n int) (*Decision, bool, error) {
	if err := checkIndex("decision", n); err != nil {
		return nil, false, fmt.Errorf("runstate: %w", err)
	}
	var d Decision
	ok, err := s.load(DecisionPath(s.runID, n), &d)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &d, true, nil
}

// Evidence returns one evidence record by its key.
func (s *Store) Evidence(key string) (*Evidence, bool, error) {
	if err := checkSegment("evidence key", key); err != nil {
		return nil, false, fmt.Errorf("runstate: %w", err)
	}
	var e Evidence
	ok, err := s.load(EvidencePath(s.runID, key), &e)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &e, true, nil
}

// Attempts returns every dispatch marker in the run, in attempt order.
func (s *Store) Attempts() ([]Attempt, error) {
	out := []Attempt{}
	for _, n := range s.numbered("attempts") {
		a, ok, err := s.Attempt(n)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, *a)
		}
	}
	return out, nil
}

// Decisions returns every decision in the run, in decision order.
func (s *Store) Decisions() ([]Decision, error) {
	out := []Decision{}
	for _, n := range s.numbered("decisions") {
		d, ok, err := s.Decision(n)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, *d)
		}
	}
	return out, nil
}

// EvidenceKeys returns the keys of the run's evidence, sorted.
func (s *Store) EvidenceKeys() []string {
	prefix := RunDir(s.runID) + "/evidence/"
	keys := []string{}
	for path := range s.view {
		if name, ok := strings.CutPrefix(path, prefix); ok {
			if key, ok := strings.CutSuffix(name, ".json"); ok && !strings.Contains(key, "/") {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

func (s *Store) numbered(dir string) []int {
	prefix := RunDir(s.runID) + "/" + dir + "/"
	numbers := []int{}
	for path := range s.view {
		name, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		digits, ok := strings.CutSuffix(name, ".json")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(digits); err == nil && n >= 1 {
			numbers = append(numbers, n)
		}
	}
	sort.Ints(numbers)
	return numbers
}

func (s *Store) load(path string, record any) (bool, error) {
	raw, ok, err := s.Read(path)
	if err != nil || !ok {
		return ok, err
	}
	if err := decodeRecord(raw, record); err != nil {
		return false, fmt.Errorf("runstate: %s: %w", path, err)
	}
	return true, nil
}

// ------------------------------------------------------------ the tag ---

// ensureRunTag places `ticfac/run-<run-id>` on origin at the run's terminal
// commit, so the full history stays reachable for a post-mortem without living
// in the target's log. It is idempotent: a tag already on origin is left where
// it is, because a restart replaying a terminal checkpoint must not move it.
func (s *Store) ensureRunTag() error {
	sha, placed, err := s.RunTag()
	if err != nil {
		return err
	}
	if placed {
		_ = sha
		return nil
	}
	tag := TagName(s.runID)
	if _, err := s.git.run("tag", "-f", tag, s.head); err != nil {
		return err
	}
	if _, _, err := s.git.try(nil, nil, "push", s.remote, "refs/tags/"+tag); err != nil {
		// Another reconciler may have placed it between the check and the
		// push. The tag existing is the outcome either way.
		if _, placed, checkErr := s.RunTag(); checkErr == nil && placed {
			return nil
		}
		return err
	}
	return nil
}

// RunTag reports the run tag on origin: its sha, and whether it is there. This
// is read from the remote, because a tag that exists only locally records
// nothing durable.
func (s *Store) RunTag() (string, bool, error) {
	ref := "refs/tags/" + TagName(s.runID)
	out, err := s.git.run("ls-remote", "--tags", s.remote, ref)
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(line, "\t")
		if ok && strings.TrimSuffix(name, "^{}") == ref {
			return sha, true, nil
		}
	}
	return "", false, nil
}

func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
