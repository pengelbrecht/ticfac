package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// The acceptance criterion, stated as a test: `ticfac run-epic x` exits 2 and
// says "no executor configured". A build with no executor behind the
// four-operation protocol must refuse rather than report a run it did not
// make — Appendix A #5's failure, in the smallest form it can take.
//
// "No executor" is CONSTRUCTED here, not inherited from the host. CheckExecutor
// resolves ticfac-exec-subprocess beside the running binary and then on PATH,
// so on a developer's machine — where that binary is installed, as it must be
// for a run to work at all — the check PASSES and the command proceeds to fail
// later on something unrelated. The test then reports the wrong refusal and the
// gate is red at base. Pointing PATH at an empty directory is what makes this
// assert the tree's behaviour rather than whether ticfac happens to be
// installed on the host running the gate.
func TestRunEpicFailsClosed(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	var stdout, stderr bytes.Buffer
	code := Run([]string{"run-epic", "x"}, &stdout, &stderr)

	if code != ExitNoExecutor {
		t.Errorf("exit code %d, want %d", code, ExitNoExecutor)
	}
	if !strings.Contains(stderr.String(), NoExecutorMessage) {
		t.Errorf("stderr %q does not carry %q", stderr.String(), NoExecutorMessage)
	}
	if stdout.Len() != 0 {
		t.Errorf("a refusal wrote to stdout: %q", stdout.String())
	}
}

func TestRunEpicNeedsExactlyOneEpicID(t *testing.T) {
	for _, args := range [][]string{{"run-epic"}, {"run-epic", "a", "b"}, {"run-epic", ""}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
	}
}

