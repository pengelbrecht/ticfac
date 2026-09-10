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

// gitCommonDir is the repository's shared git directory — the real one, which
// every linked worktree's `.git` FILE points at rather than contains.
//
// It is asked for twice and for two different reasons. It identifies the
// repository (see repoKey), and it is the directory an attempt's runner must
// be able to write: a worktree under the executor's state root cannot commit
// unless the index, refs and logs under this path are writable too, and those
// are nowhere near the worktree. It is always RESOLVED and never spelled as
// `.git`, because in a linked worktree `.git` is a file and in a submodule or
// a separate-git-dir checkout it is somewhere else entirely.
func gitCommonDir(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(out); err == nil {
		out = resolved
	}
	return filepath.Clean(out), nil
}

// repoKey identifies the REPOSITORY, not the checkout: the common git
// directory is shared by a repository's worktrees and differs between two
// clones on one machine. It is half of a handle's identity, which is what
// keeps the same tick id in two repositories from colliding.
func repoKey(dir string) (string, error) {
	common, err := gitCommonDir(dir)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(common))
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

// excludeFromGit keeps one path prefix out of this worktree's git without
// touching a single tracked file: it is appended to the exclude file
// `git rev-parse --git-path info/exclude` resolves FROM THE WORKTREE, so a
// runner that runs `git add -A` cannot stage a path this executor owns —
// whether the runner writes it before or after that add. (In every git this
// was built and tested against, info/exclude is one of the files a linked
// worktree shares with the repository's common git directory rather than
// keeping privately — gitrepository-layout(5)'s "info" entry says so
// explicitly — so this in fact excludes the prefix repo-wide, in every
// worktree of this repository. That is still correct here: an artifact
// prefix is unique per attempt, so one attempt's line never matches another
// attempt's paths, and the alternative — writing the pattern into a tracked
// .gitignore — is the one thing this function must not do.)
//
// The line is NOT permanent: unexcludeFromGit below takes it out again at
// disposal, so an executor that has disposed of everything it created leaves
// the operator's exclude file as it found it — the same promise disposal makes
// about the worktree and the branch.
func excludeFromGit(worktree, prefix string) error {
	trimmed := strings.Trim(strings.TrimSpace(prefix), "/")
	if trimmed == "" {
		return fmt.Errorf("artifact_prefix is empty: nothing to exclude")
	}
	path, err := git(worktree, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("resolve this worktree's git exclude file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line := "/" + trimmed
	if existing, err := os.ReadFile(path); err == nil {
		for _, have := range strings.Split(string(existing), "\n") {
			if strings.TrimSpace(have) == line {
				return nil
			}
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

// unexcludeFromGit removes the one line excludeFromGit appended, and is what
// keeps that write from being a permanent edit to a file this executor does
// not own. It is called from disposal with the ATTEMPT WORKTREE while it still
// exists, so the exclude file it rewrites is resolved exactly the way the
// append resolved it; `dir` may be the repository instead once the worktree is
// gone, which resolves to the same shared file in every git this was built
// against.
//
// Absent line, absent file and absent git are all "nothing to remove" rather
// than errors: disposal must not fail because a cleanup it already did cannot
// be done twice. Two attempts disposing at the same instant can still lose one
// line the way two appending at the same instant can lose one — the append has
// always had that race, and an artifact prefix is per attempt, so the loss is
// a stale line rather than a staged report.
func unexcludeFromGit(dir, prefix string) error {
	trimmed := strings.Trim(strings.TrimSpace(prefix), "/")
	if trimmed == "" {
		return nil
	}
	path, err := git(dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return nil
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	line := "/" + trimmed
	kept := make([]string, 0, 8)
	removed := false
	for _, have := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(have) == line {
			removed = true
			continue
		}
		kept = append(kept, have)
	}
	if !removed {
		return nil
	}
	return atomicWrite(path, []byte(strings.Join(kept, "\n")), 0o644)
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

// readRemotes is every remote a worktree has and every URL those remotes
// resolve to, fetch and push alike. It is asked for by the source grade: a
// read-only attempt pins one pushurl per NAME and one pushInsteadOf per URL,
// so both `git push origin` and `git push <the url origin means>` land on the
// same refusal.
//
// A repository with no remotes answers empty, which is the right answer: there
// is then nothing to pin, and the prefix rewrites still cover an explicit URL.
func readRemotes(dir string) remoteSet {
	out, err := git(dir, "remote", "-v")
	if err != nil {
		return remoteSet{}
	}
	var set remoteSet
	names, urls := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if name := fields[0]; !names[name] {
			names[name] = true
			set.Names = append(set.Names, name)
		}
		if url := fields[1]; !urls[url] {
			urls[url] = true
			set.URLs = append(set.URLs, url)
		}
	}
	return set
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
