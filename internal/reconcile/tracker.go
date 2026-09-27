package reconcile

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The tracker's writes, made DURABLE.
//
// A claim, a note and a close are effects on the tracker, and the tracker's
// records are files: `.tick/` of the repository they are about. Running tk in
// the reconciler's own checkout leaves them as uncommitted edits on whatever
// that checkout has out — main — and a record that exists only in a working
// tree is a record nobody else can read. That is not a theoretical hole: a
// Phase 1 gate run closed two ticks behind their gates, origin still said open,
// and the next wave's worker read `.tick/issues/<blocker>.json` from the
// integration branch it had branched from, found it open, and answered BLOCKED.
//
// So this package holds the same rule for the tracker that the run-state store
// holds for `.ticfac/`: DURABLE MEANS PUSHED.
//
//   - tk runs in a DETACHED worktree of its own, at the integration branch as
//     origin has it. The reconciler's checkout is never written, and main is
//     never written.
//   - every write is followed IN THE SAME STEP by a commit of `.tick/` onto the
//     integration branch and a push, under the same compare-and-swap the store
//     uses: `push --force-with-lease`, a refused lease re-examined against
//     origin, the commit rebuilt on the new head, and a bounded number of
//     rebuilds rather than a spin.
//   - the commit carries exactly the `.tick/` paths this write changed,
//     applied onto origin's current head with plumbing. A writer that pushed a
//     whole subtree would clobber what a closeout wrote into `.tick/` between
//     its own read and its own write.
//
// The worktree is detached for integrate.go's reason as well as this one: the
// integration branch must not be CHECKED OUT anywhere, because the run-state
// store moves that ref under whoever holds it.

// trackerRoot is the only path this package's tracker commits touch. The
// tracker is one of the run's two authorities and `.tick/` is where it keeps
// its records; nothing else in the tree is any of a tracker write's business.
const trackerRoot = ".tick"

// trackerRecordPath is where the tracker keeps one record — epic or tick —
// in a checkout it reads: `.tick/issues/<id>.json`, the layout the durable
// tracker commits against, the fold merges through tk's drivers, and tk
// itself opens (the first per-tick Cloudflare smoke run failed on exactly
// this file). It is evidence about a TREE, not a tracker API: reading it
// answers "does this record exist here", which no manifest command asks.
func trackerRecordPath(dir, id string) string {
	return filepath.Join(dir, trackerRoot, "issues", id+".json")
}

// maxTrackerPushes bounds the rebuild-on-a-lost-lease loop, for
// internal/runstate's reason: a ref moving forever under a writer is an
// operational problem to report, not a conflict to spin on.
const maxTrackerPushes = 8

// trackerTree is the checkout the tracker runs in, and the writer that makes
// what the tracker wrote there a record on origin.
type trackerTree struct {
	git    *repoGit
	remote string
	branch string
	runID  string

	dir    string
	remove func()

	// base is the commit this worktree is synced to — the one a change set is
	// computed against. It is not necessarily origin's head: origin moves under
	// this writer whenever the store records anything, which is what publish
	// rebuilds for.
	base string

	// pushes counts the tracker writes that reached origin. Durable means
	// pushed, so this is the number of tracker records that exist.
	pushes int
}

// openTrackerTree prepares the tracker's worktree at the integration branch's
// head as origin has it.
func openTrackerTree(g *repoGit, remote, branch, runID string) (*trackerTree, error) {
	t := &trackerTree{git: g, remote: remote, branch: branch, runID: runID}
	head, err := t.originHead()
	if err != nil {
		return nil, err
	}
	dir, remove, err := g.tempWorktree("ticfac-tracker-", head)
	if err != nil {
		return nil, err
	}
	t.dir, t.remove, t.base = dir, remove, head
	return t, nil
}

// close removes the worktree. Nothing is lost with it: everything the tracker
// wrote there was pushed as it was written.
func (t *trackerTree) close() {
	if t != nil && t.remove != nil {
		t.remove()
		t.remove = nil
	}
}

