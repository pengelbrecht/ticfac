package runstate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/tempdir"
)

// git is the store's whole dependency on git: a runner in one repository, with
// an identity that does not depend on the machine's git config. The reconciler
// may be a container with no `user.email`, and a commit it cannot make is a
// record nobody has.
type git struct {
	dir string
	env []string

	// retry is the bound on waiting through a transient remote failure (tick
	// enj). It applies to the subcommands that reach the network and to
	// nothing else — see try.
	retry RemoteRetry

	// reader is the held-open object reader (see batch.go). Nil is valid
	// and means every read spawns its own process, which is what this store
	// did before.
	reader *objectReader
}

func newGit(dir, authorName, authorEmail string, retry RemoteRetry) *git {
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
		// A prompt in a reconciler is a hang, and a hang is worse than a
		// refusal: fail loudly instead of waiting for a terminal nobody is at.
		"GIT_TERMINAL_PROMPT=0",
	)
	// The transport bound (gitbin.TransportEnv: a git that has gone silent
	// must give up) is added per invocation, in once, where the guard that
	// every network git carries it can see it. The held-open object reader
	// never reaches a remote and does without it.
	g := &git{dir: dir, env: env, retry: retry}
	g.reader = newObjectReader(dir, env)
	return g
}

// run returns trimmed stdout, or an error carrying stderr — git says why in
// stderr and a wrapped exit status alone is unactionable.
func (g *git) run(args ...string) (string, error) {
	out, _, err := g.try(nil, nil, args...)
	return out, err
}

func (g *git) runWith(extraEnv []string, args ...string) (string, error) {
	out, _, err := g.try(nil, extraEnv, args...)
	return out, err
}

func (g *git) runInput(stdin []byte, args ...string) (string, error) {
	out, _, err := g.try(stdin, nil, args...)
	return out, err
}

// safeArgs are prepended to every invocation. A reconciler is not a person at a
// terminal: a repository configured to sign commits or tags would stop this
// store dead on a passphrase prompt, and a run's record is not the place to
// carry a signature nothing verifies.
//
// Nor may the store's fetch start a background repack of the repository it
// is about to write records into: gitbin.NoAutoMaintenance says why, and
// what failing to say it cost (tick mel).
var safeArgs = append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, gitbin.NoAutoMaintenance...)

// try is run without the error wrapping: it hands back stderr so a caller that
// must classify a refusal (a push the lease rejected) can read it.
//
// A subcommand that reaches the network is run through the retry bound (tick
// enj): a connection the remote reset is waited through rather than stopping
// the run, and everything else — a lease the remote refused, a ref that is
// not there, a credential that is wrong — comes back on the first attempt,
// untouched, because that is what the callers below are written against.
//
// A retried PUSH has one seam, and the CAS in store.go closes it (tick o82).
// If the remote accepted a push and the connection died before git read the
// response, the retry pushes against a lease naming the OLD head while origin
// may already be at this writer's own commit — and a remote that committed
// the first push late refuses the retry outright. Epic-gvc halted on exactly
// that, as conflict_stale_sha. So a refusal that follows an attempt this
// writer never heard back from is not, by itself, evidence of anybody else;
// tryCounted is how the store learns there was such an attempt, and put asks
// whether origin's head is this writer's own write before calling it a loss.
func (g *git) try(stdin []byte, extraEnv []string, args ...string) (stdout, stderr string, err error) {
	stdout, stderr, _, err = g.tryCounted(stdin, extraEnv, args...)
	return stdout, stderr, err
}

// tryCounted is try, also saying how many times the command ran. More than
// one means an earlier attempt failed in transit, and what it did on the
// remote is unknown.
func (g *git) tryCounted(stdin []byte, extraEnv []string, args ...string) (stdout, stderr string, tries int, err error) {
	if sub, remote := RemoteSubcommand(args); remote {
		retryErr := g.retry.Do("git "+sub, func() error {
			tries++
			stdout, stderr, err = g.once(stdin, extraEnv, args...)
			return err
		})
		// The last attempt's stdout and stderr are what a caller reading a
		// refusal out of stderr should see; the error is the bound's, which
		// wraps that attempt's and names how many there were.
		return stdout, stderr, tries, retryErr
	}
	stdout, stderr, err = g.once(stdin, extraEnv, args...)
	return stdout, stderr, 1, err
}

// once is one invocation: no retry, no classification, just the process.
func (g *git) once(stdin []byte, extraEnv []string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(gitbin.Path(), append(append([]string{}, safeArgs...), args...)...)
	cmd.Dir = g.dir
	cmd.Env = append(append(append([]string{}, g.env...), gitbin.TransportEnv()...), extraEnv...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout, stderr = strings.TrimRight(out.String(), "\n"), strings.TrimSpace(errBuf.String())
	if runErr != nil {
		return stdout, stderr, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), runErr, stderr)
	}
	return stdout, stderr, nil
}

// catFile returns a blob's bytes, untrimmed: a record's content is what it is,
// and a reader that eats a trailing newline hands back a different file.
func (g *git) catFile(sha string) ([]byte, error) {
	return g.blobThroughBatch(sha)
}

// lsTree lists the blobs under a pathspec at a commit, as path -> blob sha.
// This is the store's VIEW of origin: the only thing a sha guard may compare
// against.
func (g *git) lsTree(commit, pathspec string) (map[string]string, error) {
	out, err := g.run("ls-tree", "-r", commit, "--", pathspec)
	if err != nil {
		return nil, err
	}
	view := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		// <mode> SP <type> SP <sha> TAB <path>
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("git ls-tree wrote a line this reader cannot parse: %q", line)
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		view[path] = fields[2]
	}
	return view, nil
}

// writeBlob stores content as a loose object and returns its sha.
func (g *git) writeBlob(content []byte) (string, error) {
	return g.runInput(content, "hash-object", "-w", "--stdin")
}

// commitWithFile builds a commit that is `base` plus one file, without touching
// a working tree or the repository's index: the tree is assembled in a
// throwaway index so the store can run in a repository a human is also using.
func (g *git) commitWithFile(base, path, blob, message string) (string, error) {
	indexDir, removeIndex, err := tempdir.Make("ticfac-index-")
	if err != nil {
		return "", err
	}
	defer removeIndex()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(indexDir, "index")}

	if base != "" {
		if _, err := g.runWith(env, "read-tree", base); err != nil {
			return "", err
		}
	}
	if _, err := g.runWith(env, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path); err != nil {
		return "", err
	}
	tree, err := g.runWith(env, "write-tree")
	if err != nil {
		return "", err
	}

	args := []string{"commit-tree", tree}
	if base != "" {
		args = append(args, "-p", base)
	}
	args = append(args, "-m", message)
	return g.run(args...)
}

// refusedPush reports whether a failed push was the remote refusing the update
// — a lost lease or a non-fast-forward — rather than git failing to run at all.
// The difference matters: the first is the compare-and-swap doing its job and
// is re-examined against origin; the second is an error.
func refusedPush(stderr string) bool {
	return strings.Contains(stderr, "[rejected]") ||
		strings.Contains(stderr, "stale info") ||
		strings.Contains(stderr, "non-fast-forward") ||
		strings.Contains(stderr, "fetch first") ||
		strings.Contains(stderr, "cannot lock ref")
}
