package cloudflaresandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

// The git collect needs — the READ half only, and pointed at the
// ORCHESTRATOR'S OWN CHECKOUT, never at a worktree of the attempt's. The
// attempt's worker ran in a container that is gone; its work survives as the
// landing branch the container pushed to the remote, and the orchestrator
// holds a clone of that remote. Collect therefore reads the durable layer the
// same way worker-collect.ts reads it — commits, the changed-file list, the
// report the container's own entrypoint committed to the branch — only
// through git, because the Go side has the clone the TypeScript side has to
// do without.
//
// The helpers mirror internal/exec/herdr's rather than sharing its
// unexported ones, for the same reason that package states: a second executor
// owning its own mechanics is cheaper than exporting a private file's worth
// of plumbing across the seam, and the two sets stay comparable line by
// line.

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
	cmd := exec.Command(gitbin.Path(), args...)
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

// remoteHead is the remote's commit for one branch, read from the remote
// itself rather than from a tracking ref: a tracking ref is this checkout's
// memory of the remote, and the durable layer is the remote. Empty when the
// remote does not have the branch — which is a FACT here, the container's
// push never having landed, and never a verdict on the work.
func remoteHead(repo, remote, branch string) (string, error) {
	out, err := git(repo, "ls-remote", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(line, "\t")
		if ok && strings.TrimSpace(name) == "refs/heads/"+branch {
			return strings.TrimSpace(sha), nil
		}
	}
	return "", nil
}

// fetchBranch brings one branch's objects from the remote into the
// orchestrator's own checkout under a ref this executor owns, and answers the
// commit it landed at.
//
// --no-write-fetch-head and --refmap= keep this fetch off the two pieces of
// state every other git process in the checkout also writes: FETCH_HEAD, and
// the remote-tracking ref git updates opportunistically when a branch is
// fetched from a NAMED remote. Sharing either is a race — two runs in one
// checkout, or an operator's own `git fetch` — and only the refspec on this
// command line is updated. The refspec is FORCED for the same reason the
// reconciler's own fetch is: the remote is the durable authority, and a
// stale local ref must never wedge a later fetch on a non-fast-forward
// nobody can see.
func fetchBranch(repo, remote, branch string) (string, error) {
	dest := "refs/ticfac/exec/cloudflare-sandbox/" + fetch1D() + "/" + branch
	if _, err := git(repo, "fetch", "--quiet", "--no-write-fetch-head", "--refmap=",
		remote, "+refs/heads/"+branch+":"+dest); err != nil {
		return "", err
	}
	return resolveCommit(repo, dest)
}

// fetch1D is this PROCESS's fetch id, assigned on first use, so two
// executors in one process — or two runs in one checkout — never collide on
// a fetch destination. A pid alone is not enough for the same reason the
// reconciler's own fetch id says it is not: two of these can live in one
// process.
func fetch1D() string {
	if fetchID == "" {
		fetchID = strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(fetchSeq.Add(1), 10)
	}
	return fetchID
}

var (
	fetchID  string
	fetchSeq atomic.Uint64
)

// resolveCommit turns a revision into the commit sha it names, and fails when
// the revision is not in this repository.
func resolveCommit(dir, rev string) (string, error) {
	return git(dir, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
}

// commitsBeyond counts the commits on head that base does not have. The
// attempt's base is present in the orchestrator's checkout because the
// dispatch was cut there; a base that is not is an error to surface, never a
// count to guess.
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
// attempt's recorded base and the head its container pushed.
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

// showFile reads one path out of one commit, which is how the report comes
// off the pushed branch: the container is gone, and the branch is where its
// own entrypoint committed the report to.
func showFile(repo, ref, path string) (string, bool) {
	out, err := git(repo, "show", ref+":"+path)
	if err != nil {
		return "", false
	}
	return out, true
}

// shortSHA is the 12-character form refusal sentences read better in.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// repoExists says whether the configured repository is a checkout this
// executor can read git from, for the constructor-time refusal.
func repoExists(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := git(dir, "rev-parse", "--show-toplevel")
	return err == nil
}
