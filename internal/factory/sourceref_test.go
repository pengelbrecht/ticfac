package factory

import (
	"os"
	"reflect"
	"strings"
	"testing"

	ticfac "github.com/pengelbrecht/ticfac"
)

// The pin the deploy path will stamp into the staged Dockerfile, read from
// the ONE place it lives: factory.pin.json at the repository root, embedded
// into the binary. A pin that names a moving ref or an abbreviated commit is
// a stop that says so, not a docker build that fails twenty minutes later.
func TestPinnedSourceReadsTheCommittedPin(t *testing.T) {
	pin, err := PinnedSource()
	if err != nil {
		t.Fatalf("PinnedSource: %v", err)
	}
	if pin.Repository == "" || pin.Module == "" {
		t.Errorf("pin = %+v, want a repository and a module named", pin)
	}
	if !commitPattern.MatchString(pin.Ref) {
		t.Errorf("ref %q is not a full commit sha", pin.Ref)
	}
	if pin.Version == "" {
		t.Error("the pin names no version label for the image to stamp the built tk with")
	}
}

// The embedded copy and the checked-in file must be the same bytes: the
// file is the reviewable decision, the embed is what a shipped binary
// enforces, and a drift between them means someone edited one and not the
// other.
func TestEmbeddedPinMatchesTheCheckedInFile(t *testing.T) {
	data, err := os.ReadFile("../../" + SourcePinFile)
	if err != nil {
		t.Fatalf("read %s: %v", SourcePinFile, err)
	}
	if !reflect.DeepEqual(strings.TrimSpace(string(data)), strings.TrimSpace(string(ticfac.SourcePinJSON))) {
		t.Errorf("the embedded %s does not match the checked-in copy — rebuild the embed or revert the stray edit", SourcePinFile)
	}
}

func TestSourcePinRefusesAMovingRef(t *testing.T) {
	err := SourcePin{Repository: "pengelbrecht/ticks", Module: "github.com/pengelbrecht/ticks/cmd/tk", Version: "dev", Ref: "main"}.Validate()
	if err == nil || !strings.Contains(err.Error(), "moving ref") {
		t.Errorf("a branch name as the ref: %v", err)
	}
}

func TestSourcePinRefusesAnAbbreviatedCommit(t *testing.T) {
	err := SourcePin{Repository: "pengelbrecht/ticks", Module: "github.com/pengelbrecht/ticks/cmd/tk", Version: "dev", Ref: "f3be01bc"}.Validate()
	if err == nil || !strings.Contains(err.Error(), "full commit sha") {
		t.Errorf("an abbreviated commit as the ref: %v", err)
	}
}

func TestSourcePinRefusesAVersionThatIsNotALabel(t *testing.T) {
	pin := SourcePin{Repository: "pengelbrecht/ticks", Module: "github.com/pengelbrecht/ticks/cmd/tk", Version: "1 2 3", Ref: "f3be01bcc160217517df153343c81d2efb18358e"}
	if err := pin.Validate(); err == nil {
		t.Error("a version label with whitespace was accepted — it cannot sit in a Dockerfile ARG")
	}
}

func TestSourcePinRefusesAModuleFromAnotherRepository(t *testing.T) {
	pin := SourcePin{Repository: "pengelbrecht/ticks", Module: "github.com/other/toolkit/cmd/tk", Version: "dev", Ref: "f3be01bcc160217517df153343c81d2efb18358e"}
	err := pin.Validate()
	if err == nil || !strings.Contains(err.Error(), "does not come from repository") {
		t.Errorf("a module from a different repository: %v", err)
	}
}
