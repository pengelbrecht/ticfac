package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The gate runs in a STABLE directory, and this file is why (tick 6wh).
//
// Go's test cache keys a package's result on, among other things, the files the
// test OPENED, by absolute path. Measured on 2026-09-20, two worktrees of the
// same commit:
//
//	worktree A, first run    ok internal/schema 0.309s   ok internal/runfeed 0.406s
//	worktree A, second run   ok internal/schema (cached) ok internal/runfeed (cached)
//	worktree B, same commit  ok internal/schema 0.311s   ok internal/runfeed 0.427s
//
// The path is the whole difference. tempWorktree makes a FRESH directory under
// the host's temp dir for every call, so every gate was worktree B: a cold
// cache and the entire suite re-run, for a tick that touched one package. On a
// quiet host that is ~6 minutes; under contention it has been measured at 32,
// 37, 78 and 105 minutes. It also meant the operator's decision on 2026-09-18
// to drop `-count=1` from the declared gate — explicitly so that "a package
// whose inputs are unchanged answers from cache" — bought nothing at all: the
// reasoning was right and the mechanism was already broken.
//
// So the gate takes a SLOT: a directory per repository, per slot, reused by
// every gate that runs there. Two properties the throwaway directory was buying
// by accident have to be kept on purpose, and both are kept by construction
// rather than by discipline:
//
//   - No contamination. The tree is reset on ACQUIRE, never on release (see
//     resetWorktreeTo). Release is a deferred call and a deferred call is
//     exactly what a SIGKILL does not run, so a gate that cleaned up after
//     itself would be trusting the previous gate to have survived. Reset on
//     acquire holds whatever the previous holder did or did not do, including
//     having been killed mid-suite.
//   - No sharing. A slot is held under an flock for the life of the gate
//     (lockGateSlot), by the reconciler AND by the shell it starts there, so a
//     gate that outlives the reconciler that started it goes on holding its
//     directory. A gate that finds every slot held falls back to the throwaway
//     worktree it used to always get: slower, never wrong. The lock is per
//     SLOT, not per host, so this does not serialise gates — bounding what runs
//     beside a gate is tick w1j's, and deliberately not decided here.
//
// What this does NOT do is keep the directory tidy for its own sake: a slot's
// tree survives the gate that used it, on purpose, because that is the thing
// the cache is keyed on. The cost is bounded by construction at gateSlots trees
// per repository, and each one is a checkout of a commit the repository already
// holds.

// gateSlots is how many gates one repository can run at once before a gate
// falls back to a throwaway directory. Four, because the gate itself is
// single-threaded per run (gateProgress: "there is still exactly one gate
// running at a time") so a slot is per RUN in practice, and four concurrent
// runs against one checkout is already more than the default max_parallel of
// the one orchestrator that drives them. Above it the fallback is correct, just
// cold — the behaviour every gate had before this tick.
const gateSlots = 4

// gateSlotBase is where a repository's slots live: OUTSIDE the repository, like
// every other worktree this package makes, because a worktree inside the tree
// being gated would show up in the diff the boundary check reads.
//
// It is a var for exactly one reason: this package's own test harness points it
// at a directory it removes (harness_test.go's TestMain). Every fixture test
// builds its own throwaway repository, and a per-repository slot root under the
// host's temp directory would otherwise leave one checkout per fixture behind
// after every suite run.
var gateSlotBase = os.TempDir()

// errGateSlotBusy is another gate holding the slot: try the next one.
var errGateSlotBusy = errors.New("the gate slot is held")

// errNoGateSlotLock is a platform or filesystem that cannot lock at all. There
// is no next slot to try — without a lock, nothing keeps two gates out of one
// directory — so the caller takes a throwaway worktree instead.
var errNoGateSlotLock = errors.New("the gate slot cannot be locked here")

