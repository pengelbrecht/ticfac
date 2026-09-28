package cli

// The image's scripts call ticfac for the verbs tk no longer has (tick 46x):
// the sandbox questions and the branch write. For tk, the image build's last
// layer runs every required subcommand against the tk it just built
// (image/required-tk-commands). ticfac needs no such layer — the deploy
// cross-compiles it from the same tree as the scripts it stages — so the
// same property is proven here, on the command tree itself: every `ticfac`
// invocation in a run script names a command this binary has. A script that
// calls a verb nobody implemented fails the build rather than a boot.

import (
	"io"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/factory"
)

func TestEveryTicfacCommandTheImageRunsExists(t *testing.T) {
	commands, err := factory.EntrypointTicfacCommands()
	if err != nil {
		t.Fatal(err)
	}
	// The floor: the verbs the scripts took over from tk. A scanner that
	// found nothing would pass everything below.
	for _, want := range []string{
		"cloud branch",
		"sandbox environment",
		"sandbox image",
		"sandbox model",
		"sandbox setup",
		"sandbox substrate",
		"sandbox toolchain",
		"sandbox worker-prompt",
	} {
		found := false
		for _, c := range commands {
			found = found || c == want
		}
		if !found {
			t.Errorf("the image's scripts no longer run `ticfac %s` (found %q) — the scanner or the scripts changed", want, commands)
		}
	}

	root := newRootCommand(io.Discard, io.Discard)
	for _, c := range commands {
		path := strings.Fields(c)
		found, rest, err := root.Find(path)
		if err != nil || len(rest) != 0 || found.CommandPath() != "ticfac "+c {
			t.Errorf("the image runs `ticfac %s`, which this binary does not have (resolved %q, left %q, %v)",
				c, found.CommandPath(), rest, err)
		}
	}
}
