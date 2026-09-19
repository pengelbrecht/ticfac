package subprocess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The runner's git is told not to start automatic maintenance, whatever the
// grade (tick mel).
//
// The runner's worktree is a LINKED worktree: its commits land in the object
// store the reconciler is writing trees, blobs and commits into at the same
// time, and on a git whose automatic maintenance repacks in the background
// (2.55's geometric default) every one of those commits could start a repack
// whose prune-packed removes an object directory under the reconciler's
// write. The read-only grade is asked separately because it pins git config
// of its own through the same variables, and the setting must survive
// alongside those pins rather than replace them.
func TestTheRunnersGitStartsNoMaintenanceWhateverTheGrade(t *testing.T) {
	t.Parallel()
	for _, grade := range []string{gradeWrite, gradeReadOnly} {
		f := newFixture(t, fixtureOptions{mode: "maintenance_seen", name: "mel-" + grade})
		spec := f.spec("run-mel/tick-mel/attempt-1", "mel")
		if grade == gradeReadOnly {
			spec = readOnlySpec(f, "run-mel/tick-mel/attempt-1", "mel")
		}
		handle := f.Start(spec)
		f.waitSettled(handle)

		local, _ := handle.Local()
		seen, err := os.ReadFile(filepath.Join(local.Worktree, "maintenance-seen.txt"))
		if err != nil {
			t.Fatalf("%s grade: the runner recorded nothing: %v", grade, err)
		}
		if got := strings.TrimSpace(string(seen)); got != "false" {
			t.Errorf("%s grade: the runner's git reads maintenance.auto as %q, want false: its commits start "+
				"background repacks of the object store the run writes into (tick mel)", grade, got)
		}
	}
}
