package reconcile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// git, as the reconciler needs it: resolve, merge, push, and answer whether a
// commit is already contained in another. Nothing here reads a working tree
// the run is using — the merge happens in a DETACHED worktree of its own, so
// the integration branch is never checked out anywhere the run-state store
// might be pointed at.

type repoGit struct {
	dir    string
	name   string
	email  string
	remote string
	// fetchID makes this instance's fetch destinations its own: a pid is not
	// enough, because two of these can live in one process (tick emk's third
	// layer — see internal/runstate.newFetchID).
	fetchID string
	// retry bounds how long a transient remote failure is waited through
	// (tick enj). It is the reconciler's, so the retries it reports land in
	// the run's feed.
	retry runstate.RemoteRetry
}

func (g *repoGit) run(dir string, args ...string) (string, error) {
	out, _, err := g.try(dir, args...)
	return out, err
}

func (g *repoGit) try(dir string, args ...string) (stdout, stderr string, err error) {
	return g.tryEnv(dir, nil, args...)
}

// tryEnv is try with environment overrides appended. The one override this
// package uses is GIT_INDEX_FILE: the tracker's commits are assembled in a
// throwaway index, so a worktree a run is using never has its own index
// rewritten under it.
//
// A subcommand that reaches the network is run through the retry bound (tick
// enj), exactly as the run-state store's runner is: the reset that killed run
// epic-ncv landed on a fetch, but the same reset lands on the pushes below
// and on the ls-remote remoteHead reads origin with, so the bound is wired
// here rather than at the one call site that was observed failing. A push the
// remote REFUSED is untouched by it — a rejected lease is not a transient
// failure, and integrate's CAS reads that refusal out of stderr.
func (g *repoGit) tryEnv(dir string, extraEnv []string, args ...string) (stdout, stderr string, err error) {
	if sub, remote := runstate.RemoteSubcommand(args); remote {
		retryErr := g.retry.Do("git "+sub, func() error {
			stdout, stderr, err = g.onceEnv(dir, extraEnv, args...)
			return err
		})
		return stdout, stderr, retryErr
	}
	return g.onceEnv(dir, extraEnv, args...)
}

// onceEnv is one invocation: no retry, no classification, just the process.
func (g *repoGit) onceEnv(dir string, extraEnv []string, args ...string) (stdout, stderr string, err error) {
	if dir == "" {
		dir = g.dir
	}
	// gitbin.NoAutoMaintenance: this repository is the one the run writes its
	// trees, blobs and commits into, and a fetch or a merge that started a
	// background repack of it would race those writes (tick mel).
	//
	// gitbin.NoRerere: this repository is the OPERATOR'S, and a host whose
	// rerere is enabled globally shares .git/rr-cache with a person's own
	// merges — a resolution recorded by hand must neither be replayed into a
	// run's merge nor have the run's conflicts recorded against it (tick 6na).
	// Same category as GIT_TERMINAL_PROMPT=0 below: the run states its own git
	// environment rather than inheriting the host's.
	base := append(append([]string{
		"-c", "user.name=" + g.name,
		"-c", "user.email=" + g.email,
		"-c", "commit.gpgsign=false",
	}, gitbin.NoAutoMaintenance...), gitbin.NoRerere...)
	cmd := exec.Command(gitbin.Path(), append(base, args...)...)
	cmd.Dir = dir
	// GIT_TERMINAL_PROMPT=0 bounds the prompt; runstate.TransportEnv bounds the
	// network, which is the one that stopped a run for two and a half hours.
	cmd.Env = append(append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), runstate.TransportEnv()...), extraEnv...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	err = cmd.Run()
	stdout = strings.TrimSpace(outBuf.String())
	stderr = strings.TrimSpace(errBuf.String())
	if err != nil {
		err = fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr)
	}
	return stdout, stderr, err
}

func (g *repoGit) resolve(rev string) (string, error) {
	return g.run("", "rev-parse", "--verify", "--quiet", rev+"^{commit}")
}

// remoteHead is origin's commit for a branch, read from the remote rather than
// from a tracking ref: a tracking ref is this checkout's memory of origin, and
// the run's authority is origin itself.
func (g *repoGit) remoteHead(branch string) (string, error) {
	out, err := g.run("", "ls-remote", g.remote, refFor(branch))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(line, "\t")
		if ok && strings.TrimSpace(name) == refFor(branch) {
			return sha, nil
		}
	}
	return "", nil
}

func (g *repoGit) contains(commit, container string) bool {
	if commit == "" || container == "" {
		return false
	}
	_, _, err := g.try("", "merge-base", "--is-ancestor", commit, container)
	return err == nil
}

