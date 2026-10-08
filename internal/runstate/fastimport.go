package runstate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/tempdir"
)

// The write-path half of the streaming plumbing (tick pul opened the read
// path: a held-open `git cat-file --batch`). This one writes: one
// `git fast-import` process builds EVERY commit a step holds, instead of a
// throwaway index per record.
//
// # What it was, measured
//
// One record — a checkpoint update, a note, a close — cost five git
// processes: hash-object, read-tree, update-index, write-tree and
// commit-tree, each in its own process, plus a temp directory created and
// removed per record. A reconcile end-to-end fixture spawns about a hundred
// and twenty of those per tick it lands, and process creation — not git's
// work — is the majority of that suite's wall clock (pul measured a spawned
// git at 21.6ms against 0.90ms for the same query through a held-open
// process; the write path had no held-open form until now).
//
// # What fast-import is here
//
// git fast-import is git's own plumbing for exactly this shape: a stream of
// commits on stdin, written as objects by one process. It is still real git
// writing the same objects in the same format — the fidelity guard beside
// this file builds the same record both ways and asserts the same sha — so
// the defect classes this store exists to survive (FETCH_HEAD collisions, a
// missing --refmap=, ref-lock contention) are untouched: none of them live
// in how a commit object is created, and every fetch and every push this
// store makes is unchanged.
//
// # The ref fast-import is pointed at
//
// A fast-import commit directive names a ref, and fast-import updates that
// ref when the stream ends. The store needs no such update — its CAS is the
// push's lease — so the stream is pointed at a THROWAWAY ref of this store
// instance's own (refs/ticfac/import/<fetch id>/<run>), updated with --force
// because successive flushes build on bases that need not contain each
// other. The ref is local to the run's checkout, never pushed, and holds
// the step's tip — which is also what keeps an unpushed chain's objects
// reachable for the flush's retries.
//
// # Failure
//
// Any failure of the stream — a git without fast-import, a malformed
// directive, a full disk — falls back to the per-record path this file
// replaces (git.go's commitWithFile and commitWithChanges), exactly the way
// a broken batch reader falls back to one process per read (batch.go): a
// store that cannot fast-import keeps working, and the only cost is speed.
var errFastImportUnavailable = fmt.Errorf("runstate: fast-import is not available")

// importChange is one path change in a commit: what update-index --cacheinfo
// (--add for a record, the change set's own mode) and update-index
// --force-remove put into the throwaway index, stated as data.
type importChange struct {
	removed bool
	mode    string // "100644" unless a change set says otherwise
	blob    string // the existing blob's sha
	path    string
}

// importCommit is one record's commit: the parent it builds on ("" means the
// previous commit of the same stream, which is how a step's chain hangs
// together), the message the per-record path would give it, the instant the
// record was written (the store's clock, captured at write time — commit-tree
// would have used the moment of its own invocation), and the path changes.
type importCommit struct {
	parent  string
	message string
	when    time.Time
	changes []importChange
}

// importCommits writes commits through one `git fast-import` process, in
// order, and answers their shas in the same order. The git struct's
// fastImport field is the seam a test wraps or breaks; this method is the
// production half of that seam.
//
// The stream is built in memory and handed to one process; the shas come
// back from fast-import's own --export-marks file, which is the only way a
// caller can learn what it wrote.
func (g *git) importCommits(ref string, commits []importCommit) ([]string, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	marksDir, removeMarks, err := tempdir.Make("ticfac-import-")
	if err != nil {
		return nil, err
	}
	defer removeMarks()
	marks := filepath.Join(marksDir, "marks")

	var stream bytes.Buffer
	nextMark := 1
	shas := make([]string, len(commits))
	for i, c := range commits {
		mark := nextMark
		nextMark++
		fmt.Fprintf(&stream, "commit %s\n", ref)
		fmt.Fprintf(&stream, "mark :%d\n", mark)
		fmt.Fprintf(&stream, "author %s <%s> %d +0000\n", g.name, g.email, c.when.Unix())
		fmt.Fprintf(&stream, "committer %s <%s> %d +0000\n", g.name, g.email, c.when.Unix())
		// commit-tree appends the trailing newline to -m it does not find one
		// on, so the durable message of every record this store has ever
		// written ends in one; the byte-for-byte guard beside this file holds
		// the stream to the same spelling.
		message := c.message
		if !strings.HasSuffix(message, "\n") {
			message += "\n"
		}
		fmt.Fprintf(&stream, "data %d\n", len(message))
		stream.WriteString(message)
		switch {
		case c.parent != "":
			fmt.Fprintf(&stream, "from %s\n", c.parent)
		case i > 0:
			fmt.Fprintf(&stream, "from :%d\n", mark-1)
		}
		for _, change := range c.changes {
			if change.removed {
				fmt.Fprintf(&stream, "D %s\n", change.path)
				continue
			}
			fmt.Fprintf(&stream, "M %s %s %s\n", change.mode, change.blob, change.path)
		}
	}
	stream.WriteString("done\n")

	cmd := exec.Command(gitbin.Path(), append(append([]string{}, safeArgs...),
		"fast-import", "--force", "--quiet", "--export-marks="+marks)...)
	cmd.Dir = g.dir
	cmd.Env = g.env
	cmd.Stdin = bytes.NewReader(stream.Bytes())
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	if runErr != nil {
		return nil, fmt.Errorf("git fast-import: %w: %s", runErr, strings.TrimSpace(errBuf.String()))
	}
	exported, err := os.ReadFile(marks)
	if err != nil {
		return nil, err
	}
	byMark := map[int]string{}
	for _, line := range strings.Split(string(exported), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[0], ":") {
			continue
		}
		mark, err := strconv.Atoi(fields[0][1:])
		if err != nil {
			continue
		}
		byMark[mark] = fields[1]
	}
	for i := range commits {
		sha := byMark[i+1]
		if sha == "" {
			return nil, fmt.Errorf("git fast-import: the export names no commit for mark %d", i+1)
		}
		shas[i] = sha
	}
	return shas, nil
}