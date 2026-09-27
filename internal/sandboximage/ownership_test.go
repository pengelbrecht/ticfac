package sandboximage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ticfac authors image/ (tick r6w). ticks became tracker-only (ticks epic
// chz, v0.32.0): `tk factory` is gone, cloud/sandbox and cloud/factory are
// gone, and internal/sandbox moved here. A refusal that tells an operator to
// run a verb that no longer exists is a dead end printed at the worst moment,
// and a comment pointing at a path in the other repository sends the next
// editor looking for a file that is not there.
//
// `tk sandbox …` and `tk cloud branch` are deliberately NOT on this list: the
// scripts still call them, and tick 46x ports them to ticfac.
//
// short: reads the image files and asserts on their text; no process runs
func TestTheImageNamesNoVerbOrPathTicksDropped(t *testing.T) {
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	stale := []string{"tk factory", "cloud/sandbox", "cloud/factory", "internal/sandbox/"}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for n, line := range strings.Split(string(body), "\n") {
			for _, s := range stale {
				if strings.Contains(line, s) {
					t.Errorf("image/%s:%d names %q, which ticks no longer has: %s",
						entry.Name(), n+1, s, strings.TrimSpace(line))
				}
			}
		}
	}
}