// ensureRemoteBranch creates the integration branch on origin if it is not
// there. The run-state store fetches this branch before it can write anything,
// so a run whose branch does not exist yet has nowhere to record that it
// started.
func (g *repoGit) ensureRemoteBranch(branch, base string) (string, error) {
	if head, err := g.remoteHead(branch); err == nil && head != "" {
		return head, nil
	}
	baseSHA, err := g.resolve(base)
	if err != nil {
		return "", fmt.Errorf("the base %q for %s is not a commit this checkout has: %w", base, branch, err)
	}
	if _, err := g.run("", "push", g.remote, baseSHA+":"+refFor(branch)); err != nil {
		// Another actor may have created it between the check and the push.
		if head, headErr := g.remoteHead(branch); headErr == nil && head != "" {
			return head, nil
		}
		return "", err
	}
	return baseSHA, nil
}

// worktreeAt makes a DETACHED worktree at a commit. Detached on purpose: the
// integration branch must not be checked out anywhere, because a store that
// moved a ref under a checkout would be writing into somebody's working tree.
func (g *repoGit) worktreeAt(dir, commit string) error {
	if _, err := g.run("", "worktree", "add", "--detach", "--quiet", dir, commit); err != nil {
		return err
	}
	return nil
}

func (g *repoGit) removeWorktree(dir string) {
	_, _, _ = g.try("", "worktree", "remove", "--force", dir)
	_ = os.RemoveAll(dir)
	_, _, _ = g.try("", "worktree", "prune")
}

// pruneWorktrees drops worktree registrations whose directory is gone, and
// answers with what git said it removed.
//
// Every worktree this package makes is a throwaway outside the repository, and
// every one of them is removed on the way out of the leg that made it — by a
// defer, which is exactly the thing a SIGKILL does not run. What survives such
// a run is an ADMIN ENTRY under .git/worktrees pointing at a directory the
// operating system has since cleared out: `git worktree list` grows one line
// per killed run, and `git worktree add` can refuse a path one of them still
// claims.
//
// It is deliberately only the registered-but-MISSING ones. A registration
// whose directory is still there may be another run's live worktree — two
// epics can be reconciled in one checkout — and removing that would break a
// run this one knows nothing about.
func (g *repoGit) pruneWorktrees() (string, error) {
	return g.run("", "worktree", "prune", "--verbose")
}

// fetchInto brings a remote branch's objects into this repository under a
// local ref this reconciler owns, so everything afterwards names a commit that
// is definitely here. The refspec is FORCED (+): the remote is the durable
// authority this run leases against, so its word is final — a local
// fetched-tracking ref that has drifted ahead of the remote (an operator who
// reset the integration branch backwards between runs, a re-run after a
// force push) must never override what the remote says now. Without the + a
// backwards move on the remote wedges every future fetch on a non-fast-
// forward against a stale local ref nobody can see.
func (g *repoGit) fetch(branch string) error {
	// --no-write-fetch-head and --refmap= keep this fetch off the two pieces of
	// state every other git process in the checkout also writes: FETCH_HEAD,
	// and the remote-tracking ref git updates opportunistically when a branch
	// is fetched from a NAMED remote. An operator's `git fetch origin` writes
	// both. Sharing either is a race: FETCH_HEAD hands back another branch's
	// head, and refs/remotes/<remote>/<branch> fails to lock and fails the
	// fetch. Only the refspec on this command line is updated. Tick wdb.
	_, err := g.run("", "fetch", "--quiet", "--no-write-fetch-head", "--refmap=", g.remote,
		"+"+refFor(branch)+":"+refFor("refs/ticfac/fetched/"+g.fetch1D()+"/"+branch))
	return err
}

// fetch1D is this instance's fetch id, assigned on first use.
func (g *repoGit) fetch1D() string {
	if g.fetchID == "" {
		g.fetchID = strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(repoGitSeq.Add(1), 10)
	}
	return g.fetchID
}

var repoGitSeq atomic.Uint64

func refFor(branch string) string {
	if strings.HasPrefix(branch, "refs/") {
		return branch
	}
	return "refs/heads/" + branch
}

// tempWorktree makes a DETACHED worktree at a commit, outside the repository,
// and returns it with the one function that removes both it and the directory
// it lives in. A worktree inside the tree being merged would show up in the
// diff the boundary check reads.
func (g *repoGit) tempWorktree(prefix, commit string) (dir string, remove func(), err error) {
	root, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", nil, err
	}
	dir = filepath.Join(root, "tree")
	remove = func() {
		g.removeWorktree(dir)
		_ = os.RemoveAll(root)
	}
	if err := g.worktreeAt(dir, commit); err != nil {
		remove()
		return "", nil, err
	}
	return dir, remove, nil
}
