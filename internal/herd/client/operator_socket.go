package client

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// liveOptInEnv is the one way a test may reach the operator's own herdr: the
// explicit opt-in the live round-trip test (live_test.go) already requires.
const liveOptInEnv = "TK_HERD_LIVE_TEST"

// operatorSockets are the sockets the operator's own herdr listens on, as
// this process found them when it STARTED — before any test could move HOME
// or t.Setenv a fake: the HERDR_SOCKET_PATH it inherited (a process started
// inside a herdr pane carries the live server's socket) and the default
// ~/.config/herdr/herdr.sock under the home it started with.
var operatorSockets = func() []string {
	home, _ := os.UserHomeDir()
	return operatorSocketsFrom(os.Getenv(SocketPathEnv), home)
}()

// paneEnv is what a herdr pane exports to every process in it: the live
// server's socket, the pane and its tab and workspace, and HERDR_ENV=1, the
// substrate's env probe (runconfig.EnvVar).
var paneEnv = []string{SocketPathEnv, "HERDR_ENV", "HERDR_PANE_ID", "HERDR_TAB_ID", "HERDR_WORKSPACE_ID"}

// A test binary does not inherit the operator's herdr pane. Every package
// that can reach herdr links this one, so its init is where the pane is
// shed — AFTER operatorSockets has recorded what was inherited, and before
// any test runs. Without this, a test started in a pane reads HERDR_ENV=1
// and auto-detects the herdr substrate the operator happens to be sitting
// in, resolves the live socket, and hands the pane's identity to every
// child it spawns (a real `claude` a test starts reports its session into
// the operator's pane through its SessionStart hook). HERDR_SOCKET_PATH is
// pointed at a socket nothing listens on, so resolution answers "no live
// herdr" on every host alike instead of falling through to the default
// path; a test that wants herdr sets its own fake with t.Setenv. The live
// round-trip test opts out with TK_HERD_LIVE_TEST=1.
func init() {
	if !testing.Testing() || os.Getenv(liveOptInEnv) != "" {
		return
	}
	for _, name := range paneEnv {
		os.Unsetenv(name)
	}
	os.Setenv(SocketPathEnv, filepath.Join(os.TempDir(), fmt.Sprintf("ticfac-test-no-herdr-%d.sock", os.Getpid())))
}

// operatorSocketsFrom names the operator's sockets from an inherited
// HERDR_SOCKET_PATH and a home directory, canonicalized for comparison.
func operatorSocketsFrom(inherited, home string) []string {
	var out []string
	if inherited != "" {
		out = append(out, canonicalSocket(inherited))
	}
	if home != "" {
		out = append(out, canonicalSocket(filepath.Join(home, filepath.FromSlash(DefaultSocketPath))))
	}
	return out
}

// canonicalSocket is a socket path with its symlinks resolved when it
// stands, cleaned when it does not — so /tmp and /private/tmp, or a home
// reached through a link, name one socket.
func canonicalSocket(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

// refuseTheOperatorsHerdrUnderTest panics when a TEST binary is about to dial
// the operator's own herdr. A test process started from a herdr pane
// inherits the live server's socket in HERDR_SOCKET_PATH, so a test that
// resolves herdr without injecting a fake reaches the operator's real
// session — its panes, their titles, their agent sessions — and whatever the
// test's calls do there, they do to the operator's workspace. A panic, not
// an error: callers read a dial error as "herdr is not running" and carry
// on, and a refusal that reads as a quiet negative would hide the very test
// that needs fixing. A production binary is never a test binary, so this
// never fires outside a test; the live round-trip test opts in explicitly.
func refuseTheOperatorsHerdrUnderTest(socketPath string) {
	if !testing.Testing() || os.Getenv(liveOptInEnv) != "" {
		return
	}
	target := canonicalSocket(socketPath)
	for _, operator := range operatorSockets {
		if target == operator {
			panic(fmt.Sprintf("herd/client: a test dialed the operator's own herdr at %s. A test process "+
				"inherits the live server's socket (%s, or the default ~/%s) when it runs in a herdr pane; "+
				"a test that needs herdr must inject a fake (internal/herd/herdtest) through t.Setenv(%q, ...) "+
				"or an explicit socket path, and the one deliberate live test opts in with %s=1",
				socketPath, SocketPathEnv, DefaultSocketPath, SocketPathEnv, liveOptInEnv))
		}
	}
}
