package contracts

import (
	"fmt"
	"os"
	"path/filepath"
)

// Dir returns the vendored contracts directory at the repository root.
//
// Tests live at varying depths, and the bundle is one directory that all of
// them read, so the path is derived from the module root rather than spelled
// as a different number of `..` in every reader.
func Dir() (string, error) {
	root, err := RepoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, DirName), nil
}

// DirName is where the vendored bundle lives, relative to the repository root.
//
// It is the root, exactly as cloud/factory/CONTRACTS.md prescribes for a
// consuming repository: the copy sits where ticks' own copy sits, so a reader
// ported from ticks resolves the same relative path.
const DirName = "contracts"

// RepoRoot walks up from the working directory to the module root — the
// directory holding go.mod.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s: cannot locate the repository root", dir)
		}
		dir = parent
	}
}
