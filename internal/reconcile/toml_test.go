package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The one table the reconciler is allowed to read, in both spellings, and the
// refusals that keep it from reading anything else.

func TestGateCommandsReadTheInlineTableSpelling(t *testing.T) {
	got, err := parseGateCommands(`
version = 2

[orchestration]
substrate = "auto"

[testing.commands]
go = { command = "go test -short ./...", description = "Go" }
lint = { command = "golangci-lint run", description = "Lint" }

[environment.commands]
which-go = { command = "which go", description = "Go toolchain on PATH" }
`)
	if err != nil {
		t.Fatal(err)
	}
	want := GateCommands{
		{Name: "go", Command: "go test -short ./...", Description: "Go"},
		{Name: "lint", Command: "golangci-lint run", Description: "Lint"},
	}
	if len(got) != len(want) {
		t.Fatalf("read %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("command %d is %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestGateCommandsReadTheSubTableSpelling(t *testing.T) {
	got, err := parseGateCommands(`
[testing.commands.go]
command = "go test ./..."
description = "Go"
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Command != "go test ./..." || got[0].Name != "go" {
		t.Fatalf("read %+v", got)
	}
}

// [environment.commands] configures somebody else's process. The reconciler
// running one of those would be the reconciler running a command nothing
// authorised, so the reader must not see them at all.
func TestOnlyTheTestingCommandsTableIsRead(t *testing.T) {
	got, err := parseGateCommands(`
[environment.commands]
which-go = { command = "which go", description = "x" }

[roles.implement]
kind = "claude"
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("the reader returned %+v from tables that are not [testing.commands]", got)
	}
}

func TestCommentsAndQuotedHashesAreHandled(t *testing.T) {
	got, err := parseGateCommands(`
[testing.commands] # the gate
go = { command = "go test -run 'A#B' ./...", description = "Go" } # trailing
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Command != "go test -run 'A#B' ./..." {
		t.Fatalf("read %+v", got)
	}
}

func TestAGateWithNoCommandIsRefused(t *testing.T) {
	_, err := parseGateCommands("[testing.commands.go]\ndescription = \"Go\"\n")
	if err == nil || !strings.Contains(err.Error(), "declares no command") {
		t.Fatalf("a named gate that runs nothing was accepted: %v", err)
	}
}

func TestABareStringCommandIsRefused(t *testing.T) {
	_, err := parseGateCommands("[testing.commands]\ngo = \"go test ./...\"\n")
	if err == nil || !strings.Contains(err.Error(), "inline table") {
		t.Fatalf("a bare string was accepted as a gate: %v", err)
	}
}

// This repository's own runners.toml is the one document the reader must not
// get wrong: it is what the epic's integrated gate runs.
func TestThisRepositorysGateIsReadable(t *testing.T) {
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".tick", "runners.toml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no %s in this checkout", path)
	}
	got, err := ReadGateCommands(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("this repository declares [testing.commands] and the reader found none")
	}
	for _, command := range got {
		if !strings.Contains(command.Command, "go test") {
			t.Errorf("gate %q is %q", command.Name, command.Command)
		}
	}
}
