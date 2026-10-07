package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The acceptance fixture for the touched-packages check (tick r1f): a probe
// repository carrying a Go module of two packages, a gate that declares the
// real gate's two Go halves beside each other — the whole-repo short suite
// and the touched check — and a worker mode (fake-runner.sh's
// gate_break_touched) whose tick changes one package's code in a way only
// that package's FULL suite can see fail.
//
// dz1's shape, exactly: the regression lived in end-to-end tests the -short
// gate skips, in a package the tick touched, and it cost a repair and two
// ~30-minute CI cycles to surface at close-out. The check the fixture proves
// is the one the gate now runs: the touched package's full suite, in the
// gate, refusing the tick before anything else builds on it.
//
// The probe's own cmd/gate-touched is a faithful stand-in for ticfac's own
// (internal/gatescope, cmd/gate-touched): it reads the same exported pair —
// and exits 2 when the pair is not there, so a contract break fails this
// fixture loudly — and selects by the same ownership rule. What it does
// without is the reverse-dependency closure, which the real tool's own tests
// pin; the probe module has no import edges to walk.
//
// short: each test is one full fixture run whose gate really compiles and runs a module; skipped under -short with the rest of the end-to-end suite

// touchedGate is the probe repository's declared gate: the two Go halves of
// ticfac's own, with numbers small enough for a fixture (the real ones live
// in .tick/runners.toml, spelled out there). The cells and the tier policy
// are repairGate's, so the repair job the failing gate dispatches routes.
const touchedGate = `version = 2

[orchestration]
max_parallel = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.strong]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.frontier]
kind = "claude"
model = "sonnet"

[roles.review]
kind = "claude"
model = "opus"

[roles.review.tiers.frontier]
kind = "claude"
model = "opus"

[tier_policy]
default = "strong"
ceiling = "frontier"

[testing.commands]
short = { command = "go test -short -timeout 10m ./...", description = "the whole-repo short suite, as the real gate runs it" }
touched = { command = "go run ./cmd/gate-touched -timeout 10m -parallel 4", description = "the full suites of the packages the tick touched" }
`

// probeTool is the probe repository's own check, committed into the probe at
// base so the gate's worktree runs the tree's copy, the way ticfac's own gate
// runs its. Raw-string safe: it contains no backticks.
const probeTool = `package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	base, baseOK := os.LookupEnv("TICFAC_GATE_TOUCHED_BASE")
	head, headOK := os.LookupEnv("TICFAC_GATE_TOUCHED_HEAD")
	if !baseOK || !headOK {
		fmt.Println("gate-touched: the gate exported no touched pair: a contract break")
		os.Exit(2)
	}
	if base == "" || head == "" {
		fmt.Println("gate-touched: this gate names no touched diff, so no package's full suite runs")
		return
	}
	diff, err := output("git", "diff", "-z", "--name-only", base+"..."+head)
	if err != nil {
		fail(err)
	}
	listing, err := output("go", "list", "-f", "{{.ImportPath}}\t{{.Dir}}", "./...")
	if err != nil {
		fail(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	var pkgs []string
	seen := map[string]bool{}
	for _, file := range strings.Split(diff, "\x00") {
		if file == "" {
			continue
		}
		owner, err := ownerOf(listing, cwd, filepath.Dir(file))
		if err != nil {
			fail(err)
		}
		if owner == "" || seen[owner] {
			continue
		}
		seen[owner] = true
		pkgs = append(pkgs, owner)
	}
	sort.Strings(pkgs)
	if len(pkgs) == 0 {
		fmt.Printf("gate-touched: the diff %s...%s changes no Go package, so no full suite runs\n", base, head)
		return
	}
	fmt.Printf("gate-touched: the diff %s...%s; full (non-short) suites of %d package(s): %s\n",
		base, head, len(pkgs), strings.Join(pkgs, ", "))
	args := []string{"test", "-timeout", "10m", "-parallel", "4"}
	args = append(args, pkgs...)
	cmd := exec.Command("go", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		fail(err)
	}
}

// ownerOf is the ownership rule the real check runs (internal/gatescope):
// the deepest package whose directory is an ancestor of the changed file's.
func ownerOf(listing, cwd, dir string) (string, error) {
	best, bowner := "", ""
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 || fields[0] == "" {
			continue
		}
		rel, err := filepath.Rel(cwd, fields[1])
		if err != nil {
			return "", err
		}
		pdir := filepath.ToSlash(rel)
		if pdir != dir && !strings.HasPrefix(dir, pdir+"/") {
			continue
		}
		if len(pdir) > len(best) {
			best, bowner = pdir, fields[0]
		}
	}
	return bowner, nil
}

func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func fail(err error) {
	fmt.Println("gate-touched:", err)
	os.Exit(1)
}
`

