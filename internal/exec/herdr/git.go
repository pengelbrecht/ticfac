package herdr

import (
	"bytes"
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
	return gitEnv(dir, nil, args...)
}

// gitEnv is git with extra environment, which exists for one caller: the
// wip snapshot stages into a PRIVATE index (GIT_INDEX_FILE), so it never
// touches the worktree's own index — the agent is still alive and running
// its own git when the snapshot is taken, and a snapshot that disturbed
// the working state it is preserving would be worse than none.
func gitEnv(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	// A git that reads the invoking user's hooks, editors or pagers is a git
	// that can block forever in a non-interactive executor.
	cmd.Env = append(append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_OPTIONAL_LOCKS=0",
	), env...)
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

// excludeFromGit keeps one path prefix out of this worktree's git without
// touching a single tracked file: it is appended to the exclude file
// `git rev-parse --git-path info/exclude` resolves FROM THE WORKTREE, so an
// agent that runs `git add -A` in the worktree herdr made cannot stage a
// path this executor owns — whether the agent writes its report before or
// after that add, and whatever kind of agent it is. (In every git this was
// built and tested against, info/exclude is one of the files a linked
// worktree shares with the repository's common git directory rather than
// keeping privately — gitrepository-layout(5)'s "info" entry says so
// explicitly — so this in fact excludes the prefix repo-wide, in every
// worktree of this repository. That is still correct here: an artifact
// prefix is unique per attempt, so one attempt's line never matches another
// attempt's paths, and the alternative — writing the pattern into a tracked
// .gitignore — is the one thing this function must not do.)
//
// The line is NOT permanent: unexcludeFromGit below takes it out again at
// disposal, so an executor that has disposed of everything it created
// leaves the operator's exclude file as it found it.
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

// unexcludeFromGit removes the one line excludeFromGit appended, and is
// what keeps that write from being a permanent edit to a file this executor
// does not own. It is called from disposal with the ATTEMPT WORKTREE while
// it still exists, so the exclude file it rewrites is resolved exactly the
// way the append resolved it — and BEFORE the worktree remove, so nothing
// is orphaned in info/exclude once the worktree is gone. `dir` may be the
// repository instead once the worktree is already missing, which resolves
// to the same shared file in every git this was built against.
//
// Absent line, absent file and absent git are all "nothing to remove"
// rather than errors: disposal must not fail because a cleanup it already
// did cannot be done twice.
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

// gitStdin is gitEnv with stdin, for the one call that takes a path list
// on standard input: the wip snapshot's update-index.
func gitStdin(dir string, env []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Env = append(append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_OPTIONAL_LOCKS=0",
	), env...)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		return "", &gitError{args: args, dir: dir, stderr: stderr.String(), err: err}
	}
	return strings.TrimSpace(string(out)), nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// ---------------------------------------------------------------------------
// The wip snapshot: preserving uncommitted work before the pane close.
// ---------------------------------------------------------------------------

// wipRefFor is the ref one attempt's preserved work lives on (tick rj0, per
// pbb): refs/ticfac/wip/<run>/<tick>/<attempt>, derived from the job id —
// which already names the run, the tick and the attempt — with every path
// segment sanitised, because a job id is opaque to this executor and any
// character of it may be one a ref cannot carry. The ref is OUTSIDE
// refs/heads, so it never presents itself as a branch the run or a person
// could merge by accident: the snapshot is material a later attempt can be
// POINTED at, never evidence of completion.
func wipRefFor(record *attemptRecord) string {
	var segments []string
	for _, seg := range strings.Split(record.JobID, "/") {
		var b strings.Builder
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
				r == '-', r == '_', r == '.':
				b.WriteRune(r)
			default:
				b.WriteRune('-')
			}
		}
		clean := strings.Trim(b.String(), "-.")
		if clean != "" {
			segments = append(segments, clean)
		}
	}
	if len(segments) == 0 {
		segments = []string{"attempt"}
	}
	return "refs/ticfac/wip/" + strings.Join(segments, "/")
}

