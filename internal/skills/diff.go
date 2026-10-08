package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// DiffResult is one installed directory compared against the embedded
// bundle: what Install would change if run again.
type DiffResult struct {
	Dir           string
	Installed     bool   // false: dir does not exist at all
	Stamp         string // the installed StampFile's version; "" if unstamped
	BundleVersion string // the version Diff was asked to compare against
	Added         []string
	Removed       []string
	Changed       []string
}

// Drift reports whether dir differs from the bundle in any way Install
// would fix: missing entirely, an unstamped or differently-versioned copy,
// or files that no longer match.
func (d DiffResult) Drift() bool {
	if !d.Installed {
		return true
	}
	return d.Stamp != d.BundleVersion || len(d.Added) > 0 || len(d.Removed) > 0 || len(d.Changed) > 0
}

// Diff compares the skill named installed at dir against the embedded
// bundle, with bundleVersion as the version the bundle is compared
// against (the running binary's own Version, in the caller). A dir that
// does not exist is reported as DiffResult.Installed == false, not an
// error: a missing install is as ordinary an outcome as a stale one, and
// the caller decides what to do about each.
func Diff(name, dir, bundleVersion string) (DiffResult, error) {
	result := DiffResult{Dir: dir, BundleVersion: bundleVersion}

	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return result, nil
	case err != nil:
		return result, fmt.Errorf("diff %s: stat %s: %w", name, dir, err)
	case !info.IsDir():
		return result, fmt.Errorf("diff %s: %s exists and is not a directory", name, dir)
	}
	result.Installed = true

	bundlePaths, err := Paths(name)
	if err != nil {
		return result, err
	}
	diskPaths, err := diskFilePaths(dir)
	if err != nil {
		return result, fmt.Errorf("diff %s: %w", name, err)
	}
	onDisk := make(map[string]bool, len(diskPaths))
	for _, p := range diskPaths {
		onDisk[p] = true
	}
	inBundle := make(map[string]bool, len(bundlePaths))
	for _, p := range bundlePaths {
		inBundle[p] = true
	}

	for _, p := range diskPaths {
		if !inBundle[p] {
			result.Added = append(result.Added, p)
		}
	}
	for _, p := range bundlePaths {
		if !onDisk[p] {
			result.Removed = append(result.Removed, p)
			continue
		}
		want, err := Read(name, p)
		if err != nil {
			return result, err
		}
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			return result, fmt.Errorf("diff %s: %w", name, err)
		}
		if !bytes.Equal(got, want) {
			result.Changed = append(result.Changed, p)
		}
	}
	sort.Strings(result.Added)
	sort.Strings(result.Changed)

	if stamp, err := ReadStamp(dir); err == nil {
		result.Stamp = stamp.Version
	}
	return result, nil
}

// diskFilePaths returns every file under dir except the StampFile, sorted,
// as slash-separated paths relative to dir — the disk side of the same
// shape Paths returns for the bundle.
func diskFilePaths(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == StampFile {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