func TestVersionJSONReportsTheContractBundle(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"version", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr.String())
	}

	var info struct {
		Ticfac          string `json:"ticfac"`
		ContractBundle  string `json:"contract_bundle"`
		TicksRepository string `json:"ticks_repository"`
		TicksRef        string `json:"ticks_ref"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatalf("version --json is not JSON: %v\n%s", err, stdout.String())
	}
	if info.Ticfac == "" {
		t.Error("version --json reports no ticfac version")
	}
	if info.ContractBundle == "" || info.TicksRef == "" || info.TicksRepository == "" {
		t.Errorf("version --json must name the bundle and the ticks ref it was built against: %+v", info)
	}

	// The embedded answer must equal the vendored one. They are embedded so
	// that a binary and the tree beside it cannot disagree; this asserts the
	// embedding is of the right files.
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "contracts", "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var bundle struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if info.ContractBundle != bundle.Version {
		t.Errorf("the binary reports bundle %s; contracts/bundle.json is %s", info.ContractBundle, bundle.Version)
	}
}

func TestVersionWithoutJSONIsHumanReadable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "ticfac ") {
		t.Errorf("version printed %q", stdout.String())
	}
}

func TestAnUnknownCommandIsAUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"reconcile-everything"}, &stdout, &stderr); code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
}

// The exit code as the shell sees it. Run() returning 2 and the process
// exiting 2 are different claims, and the acceptance criterion is about the
// second one.
func TestTheBuiltBinaryExitsTwo(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "ticfac")

	build := exec.Command("go", "build", "-o", binary, "./cmd/ticfac")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// The refusal is "no executor", so the host's executor must not be
	// findable: the binary looks for ticfac-exec-subprocess beside itself
	// (a temp dir, so never) and then on PATH — and a developer who has
	// installed ticfac has it on PATH, which made this test red on every
	// such machine and green in CI (tick yjs). TestRunEpicFailsClosed
	// empties PATH for the same reason. The cwd is a temp dir too: the test
	// measures the binary, not whichever checkout it happens to run in.
	cmd := exec.Command(binary, "run-epic", "x")
	cmd.Env = append(os.Environ(), "PATH="+t.TempDir())
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("ticfac run-epic x succeeded:\n%s", out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running ticfac: %v", err)
	}
	if exitErr.ExitCode() != ExitNoExecutor {
		t.Errorf("exit code %d, want %d\n%s", exitErr.ExitCode(), ExitNoExecutor, out)
	}
	if !strings.Contains(string(out), NoExecutorMessage) {
		t.Errorf("output %q does not carry %q", out, NoExecutorMessage)
	}
}

// A shipped binary has no checkout beside it: nothing above a bare install
// directory holds a go.mod. Before this test's fix, the tk client located its
// JSON manifest by walking up from the process's working directory looking
// for one, so a binary run from outside this repository failed the tracker
// with "cannot locate the repository root" before it ever got to using it.
// The manifest now travels embedded in the binary; this proves it reads from
// a cwd with no repository above it at all.
func TestTheBuiltBinaryLoadsTheManifestOutsideTheRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	binary := filepath.Join(bin, "ticfac")
	build := exec.Command("go", "build", "-o", binary, "./cmd/ticfac")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ticfac: %v\n%s", err, out)
	}
	buildSub := exec.Command("go", "build", "-o", filepath.Join(bin, "ticfac-exec-subprocess"), "./cmd/ticfac-exec-subprocess")
	buildSub.Dir = root
	if out, err := buildSub.CombinedOutput(); err != nil {
		t.Fatalf("go build ticfac-exec-subprocess: %v\n%s", err, out)
	}

	// t.TempDir() sits under the OS temp directory, never under this
	// repository's checkout — exactly the "no go.mod above me" cwd a shipped
	// binary runs from. It doubles as --repo: an empty, non-tick directory,
	// so the run fails closed on the tracker or the repository, never on the
	// manifest.
	outside := t.TempDir()

	cmd := exec.Command(binary, "run-epic", "--repo", outside, "x")
	cmd.Dir = outside
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("ticfac run-epic x succeeded from outside the repository:\n%s", out)
	}
	if strings.Contains(string(out), "cannot locate the repository root") {
		t.Fatalf("manifest loading depends on a repository checkout beside the binary:\n%s", out)
	}
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// With an executor on PATH the refusal moves on to the next thing that can
// refuse. What must NOT happen is a run reported against a repository or a
// tracker this build cannot use.
func TestRunEpicRefusesAnUnusableRepositoryRatherThanReportingARun(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "ticfac-exec-subprocess"), "./cmd/ticfac-exec-subprocess")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	code := Run([]string{"run-epic", "--repo", t.TempDir(), "no-such-epic"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code %d, want 1: %s%s", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("a refusal wrote to stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "run-epic") {
		t.Errorf("stderr does not say which command refused: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), NoExecutorMessage) {
		t.Errorf("an executor is on PATH and the refusal still says %q: %q", NoExecutorMessage, stderr.String())
	}
}

// A12 at the surface an operator reads: the number that will GOVERN is printed
// at submission, while the run can still be cancelled cheaply. Before this, the
// clamp happened and the effective number went into the reconciler's in-memory
// journal — so an operator asking for 40 under a ceiling of 8 was never told 8
// by anything they could see.
func TestTheEffectiveBudgetIsPrintedBeforeTheRun(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "ticfac-exec-subprocess"), "./cmd/ticfac-exec-subprocess")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	// The run itself refuses on the repository, which is not what this is
	// about: the budget line has to be there BEFORE anything that can refuse.
	Run([]string{"run-epic", "--repo", t.TempDir(), "--budget", "40", "--ceiling", "8", "no-such-epic"},
		&stdout, &stderr)

	line := stdout.String()
	if !strings.Contains(line, "$8.00 effective") {
		t.Errorf("the operator is not told the number that will govern: %q", line)
	}
	if !strings.Contains(line, "clamped") || !strings.Contains(line, "$40.00") {
		t.Errorf("the line does not say what was asked for or that it was clamped: %q", line)
	}
	if !strings.Contains(line, "informational") {
		t.Errorf("the line does not say what the number does on this host: %q", line)
	}
}

// A redirected `ticfac run-epic` is empty until the run ends (tick bzx): it
// is no monitoring signal. run.log and `ticfac status` now cover most of the
// need; this is the cheap remainder — the ONE line at startup naming the run
// id and the two commands that follow it, said before anything is dispatched,
// so a redirected invocation says what to watch from its first line. The line
// also lands in run.log, which keeps what this process said whether or not
// whoever launched it captured the output.
func TestTheStartupLineNamesTheRunAndItsCommands(t *testing.T) {
	line := startupLine("epic-9pd")
	for _, want := range []string{
		"epic-9pd",
		"ticfac status epic-9pd",
		"ticfac events epic-9pd --follow",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the startup line does not say %q: %q", want, line)
		}
	}
}

// A feed write failure is not a verdict about the work, so it must not fail
// the run — and silence is the one thing it must not be (tick d6s): an
// operator who runs `ticfac events <run-id> --follow` against a run whose
// feed cannot be written waits forever on a file that will never appear. The
// warning is the run's own report saying what happened and what it does NOT
// mean, on the surface the operator is already reading.
func TestTheFeedFailureWarningSaysWhatItMeans(t *testing.T) {
	line := feedFailureLine("r-abc", errors.New("append: the path is a directory"))
	for _, want := range []string{
		"r-abc",
		"not a verdict about the work",
		"ticfac events r-abc",
		"append: the path is a directory",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the warning does not say %q: %q", want, line)
		}
	}
}

// Supervision is ON by default and --supervise=false is the way off (tick
// go6), and the two flags meet in one number the reconciler reads. The
// default is the argument: the behaviour it replaces is not "the run stops",
// it is "the run stops and a person retypes the identical command" — which
// the operator did about fifteen times in one day, and which is the same
// behaviour with worse latency and no record.
func TestSupervisionIsOnByDefaultAndTheFlagIsHowItGoesOff(t *testing.T) {
	if got := autoResumeCap(true, reconcile.DefaultAutoResumeCap); got != reconcile.DefaultAutoResumeCap {
		t.Errorf("a supervised run's cap is %d, want the default %d", got, reconcile.DefaultAutoResumeCap)
	}
	if got := autoResumeCap(false, reconcile.DefaultAutoResumeCap); got >= 0 {
		t.Errorf("--supervise=false produced a cap of %d; supervision off is a NEGATIVE cap, which is the one "+
			"number that makes Supervise exactly Run", got)
	}
}

// The intervention count an operator reads (tick zi2): the number, and what
// each stop was. A run that says "completed" after four automatic
// continuations did not run unattended, and the line has to say so on the
// surface the operator is already reading rather than only in the feed.
func TestTheInterventionLineNamesEveryAutomaticContinuation(t *testing.T) {
	line := resumeLine(&reconcile.Result{Resumes: []reconcile.Resume{
		{Reason: reconcile.RefusedClaimWidth, TickID: "a2"},
		{Reason: reconcile.StoppedRemoteTransient},
	}})
	for _, want := range []string{
		"interventions: 2",
		reconcile.RefusedClaimWidth + " on a2",
		reconcile.StoppedRemoteTransient,
		"did not run unattended",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the intervention line does not say %q: %q", want, line)
		}
	}
}
