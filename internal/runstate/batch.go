package runstate

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

// The object reader, held open.
//
// Every read used to be its own `git cat-file blob <sha>` process. Git answers
// such a query in well under a millisecond; on this project's hosts the FORK
// costs about twenty. A single reconcile end-to-end test spawned 1148 git
// processes, and process creation — not git, and not the tests' logic — was
// the majority of the suite's wall clock.
//
// `git cat-file --batch` is git's own answer to that shape: one process, a sha
// per line on stdin, a header and the bytes back on stdout. Measured here, the
// same query costs 0.90ms through the batch against 21.6ms through a fresh
// process.
//
// This keeps full fidelity, which is the point. The defects this store exists
// to survive — FETCH_HEAD collisions, a missing --refmap=, a shared peek ref,
// ref-lock contention — all live in real git's behaviour, and a reimplementation
// of the object format in Go would have reproduced none of them. The batch is
// still git, reading the same objects the same way; only the number of
// processes changes.
type objectReader struct {
	mu     sync.Mutex
	dir    string
	env    []string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	broken bool
}

func newObjectReader(dir string, env []string) *objectReader {
	return &objectReader{dir: dir, env: env}
}

// start brings the batch process up. It is lazy: a store that never reads an
// object never pays for one.
func (r *objectReader) start() error {
	if r.cmd != nil {
		return nil
	}
	cmd := exec.Command(gitbin.Path(), append(append([]string{}, safeArgs...), "cat-file", "--batch")...)
	cmd.Dir = r.dir
	cmd.Env = r.env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// stderr is deliberately dropped rather than collected: cat-file --batch
	// reports a missing object on STDOUT as "<sha> missing", which is the
	// answer this reader parses. Anything on stderr is the process dying, and
	// that shows up as a read error below with more context than the text.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	r.cmd, r.stdin, r.stdout = cmd, stdin, bufio.NewReader(stdout)
	return nil
}

// blob returns one object's bytes.
//
// On any protocol surprise the reader marks itself broken and refuses to keep
// using the process, because a batch stream that has lost its place answers the
// NEXT caller with this caller's bytes. A wrong answer from a durable-state
// store is worse than a slow one, so the failure is loud and the fallback is a
// fresh process per read.
func (r *objectReader) blob(sha string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.broken {
		return nil, errReaderBroken
	}
	if err := r.start(); err != nil {
		r.broken = true
		return nil, err
	}
	if _, err := io.WriteString(r.stdin, sha+"\n"); err != nil {
		r.broken = true
		return nil, fmt.Errorf("git cat-file --batch: ask for %s: %w", sha, err)
	}
	header, err := r.stdout.ReadString('\n')
	if err != nil {
		r.broken = true
		return nil, fmt.Errorf("git cat-file --batch: read the header for %s: %w", sha, err)
	}
	fields := strings.Fields(strings.TrimRight(header, "\n"))
	if len(fields) == 2 && fields[1] == "missing" {
		return nil, fmt.Errorf("git cat-file --batch: %s is missing", sha)
	}
	if len(fields) != 3 {
		r.broken = true
		return nil, fmt.Errorf("git cat-file --batch: unparseable header %q for %s", header, sha)
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil {
		r.broken = true
		return nil, fmt.Errorf("git cat-file --batch: unparseable size in %q: %w", header, err)
	}
	// size bytes, then the newline git writes after them. Both are read, or
	// the stream is left mid-object for the next caller.
	content := make([]byte, size)
	if _, err := io.ReadFull(r.stdout, content); err != nil {
		r.broken = true
		return nil, fmt.Errorf("git cat-file --batch: read %d bytes of %s: %w", size, sha, err)
	}
	if _, err := r.stdout.Discard(1); err != nil {
		r.broken = true
		return nil, fmt.Errorf("git cat-file --batch: read the separator after %s: %w", sha, err)
	}
	return content, nil
}

// close stops the batch process. A store that is done with it should say so;
// leaving it running would hold a git process per store for the life of the
// run.
func (r *objectReader) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil {
		return
	}
	_ = r.stdin.Close()
	_ = r.cmd.Wait()
	r.cmd, r.stdin, r.stdout = nil, nil, nil
}

var errReaderBroken = fmt.Errorf("the cat-file batch is broken and will not be reused")

// blobThroughBatch reads an object through the held-open batch, falling back to
// a fresh process when the batch cannot serve it.
//
// The fallback is not politeness: it is what makes adopting the batch safe. A
// store whose batch dies keeps working exactly as it did before this change,
// one process per read, and the only cost is speed.
func (g *git) blobThroughBatch(sha string) ([]byte, error) {
	if g.reader != nil {
		content, err := g.reader.blob(sha)
		if err == nil {
			return content, nil
		}
		if err != errReaderBroken && strings.Contains(err.Error(), "is missing") {
			return nil, err
		}
	}
	return g.catFileProcess(sha)
}

// catFileProcess is the original one-process read, kept as the fallback and as
// the thing the batch is measured against.
func (g *git) catFileProcess(sha string) ([]byte, error) {
	cmd := exec.Command(gitbin.Path(), append(append([]string{}, safeArgs...), "cat-file", "blob", sha)...)
	cmd.Dir = g.dir
	cmd.Env = g.env
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git cat-file blob %s: %w: %s", sha, err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}