// gateWorktree is tempWorktree's guarantees — a detached worktree at `commit`,
// clean, and a function that gives it up — over a directory that is REUSED, so
// that Go's test cache recognises the path.
//
// It also hands back the slot's open LOCK, which the caller is expected to give
// to the process it runs in this directory (gate.go's startShell puts it in the
// command's ExtraFiles). That is not decoration. The one way a reused directory
// can still be contaminated is an ORPHAN: kill the reconciler — not stop it,
// kill it — while a gate is running, and the gate's shell is in a process group
// of its own and goes on running with nothing left to record its verdict. The
// reconciler's death releases every lock it held, so the next run would take
// that slot and reset the tree under a suite that is still reading it. A lock
// the SHELL also holds cannot be released by the reconciler dying: the kernel
// keeps it until every process holding that descriptor is gone, which is
// exactly the condition under which the directory is safe to reuse.
//
// nil lock means a throwaway directory, which is nobody else's and needs none.
//
// The returned remove() releases the reconciler's own hold on the slot. It
// deliberately does not delete the tree: see the file comment.
func (g *repoGit) gateWorktree(prefix, commit string) (dir string, lock *os.File, remove func(), err error) {
	root, err := g.gateSlotRoot(prefix)
	if err != nil {
		// Not fatal: this is an optimisation, and a repository that cannot say
		// where its own git directory is has bigger problems than a cold gate.
		dir, remove, err := g.tempWorktree(prefix, commit)
		return dir, nil, remove, err
	}
	for slot := 0; slot < gateSlots; slot++ {
		held := filepath.Join(root, fmt.Sprintf("slot-%d", slot))
		if err := os.MkdirAll(held, 0o700); err != nil {
			return "", nil, nil, err
		}
		lock, err := lockGateSlot(filepath.Join(held, "lock"))
		if errors.Is(err, errGateSlotBusy) {
			continue
		}
		if err != nil {
			break // errNoGateSlotLock, or a lock file that will not open
		}
		tree := filepath.Join(held, "tree")
		if err := g.resetWorktreeTo(tree, commit); err != nil {
			_ = lock.Close()
			return "", nil, nil, err
		}
		return tree, lock, func() { _ = lock.Close() }, nil
	}
	throwaway, remove, err := g.tempWorktree(prefix, commit)
	return throwaway, nil, remove, err
}

// gateSlotRoot is where THIS checkout's slots live.
//
// Keyed by the repository's common git directory, because that is what a
// worktree is registered against: two checkouts of the same repository must not
// share a slot, and the same checkout reached by two spellings of its path must.
// Hashed because the key has to be one path segment and a checkout's location
// is not one — and because a directory named after where the operator keeps
// their checkouts is a host detail this gate then carries around in $PWD.
func (g *repoGit) gateSlotRoot(prefix string) (string, error) {
	common, err := g.commonDir()
	if err != nil {
		return "", err
	}
	// Created before it is resolved, and resolved at all, because the slot's
	// path must be the SAME STRING on every gate or the cache this exists for
	// does not hit. EvalSymlinks answers only about a directory that is there,
	// so a base that does not exist yet would come back unresolved on the
	// first gate (/var/...) and resolved on the next (/private/var/... — macOS
	// spells its temp directory both ways), and the second gate would be a
	// cold one — which is how
	// TestASecondGateOverAnUnchangedTreeIsServedFromGosTestCache first failed.
	//
	// Resolving is load-bearing for a second reason, measured while writing
	// TestACachedPassCannotHideAnEditToAFileAGuardReads: a module reached
	// through a symlink puts the files its tests open outside the module as far
	// as cmd/go is concerned, and go then declines to cache that package AT
	// ALL. A slot at an unresolved path would have been a stable directory that
	// still never hit the cache — this tick, done and useless.
	base := gateSlotBase
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	sum := sha256.Sum256([]byte(common))
	return filepath.Join(base, prefix+"slots-"+hex.EncodeToString(sum[:6])), nil
}

