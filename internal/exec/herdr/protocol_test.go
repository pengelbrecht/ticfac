package herdr

// The executor's protocol surface at construction (finding 2, tick ic0).
//
// The client already degrades an above-warn server to a WARNING — a herdr
// speaking a protocol newer than the newest this client has observed
// proceeds, and the warning is the one signal that says so. But the warning
// only travels through Options.ProtocolWarning, and the executor's only
// constructor omitted the writer, so in production the signal was silently
// dropped. This machine runs herdr 0.9.0 / protocol 22 against a client
// verified at 20; the NEXT herdr upgrade crosses the warn line, and an
// operator would have heard nothing.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/herd/client"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// TestAboveWarnProtocolWarningReachesTheWriter pins that the constructor
// hands the client a protocol-warning writer, so the above-warn warning
// lands somewhere an operator reads. The fixture is the local subject one
// step on: herdr 0.9.0 speaking one protocol above the warn line.
func TestAboveWarnProtocolWarningReachesTheWriter(t *testing.T) {
	repo := newRepo(t, "repo")
	root := t.TempDir()
	// A non-default version/protocol pair also answers ping WITHOUT a
	// capabilities block, which is fine: no operation demands a capability.
	s := herdtest.New(t, herdtest.Config{
		Version: "0.9.0", Protocol: int(client.ProtocolWarnVersion + 1),
	})

	var warning bytes.Buffer
	if _, err := New(Options{
		Repo:            repo.Dir,
		StateDir:        filepath.Join(root, "state"),
		SocketPath:      s.Path(),
		ProtocolWarning: &warning,
	}); err != nil {
		t.Fatalf("an above-warn herdr refused the constructor: %v — a forward-compatible upgrade proceeds, it warns", err)
	}
	out := warning.String()
	if out == "" {
		t.Fatal("the above-warn protocol warning was discarded: the constructor handed the client no " +
			"writer, so the one signal that production's herdr is newer than the observed range reaches nobody")
	}
	for _, want := range []string{
		"warning",
		"0.9.0",
		fmt.Sprintf("reports protocol %d", client.ProtocolWarnVersion+1),
		fmt.Sprintf("newer than protocol %d", client.ProtocolWarnVersion),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning %q does not name %q", out, want)
		}
	}
}

// TestTheProtocolWarningDefaultsToStderr pins the PRODUCTION half of the
// same fix: the only constructor is called without Options.ProtocolWarning,
// so the default must be the operator's own stream, not a dropped writer.
func TestTheProtocolWarningDefaultsToStderr(t *testing.T) {
	repo := newRepo(t, "repo")
	root := t.TempDir()
	s := herdtest.New(t, herdtest.Config{}) // canonical 0.8.2 / protocol 20

	ex, err := New(Options{
		Repo:       repo.Dir,
		StateDir:   filepath.Join(root, "state"),
		SocketPath: s.Path(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ex.warn != os.Stderr {
		t.Errorf("the resolved protocol-warning writer is %v, want os.Stderr: the constructor runs in "+
			"production without an explicit writer, and omitting one there is what dropped the warning", ex.warn)
	}
}
