package reconcile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
}

func (g *repoGit) run(dir string, args ...string) (string, error) {
	out, _, err := g.try(dir, args...)
	return out, err
}

func (g *repoGit) try(dir string, args ...string) (stdout, stderr string, err error) {
	if dir == "" {
		dir = g.dir
	}
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=" + g.name,
		"-c", "user.email=" + g.email,
		"-c", "commit.gpgsign=false",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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

// fetchInto brings a remote branch's objects into this repository under a
// local ref this reconciler owns, so everything afterwards names a commit that
// is definitely here.
func (g *repoGit) fetch(branch string) error {
	_, err := g.run("", "fetch", "--quiet", g.remote, refFor(branch)+":"+refFor("refs/ticfac/fetched/"+branch))
	return err
}

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
