package cli

// The command tree, proven as a tree (tick nwj): every command runs on
// cobra wrapped by fang, so the styled help, --version, the completion
// command and the man pages are all DERIVED from the one tree rather than
// hand-rolled beside it — and the derivation is what these tests pin, because
// a tree that stopped deriving them is a tree the next surface (the skills
// and MCP work the epic names) would be built on sand.
//
// What these tests do NOT pin is the styling: fang renders through a
// colorprofile writer that strips ANSI for anything that is not a terminal,
// so a buffer sees exactly the words, and the words are the contract.

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// `ticfac --version` is fang's: cobra's version flag, fed from the build's
// Version variable — the same value `ticfac version` reports beside the
// contract bundle.
func TestVersionFlagReportsTheBuild(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--version"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("--version exits %d, want %d: %s", code, exitSuccess, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "ticfac version ") {
		t.Errorf("--version does not report the build: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), Version) {
		t.Errorf("--version does not carry this build's version %q: %q", Version, stdout.String())
	}
}

// The help is rendered from the tree, so it has to NAME the tree: every
// command a script or an operator can type, in the one listing an operator
// reads first. This is the derivation the skills/MCP work stands on — a
// surface generated from a tree that does not carry the commands is a surface
// generated from the wrong tree.
func TestHelpIsRenderedFromTheTree(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"help"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != exitSuccess {
			t.Fatalf("%v: exits %d, want %d: %s", args, code, exitSuccess, stderr.String())
		}
		if stdout.Len() == 0 {
			t.Fatalf("%v: help printed nothing", args)
		}
		for _, name := range []string{
			"run-epic", "init", "doctor", "settle", "findings", "finding", "triage", "status", "events", "watch",
			"version", "factory", "herd", "cloud",
		} {
			if !strings.Contains(stdout.String(), name) {
				t.Errorf("%v: the help does not name %q:\n%s", args, name, stdout.String())
			}
		}
	}
}

// A subcommand's help carries its flags, because the flags are declared on
// the tree: an operator reading `ticfac run-epic --help` sees the same flag
// surface the command parses, and a drift between the two is a defect in
// the listing, not a difference in kind.
func TestACommandsHelpCarriesItsFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"run-epic", "--help"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("run-epic --help exits %d: %s", code, stderr.String())
	}
	for _, flag := range []string{"--repo", "--remote", "--branch", "--run-id", "--wall",
		"--supervise", "--absorption-depth", "--evacuate-seconds"} {
		if !strings.Contains(stdout.String(), flag) {
			t.Errorf("run-epic --help does not list %s:\n%s", flag, stdout.String())
		}
	}
}

// The completion command is fang's, generated from the tree: a shell that
// completes `ticfac <TAB>` asks cobra's __complete, and __complete answers
// from the tree — so the assertion runs the real question a shell asks rather
// than grepping the generated script, which delegates at runtime.
func TestTheCompletionCommandDerivesFromTheTree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"completion", "bash"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("completion bash exits %d, want %d: %s", code, exitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ticfac") {
		t.Errorf("the bash completion script does not complete ticfac itself:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"__complete", ""}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("__complete exits %d, want %d: %s", code, exitSuccess, stderr.String())
	}
	out := stdout.String()
	for _, name := range []string{"run-epic", "init", "doctor", "settle", "findings", "finding", "triage", "status", "events", "watch",
		"version", "factory", "herd", "cloud"} {
		if !strings.Contains(out, name) {
			t.Errorf("__complete does not offer %q:\n%s", name, out)
		}
	}
}

// The hidden `man` command (fang's) renders the tree as a man page. It
// writes to the process's own stdout — not the tree's writers — so the exit
// code is what a buffer can read and the page itself is asserted by the
// built-binary test below, which sees the process's stdout the way a person
// piping `ticfac man` does. The process stdout is pointed at the null device
// for the duration, so the gate's evidence stays readable.
func TestTheManCommandRendersTheTree(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open the null device: %v", err)
	}
	t.Cleanup(func() { _ = devnull.Close() })
	old := os.Stdout
	os.Stdout = devnull
	defer func() { os.Stdout = old }()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"man"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("man exits %d, want %d: %s", code, exitSuccess, stderr.String())
	}
}

// The built binary proves the man page bytes and the --version flag together:
// both are surfaces only the shipped artifact has, and both were the tick's
// acceptance (man pages and --version from build info).
func TestTheBuiltBinaryRendersManPagesAndReportsItsVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	binary := t.TempDir() + "/ticfac"
	build := exec.Command("go", "build", "-o", binary, "./cmd/ticfac")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	man := exec.Command(binary, "man")
	man.Dir = t.TempDir()
	page, err := man.Output()
	if err != nil {
		t.Fatalf("ticfac man: %v", err)
	}
	for _, want := range []string{".TH", "ticfac", "run-epic", "factory", "cloud"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the man page does not carry %q:\n%s", want, string(page))
		}
	}

	ver := exec.Command(binary, "--version")
	ver.Dir = t.TempDir()
	out, err := ver.Output()
	if err != nil {
		t.Fatalf("ticfac --version: %v", err)
	}
	if !strings.HasPrefix(string(out), "ticfac version ") {
		t.Errorf("the built binary reports %q for --version", string(out))
	}
}

// The bare invocation IS the overview (tick 2qz): `ticfac` with no
// arguments lists every run the checkout and the factory know, attention
// first. fi3's contract — a bare call is a usage refusal, because a program
// with no default action refuses — is deliberately replaced by this tick:
// ticfac now has a default action, the one screen an unattended factory is
// glanced at with, and `ticfac --help` stays the place a person reads the
// whole tree. The empty world here is pinned where the overview's own
// tests pin the full one.
func TestABareInvocationIsTheOverview(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// The overview enumerates the machine's run registry (tick 9ss), and
	// this package's tests claim runs into ONE registry shared by the whole
	// package run — a claim an earlier test leaves there would surface here
	// as a phantom run. This test pins the EMPTY world, so it holds its own.
	ownRegistry(t)
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("ticfac exits %d, want %d: %s", code, exitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No runs.") {
		t.Errorf("the bare invocation does not list the (absent) runs: %q", stdout.String())
	}
}
