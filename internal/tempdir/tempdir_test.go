package tempdir

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// deadPid is a pid that belonged to a process which has exited.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// age sets the mtime of everything under path, path included.
func age(t *testing.T, path string, at time.Time) {
	t.Helper()
	err := filepath.WalkDir(path, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, at, at)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, root, name string, withFile bool) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	if withFile {
		if err := os.WriteFile(filepath.Join(dir, "tree", "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSweepRemovesOnlyWhatAKilledProcessLeftADayAgo(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	old := now.Add(-25 * time.Hour)
	dead := strconv.Itoa(deadPid(t))
	live := strconv.Itoa(os.Getppid()) // the go command running this test

	gone := mkdir(t, root, "ticfac-tracker-p"+dead+"-123", true)
	legacy := mkdir(t, root, "ticfac-merge-456", true) // an older build's name: no pid
	fresh := mkdir(t, root, "ticfac-gate-p"+dead+"-789", true)
	alive := mkdir(t, root, "ticfac-merge-p"+live+"-111", true)
	slots := mkdir(t, root, "ticfac-gate-slots-0123456789ab", true)
	stillWritten := mkdir(t, root, "ticfac-tracker-222", true)
	foreign := mkdir(t, root, "go-build333", true)
	for _, d := range []string{gone, legacy, alive, slots, stillWritten, foreign} {
		age(t, d, old)
	}
	// A live run's tree whose root is old but whose contents are not.
	if err := os.WriteFile(filepath.Join(stillWritten, "tree", "f"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stillWritten, old, old); err != nil {
		t.Fatal(err)
	}

	removed, err := Sweep(root, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{gone, legacy} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep (err %v)", filepath.Base(d), err)
		}
	}
	for _, d := range []string{fresh, alive, slots, stillWritten, foreign} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(d), err)
		}
	}
	if len(removed) != 2 {
		t.Errorf("Sweep reported %v, want the two stale dirs", removed)
	}
}

func TestMakeNamesItsOwnerAndReleaseAllRemovesWhatIsOpen(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	a, removeA, err := Make("ticfac-test-")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Make("ticfac-test-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.Base(a), "-p"+strconv.Itoa(os.Getpid())+"-") {
		t.Errorf("%s does not name its owner", a)
	}
	if !owner.MatchString(filepath.Base(a)) {
		t.Errorf("Sweep cannot read the owner out of %s", a)
	}
	removeA()
	removeA() // idempotent
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Errorf("remove left %s", a)
	}
	ReleaseAll()
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Errorf("ReleaseAll left %s", b)
	}
	if Pending() != 0 {
		t.Errorf("%d cleanups still pending after ReleaseAll", Pending())
	}
	if _, _, err := Make("not-ours-"); err == nil {
		t.Error("Make accepted a name Sweep would never recognise")
	}
}