// commonDir is this repository's shared git directory, resolved through
// symlinks (a macOS temp dir is /var/... and git answers /private/var/...) and
// remembered: it cannot change under a repoGit, and every gate would otherwise
// pay a git process to be told the same thing.
func (g *repoGit) commonDir() (string, error) {
	if g.common != "" {
		return g.common, nil
	}
	out, err := g.run("", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(out); err == nil {
		out = resolved
	}
	g.common = out
	return out, nil
}

// resetWorktreeTo makes `dir` a detached worktree at `commit` whose tree is
// indistinguishable from a freshly added one — whatever it held before.
//
// The restore is tried first and the rebuild is the fallback, not the other way
// round, because rebuilding throws away the only thing the slot exists for: git
// worktree remove deletes the directory, and the next `worktree add` recreates
// every file in it. A restore touches only what changed.
func (g *repoGit) resetWorktreeTo(dir, commit string) error {
	if g.worktreeIsMine(dir) {
		if err := g.restoreWorktree(dir, commit); err == nil {
			return nil
		}
	}
	// Anything unexpected — the directory gone, a registration pruned out from
	// under it by another checkout's repair, a half-written tree from a gate
	// that was killed between two of the three commands below — is answered by
	// starting over. removeWorktree deletes the directory as well as the
	// registration, so `worktree add` sees the clean slate it needs.
	g.removeWorktree(dir)
	return g.worktreeAt(dir, commit)
}

// worktreeIsMine answers whether `dir` is still a worktree of THIS repository,
// rather than a directory that happens to be there. Anything else — a stale
// tree whose registration was pruned, someone else's checkout, nothing at all —
// answers false and is rebuilt.
func (g *repoGit) worktreeIsMine(dir string) bool {
	common, err := g.commonDir()
	if err != nil {
		return false
	}
	out, _, err := g.try(dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return false
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		return false
	}
	return samePath(lines[0], dir) && samePath(lines[1], common)
}

// samePath compares two paths as the filesystem sees them, not as they were
// spelled: a macOS temp directory is reached as /var/... and git answers about
// it as /private/var/....
func samePath(a, b string) bool {
	if resolved, err := filepath.EvalSymlinks(strings.TrimSpace(a)); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(strings.TrimSpace(b)); err == nil {
		b = resolved
	}
	return filepath.Clean(strings.TrimSpace(a)) == filepath.Clean(strings.TrimSpace(b))
}

// restoreWorktree is the two commands that leave a reused tree carrying nothing
// from the gate before it.
//
//   - `checkout --detach --force` puts HEAD, the INDEX and every TRACKED file
//     at the gate's commit, discarding whatever the previous gate did to them
//     rather than refusing to. The index matters as much as the files: a gate
//     command that staged something (a formatter, a codegen check running `git
//     add`) leaves a tree that reads clean to `git status` while `git diff
//     --cached` disagrees. So does an interrupted merge: a MERGE_HEAD left by a
//     killed gate would make the next gate's tree a merge in progress. Both
//     were measured here — checkout --force clears them, and a `reset --hard`
//     after it was verified to be doing nothing this code needs, so it is not
//     here.
//   - `clean -xdff` is everything git is not tracking: untracked files (-d for
//     directories), IGNORED files (-x), and nested repositories (-ff), which an
//     npm install or a vendored checkout leaves behind and which a plain -f
//     walks straight past.
//
// What is NOT covered by either, and is worth saying out loud: a gate command
// that writes OUTSIDE its working directory. Nothing in a reused directory can
// bound that, and nothing in a throwaway one could either.
//
// The -x is the deliberate one, because it deletes build artefacts a gate might
// have wanted to keep for speed. It stays, and the argument is that it costs
// this gate nothing: Go's build and test caches live in GOCACHE, outside the
// tree entirely, so cleaning the worktree does not cost a single recompile of
// the thing this tick is about. What -x buys in exchange is that a gate cannot
// pass on an artefact a previous commit built — which is the failure mode that
// makes a reused directory worse than no cache at all.
func (g *repoGit) restoreWorktree(dir, commit string) error {
	if _, _, err := g.try(dir, "checkout", "--quiet", "--detach", "--force", commit); err != nil {
		return err
	}
	if _, _, err := g.try(dir, "clean", "--quiet", "-xdff"); err != nil {
		return err
	}
	return nil
}