// snapshotWorktree preserves the worktree as it stands — every tracked
// change and every untracked file — on the given ref, without touching the
// worktree's own index, branch or HEAD (tick rj0, per pbb). It stages into a
// PRIVATE index seeded from HEAD, writes a tree, wraps it in a commit whose
// parent is HEAD, and points the ref at it; the worktree is left exactly as
// it was found.
//
// The exclusions are the boundary's, at snapshot scale: nothing under
// .tick/ or .ticfac/ and nothing under the attempt's own artifact prefix —
// which is where the report lives, archived separately — may ride along as
// NEW work. Tracked content those paths already carry in HEAD stays as
// HEAD has it, because the snapshot never UNDOES a commit; what is excluded
// is the agent's uncommitted writing under those paths. Build output needs
// no rule of its own (--exclude-standard honours .gitignore), and a build
// product the repository deliberately tracks is repository content, not a
// snapshot exclusion.
func snapshotWorktree(worktree, ref, artifactPrefix string) (commit string, err error) {
	tmpDir, err := os.MkdirTemp("", "ticfac-wip")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	index := filepath.Join(tmpDir, "index")
	indexEnv := []string{"GIT_INDEX_FILE=" + index}

	if _, err := gitEnv(worktree, indexEnv, "read-tree", "HEAD"); err != nil {
		return "", fmt.Errorf("seed the snapshot index from HEAD: %w", err)
	}
	// The stage is NOT `git add`: add refuses a pathspec that matches an
	// ignored directory — and the attempt's own artifact prefix is in this
	// worktree's info/exclude, put there by Start — so an add-based snapshot
	// would fail on exactly the paths it exists to exclude. ls-files plus
	// one update-index is the same walk with no advice machinery: the
	// tracked-at-HEAD set from the seeded private index, the
	// untracked-and-not-ignored set from the worktree, minus the boundary
	// paths, piped into a single --add --remove. Build output needs no rule
	// of its own (--exclude-standard honours .gitignore), and a build
	// product the repository deliberately tracks is repository content.
	listed, err := gitEnv(worktree, indexEnv, "ls-files", "-z", "-c", "-o", "--exclude-standard", "--", ".")
	if err != nil {
		return "", fmt.Errorf("list the worktree's files: %w", err)
	}
	prefix := strings.Trim(strings.TrimSpace(artifactPrefix), "/")
	var kept []byte
	for _, path := range strings.Split(listed, "\x00") {
		if path == "" || snapshotExcluded(path, prefix) {
			continue
		}
		kept = append(kept, []byte(path)...)
		kept = append(kept, 0)
	}
	if len(kept) > 0 {
		if _, err := gitStdin(worktree, indexEnv, kept, "update-index", "-z", "--add", "--remove", "--stdin"); err != nil {
			return "", fmt.Errorf("stage the worktree's changes: %w", err)
		}
	}
	tree, err := gitEnv(worktree, indexEnv, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write the snapshot tree: %w", err)
	}
	commit, err = gitEnv(worktree, nil, "commit-tree", tree, "-p", "HEAD",
		"-m", "ticfac: work-in-progress snapshot at the wall-clock stop — not evidence, never merged")
	if err != nil {
		return "", fmt.Errorf("commit the snapshot: %w", err)
	}
	if _, err := gitEnv(worktree, nil, "update-ref", ref, commit); err != nil {
		return "", fmt.Errorf("point %s at the snapshot: %w", ref, err)
	}
	return commit, nil
}

// snapshotExcluded is the boundary at snapshot scale (pbb's care, rj0's
// rule): nothing under .tick/ or .ticfac/ and nothing under the attempt's
// own artifact prefix — where the report lives, archived separately — may
// ride along as work. Any path SEGMENT naming those directories is
// excluded, at any depth; tracked content those paths already carry in
// HEAD stays as HEAD has it, because the snapshot never undoes a commit —
// what is excluded is the agent's uncommitted writing under them.
func snapshotExcluded(path, artifactPrefix string) bool {
	if path == artifactPrefix || strings.HasPrefix(path, artifactPrefix+"/") {
		return true
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".tick" || seg == ".ticfac" {
			return true
		}
	}
	return false
}
