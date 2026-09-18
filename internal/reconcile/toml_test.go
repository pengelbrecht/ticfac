package reconcile

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The one table the reconciler is allowed to read, in both spellings, and the
// refusals that keep it from reading anything else.

func TestGateCommandsReadTheInlineTableSpelling(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	_, err := parseGateCommands("[testing.commands.go]\ndescription = \"Go\"\n")
	if err == nil || !strings.Contains(err.Error(), "declares no command") {
		t.Fatalf("a named gate that runs nothing was accepted: %v", err)
	}
}

func TestABareStringCommandIsRefused(t *testing.T) {
	t.Parallel()
	_, err := parseGateCommands("[testing.commands]\ngo = \"go test ./...\"\n")
	if err == nil || !strings.Contains(err.Error(), "inline table") {
		t.Fatalf("a bare string was accepted as a gate: %v", err)
	}
}

// [testing.commands] entries are pinned to exactly `command` and
// `description` (contracts/runners-config-contract.json, testing.commands). A
// third key is not a future schema addition to tolerate — it is a malformed
// entry, and the refusal must name the file and the offending line.
func TestAThirdKeyInACommandsEntryIsRefused(t *testing.T) {
	t.Parallel()
	for _, document := range []string{
		"[testing.commands]\ngo = { command = \"go test ./...\", timeout = \"30s\" }\n",
		"[testing.commands.go]\ncommand = \"go test ./...\"\ntimeout = \"30s\"\n",
	} {
		_, err := parseGateCommands(document)
		if err == nil {
			t.Fatalf("%q was accepted with an unrecognised key", document)
		}
		if !strings.Contains(err.Error(), "runners.toml") || !strings.Contains(err.Error(), "timeout") {
			t.Errorf("%q was refused with %q, which does not name the file and the offending key", document, err)
		}
	}
}

// This repository's own runners.toml is the one document the reader must not
// get wrong: it is what the epic's integrated gate runs.
func TestThisRepositorysGateIsReadable(t *testing.T) {
	t.Parallel()
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

// TestTheHarnessBoundOutlivesEveryDeclaredGateBound.
//
// A gate command that names its own timeout has said what it considers too
// long, and that bound is the useful one: `go test -timeout` prints the stack
// of every running goroutine, so the answer is "this test hung, here is where".
// The harness bound underneath it answers `signal: killed`, exit -1, which
// names nothing and points at nothing.
//
// The two disagreed for as long as they both existed — a 30 minute harness
// bound under a declared 45 — and nothing noticed while the run worked one
// tick at a time and the gate finished in ten minutes. The first widened run
// hit it on its first gate: bzx's ran against three live workers competing for
// the machine, crossed 30 minutes, and was killed undiagnosed. The tick was
// refused for the run's own scheduling rather than for anything about its work.
func TestTheHarnessBoundOutlivesEveryDeclaredGateBound(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".tick", "runners.toml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no %s in this checkout", path)
	}
	commands, err := ReadGateCommands(path)
	if err != nil {
		t.Fatal(err)
	}
	declared := regexp.MustCompile(`-timeout[= ]([0-9]+[smh])`)
	for _, command := range commands {
		match := declared.FindStringSubmatch(command.Command)
		if match == nil {
			continue // a command that declares no bound of its own is what the harness bound is FOR
		}
		own, err := time.ParseDuration(match[1])
		if err != nil {
			t.Fatalf("gate %q declares an unparseable timeout %q: %v", command.Name, match[1], err)
		}
		if DefaultGateTimeout <= own {
			t.Errorf("gate %q declares -timeout %s and the harness kills it at %s: the harness would "+
				"pre-empt the command's own bound, replacing a report that names the hung test with "+
				"`signal: killed`. The harness bound is the OUTER one.",
				command.Name, own, DefaultGateTimeout)
		}
	}
}
