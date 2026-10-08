package subprocess

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	ticfac "github.com/pengelbrecht/ticfac"
)

// cachedLocalHarnessBundle writes the embedded local pi-durable harness
// bundle (ticfac.LocalHarnessBundleJS, built by harness/scripts/
// build-local-bundle.mjs from harness/src/local/main.ts) to a cache
// directory and returns its path — once per distinct build, never per
// attempt: the cache key is the bundle's own content hash, so two ticfac
// binaries built from different commits cache beside each other rather than
// racing to overwrite one file, and a binary that has not changed since the
// last attempt pays nothing but a stat.
//
// This is the production path for the "pi" runner (runner.go): no checkout
// of harness/ and no npm install on the machine running the attempt, only
// Node. $TICFAC_HARNESS_DIR bypasses this entirely for harness development
// (executor.go's harnessSourceDir).
func cachedLocalHarnessBundle() (string, error) {
	data := ticfac.LocalHarnessBundleJS()
	if len(data) == 0 {
		return "", fmt.Errorf("this build carries no embedded local harness bundle")
	}
	sum := sha256.Sum256(data)
	dir := filepath.Join(harnessBundleCacheRoot(), hex.EncodeToString(sum[:16]))
	path := filepath.Join(dir, "local-main.bundle.mjs")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat the cached harness bundle at %s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("make the harness bundle cache directory %s: %w", dir, err)
	}
	// Written to a per-process temp name and renamed into place, so a second
	// process racing to populate the same cache entry never serves a
	// partially written file: rename is atomic within one directory.
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("write the harness bundle cache file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("install the harness bundle cache file: %w", err)
	}
	return path, nil
}

// harnessBundleCacheRoot is where cachedLocalHarnessBundle stages the
// embedded bundle: $TICFAC_CACHE_DIR when an operator named one (the same
// preference surface TICFAC_HARNESS_DIR belongs to), else the OS user cache
// directory, else a temp directory as the last resort — this cache is a
// performance optimization, never a correctness requirement, so a machine
// with no durable cache directory still runs, it just re-stages every time.
func harnessBundleCacheRoot() string {
	if dir := os.Getenv("TICFAC_CACHE_DIR"); dir != "" {
		return filepath.Join(dir, "harness")
	}
	if base, err := os.UserCacheDir(); err == nil && base != "" {
		return filepath.Join(base, "ticfac", "harness")
	}
	return filepath.Join(os.TempDir(), "ticfac-harness-cache")
}
