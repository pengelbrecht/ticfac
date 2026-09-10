package runstate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// git is the store's whole dependency on git: a runner in one repository, with
// an identity that does not depend on the machine's git config. The reconciler
// may be a container with no `user.email`, and a commit it cannot make is a
// record nobody has.
type git struct {
	dir string
	env []string
}

func newGit(dir, authorName, authorEmail string) *git {
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
		// A prompt in a reconciler is a hang, and a hang is worse than a
		// refusal: fail loudly instead of waiting for a terminal nobody is at.
		"GIT_TERMINAL_PROMPT=0",
	)
	return &git{dir: dir, env: env}
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
var safeArgs = []string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}

// try is run without the error wrapping: it hands back stderr so a caller that
// must classify a refusal (a push the lease rejected) can read it.
func (g *git) try(stdin []byte, extraEnv []string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command("git", append(append([]string{}, safeArgs...), args...)...)
	cmd.Dir = g.dir
	cmd.Env = append(append([]string{}, g.env...), extraEnv...)
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
	cmd := exec.Command("git", append(append([]string{}, safeArgs...), "cat-file", "blob", sha)...)
	cmd.Dir = g.dir
	cmd.Env = g.env
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git cat-file blob %s: %w: %s", sha, err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
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
	indexDir, err := os.MkdirTemp("", "ticfac-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(indexDir)
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
