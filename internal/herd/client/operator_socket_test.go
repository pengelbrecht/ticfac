package client

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The operator's own herdr is live state a test must never reach. A test
// process started from inside a herdr pane inherits HERDR_SOCKET_PATH (the
// operator's real socket), and any test that resolves herdr without injecting
// a fake — `ticfac run`'s live probe, doctor, the executor — dials it. So the
// one dial point refuses, in a test binary, the sockets the operator's herdr
// listens on: the HERDR_SOCKET_PATH this process inherited and the default
// ~/.config/herdr/herdr.sock under the home it started with. A test that
// wants herdr injects a fake (herdtest) through t.Setenv or an explicit path,
// and the one deliberate live test opts in with TK_HERD_LIVE_TEST.

// shortTempDir is a temp dir short enough for a unix socket path (darwin
// caps one at 104 bytes, and t.TempDir() under /var/folders can exceed it).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// listenUnix stands up a unix socket that records whether anything connected.
func listenUnix(t *testing.T) (path string, connected func() bool) {
	t.Helper()
	path = filepath.Join(shortTempDir(t), "h.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Close()
		got <- struct{}{}
	}()
	return path, func() bool {
		select {
		case <-got:
			return true
		case <-time.After(200 * time.Millisecond):
			return false
		}
	}
}

// Serial: it swaps the package's record of the operator's sockets.
func TestATestBinaryCannotDialTheOperatorsHerdr(t *testing.T) {
	operator, operatorConnected := listenUnix(t)
	fake, fakeConnected := listenUnix(t)

	saved := operatorSockets
	t.Cleanup(func() { operatorSockets = saved })
	operatorSockets = operatorSocketsFrom(operator, shortTempDir(t))
	t.Setenv(liveOptInEnv, "")

	func() {
		defer func() {
			if recover() == nil {
				t.Errorf("a test binary dialed the operator's herdr socket %s without being refused", operator)
			}
		}()
		conn, err := NewUnixTransport(operator).Dial(context.Background())
		if err == nil {
			conn.Close()
		}
	}()
	if operatorConnected() {
		t.Errorf("the refused dial still reached the operator's herdr")
	}

	// A fake the test stood up itself is what a test dials, freely.
	conn, err := NewUnixTransport(fake).Dial(context.Background())
	if err != nil {
		t.Fatalf("dial the test's own fake: %v", err)
	}
	conn.Close()
	if !fakeConnected() {
		t.Errorf("the dial to the test's own fake never arrived")
	}

	// The one deliberate live test opts in, and is let through.
	t.Setenv(liveOptInEnv, "1")
	conn, err = NewUnixTransport(operator).Dial(context.Background())
	if err != nil {
		t.Fatalf("an opted-in live dial was refused: %v", err)
	}
	conn.Close()
}

// A test binary sheds the pane it was started in: no HERDR_ENV (the
// substrate's env probe), no pane identity for children to report into, and
// a HERDR_SOCKET_PATH nothing listens on, so resolution never falls through
// to the operator's default socket. Run `go test` from a herdr pane (or with
// HERDR_ENV=1 HERDR_SOCKET_PATH=... in the environment) to see this bite.
func TestATestBinaryDoesNotInheritTheOperatorsPane(t *testing.T) {
	if os.Getenv(liveOptInEnv) != "" {
		t.Skip("the live round-trip test opted into the operator's herdr")
	}
	for _, name := range paneEnv[1:] {
		if value, ok := os.LookupEnv(name); ok {
			t.Errorf("%s=%q reached the test binary from the pane it was started in", name, value)
		}
	}
	path, err := ResolveSocketPath("")
	if err != nil {
		t.Fatal(err)
	}
	for _, operator := range operatorSockets {
		if canonicalSocket(path) == operator {
			t.Errorf("a test resolves herdr to the operator's socket %s", path)
		}
	}
	if conn, err := NewUnixTransport(path).Dial(context.Background()); err == nil {
		conn.Close()
		t.Errorf("something answers at the test binary's herdr socket %s", path)
	}
}

// The operator's sockets are the inherited HERDR_SOCKET_PATH and the default
// path under the home the process started with.
func TestTheOperatorsSocketsAreTheInheritedAndTheDefault(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	got := operatorSocketsFrom("/run/herdr/live.sock", home)
	want := map[string]bool{
		canonicalSocket("/run/herdr/live.sock"):                                     true,
		canonicalSocket(filepath.Join(home, filepath.FromSlash(DefaultSocketPath))): true,
	}
	if len(got) != len(want) {
		t.Fatalf("operatorSocketsFrom = %v, want %v", got, want)
	}
	for _, path := range got {
		if !want[path] {
			t.Errorf("operatorSocketsFrom names %s, want only %v", path, want)
		}
	}
	if got := operatorSocketsFrom("", ""); len(got) != 0 {
		t.Errorf("with no inherited socket and no home, operatorSocketsFrom = %v, want none", got)
	}
}
