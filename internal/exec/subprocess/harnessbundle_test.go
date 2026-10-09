package subprocess

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	ticfac "github.com/pengelbrecht/ticfac"
	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// TestLocalHarnessBundleMatchesItsSources is the staleness gate (tick 0ek,
// "harness as a machine prerequisite"): harness/scripts/build-local-bundle.mjs
// writes a sources hash beside the embedded bundle every time it regenerates
// it, and this re-walks the SAME sources in pure Go — no node, no esbuild —
// so it runs in `make gate` exactly like any other short test, and fails
// when the committed hash and the tree disagree: harness/src (or
// package.json / pnpm-lock.yaml) changed since the bundle was last built,
// and nobody ran the script to pick it up.
//
// This does not re-bundle and so cannot catch every possible drift (a
// hand-edited embed/local-main.bundle.mjs with its sources untouched, say) —
// see localHarnessBundleCurrentSourcesHash's own comment for why that
// trade is worth it here.
func TestLocalHarnessBundleMatchesItsSources(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	got, err := localHarnessBundleCurrentSourcesHash(filepath.Join(root, "harness"))
	if err != nil {
		t.Fatal(err)
	}
	want := ticfac.LocalHarnessBundleSourcesHash()
	if want == "" {
		t.Fatal("this build carries no committed sources hash for the embedded local harness bundle")
	}
	if got != want {
		t.Fatalf("harness/src (or package.json / pnpm-lock.yaml) changed since the embedded local harness "+
			"bundle was last built:\n  tree hash:      %s\n  committed hash: %s\n"+
			"run `make harness-bundle` (needs node and harness/'s pnpm packages installed) and commit its output",
			got, want)
	}
}

// localHarnessBundleCurrentSourcesHash recomputes, in pure Go, the exact
// digest harness/scripts/build-local-bundle.mjs's hashSources writes beside
// the embedded bundle: a sha256 over every file under harness/src (skipping
// any node_modules) plus harness/package.json and harness/pnpm-lock.yaml —
// each file's slash-separated path relative to harnessRoot, then its
// content, in path-sorted order.
//
// Deliberately conservative, not precise: it covers the whole harness/src
// tree, not just src/local/main.ts's own import graph, the same trade the
// JS generator documents. A false "stale" that an unrelated harness edit
// provokes costs a rerun of the generator; a false "fresh" that a real
// drift slips past costs a local pi-durable run with no npm install to
// patch itself up — the asymmetry is why this errs wide.
func localHarnessBundleCurrentSourcesHash(harnessRoot string) (string, error) {
	type sourceFile struct {
		rel, abs string
	}
	var files []sourceFile
	srcRoot := filepath.Join(harnessRoot, "src")
	if err := filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(harnessRoot, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, sourceFile{rel: filepath.ToSlash(rel), abs: path})
		return nil
	}); err != nil {
		return "", fmt.Errorf("walk %s: %w", srcRoot, err)
	}
	for _, extra := range []string{"package.json", "pnpm-lock.yaml"} {
		files = append(files, sourceFile{rel: extra, abs: filepath.Join(harnessRoot, extra)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })

	digest := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(f.abs)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", f.abs, err)
		}
		digest.Write([]byte(f.rel))
		digest.Write([]byte{0})
		digest.Write(data)
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
