package herdtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A socket test must pass under ANY TMPDIR. The integrated gate runs its
// command with a TMPDIR inside its slot, a path well past darwin's ~104-byte
// cap on a unix socket's address, and a fake that bound under os.TempDir()
// failed every herd test there with "bind: invalid argument".
func TestTheFakeListensUnderALongTMPDIR(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("a-long-temp-directory-", 6))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	if len(filepath.Join(long, "hd0000000000", "h.sock")) <= 104 {
		t.Fatalf("the fixture's TMPDIR (%d bytes) is not long enough to prove anything", len(long))
	}

	s := New(t, Config{})
	if len(s.Path()) > 100 {
		t.Errorf("the socket path is %d bytes, past what darwin binds: %s", len(s.Path()), s.Path())
	}
	if reply := dialOne(t, s, MethodPing); reply.Error != nil {
		t.Fatalf("ping under a long TMPDIR: %+v", reply.Error)
	}
}
