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
			"run-epic", "settle", "findings", "finding", "status", "events", "watch",
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
	for _, name := range []string{"run-epic", "settle", "findings", "finding", "status", "events", "watch",
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

// The bare invocation keeps the contract it had before the tree: usage on
// stderr and exit 2 — a program with no default action says what it does and
// refuses. What the tree changed is where the TEXT comes from, not what an
// argumentless call means: the refusal is the tree's own help (below), so
// the text cannot drift from the tree the way the hand-rolled usage const
// did (tick fi3).
func TestABareInvocationIsStillAUsageRefusal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != exitUsage {
		t.Fatalf("ticfac exits %d, want %d", code, exitUsage)
	}
	if stdout.Len() != 0 {
		t.Errorf("the bare invocation wrote to stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "ticfac") {
		t.Errorf("the bare invocation does not say what ticfac does: %q", stderr.String())
	}
}

// The refusal is DERIVED, not maintained (tick fi3): a bare invocation must
// print byte for byte what `ticfac --help` prints — the tree's own styled
// help, rendered by fang — only to stderr and with exit 2. Before the tree,
// this was a hand-rolled usage const in cli.go that said the same things a
// second time and drifted; the pin is the equality, because a hand-maintained
// copy cannot pass it.
func TestTheBareInvocationRefusalIsTheTreeHelp(t *testing.T) {
	var bareStdout, bareStderr bytes.Buffer
	if code := Run(nil, &bareStdout, &bareStderr); code != exitUsage {
		t.Fatalf("ticfac exits %d, want %d", code, exitUsage)
	}
	if bareStdout.Len() != 0 {
		t.Errorf("the bare invocation wrote to stdout: %q", bareStdout.String())
	}

	var helpStdout, helpStderr bytes.Buffer
	if code := Run([]string{"--help"}, &helpStdout, &helpStderr); code != exitSuccess {
		t.Fatalf("--help exits %d, want %d: %s", code, exitSuccess, helpStderr.String())
	}
	if got, want := bareStderr.String(), helpStdout.String(); got != want {
		t.Errorf("the bare invocation refuses with something other than the tree's help:\n"+
			"--help prints:\n%s\nthe bare invocation prints:\n%s", want, got)
	}
}
