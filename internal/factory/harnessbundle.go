package factory

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
)

// The harness package (epic 43y, tick xd3): the pi-durable worker harness the
// factory bundle imports for its WorkerAgent Durable Object
// (cloudflare/src/worker-agent.ts), through a pnpm link to ../harness. A
// third payload beside the bundle and the image context, with the same
// seam-and-cache shape, so the staging mechanics stay testable against a fake.

// harnessFS is the embedded harness package, rooted at the repository root:
// its paths carry the "harness" prefix. Wired by payload.go.
var harnessFS fs.FS

// harnessRoot is the prefix the embedded FS uses for the harness package.
const harnessRoot = "harness"

// harnessRelativeToBundle is where the harness package sits relative to the
// bundle directory — in this repository and in every staged copy. It is one
// half of a pair that must not drift: cloudflare/package.json's
// `"ticfac-harness": "link:../harness"` says the same thing, and the guard in
// harnessbundle_test fails first if they part.
const harnessRelativeToBundle = "../harness"

var (
	harnessPathsOnce sync.Once
	harnessPathsList []string
	harnessPathsErr  error
)

// resetHarnessCaches drops what HarnessPaths memoized; resetPayloadCaches
// calls it, so one reset covers every payload.
func resetHarnessCaches() {
	harnessPathsOnce = sync.Once{}
	harnessPathsList = nil
	harnessPathsErr = nil
}

// HarnessPaths returns every file in the embedded harness package, as
// slash-separated paths relative to harness/, sorted. Empty when this build
// carries no payload.
func HarnessPaths() []string {
	harnessPathsOnce.Do(func() {
		if harnessFS == nil {
			return
		}
		err := fs.WalkDir(harnessFS, harnessRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(harnessRoot, p)
			if relErr != nil {
				return relErr
			}
			harnessPathsList = append(harnessPathsList, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			// Unreachable: the tree is embedded at compile time.
			harnessPathsErr = err
			return
		}
		sort.Strings(harnessPathsList)
	})
	if harnessPathsErr != nil {
		return nil
	}
	return append([]string(nil), harnessPathsList...)
}

// ReadHarnessFile returns the contents of one embedded harness file.
func ReadHarnessFile(p string) ([]byte, error) {
	if harnessFS == nil {
		return nil, missingPayload("harness package")
	}
	return fs.ReadFile(harnessFS, path.Join(harnessRoot, p))
}

// HarnessDir is where the harness package is staged for a given bundle
// directory: at harnessRelativeToBundle, mirroring the repository layout, so
// the bundle's committed link resolves to it.
func HarnessDir(bundleDir string) string {
	return filepath.Join(bundleDir, filepath.FromSlash(harnessRelativeToBundle))
}

// MaterializeHarness writes the embedded harness package to dir, with
// Materialize's contract: every shipped file rewritten, anything ticfac wrote
// before and no longer ships removed (node_modules is the install's, kept).
func MaterializeHarness(dir string) error {
	if harnessFS == nil {
		return missingPayload("harness package")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("preparing the harness package directory: %w", err)
	}
	owned := make(map[string]bool)
	for _, p := range HarnessPaths() {
		data, err := ReadHarnessFile(p)
		if err != nil {
			return fmt.Errorf("reading the embedded harness package: %w", err)
		}
		dest := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("preparing %s: %w", filepath.Dir(dest), err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
		owned[filepath.ToSlash(p)] = true
	}
	return pruneStale(dir, owned)
}
