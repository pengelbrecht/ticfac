package herdtest

import (
	"os"
	"testing"
)

// socketBase is where SocketDir makes its directories: short and fixed, and
// never os.TempDir(). A unix socket's path is capped at 104 bytes on darwin
// (108 on linux), and TMPDIR can be anything — the integrated gate points it
// inside its slot, which is already past the cap. A socket under a long
// TMPDIR fails to bind with "invalid argument".
const socketBase = "/tmp"

// SocketDir is a fresh directory a test can bind unix sockets in, whatever
// TMPDIR is, removed when the test ends.
func SocketDir(t testing.TB, prefix string) string {
	t.Helper()
	base := socketBase
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		base = "" // no /tmp on this host: os.TempDir() is all there is
	}
	dir, err := os.MkdirTemp(base, prefix)
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
