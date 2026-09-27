package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The gate's TMPDIR (tick w9j follow-up). A gate that runs `go test` and is
// killed at its bound leaves whatever the suite had in the host's temp
// directory: t.TempDir()s, go-build work dirs, test binaries, in ANY package,
// none of which a ticfac cleanup can know about. So the gate's command runs
// with a TMPDIR of its own beside the tree it gates, emptied before the
// command starts and again when the gate is collected — killed or not. The
// path is the slot's, so it is the SAME string on every gate in that slot:
// go keys a cached test result on the environment variables the test read,
// TMPDIR among them for anything that calls t.TempDir(), and a TMPDIR that
// moved would make every gate a cold one.

// gateOnce runs one command in dir the way the integrated gate does and
// answers with its stdout.
func gateOnce(t *testing.T, dir, command string) string {
	t.Helper()
	shell, err := startShell(dir, command, time.Minute, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for !shell.settled() {
		time.Sleep(10 * time.Millisecond)
	}
	stdout, stderr, code, err := shell.wait()
	if code != 0 || err != nil {
		t.Fatalf("%s: code %d, err %v\n%s%s", command, code, err, stdout, stderr)
	}
	return stdout
}

func assertEmptyDir(t *testing.T, dir, after string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 0 {
		t.Errorf("%s left %v in the gate's TMPDIR %s", after, names, dir)
	}
}

func TestAGateRunsWithATempDirOfItsOwnAndLeavesItEmpty(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, t.TempDir(), "repo", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}

	dir, _, release, err := g.gateWorktree("ticfac-gate-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	tmp := gateTempDir(dir)
	// What a gate killed with its reconciler left: nothing was there to
	// collect it, and the next gate in the slot must not start on top of it.
	if err := os.MkdirAll(filepath.Join(tmp, "TestLeftByAKilledGate123", "001"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Everything a go test leaves in a temp directory, including a directory
	// it cannot write into (a module cache is read-only on disk).
	out := gateOnce(t, dir, `printf '%s\n' "$TMPDIR"; ls -A "$TMPDIR"; `+
		`mkdir -p "$TMPDIR/TestSomething42/001" "$TMPDIR/go-build7/b001" "$TMPDIR/ro/inner" && `+
		`touch "$TMPDIR/ro/inner/f" && chmod 555 "$TMPDIR/ro/inner" "$TMPDIR/ro"`)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != tmp {
		t.Errorf("the gate ran with TMPDIR=%q, want the slot's own %q", lines[0], tmp)
	}
	if len(lines) > 1 {
		t.Errorf("the gate started on top of a previous gate's leftovers: %v", lines[1:])
	}
	assertEmptyDir(t, tmp, "a gate that finished")
	release()

	again, releaseAgain := reacquire(t, g, repo.Base, dir)
	defer releaseAgain()
	if again == dir {
		if second := strings.TrimSpace(gateOnce(t, again, `printf '%s' "$TMPDIR"`)); second != lines[0] {
			t.Errorf("the second gate in the slot ran with TMPDIR=%q, the first with %q: go's test cache "+
				"keys on it, so a gate whose TMPDIR moves is a cold gate", second, lines[0])
		}
	}
}

func TestAGateKilledAtItsBoundLeavesNothingInItsTempDir(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, t.TempDir(), "repo", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}
	dir, lock, release, err := g.gateWorktree("ticfac-gate-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	shell, err := startShell(dir, `mkdir -p "$TMPDIR/TestKilled1/001" && touch "$TMPDIR/started" && sleep 60`,
		time.Hour, time.Now(), lock)
	if err != nil {
		t.Fatal(err)
	}
	tmp := gateTempDir(dir)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(tmp, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the gate command never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// wait before the sentinel is the bound firing: it kills the group.
	if _, _, code, _ := shell.wait(); code >= 0 {
		t.Fatalf("the gate was meant to be killed, and answered %d", code)
	}
	assertEmptyDir(t, tmp, "a gate killed at its bound")
}

// The cache the slot exists for must survive the gate's TMPDIR: a package
// whose test reads TMPDIR (every t.TempDir() does) is served from go's cache
// on the second gate in the same slot.
func TestAGateWithItsOwnTempDirIsStillServedFromGosTestCache(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, t.TempDir(), "repo", passingGate)
	write(t, filepath.Join(repo.Dir, "go.mod"), "module ticfac.example/gatetmp\n\ngo 1.21\n")
	write(t, filepath.Join(repo.Dir, "tmp_test.go"), fmt.Sprintf(
		"package gatetmp\n\nimport \"testing\"\n\nconst salt = %q\n\n"+
			"func TestUsesTempDir(t *testing.T) { _ = salt; _ = t.TempDir() }\n", t.Name()+time.Now().String()))
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "a package that reads TMPDIR")
	head := gitHead(t, repo.Dir)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}

	first, _, release, err := g.gateWorktree("ticfac-gate-", head)
	if err != nil {
		t.Fatal(err)
	}
	cold := gateOnce(t, first, "go test ./...")
	release()
	if strings.Contains(cold, "(cached)") {
		t.Fatalf("the first gate over a package this test just wrote was served from cache:\n%s", cold)
	}
	second, release2 := reacquire(t, g, head, first)
	defer release2()
	if second != first {
		t.Skipf("the slot was not handed back (%s, not %s); nothing to measure", second, first)
	}
	if warm := gateOnce(t, second, "go test ./..."); !strings.Contains(warm, "(cached)") {
		t.Errorf("a second gate over an unchanged tree re-ran the suite: the gate's TMPDIR is not stable:\n%s", warm)
	}
	assertEmptyDir(t, gateTempDir(second), "two go test gates")
}
