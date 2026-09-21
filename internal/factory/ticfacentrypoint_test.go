package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stagedEntrypoint is the staged image context as the deploy leaves it: the
// embedded tree materialized, then the orchestrator entrypoint rewritten. It
// returns the path of the staged entrypoint.
func stagedEntrypoint(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
	if err := SetSandboxOrchestratorEntrypoint(dir); err != nil {
		t.Fatalf("SetSandboxOrchestratorEntrypoint: %v", err)
	}
	return filepath.Join(dir, ticfacEntrypointName)
}

// The acceptance criterion at the level this package owns: the entrypoint the
// image installs as /usr/local/bin/ticks-orchestrator boots ticfac, and boots
// it with a real base branch rather than the literal "HEAD" (tick udu).
//
// Run against the VENDORED image/entrypoint.sh this fails, which is the point:
// that script execs a harness on the ticks skill loop.
func TestStagedOrchestratorEntrypointExecsTicfac(t *testing.T) {
	path := stagedEntrypoint(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)

	for _, want := range []string{
		"exec \"${cmd[@]}\"",
		"ticfac run-epic",
		`--base "$base_branch"`,
		`--repo "$workdir"`,
		// The container runs as root and every worker is launched with
		// `--permission-mode bypassPermissions`, which the claude CLI refuses
		// under root unless it is told it is in a sandbox. Proved by running:
		// without it the harness probe died on "cannot be used with root/sudo
		// privileges for security reasons".
		"export IS_SANDBOX=1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the staged orchestrator entrypoint does not carry %q — it is not booting ticfac", want)
		}
	}

	// Everything before the exec is KEPT. The keeper above all: it is why
	// committed work outlives an evicted container.
	for _, want := range []string{"start_keeper", "run_preflight", "clone_at_sha", "TK_ACTOR"} {
		if !strings.Contains(text, want) {
			t.Errorf("the staged orchestrator entrypoint lost %q — the boot before the exec must not change", want)
		}
	}
}

// The staged script has to be a script. bash -n is what the image's own build
// runs over it, so a rewrite that produced a syntax error would fail the docker
// build rather than the suite — much later, and much more expensively.
func TestStagedOrchestratorEntrypointParses(t *testing.T) {
	path := stagedEntrypoint(t)
	if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("bash -n on the staged entrypoint failed: %v\n%s", err, out)
	}
}

// A second application is a refusal, not a second definition of one function.
// MaterializeSandbox writes the staged copy fresh on every deploy, so a second
// insertion means the staging order is wrong and the deploy should say so.
func TestSetSandboxOrchestratorEntrypointRefusesASecondApplication(t *testing.T) {
	dir := t.TempDir()
	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
	if err := SetSandboxOrchestratorEntrypoint(dir); err != nil {
		t.Fatalf("first application: %v", err)
	}
	err := SetSandboxOrchestratorEntrypoint(dir)
	if err == nil {
		t.Fatal("a second application was accepted; the staged entrypoint now defines start_harness twice")
	}
	if !strings.Contains(err.Error(), "already execs ticfac") {
		t.Errorf("refusal does not say what happened: %v", err)
	}
}

// The anchor is the vendored script's last line. If ticks ever stops ending
// entrypoint.sh with `main "$@"`, this rewrite has to be re-derived — and the
// deploy has to stop rather than ship an image whose override is never called.
func TestSetSandboxOrchestratorEntrypointRefusesAMissingAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ticfacEntrypointName)
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nstart_harness() { :; }\nstart_harness\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := SetSandboxOrchestratorEntrypoint(dir)
	if err == nil {
		t.Fatal("an entrypoint with no main \"$@\" was accepted")
	}
	if !strings.Contains(err.Error(), "no final") {
		t.Errorf("refusal does not name the anchor: %v", err)
	}
}

