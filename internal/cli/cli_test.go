package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The acceptance criterion, stated as a test: `ticfac run-epic x` exits 2 and
// says "no executor configured". A build with no executor behind the
// four-operation protocol must refuse rather than report a run it did not
// make — Appendix A #5's failure, in the smallest form it can take.
func TestRunEpicFailsClosed(t *testing.T) {
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

	cmd := exec.Command(binary, "run-epic", "x")
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
