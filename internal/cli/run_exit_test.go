package cli

// `ticfac run`'s attach ends through the watch's own exit code, mapped into
// the exit table's class and the --json document's state word by
// runAttachState — so an attach that ends holding something for a person is
// exit 3 on a terminal and on a pipe alike, and the document says held
// (tick 4mv: the interactive form and the piped one are the same ending and
// must carry the same verdict).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runlife"
)

func TestRunAnswersAnAttachThatEndedHoldingWithTheHeldClass(t *testing.T) {
	saveRunSeams(t)
	repo := t.TempDir()
	life, err := runlife.Claim(repo, "epic-foo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { life.Release("the test's claim") })
	runStartDetached = func(argv []string, out io.Writer) (runChild, error) {
		t.Errorf("a live run was started again: %v", argv)
		return &fakeChild{}, nil
	}
	attach := &attachRecorder{code: ExitHeld}
	runAttach = attach.seam

	// The plain invocation: the attach's held verdict is the command's.
	var stdout, stderr bytes.Buffer
	code := runBody(context.Background(), t, []string{"--repo", repo, "foo"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("an attach that ended holding for a person exits %d, want %d; stderr %q",
			code, ExitHeld, stderr.String())
	}
	if len(attach.called) != 1 || attach.called[0] != "epic-foo" {
		t.Fatalf("the attach was asked for %v, want the live run epic-foo", attach.called)
	}

	// The --json invocation: one document, its state word held, and the exit
	// code the word's class — the property an agent's parse rests on.
	stdout, stderr = bytes.Buffer{}, bytes.Buffer{}
	code = runBody(context.Background(), t, []string{"--repo", repo, "--json", "foo"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("an attach that ended holding exits %d under --json, want %d", code, ExitHeld)
	}
	var doc struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the run document does not parse: %v\n%s", err, stdout.String())
	}
	if doc.State != agentStateHeld {
		t.Errorf("the run document's state word is %q, want %q", doc.State, agentStateHeld)
	}
	if strings.Contains(stdout.String(), "run epic-foo is alive") {
		t.Errorf("prose that belongs to stderr landed in the document:\n%s", stdout.String())
	}
}
