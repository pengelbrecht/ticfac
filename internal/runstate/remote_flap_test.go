package runstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

// Nothing in this file is a model. The store runs a real `git fetch` over a
// real ssh transport against a real bare repository — the transport is just
// one this test can make fail.
//
// The trick is that git's ssh transport is a program, not a protocol: git runs
// $GIT_SSH_COMMAND with the host and `git-upload-pack '<path>'`, and speaks the
// pack protocol over its stdio. A script that counts its invocations can
// therefore print the exact stderr GitHub printed when run epic-ncv died,
// exit non-zero for the first N attempts, and then hand the connection
// through to the local repository — which is a connection reset, as far as
// everything above the socket can tell.

// flap is a transport that fails its first failures invocations with stderr,
// and works afterwards.
type flap struct {
	t       *testing.T
	command string
	counter string
}

func newFlap(t *testing.T, failures int, stderr string) *flap {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the flapping transport is a /bin/sh script")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no /bin/sh to build a flapping transport with")
	}
	dir := t.TempDir()
	f := &flap{t: t, command: filepath.Join(dir, "flaky-ssh"), counter: filepath.Join(dir, "invocations")}

	// The last argument git passes is the shell-quoted remote command
	// (`git-upload-pack '/path/to/origin.git'`), so eval is how it is run —
	// the quoting is git's and must survive. git-upload-pack lives beside the
	// git binary this process resolved, which on macOS is NOT /usr/bin.
	script := "#!/bin/sh\n" +
		"PATH=\"" + filepath.Dir(gitbin.Path()) + ":$PATH\"; export PATH\n" +
		"n=$(cat \"" + f.counter + "\" 2>/dev/null || echo 0)\n" +
		"n=$((n + 1))\n" +
		"printf '%s\\n' \"$n\" > \"" + f.counter + "\"\n" +
		"if [ \"$n\" -le " + strconv.Itoa(failures) + " ]; then\n" +
		"  printf '%s\\n' \"$TICFAC_TEST_TRANSPORT_STDERR\" >&2\n" +
		"  exit 255\n" +
		"fi\n" +
		"for arg in \"$@\"; do cmd=\"$arg\"; done\n" +
		"eval \"exec $cmd\"\n"
	if err := os.WriteFile(f.command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Set in the environment rather than baked into the script, so the stderr
	// under test stays readable in the test that chose it.
	t.Setenv("TICFAC_TEST_TRANSPORT_STDERR", stderr)
	// The store copies os.Environ(), and TransportEnv leaves an operator's own
	// GIT_SSH_COMMAND alone — which is what lets this be substituted at all.
	t.Setenv("GIT_SSH_COMMAND", f.command)
	// Without this git PROBES the command first (`<command> -G <host>`) to
	// work out which ssh it is, which would be a second invocation per fetch
	// and would make the counter below count something other than attempts.
	t.Setenv("GIT_SSH_VARIANT", "ssh")
	return f
}

// invocations is how many times git actually reached for the network.
func (f *flap) invocations() int {
	f.t.Helper()
	raw, err := os.ReadFile(f.counter)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		f.t.Fatalf("the transport's counter reads %q: %v", raw, err)
	}
	return n
}

// overSSH is a clone of origin whose remote is an ssh:// URL, so every fetch
// and push goes through the flapping transport above. The clone itself is
// made over the local path, before the transport is in play: seeding a
// fixture is not the operation under test.
func (o *origin) overSSH(t *testing.T, retry RemoteRetry) *Store {
	t.Helper()
	dir := filepath.Join(o.root, "over-ssh")
	gitRun(t, o.root, "clone", "--quiet", "--no-checkout", o.bare, dir)
	gitRun(t, dir, "remote", "set-url", "origin", "ssh://remote.invalid"+o.bare)

	s, err := Open(Options{
		Repo:        dir,
		Remote:      "origin",
		Branch:      o.branch,
		RunID:       "r-flap",
		Now:         time.Now,
		RemoteRetry: retry,
	})
	if err != nil {
		t.Fatalf("open a store over the flapping transport: %v", err)
	}
	return s
}