// seedProbeModule commits the probe module into the fixture's repository, on
// the main the epic branch is cut from: two packages whose tests skip under
// -short (dz1's shape), one of them always failing in full, and the check
// the gate declares.
func seedProbeModule(t *testing.T, repo *testRepo) {
	t.Helper()
	files := map[string]string{
		"go.mod":                   "module example.com/probe\n\ngo 1.21\n",
		"alpha/alpha.go":           "package alpha\n\n// Answer is what the tick's change can break.\nconst Answer = 1\n",
		"alpha/alpha_test.go":      "package alpha\n\nimport \"testing\"\n\n// dz1's shape: this test skips under -short, so the short suite cannot see\n// the tick's break.\nfunc TestAnswer(t *testing.T) {\n\tif testing.Short() {\n\t\tt.Skip(\"the full suite's test, skipped by the short suite\")\n\t}\n\tif Answer != 1 {\n\t\tt.Fatalf(\"Answer = %d, want 1: the tick broke it\", Answer)\n\t}\n}\n",
		"beta/beta.go":             "package beta\n\n// Beta imports nothing of alpha: the gate must never select it.\n",
		"beta/beta_test.go":        "package beta\n\nimport \"testing\"\n\n// The untouched package's full suite, failing unconditionally: if any gate\n// ever runs it, this fixture's gates stop passing and the test says so.\nfunc TestBetaFailsInFull(t *testing.T) {\n\tif testing.Short() {\n\t\tt.Skip(\"the full suite's test, skipped by the short suite\")\n\t}\n\tt.Fatal(\"the untouched package's full suite ran: no gate may select it\")\n}\n",
		"cmd/gate-touched/main.go": probeTool,
	}
	for path, content := range files {
		full := filepath.Join(repo.Dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "the probe module: two packages and the touched check")
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", "main")
}

// TestATickThatBreaksAFullSuiteInAPackageItTouchedFailsItsGate is the
// acceptance criterion as one run: the tick's merge lands, the whole-repo
// short suite stays green (it cannot see the break), and the touched check
// refuses the tick — twice, because the repair job fixes the other half and
// the re-gate over the repaired tree must still cover the tick's exposure.
func TestATickThatBreaksAFullSuiteInAPackageItTouchedFailsItsGate(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: touchedGate, mode: "gate_break_touched"})
	seedProbeModule(t, f.Repo)

	r, result, err := f.run(f.Repo, fixtureOptions{gate: touchedGate, mode: "gate_break_touched"})
	if err != nil {
		t.Fatalf("the run should have finished with a failed state, not an error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s (%s): a tick that broke a full suite its gate skips must be refused",
			result.State, result.Reason)
	}
	if !strings.Contains(result.Reason, "touched (fail)") {
		t.Errorf("the refusal does not name the touched check: %s", result.Reason)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Error("a1 closed behind a gate its own diff breaks")
	}

	// The evidence: the touched check's records — the first gate's, and the
	// re-gate's over the repaired tree — each refuse naming alpha, and never
	// once name beta, whose full suite fails unconditionally. Untouched
	// packages' full suites do not run.
	var touched int
	for _, record := range gateEvidence(t, f, r) {
		if record.Check.ID != "touched" {
			continue
		}
		touched++
		if record.Result != "fail" {
			t.Errorf("the touched check recorded %s over a tick that broke alpha's full suite", record.Result)
			continue
		}
		out := evidenceOutput(record)
		if !strings.Contains(out, "example.com/probe/alpha") {
			t.Errorf("the touched check's output does not name the package the tick broke:\n%s", out)
		}
		if strings.Contains(out, "example.com/probe/beta") {
			t.Errorf("the touched check ran beta's full suite, which no tick touched:\n%s", out)
		}
	}
	if touched < 2 {
		t.Errorf("%d touched check record(s): the gate must have refused the tick AND the repair's tree (the union pair)", touched)
	}
}

// TestATickThatChangesNoGoPackagePassesItsGateWithoutAnyFullSuite is the
// criterion's other half: the untouched packages' full suites are not run.
// The default runner's tick commits a work file the probe's root owns —
// which is no package — so the touched check runs nothing and says so, and
// the gate passes although beta's full suite fails unconditionally: had any
// full suite run, it would have refused an innocent tick.
func TestATickThatChangesNoGoPackagePassesItsGateWithoutAnyFullSuite(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: touchedGate})
	seedProbeModule(t, f.Repo)

	r, result, err := f.run(f.Repo, fixtureOptions{gate: touchedGate})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a tick that changed no Go package must close", result.State, result.Reason)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s, not closed", current.Status)
	}
	var a1, found = runstate.Evidence{}, false
	for _, record := range gateEvidence(t, f, r) {
		if record.Key == "gate-a1-1-touched" {
			a1, found = record, true
		}
	}
	if !found {
		t.Fatal("a1's touched check left no evidence record")
	}
	if a1.Result != "pass" {
		t.Errorf("a1's touched check recorded %s over a tick that changed no Go package", a1.Result)
	}
	if out := evidenceOutput(a1); !strings.Contains(out, "changes no Go package") {
		t.Errorf("a1's touched check ran suites over a diff no package owns:\n%s", out)
	}
}

// gateEvidence reads the run's gate evidence records off origin.
func gateEvidence(t *testing.T, f *fixture, r *Reconciler) []runstate.Evidence {
	t.Helper()
	store, err := runstate.Open(runstate.Options{
		Repo: f.Repo.Dir, Remote: "origin", Branch: r.IntegrationBranch(), RunID: r.RunID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	keys := store.EvidenceKeys()
	out := make([]runstate.Evidence, 0, len(keys))
	for _, key := range keys {
		record, ok, err := store.Evidence(key)
		if err != nil || !ok {
			t.Fatalf("evidence %s: %v (found %v)", key, err, ok)
		}
		out = append(out, *record)
	}
	return out
}

func evidenceOutput(record runstate.Evidence) string {
	if record.Output.Inline == nil {
		return ""
	}
	return record.Output.Inline.Stdout
}
