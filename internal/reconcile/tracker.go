package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	if _, err := t.git.run("", "fetch", "--quiet", t.remote, refFor(t.branch)); err != nil {
		return "", fmt.Errorf("fetch %s from %s: %w", t.branch, t.remote, err)
	}
	return t.git.run("", "rev-parse", "FETCH_HEAD")
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
// keeps a tracker write from clobbering whatever else landed in `.tick/`
// between this writer's read and its push.
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
