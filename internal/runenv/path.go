package runenv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// # The host's installed ticfac
//
// The environment is not the only thing a gate and a test binary inherit from
// the machine that describes something other than the tree under test: PATH
// does too. The operator's Mac has ticfac installed (`ticfac`,
// `ticfac-exec-subprocess`, `ticfac-dash` in ~/.local/bin), and ticfac's own
// code resolves `ticfac-exec-subprocess` beside its executable and then on
// PATH (reconcile.CheckExecutor). Epic hn6's run added two tests that reached
// that lookup without providing the binary: its integrated gate passed on the
// Mac, because the installed copy answered, and GitHub CI — which installs no
// ticfac — failed both. A gate that passes what CI fails for a reason of the
// host's is not a gate.
//
// So both layers hide the host's ticfac binaries as well. Not by dropping the
// directories that hold them: ~/.local/bin also holds tk, herdr and claude,
// which this repository's tests use when present (internal/tk's real-tk
// test, the herdr wire-vocabulary drift test) and another repository's gate
// may need. Each such PATH directory is replaced, in place, by a mirror that
// links every entry of it EXCEPT ticfac's own binaries. A test that needs a
// ticfac binary builds it from the tree (or stubs it) and puts it on PATH
// itself, which is the only copy that says anything about this tree.

// IsTicfacBinary reports whether a file name is one of ticfac's own binaries
// — `ticfac` or any `ticfac-*` (ticfac-exec-subprocess, ticfac-dash, …).
func IsTicfacBinary(name string) bool {
	return name == "ticfac" || strings.HasPrefix(name, "ticfac-")
}

// HideTicfacBinaries returns path with every directory that holds a ticfac
// binary replaced by a mirror of it under mirrorRoot that lacks them, and the
// directories it replaced. A directory without one, or that cannot be read,
// is kept as it is, in its place. path is a PATH value
// (os.PathListSeparator-separated); mirrorRoot is created if it is missing.
// A mirror is named for the directory and its listing, so one already made
// for the same directory, as it stands now, is reused.
func HideTicfacBinaries(path, mirrorRoot string) (string, []string, error) {
	if path == "" {
		return path, nil, nil
	}
	dirs := filepath.SplitList(path)
	var hidden []string
	for i, dir := range dirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var keep []string
		holds := false
		for _, e := range entries {
			if IsTicfacBinary(e.Name()) {
				holds = true
				continue
			}
			keep = append(keep, e.Name())
		}
		if !holds {
			continue
		}
		mirror, err := mirrorOf(dir, keep, mirrorRoot)
		if err != nil {
			return path, hidden, fmt.Errorf("mirror %s without its ticfac binaries: %w", dir, err)
		}
		dirs[i] = mirror
		hidden = append(hidden, dir)
	}
	return strings.Join(dirs, string(os.PathListSeparator)), hidden, nil
}

// mirrorOf makes (or finds) the mirror of dir that links exactly keep. It is
// built under a temporary name and renamed into place, so two processes
// making the same mirror at once both end with a complete one.
func mirrorOf(dir string, keep []string, mirrorRoot string) (string, error) {
	sort.Strings(keep)
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00", dir)
	for _, name := range keep {
		fmt.Fprintf(sum, "%s\x00", name)
	}
	id := hex.EncodeToString(sum.Sum(nil))[:16]
	mirror := filepath.Join(mirrorRoot, "path-"+filepath.Base(dir)+"-"+id)
	if complete(mirror, len(keep)) {
		return mirror, nil
	}
	// Missing, or thinned by a temp-directory sweep: made afresh.
	_ = os.RemoveAll(mirror)
	if err := os.MkdirAll(mirrorRoot, 0o755); err != nil {
		return "", err
	}
	building, err := os.MkdirTemp(mirrorRoot, ".building-")
	if err != nil {
		return "", err
	}
	for _, name := range keep {
		if err := os.Symlink(filepath.Join(dir, name), filepath.Join(building, name)); err != nil {
			_ = os.RemoveAll(building)
			return "", err
		}
	}
	if err := os.Rename(building, mirror); err != nil {
		_ = os.RemoveAll(building)
		// Another process renamed its own complete mirror into place first.
		if complete(mirror, len(keep)) {
			return mirror, nil
		}
		return "", err
	}
	return mirror, nil
}

// complete reports whether mirror is a directory of exactly n entries.
func complete(mirror string, n int) bool {
	entries, err := os.ReadDir(mirror)
	return err == nil && len(entries) == n
}

// MirrorRoot is where a process keeps the PATH mirrors HideTicfacBinaries
// makes. It is stable for a host and user, and a mirror is named for what it
// mirrors, so PATH carries the same value on every gate and every test
// binary: go keys a cached test result on the environment a test read, PATH
// among them for anything that resolves a binary, and a PATH that moved per
// gate would make every gate a cold one.
func MirrorRoot() string {
	return filepath.Join(os.TempDir(), "ticfac-path-mirrors")
}

// HidePath is [HideTicfacBinaries] over this process's environment: the PATH
// entry of env rewritten, every other entry as it was, and the directories it
// hid. On an error the PATH is left as it was and the error returned: a
// mirror that cannot be made (a full or read-only temp directory) is the
// host's problem to report, not a reason to refuse a gate.
func HidePath(env []string) ([]string, []string, error) {
	out := make([]string, 0, len(env))
	var hidden []string
	var firstErr error
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path, dirs, err := HideTicfacBinaries(value, MirrorRoot())
			if err != nil {
				firstErr = err
			} else {
				entry, hidden = "PATH="+path, dirs
			}
		}
		out = append(out, entry)
	}
	return out, hidden, firstErr
}
