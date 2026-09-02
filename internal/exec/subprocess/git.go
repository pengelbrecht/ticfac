package subprocess

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// The git this executor needs, and no more: a worktree per attempt, the facts
// collect reads off the branch, and the push that makes in-progress work
// durable.
//
// Every call names the directory it runs in. A git command that inherits a
// working directory is the bug where an attempt writes the wrong repository —
// which is precisely the collision the "two repos, one tick id" test exists
// to catch.

type gitError struct {
	args   []string
	dir    string
	stderr string
	err    error
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s (in %s): %v: %s",
		strings.Join(e.args, " "), e.dir, e.err, strings.TrimSpace(e.stderr))
}

func (e *gitError) Unwrap() error { return e.err }

// git runs one git command and returns its trimmed stdout.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	// A git that reads the invoking user's hooks, editors or pagers is a git
	// that can block forever in a non-interactive executor.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_OPTIONAL_LOCKS=0",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", &gitError{args: args, dir: dir, stderr: stderr.String(), err: err}
	}
	return strings.TrimSpace(string(out)), nil
}

// repoRoot is the top of the working tree the executor was pointed at.
func repoRoot(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not a git repository: %w", dir, err)
	}
	resolved, err := filepath.EvalSymlinks(out)
	if err != nil {
		return out, nil
	}
	return resolved, nil
}

// repoKey identifies the REPOSITORY, not the checkout: the common git
// directory is shared by a repository's worktrees and differs between two
// clones on one machine. It is half of a handle's identity, which is what
// keeps the same tick id in two repositories from colliding.
func repoKey(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(out); err == nil {
		out = resolved
	}
	sum := sha256.Sum256([]byte(filepath.Clean(out)))
	return hex.EncodeToString(sum[:])[:16], nil
}

// resolveCommit turns a revision into the commit sha it names, and fails when
// the revision is not in this repository — which is what an attempt asked to
// branch from a base its checkout has never fetched looks like.
func resolveCommit(dir, rev string) (string, error) {
	return git(dir, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
}

// branchExists is asked before an attempt claims a branch name.
func branchExists(dir, branch string) bool {
	_, err := git(dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func worktreeAdd(repo, dir, branch, base string) error {
	_, err := git(repo, "worktree", "add", "--quiet", "-b", branch, dir, base)
	return err
}

func worktreeRemove(repo, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		_, err := git(repo, "worktree", "prune")
		return err
	}
	if _, err := git(repo, "worktree", "remove", "--force", dir); err != nil {
		return err
	}
	_, err := git(repo, "worktree", "prune")
	return err
}

func branchDelete(repo, branch string) error {
	_, err := git(repo, "branch", "-D", branch)
	return err
}

// headOf is the branch's tip, or "" when the branch does not exist.
func headOf(repo, branch string) string {
	out, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return out
}

// commitsBeyond counts the commits on head that base does not have. It is the
// "commits beyond the recorded base" half of inspect, and the `commits` field
// of a JobResult's source.
func commitsBeyond(repo, base, head string) (int, error) {
	if head == "" {
		return 0, nil
	}
	out, err := git(repo, "rev-list", "--count", base+".."+head)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("rev-list --count returned %q: %w", out, err)
	}
	return n, nil
}

// changedPaths is the boundary diff: every path that differs between the
// attempt's recorded base and its head.
func changedPaths(repo, base, head string) ([]string, error) {
	if head == "" || head == base {
		return nil, nil
	}
	out, err := git(repo, "diff", "--name-only", "--no-renames", base, head)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// showFile reads one path out of a ref, for the case where the worktree is
// already gone and the report was committed.
func showFile(repo, ref, path string) (string, bool) {
	out, err := git(repo, "show", ref+":"+path)
	if err != nil {
		return "", false
	}
	return out, true
}

func hasRemote(repo, remote string) bool {
	out, err := git(repo, "remote")
	if err != nil {
		return false
	}
	for _, name := range strings.Split(out, "\n") {
		if strings.TrimSpace(name) == remote {
			return true
		}
	}
	return false
}

// pushBranch makes in-progress work durable. Plain, never forced: this
// attempt is the only writer of its own ref, so a non-fast-forward is
// something to fail loudly on rather than to overwrite.
func pushBranch(worktree, remote, branch string) error {
	_, err := git(worktree, "push", remote, "HEAD:refs/heads/"+branch)
	return err
}

// isAncestor answers whether a commit is already reachable from a ref — the
// question disposal asks before it deletes a branch.
func isAncestor(repo, commit, ref string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", commit, ref)
	cmd.Dir = repo
	return cmd.Run() == nil
}
