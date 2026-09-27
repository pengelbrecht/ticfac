// Package tempdir is where every temporary directory ticfac makes comes from,
// and the one account of how each of them goes away (tick w9j).
//
// Measured 2026-09-27: the host's temp directory held ~1,370 ticfac-* dirs
// going back two weeks — tracker, merge, gate and index trees, test binaries —
// and several of the trees were still registered as worktrees of the checkout
// they came from. Each one was made with a defer to remove it, and each one
// leaked on a path a defer does not run on:
//
//   - run-epic's signal handler leaves by os.Exit, which runs no defer: every
//     tree a run had open when it was SIGTERMed or interrupted stayed. Make and
//     Register record each cleanup, and ReleaseAll is what that handler runs
//     before it exits.
//   - a process that is SIGKILLed, or a go test binary that a gate or a timeout
//     kills, runs nothing at all. Every name Make hands out carries the pid of
//     the process that made it, and Sweep removes the ones whose process is
//     gone and whose contents nobody has touched for a day.
package tempdir

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Prefix is what every directory this package makes is named with, and the
// only names Sweep will ever touch.
const Prefix = "ticfac-"

// SlotsMarker names the gate's slot roots (reconcile/gatedir.go): persistent
// on purpose, bounded per repository, and reused so Go's test cache hits.
// Sweep never removes one.
const SlotsMarker = "-slots-"

var (
	mu      sync.Mutex
	pending = map[uint64]func(){}
	nextID  uint64
)

// Make creates a directory in os.TempDir() named prefix + "p<pid>-" + a random
// suffix, and returns the one function that removes it. prefix must start with
// Prefix. The remover is idempotent and safe to call alongside ReleaseAll.
func Make(prefix string) (dir string, remove func(), err error) {
	if !strings.HasPrefix(prefix, Prefix) {
		return "", nil, fmt.Errorf("tempdir: %q is not a %s name", prefix, Prefix)
	}
	dir, err = os.MkdirTemp("", Pattern(prefix))
	if err != nil {
		return "", nil, err
	}
	return dir, Register(func() { _ = os.RemoveAll(dir) }), nil
}

// Pattern is the os.MkdirTemp pattern Make uses: prefix + "p<pid>-". It is
// for a directory that must NOT go with ReleaseAll — a test binary's own root,
// which its TestMain removes — but should still be one Sweep can attribute.
func Pattern(prefix string) string {
	return prefix + "p" + strconv.Itoa(os.Getpid()) + "-"
}

// Register records a cleanup that must run however this process leaves, and
// returns the function that runs it now. Each cleanup runs at most once:
// whichever of the returned function and ReleaseAll gets there first.
func Register(cleanup func()) (release func()) {
	mu.Lock()
	nextID++
	id := nextID
	pending[id] = cleanup
	mu.Unlock()
	return func() {
		mu.Lock()
		c, ok := pending[id]
		delete(pending, id)
		mu.Unlock()
		if ok {
			c()
		}
	}
}

// ReleaseAll runs every cleanup still pending. It is for a process about to
// leave by os.Exit, which runs no defer.
func ReleaseAll() {
	mu.Lock()
	all := pending
	pending = map[uint64]func(){}
	mu.Unlock()
	for _, c := range all {
		c()
	}
}

// Pending is how many cleanups have not run yet.
func Pending() int {
	mu.Lock()
	defer mu.Unlock()
	return len(pending)
}

// owner is the pid Make wrote into a name: "-p<pid>-" followed by
// MkdirTemp's random digits at the end of the name.
var owner = regexp.MustCompile(`-p([0-9]+)-[0-9]+$`)

// Sweep removes what a killed process left in root: directories named with
// Prefix whose owning process (when the name records one) is gone AND nothing
// inside which has been modified within olderThan. A slot root is never
// removed. It answers with what it removed.
//
// It is conservative on purpose — a directory it cannot decide about is kept:
// one that is still being written to (a live run's tracker tree, a run
// started by an older build whose names carry no pid), one whose pid is alive
// (or reused, which the kernel cannot tell apart), one it cannot walk. A
// directory that was a git worktree leaves a registration whose directory is
// now missing; the repository's `git worktree prune` drops it, which every
// reconcile run does at its start.
func Sweep(root string, olderThan time.Duration, now time.Time) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-olderThan)
	var removed []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, Prefix) || strings.Contains(name, SlotsMarker) {
			continue
		}
		if m := owner.FindStringSubmatch(name); m != nil {
			if pid, err := strconv.Atoi(m[1]); err == nil && (pid == os.Getpid() || processAlive(pid)) {
				continue
			}
		}
		path := filepath.Join(root, name)
		if touchedSince(path, cutoff) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			continue
		}
		removed = append(removed, path)
	}
	return removed, nil
}

// touchedSince reports whether anything under path, path included, was
// modified after cutoff — or could not be read, which counts as touched.
func touchedSince(path string, cutoff time.Time) bool {
	errFresh := fmt.Errorf("fresh")
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			return errFresh
		}
		return nil
	})
	return err != nil
}