// What the override actually DOES, proved by running it rather than by reading
// it. The block is evaluated on top of a stand-in for the parts of the
// entrypoint it uses, with `exec` and `git` replaced so the command it would
// have become is observable.
func runOverride(t *testing.T, phase string, extra string) string {
	t.Helper()
	block := ticfacEntrypointBlock

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// The two binaries the override requires on PATH, and a git that answers
	// the default-branch question the way a real remote does.
	for _, name := range []string{"ticfac", "ticfac-exec-subprocess"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitStub := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *symbolic-ref*) exit 1 ;;\n" +
		"  *ls-remote*) printf 'ref: refs/heads/trunk\\tHEAD\\n' ; exit 0 ;;\n" +
		"  *fetch*) printf 'GIT-FETCH: %s\\n' \"$*\" ; exit 0 ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(gitStub), 0o755); err != nil {
		t.Fatal(err)
	}

	script := `set -uo pipefail
PATH="` + bin + `:$PATH"
EXIT_CONFIG=3; EXIT_CLONE=4; EXIT_MODEL=5
ACTOR="cloud:orchestrator"
workdir="` + dir + `"
epic="e1"; run_id="r1"; run_branch="tick-run/e1"; run_pass=""
factory_url=""; factory_token=""; factory_project=""
model="sonnet"
phase="` + phase + `"
say() { printf 'say: %s\n' "$*"; }
warn() { printf 'warn: %s\n' "$*"; }
die() { shift; printf 'die: %s\n' "$*"; exit 1; }
start_keeper() { printf 'keeper watching %s\n' "$1"; }
start_harness() { printf 'HARNESS\n'; }
exec() { printf 'EXEC: %s\n' "$*"; }
` + extra + `
` + block + `
start_harness
`
	path := filepath.Join(dir, "case.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The exit status is not the assertion here: a case that REFUSES exits
	// non-zero on purpose, and what it said is what the test reads.
	out, _ := exec.Command("bash", path).CombinedOutput()
	return string(out)
}

func TestOverrideExecsTicfacRunEpicOnTheRemotesDefaultBranch(t *testing.T) {
	out := runOverride(t, "run", "")
	if !strings.Contains(out, "EXEC: ticfac run-epic") {
		t.Fatalf("the override did not exec ticfac run-epic:\n%s", out)
	}
	// tick udu: --base must be a BRANCH. "HEAD" resolves to nothing and base
	// refresh silently does nothing every round.
	if !strings.Contains(out, "--base trunk") {
		t.Errorf("the override did not pass the remote's default branch as --base:\n%s", out)
	}
	if strings.Contains(out, "--base HEAD") {
		t.Errorf("the override passed the literal HEAD as --base (tick udu):\n%s", out)
	}
	// And the base has to be a ref the CHECKOUT holds: the clone is a fetch of
	// one SHA, so rev-parse of a bare branch name fails until it is fetched,
	// and the reconciler refuses the integration branch at its first leg.
	// Proved by running a container without this: "the base \"main\" for
	// epic/lf0 is not a commit this checkout has".
	if !strings.Contains(out, "GIT-FETCH: ") || !strings.Contains(out, "refs/heads/trunk:refs/heads/trunk") {
		t.Errorf("the override did not fetch the base branch into the checkout:\n%s", out)
	}
	if !strings.Contains(out, "--run-id r1") || !strings.HasSuffix(strings.TrimSpace(out), "e1") {
		t.Errorf("the override did not name the run and the epic:\n%s", out)
	}
	// The keeper is started before the exec, watching this process.
	if !strings.Contains(out, "keeper watching") {
		t.Errorf("the override did not start the run keeper:\n%s", out)
	}
	if strings.Contains(out, "HARNESS") {
		t.Errorf("a run boot reached the harness:\n%s", out)
	}
}

func TestOverrideLeavesTheReviewPhaseOnTheHarness(t *testing.T) {
	out := runOverride(t, "review", "")
	if !strings.Contains(out, "HARNESS") {
		t.Fatalf("a review boot did not reach the harness it still needs:\n%s", out)
	}
	if strings.Contains(out, "ticfac run-epic") {
		t.Errorf("a review boot tried to reconcile an epic:\n%s", out)
	}
}

func TestOverrideRefusesANonAnthropicRoute(t *testing.T) {
	out := runOverride(t, "run", `TICKS_MODEL_PROVIDER="workers-ai"; export TICKS_MODEL_PROVIDER; model="workers-ai/@cf/x"`)
	if !strings.Contains(out, "die:") || !strings.Contains(out, "Anthropic API") {
		t.Fatalf("a run routed away from Anthropic was not refused:\n%s", out)
	}
	if strings.Contains(out, "EXEC:") {
		t.Errorf("it dispatched workers that could not have made one model call:\n%s", out)
	}
}

func TestOverrideRefusesAnImageWithoutTheExecutor(t *testing.T) {
	// The executor removed from PATH: ticfac would refuse at the first
	// dispatch anyway, but by then the clone and the pre-flight are paid for.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ticfac"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := runOverride(t, "run", `PATH="`+dir+`"`)
	if !strings.Contains(out, "ticfac-exec-subprocess") || !strings.Contains(out, "die:") {
		t.Fatalf("an image missing the executor was not refused:\n%s", out)
	}
}