// originHead is the integration branch as origin has it, FETCHED rather than
// merely read: the value this writer leases against has to be a commit this
// repository holds, or the worktree could not be moved to it.
func (t *trackerTree) originHead() (string, error) {
	// A PRIVATE ref, never FETCH_HEAD — and this reader matters more than the
	// store's, because sync() MOVES THE WORKTREE to what this returns. FETCH_HEAD
	// is one file in .git shared by every process using the checkout, so a
	// concurrent fetch can hand this the head of another branch; the tracker
	// would then walk its worktree onto that tree and publish claims, notes and
	// closes against the wrong base. sync() refuses to move over UNCOMMITTED
	// records, but a clean worktree — the normal state between publishes — moves
	// silently. See ticfac tick wdb.
	ref := refFor("refs/ticfac/peek/tracker/" + t.git.fetch1D() + "/" + t.runID)
	if _, err := t.git.run("", "fetch", "--quiet", "--no-write-fetch-head", "--refmap=", t.remote,
		"+"+refFor(t.branch)+":"+ref); err != nil {
		return "", fmt.Errorf("fetch %s from %s: %w", t.branch, t.remote, err)
	}
	return t.git.run("", "rev-parse", ref)
}

// sync moves the worktree to origin's head, so that what tk reads next is what
// origin says and not what this worktree remembers.
//
// It refuses to move over uncommitted tracker records rather than discarding
// them. Every write publishes before it returns, so a dirty tree here is a bug
// in this package, and losing a close to it silently is the failure this whole
// file exists to remove.
func (t *trackerTree) sync() error {
	head, err := t.originHead()
	if err != nil {
		return err
	}
	if head == t.base {
		return nil
	}
	if dirty, err := t.dirty(); err != nil {
		return err
	} else if dirty != "" {
		return fmt.Errorf("the tracker's worktree carries tracker records that were never pushed (%s): "+
			"moving it to %s would discard them", firstLine(dirty), short(head))
	}
	if _, err := t.git.run(t.dir, "reset", "--hard", "--quiet", head); err != nil {
		return err
	}
	t.base = head
	return nil
}

// dirty is what the tracker has written into the worktree and nobody has
// committed, as git reports it.
func (t *trackerTree) dirty() (string, error) {
	return t.git.run(t.dir, "status", "--porcelain", "-uall", "--", trackerRoot)
}

// change is one `.tick/` path a tracker write touched.
type change struct {
	Path    string
	Mode    string
	Blob    string
	Removed bool
}

// publish commits what the tracker wrote and pushes it to the integration
// branch on origin, under the store's compare-and-swap.
//
// It answers with the commit it landed, or with "" when the write changed
// nothing on disk — a note the tracker recorded elsewhere, or a close that had
// already been made.
func (t *trackerTree) publish(reason string) (string, error) {
	changes, err := t.staged()
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "", nil
	}

	for try := 0; try < maxTrackerPushes; try++ {
		head, err := t.originHead()
		if err != nil {
			return "", err
		}
		tree, err := t.treeOn(head, changes)
		if err != nil {
			return "", err
		}
		headTree, err := t.git.run("", "rev-parse", head+"^{tree}")
		if err != nil {
			return "", err
		}
		if tree == headTree {
			// The branch already carries exactly these records — this run's
			// earlier incarnation pushed them, or another writer did. Nothing
			// is written twice.
			return head, t.landed(head)
		}
		commit, err := t.git.run("", "commit-tree", tree, "-p", head, "-m",
			fmt.Sprintf("ticfac run %s: %s", t.runID, reason))
		if err != nil {
			return "", err
		}
		_, stderr, pushErr := t.git.try("", "push",
			"--force-with-lease="+refFor(t.branch)+":"+head,
			t.remote, commit+":"+refFor(t.branch))
		if pushErr == nil {
			t.pushes++
			return commit, t.landed(commit)
		}
		if !leaseRefused(stderr) {
			return "", pushErr
		}
		// The lease is on the branch ref, and the branch moved for some other
		// path — the store's own records land on it constantly. Rebuild this
		// write's paths on the new head rather than forcing over what arrived.
	}
	return "", fmt.Errorf("%s moved under the tracker's writer %d times running while publishing %q; "+
		"that is an operational problem, not a conflict to spin on", t.branch, maxTrackerPushes, reason)
}

