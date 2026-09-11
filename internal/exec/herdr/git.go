package herdr

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

// The git this executor needs — the READ half of what collect and dispose
// ask, plus the branch delete disposal ends on. There is no worktree creation
// here: worktree.create is HERDR's operation, the first thing Start asks of
// it. The helpers mirror internal/exec/subprocess's rather than sharing its
// unexported ones, because a second executor owning its own mechanics is
// cheaper than exporting a private file's worth of plumbing across the seam —
// and the two sets stay comparable line by line.

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
	if resolved, err := filepath.EvalSymlinks(out); err == nil {
		return resolved, nil
	}
	return out, nil
}

// repoKey identifies the REPOSITORY, not the checkout — the same identity the
// local subprocess executor uses, so one tick id in two checkouts of two
// repositories gets two state directories rather than a race. The common git
// directory is shared by a repository's worktrees and differs between clones,
// which is exactly the distinction the key exists to draw.
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
// the revision is not in this repository.
func resolveCommit(dir, rev string) (string, error) {
	return git(dir, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
}

// branchExists is asked before an attempt claims a branch name through herdr:
// a leftover branch is a ref a previous attempt owns, and worktree.create on
// an existing branch would CHECK IT OUT rather than refuse.
func branchExists(dir, branch string) bool {
	_, err := git(dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func branchDelete(repo, branch string) error {
	_, err := git(repo, "branch", "-D", branch)
	return err
}

// headOf is the branch's tip, or "" when the branch does not exist. It is run
// against the repository the attempt was RECORDED against, not the checkout
// this executor happens to be pointed at, because a herdr worktree belongs to
// the repository herdr created it from and a restart may hold a different
// checkout of the same host.
func headOf(repo, branch string) string {
	out, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return out
}

// commitsBeyond counts the commits on head that base does not have.
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
// already gone and the report was committed anyway.
func showFile(repo, ref, path string) (string, bool) {
	out, err := git(repo, "show", ref+":"+path)
	if err != nil {
		return "", false
	}
	return out, true
}

// statusPorcelain is the worktree's dirt, one entry per line. It is asked by
// disposal, and only for the one narrow question there: is the only thing
// standing between this worktree and a clean worktree.remove the attempt's
// own untracked report? -uall is load-bearing: plain --porcelain reports an
// entirely-untracked directory as one directory entry, and the report's
// directory is exactly that.
func statusPorcelain(worktree string) ([]string, error) {
	out, err := git(worktree, "status", "--porcelain", "-uall")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// isAncestor answers whether a commit is already reachable from a ref — the
// question disposal asks before it deletes a branch.
func isAncestor(repo, commit, ref string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", commit, ref)
	cmd.Dir = repo
	return cmd.Run() == nil
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

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
