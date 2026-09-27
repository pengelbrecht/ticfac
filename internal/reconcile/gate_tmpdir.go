package reconcile

import (
	"io/fs"
	"os"
	"path/filepath"
)

// The gate's own TMPDIR.
//
// When a gate that runs a test suite is killed at its bound, or its
// reconciler is killed under it, the suite leaves whatever it had in the temp
// directory: t.TempDir()s, go-build work directories and test binaries, from
// any package of any repository. ticfac did not make any of them and cannot
// name them. On this host (tick w9j) the user's TMPDIR held hundreds of Test*
// and go-build* directories, most of them left by gates.
//
// So the command runs with a TMPDIR of its own. It sits beside the tree being
// gated, inside a directory only that gate owns: the slot (gatedir.go), or
// the throwaway root when every slot is held. It is emptied before the command
// starts, which cleans up after a gate whose reconciler was killed. It is
// emptied again when the gate is collected, whether the command finished or
// was killed.
//
// The path belongs to the SLOT, so it is the same string on every gate in
// that slot. That matters: go keys a cached test result on the environment
// variables the test read, and every test that calls t.TempDir() reads
// TMPDIR. A TMPDIR that changed from gate to gate would make every gate a
// cold one and undo tick 6wh.

// gateTempDir is the TMPDIR a gate's command runs with.
func gateTempDir(tree string) string {
	return filepath.Join(filepath.Dir(tree), "tmp")
}

// freshGateTempDir empties the gate's TMPDIR and makes it again.
func freshGateTempDir(tree string) (string, error) {
	tmp := gateTempDir(tree)
	if err := removeWritable(tmp); err != nil {
		return "", err
	}
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return "", err
	}
	return tmp, nil
}

// removeWritable is os.RemoveAll over a tree a test suite may have left
// read-only, as a module cache is on disk. A directory without its write bit
// cannot have its entries removed, so each directory is made writable and
// the removal is tried again.
func removeWritable(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}