// landed moves the worktree onto the commit that now carries its records, so
// the next write's change set is computed against what origin has.
func (t *trackerTree) landed(commit string) error {
	if _, err := t.git.run(t.dir, "reset", "--hard", "--quiet", commit); err != nil {
		return err
	}
	t.base = commit
	return nil
}

// staged is the set of `.tick/` paths that differ between the worktree and the
// commit it was synced from, with the blobs written into the object database.
//
// The index it uses is a throwaway: the worktree the tracker runs in has an
// index of its own, and a writer that rewrote it would be a writer that leaves
// its own checkout in a state nobody asked for.
func (t *trackerTree) staged() ([]change, error) {
	if _, err := os.Stat(filepath.Join(t.dir, trackerRoot)); err != nil {
		// No `.tick/` at all: nothing the tracker could have written.
		return nil, nil
	}
	index, done, err := tempIndex()
	if err != nil {
		return nil, err
	}
	defer done()

	env := []string{"GIT_INDEX_FILE=" + index}
	if _, _, err := t.git.tryEnv(t.dir, env, "read-tree", t.base); err != nil {
		return nil, err
	}
	if _, _, err := t.git.tryEnv(t.dir, env, "add", "-A", "--", trackerRoot); err != nil {
		return nil, err
	}
	tree, _, err := t.git.tryEnv(t.dir, env, "write-tree")
	if err != nil {
		return nil, err
	}
	raw, _, err := t.git.tryEnv(t.dir, env, "diff-tree", "-r", "-z", t.base, tree)
	if err != nil {
		return nil, err
	}
	return parseRawDiff(raw)
}

// treeOn applies a change set onto another commit's tree and returns the tree
// it produced. Applying the PATHS rather than replacing the subtree is what
// keeps a tracker write from clobbering whatever else landed in OTHER `.tick/`
// paths between this writer's read and its push.
//
// What it does not do — and at concurrency one does not have to — is merge two
// writes to the SAME path. This writer's blob for a path it touched replaces
// whatever the head it rebuilt onto has there: last writer wins, per path. One
// epic at concurrency one has exactly one tracker writer, so the only writes
// that can race are this run's own, sequenced; a second concurrent writer of
// the same tick record would need a merge this function deliberately does not
// invent.
func (t *trackerTree) treeOn(commit string, changes []change) (string, error) {
	index, done, err := tempIndex()
	if err != nil {
		return "", err
	}
	defer done()

	env := []string{"GIT_INDEX_FILE=" + index}
	if _, _, err := t.git.tryEnv(t.dir, env, "read-tree", commit); err != nil {
		return "", err
	}
	for _, c := range changes {
		if c.Removed {
			if _, _, err := t.git.tryEnv(t.dir, env, "update-index", "--force-remove", "--", c.Path); err != nil {
				return "", err
			}
			continue
		}
		if _, _, err := t.git.tryEnv(t.dir, env, "update-index", "--add",
			"--cacheinfo", c.Mode+","+c.Blob+","+c.Path); err != nil {
			return "", err
		}
	}
	tree, _, err := t.git.tryEnv(t.dir, env, "write-tree")
	return tree, err
}

// parseRawDiff reads `git diff-tree -r -z`'s raw output: for each path, a
// metadata record and the path itself, each NUL-terminated. It is read with -z
// rather than in the default format because a path git had to quote is a path
// this reader would otherwise get wrong.
func parseRawDiff(raw string) ([]change, error) {
	fields := strings.Split(raw, "\x00")
	var changes []change
	for i := 0; i+1 < len(fields); i += 2 {
		meta, path := fields[i], fields[i+1]
		if meta == "" && path == "" {
			continue
		}
		parts := strings.Fields(strings.TrimPrefix(meta, ":"))
		if len(parts) < 5 || path == "" {
			return nil, fmt.Errorf("git diff-tree wrote a record this reader cannot parse: %q %q", meta, path)
		}
		mode, blob, status := parts[1], parts[3], parts[4]
		changes = append(changes, change{
			Path: path, Mode: mode, Blob: blob,
			Removed: status == "D" || mode == "000000",
		})
	}
	return changes, nil
}