// TestTheRunSurvivesASingleConnectionReset is tick enj's first direction, end
// to end.
//
// Run epic-ncv died on exactly this stderr, mid-fetch, with two workers in
// flight — and the very next attempt succeeded. Here the very next attempt is
// the store's own, and the fetch returns the same view it would have returned
// had the network not blinked.
func TestTheRunSurvivesASingleConnectionReset(t *testing.T) {
	o := newOrigin(t)
	transport := newFlap(t, 1,
		"Connection reset by remote.invalid port 22\nfatal: Could not read from remote repository.")

	var notices []RemoteRetryNotice
	s := o.overSSH(t, RemoteRetry{
		Attempts: 4,
		Backoff:  time.Millisecond,
		Sleep:    func(time.Duration) {},
		Report:   func(n RemoteRetryNotice) { notices = append(notices, n) },
	})

	if _, err := s.Fetch(); err != nil {
		t.Fatalf("one reset still killed the fetch: %v", err)
	}
	if got := transport.invocations(); got != 2 {
		t.Errorf("the transport was reached for %d times, want the reset and the retry", got)
	}
	if s.Head() == "" {
		t.Error("the fetch that recovered produced no view of origin")
	}
	if len(notices) != 1 || notices[0].GaveUp {
		t.Errorf("notices were %+v, want one retry said out loud", notices)
	}

	// And the run goes on: a write lands on origin through the same transport.
	if got, err := s.CreateIfAbsent(CheckpointPath("r-flap"), []byte(`{"sequence":1}`)); err != nil || got != Created {
		t.Fatalf("the write after the recovered fetch: %v %v", got, err)
	}
}

// TestAStubbornlyResettingRemoteStopsTheRunNamingTheAttempts is the other
// direction. The bound is what makes waiting safe — retrying forever
// recreates the hang tick pul's transport bound exists to prevent — and when
// it is spent the run stops, with an error that says how many times it tried.
func TestAStubbornlyResettingRemoteStopsTheRunNamingTheAttempts(t *testing.T) {
	o := newOrigin(t)
	// Never recovers.
	transport := newFlap(t, 1000,
		"Connection reset by remote.invalid port 22\nfatal: Could not read from remote repository.")

	var gaveUp int
	s := o.overSSH(t, RemoteRetry{
		Attempts: 3,
		Backoff:  time.Millisecond,
		Sleep:    func(time.Duration) {},
		Report: func(n RemoteRetryNotice) {
			if n.GaveUp {
				gaveUp++
			}
		},
	})

	_, err := s.Fetch()
	if err == nil {
		t.Fatal("a remote that never came back produced a fetch that succeeded")
	}
	if got := transport.invocations(); got != 3 {
		t.Errorf("the transport was reached for %d times, want the bound of 3", got)
	}
	if !strings.Contains(err.Error(), "3 times") {
		t.Errorf("the refusal does not name the attempts: %v", err)
	}
	if gaveUp != 1 {
		t.Errorf("giving up was reported %d times, want once", gaveUp)
	}
}

// TestAKeyTheRemoteRejectedIsNotRetried.
//
// The stderr here ENDS with the same line the reset above ends with, and it
// must still be refused on the first attempt: a bounded wait on a credential
// that will never be right is a slower refusal, and it spends the run's clock
// doing it.
func TestAKeyTheRemoteRejectedIsNotRetried(t *testing.T) {
	o := newOrigin(t)
	transport := newFlap(t, 1000,
		"git@remote.invalid: Permission denied (publickey).\nfatal: Could not read from remote repository.")

	s := o.overSSH(t, RemoteRetry{
		Attempts: 5,
		Backoff:  time.Millisecond,
		Sleep:    func(time.Duration) { t.Error("the run waited on a key the remote rejected") },
		Report:   func(RemoteRetryNotice) { t.Error("a rejected key was reported as a transient failure") },
	})

	if _, err := s.Fetch(); err == nil {
		t.Fatal("a rejected key produced a fetch that succeeded")
	}
	if got := transport.invocations(); got != 1 {
		t.Errorf("the transport was reached for %d times, want exactly one", got)
	}
}
