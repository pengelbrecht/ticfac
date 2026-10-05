package cli

// The printedExit routing, headless (tick jmf): a body that has already said
// its own refusal and carries only its code must end at cobra as success,
// never as an error fang's error path takes — fang's error path queries the
// terminal for its colourscheme (OSC 11 out, stdin in) before it renders
// anything, and a watch that just returned leaves its key reader parked on
// that stdin, so the query stalls the process (see watch_pty_exit_test.go
// for the end-to-end proof on a real pty). A typed error — the usage class
// cobra itself reports before a body ever runs — still reaches fang: that is
// what fang is for, and no key reader is holding stdin then.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestAPrintedExitEndsAtCobraNotFang: a command body's own refusal — already
// printed by the body, its error only the code — comes back from cobra as a
// nil error with the code captured, so fang's error path — and the terminal
// query inside it — never runs for it.
func TestAPrintedExitEndsAtCobraNotFang(t *testing.T) {
	var stderr syncBuffer
	root := newRootCommand(io.Discard, &stderr)
	root.SetArgs([]string{"run-epic", "--repo", t.TempDir(), "no-such-epic"})
	captured := &printedExitCapture{}
	routePrintedExitsBeforeFang(root, captured)

	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("a printedExit must end at cobra as success so fang's error path never runs; cobra returned %v", err)
	}
	code, ok := captured.take()
	if !ok {
		t.Fatal("the printedExit was not captured: runContext would answer success for a refusal")
	}
	// The body's own class: a run-epic in an empty directory refuses with the
	// generic class — the point is that the body's CODE travels, not an
	// error for fang.
	if code != exitGeneric {
		t.Errorf("the captured code is %d, want the generic class %d the body owns", code, exitGeneric)
	}
	// The body's own refusal was said, byte-for-byte as always.
	if !strings.Contains(stderr.String(), "no-such-epic") {
		t.Errorf("the body's refusal is not on the wire:\n%s", stderr.String())
	}
}

// TestATypedUsageErrorStillReachesFang: the errors cobra itself reports — an
// unknown flag, parsed before the body runs — keep reaching fang, where
// they get the styled rendering that is the wrapper's whole job.
func TestATypedUsageErrorStillReachesFang(t *testing.T) {
	root := newRootCommand(io.Discard, io.Discard)
	root.SetArgs([]string{"factory", "deploy", "--no-such-flag"})
	captured := &printedExitCapture{}
	routePrintedExitsBeforeFang(root, captured)

	err := root.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("a typed usage error must still reach fang as an error to style")
	}
	var exitErr *exitError
	if !errors.As(err, &exitErr) || exitErr.code != exitUsage {
		t.Fatalf("the usage error fang got is %v, want the typed usage class", err)
	}
	if code, ok := captured.take(); ok {
		t.Errorf("a typed error was captured as a printedExit (code %d): only a body's own refusal is", code)
	}
}