// tempIndex is a git index file outside every repository, and the one function
// that removes it.
func tempIndex() (path string, done func(), err error) {
	dir, err := os.MkdirTemp("", "ticfac-tracker-index-")
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(dir, "index"), func() { _ = os.RemoveAll(dir) }, nil
}

// leaseRefused reports whether a failed push was the REMOTE refusing the update
// — the compare-and-swap doing its job — rather than git failing to run.
//
// It is the same classification internal/runstate makes over the same strings,
// and it is made again here rather than shared because the two are answers to
// two different questions: the store re-examines a per-path guard, and this
// writer rebuilds a change set. What they must not become is one function that
// decides both.
func leaseRefused(stderr string) bool {
	for _, marker := range []string{"[rejected]", "stale info", "non-fast-forward", "fetch first", "cannot lock ref"} {
		if strings.Contains(stderr, marker) {
			return true
		}
	}
	return false
}

// --------------------------------------------------------- the tracker ---

// durableTracker is the tracker as the reconciler uses it: reads see what
// ORIGIN says, and every write is followed, in the same step, by the commit and
// push that makes it a record somebody else can read.
//
// A write that cannot be published is an ERROR, not a warning. The two
// authorities of a run are the tracker and git, and a close that git does not
// carry is a close the next wave's worker cannot see — which is the whole of
// the failure this type exists to prevent.
type durableTracker struct {
	inner Tracker
	tree  *trackerTree
	r     *Reconciler
}

func (d *durableTracker) Graph(ctx context.Context, epicID string) (tk.Graph, error) {
	if err := d.tree.sync(); err != nil {
		return tk.Graph{}, err
	}
	return d.inner.Graph(ctx, epicID)
}

func (d *durableTracker) Show(ctx context.Context, tickID string) (tk.Tick, error) {
	if err := d.tree.sync(); err != nil {
		return tk.Tick{}, err
	}
	return d.inner.Show(ctx, tickID)
}

func (d *durableTracker) Claim(ctx context.Context, tickID, owner string) (tk.Tick, error) {
	return d.write(tickID, "claim "+tickID+" for "+owner, func() (tk.Tick, error) {
		return d.inner.Claim(ctx, tickID, owner)
	})
}

func (d *durableTracker) Note(ctx context.Context, tickID, text string) (tk.Tick, error) {
	return d.write(tickID, "note "+tickID, func() (tk.Tick, error) {
		return d.inner.Note(ctx, tickID, text)
	})
}

func (d *durableTracker) Close(ctx context.Context, tickID string) (tk.Tick, error) {
	return d.write(tickID, "close "+tickID, func() (tk.Tick, error) {
		return d.inner.Close(ctx, tickID)
	})
}

// write is the one shape every tracker write has: read origin, make the write,
// and push it — in that order, and with nothing between the write and the push.
func (d *durableTracker) write(tickID, reason string, apply func() (tk.Tick, error)) (tk.Tick, error) {
	if err := d.tree.sync(); err != nil {
		return tk.Tick{}, err
	}
	tick, err := apply()
	if err != nil {
		return tick, err
	}
	commit, err := d.tree.publish(reason)
	if err != nil {
		return tick, fmt.Errorf("%s reached the tracker and not %s: a tracker record that is not pushed is a record "+
			"the next wave's worker cannot read: %w", reason, d.tree.remote, err)
	}
	if commit != "" && d.r != nil {
		d.r.record(tickID, StagePublished, "%s is on %s as %s", reason, d.tree.branch, short(commit))
	}
	return tick, nil
}

// relocate points a tracker at another checkout of the same repository.
//
// The tk client's own way of saying this is `--repo`, and it is the reason the
// reconciler can put the tracker somewhere other than its own checkout at all.
// A tracker that cannot be relocated is refused rather than tolerated: it would
// write into whatever checkout it was built against, leaving the reconciler's
// tree dirty on main and origin without the record.
func relocate(tracker Tracker, dir string) (Tracker, bool) {
	switch t := tracker.(type) {
	case *tk.Client:
		return t.In(dir), true
	case interface{ In(string) Tracker }:
		return t.In(dir), true
	}
	return tracker, false
}

// ------------------------------------------------- the promotion's writes ---

// tickWriter is the tracker's half of a PROMOTION (tick npq): the two writes
// an absorption makes — a new tick record, and one blocked_by edge placing it
// before the final review. A test tracker implements both so the run is
// observable against the same seam production uses; the tk client implements
// neither, because `tk --json` publishes no create verb, so the durable
// wrapper writes the record itself — the same way the cloud control plane
// does, against the layout contracts/tracker-layout.json pins (required
// fields, defaults, the id alphabet): the pinned bundle is the contract that
// authorises the write, and the file this layer writes is the file tk reads.
type tickWriter interface {
	// CreateTick files a new tick record. It refuses an id that already
	// exists, the way the repository refuses a create with no sha: the tracker
	// is the index.
	CreateTick(ctx context.Context, tick tk.Tick) (tk.Tick, error)
	// BlockOn appends one blocker to a tick's blocked_by, idempotently —
	// the edge a person draws when absorbing a finding into a running epic,
	// made by the run on the decision a recorded verdict drove.
	BlockOn(ctx context.Context, tickID, blocker string) error
}

// tickIDCandidates are the ids one promotion will try, in order, widening the
// way Go's own minter does — three candidates at a length before the next —
// from the pinned alphabet and lengths of contracts/tracker-layout.json.
// Mirrored from the layout rather than asked of tk, because the minting
// happens where the commit happens and there is no tk verb for it; the
// layout fixture is what keeps the two honest.
func tickIDCandidates() []string {
	const (
		alphabet  = "abcdefghijklmnopqrstuvwxyz0123456789"
		minLength = 3
		maxLength = 4
		perLength = 3
	)
	var out []string
	for length := minLength; length <= maxLength; length++ {
		for attempt := 0; attempt < perLength; attempt++ {
			id := make([]byte, length)
			for i := range id {
				n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
				if err != nil {
					// No candidate rather than a predictable one: an id a run
					// cannot mint unpredictably is one a test can force and a
					// person cannot trust to be unique.
					return nil
				}
				id[i] = alphabet[n.Int64()]
			}
			out = append(out, string(id))
		}
	}
	return out
}

// MintTickID answers a tick id the tracker does not already carry, minted from
// the pinned alphabet and checked against the tracker's own worktree — the
// branch carries the records, so the branch is the index. The id is minted
// BEFORE the absorption record is written, because the record names the tick
// and the record is what a killed incarnation is resumed by.
func (d *durableTracker) MintTickID() (string, error) {
	if err := d.tree.sync(); err != nil {
		return "", err
	}
	for _, id := range tickIDCandidates() {
		if _, err := os.Stat(trackerRecordPath(d.tree.dir, id)); err == nil {
			continue // taken: the tracker is the index, not a cache of itself
		} else if !os.IsNotExist(err) {
			return "", err
		}
		return id, nil
	}
	return "", fmt.Errorf("no tick id of the pinned 3-4 character alphabet is free in this tracker: " +
		"the promotion refuses rather than colliding, and a person must file the tick by hand")
}

// CreateTick files a new tick record durably — CREATE-IF-ABSENT, so a
// resume finishing a half-made promotion behind its decision record can
// call it again on the tick the kill already created: the standing record is
// the truth and is never clobbered, exactly as a finding draft's repeat is
// refused by the repository rather than overwriting the original. The write,
// then the commit and push that make it a record the next wave's worker can
// read — the same order every tracker write here keeps.
func (d *durableTracker) CreateTick(ctx context.Context, tick tk.Tick) (tk.Tick, error) {
	if err := d.tree.sync(); err != nil {
		return tk.Tick{}, err
	}
	if w, ok := d.inner.(tickWriter); ok {
		created, err := w.CreateTick(ctx, tick)
		if err != nil {
			return created, err
		}
		if created.ID == "" {
			// A seam that answered nothing: refused rather than published,
			// because an empty record is a file the tracker refuses to read.
			return tk.Tick{}, fmt.Errorf("the tracker's create answered no tick")
		}
		if created.ID != tick.ID {
			return created, fmt.Errorf("the tracker created tick %s, not the %s the decision record names: the "+
				"promotion is what makes the record resumable, and an id that drifted is a record that lies", created.ID, tick.ID)
		}
		// The idempotent resume — the record already there — must publish
		// nothing rather than a no-change commit, and publish answers ""
		// for exactly that.
		tick = created
	} else {
		// The tk client has no create verb, so this layer writes the record
		// itself against the layout the pinned bundle pins — the control
		// plane's argument one verb along: a tick is a tracked file, and a
		// file written exactly as the tracker's owner writes it is a record
		// the tracker reads back unchanged. Create-if-absent is the file's
		// existence: a record already there is the truth, never clobbered.
		path := trackerRecordPath(d.tree.dir, tick.ID)
		if _, err := os.Stat(path); err == nil {
			raw, err := os.ReadFile(path)
			if err != nil {
				return tk.Tick{}, err
			}
			var standing tk.Tick
			if err := json.Unmarshal(raw, &standing); err != nil {
				return tk.Tick{}, fmt.Errorf("the record of %s does not read back as a tick: %w", tick.ID, err)
			}
			return standing, nil
		} else if !os.IsNotExist(err) {
			return tk.Tick{}, err
		}
		if err := writeTrackerRecord(d.tree, tick); err != nil {
			return tk.Tick{}, err
		}
	}
	reason := "create tick " + tick.ID
	commit, err := d.tree.publish(reason)
	if err != nil {
		return tick, fmt.Errorf("%s reached the tracker and not %s: a tracker record that is not pushed is a record "+
			"the next wave's worker cannot read: %w", reason, d.tree.remote, err)
	}
	if commit != "" && d.r != nil {
		d.r.record(tick.ID, StagePublished, "%s is on %s as %s", reason, d.tree.branch, short(commit))
	}
	return tick, nil
}

// BlockOn places one blocked_by edge durably: the review a promotion places
// the absorbed tick before is sequenced behind it by the tracker itself, so a
// COLD re-derivation — a fresh tk graph from the branch — reaches the same
// epic the warm run arranged, which is the whole of Axiom 1's demand on the
// placement half.
func (d *durableTracker) BlockOn(ctx context.Context, tickID, blocker string) error {
	if err := d.tree.sync(); err != nil {
		return err
	}
	if w, ok := d.inner.(tickWriter); ok {
		if err := w.BlockOn(ctx, tickID, blocker); err != nil {
			return err
		}
	} else if err := placeBlocker(d.tree, tickID, blocker); err != nil {
		return err
	}
	reason := "place " + tickID + " behind " + blocker
	commit, err := d.tree.publish(reason)
	if err != nil {
		return fmt.Errorf("%s reached the tracker and not %s: an edge that is not pushed is an edge the next "+
			"wave's worker cannot read: %w", reason, d.tree.remote, err)
	}
	if commit != "" && d.r != nil {
		d.r.record(tickID, StagePublished, "%s is blocked-by %s on %s as %s",
			tickID, blocker, d.tree.branch, short(commit))
	}
	return nil
}

// writeTrackerRecord writes one tick record exactly the way the tracker's
// owner writes it: two-space indent, the pinned required fields set, optional
// fields omitted rather than nulled (contracts/tracker-layout.json — the same
// fixture the control plane's writer is pinned by, which is what makes a file
// this layer writes one Go's Store accepts unchanged).
func writeTrackerRecord(tree *trackerTree, tick tk.Tick) error {
	raw, err := json.MarshalIndent(tick, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(trackerRecordPath(tree.dir, tick.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(trackerRecordPath(tree.dir, tick.ID), append(raw, '\n'), 0o644)
}

// placeBlocker rewrites one tick record with a blocker appended, for the
// tracker that has no verb of its own for the edge. Idempotent: an edge
// already drawn is left alone, because a resume finishing a half-made
// promotion re-places the same placement the record names.
func placeBlocker(tree *trackerTree, tickID, blocker string) error {
	path := trackerRecordPath(tree.dir, tickID)
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s to place it behind %s: %w", tickID, blocker, err)
	}
	var tick tk.Tick
	if err := json.Unmarshal(raw, &tick); err != nil {
		return fmt.Errorf("the record of %s does not read back as a tick: %w", tickID, err)
	}
	if slices.Contains(tick.BlockedBy, blocker) {
		return nil
	}
	tick.BlockedBy = append(tick.BlockedBy, blocker)
	return writeTrackerRecord(tree, tick)
}
