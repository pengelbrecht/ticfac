package cli

// Tests for `ticfac factory wait-deployed`, against a fake source: a linear
// main history, a scripted sequence of what the factory reports, and the
// deploy-factory and CI runs GitHub would list. No network, no git, no gh.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory"
)

// fakeDeployWait is a linear main: history[i] contains history[j] for j <= i.
type fakeDeployWait struct {
	history  []string
	versions []string // what the factory reports on each poll; the last repeats
	polls    int
	deployed error
	shipped  map[string][]string // "base..head" -> shipped changes
	deploys  func(poll int) []workflowRun
	cis      func(poll int) []workflowRun
}

func (f *fakeDeployWait) index(ref string) int {
	for i, c := range f.history {
		if ref != "" && strings.HasPrefix(c, ref) {
			return i
		}
	}
	return -1
}

func (f *fakeDeployWait) ResolveCommit(_ context.Context, ref string) (string, error) {
	if i := f.index(ref); i >= 0 {
		return f.history[i], nil
	}
	return "", errCommitNotFound
}

func (f *fakeDeployWait) Deployed(context.Context) (*factory.DeployedFacts, error) {
	f.polls++
	if f.deployed != nil {
		return nil, f.deployed
	}
	i := f.polls - 1
	if i >= len(f.versions) {
		i = len(f.versions) - 1
	}
	return &factory.DeployedFacts{Version: f.versions[i]}, nil
}

func (f *fakeDeployWait) Contains(_ context.Context, ancestor, descendant string) (bool, error) {
	a, d := f.index(ancestor), f.index(descendant)
	if a < 0 || d < 0 {
		return false, errCommitNotFound
	}
	return a <= d, nil
}

func (f *fakeDeployWait) ShippedChanges(_ context.Context, base, head string) ([]string, error) {
	if changes, ok := f.shipped[base[:12]+".."+head[:12]]; ok {
		return changes, nil
	}
	return []string{"internal/cli/changed.go"}, nil
}

func (f *fakeDeployWait) DeployRuns(context.Context) ([]workflowRun, error) {
	if f.deploys == nil {
		return nil, nil
	}
	return f.deploys(f.polls), nil
}

func (f *fakeDeployWait) MainCIRuns(context.Context) ([]workflowRun, error) {
	if f.cis == nil {
		return nil, nil
	}
	return f.cis(f.polls), nil
}

// sha40 makes a recognisable 40-hex commit id.
func sha40(prefix string) string { return prefix + strings.Repeat("0", 40-len(prefix)) }

var (
	commitA = sha40("aaaaaaaaaaaa") // the factory's commit before the fix
	commitB = sha40("bbbbbbbbbbbb") // the fix
	commitC = sha40("cccccccccccc") // a newer main commit that supersedes it
)

func versionOf(sha string) string { return "v0.1.0-3-g" + sha[:12] }

func useFakeDeployWait(t *testing.T, f *fakeDeployWait) {
	t.Helper()
	saved := newDeployWaitSource
	newDeployWaitSource = func(string) deployWaitSource { return f }
	t.Cleanup(func() { newDeployWaitSource = saved })
}

func at(minute int) time.Time { return time.Date(2026, 10, 1, 9, minute, 0, 0, time.UTC) }

func runWaitDeployed(t *testing.T, extra ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"factory", "wait-deployed", "--interval", "1ms"}, extra...)
	code, out, errOut := runCloudArgs(t, args)
	return code, out.String(), errOut.String()
}

func TestWaitDeployedSucceedsWhenTheFactoryAlreadyRunsADescendant(t *testing.T) {
	f := &fakeDeployWait{history: []string{commitA, commitB, commitC}, versions: []string{versionOf(commitC)}}
	useFakeDeployWait(t, f)

	code, out, errOut := runWaitDeployed(t, commitB[:12])
	if code != exitSuccess {
		t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(out, "contains it") || f.polls != 1 {
		t.Errorf("want one poll and a line saying the factory contains it; polls=%d out=%q", f.polls, out)
	}
}

// The case hand-rolled loops never ended on: the fix's own deploy was skipped
// and then cancelled because a newer commit superseded it, and the newer
// commit's deploy is the one that carries the fix.
func TestWaitDeployedWaitsThroughASupersededDeploy(t *testing.T) {
	f := &fakeDeployWait{
		history:  []string{commitA, commitB, commitC},
		versions: []string{versionOf(commitA), versionOf(commitA), versionOf(commitA), versionOf(commitC)},
		deploys: func(int) []workflowRun {
			return []workflowRun{
				{ID: 3, HeadSHA: commitC, Status: "in_progress", CreatedAt: at(12)},
				{ID: 2, HeadSHA: commitC, Status: "completed", Conclusion: "cancelled", CreatedAt: at(11)},
				{ID: 1, HeadSHA: commitB, Status: "completed", Conclusion: "skipped", CreatedAt: at(10)},
			}
		},
	}
	useFakeDeployWait(t, f)

	code, out, errOut := runWaitDeployed(t, "--json", commitB)
	if code != exitSuccess {
		t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	var doc factoryWaitDeployedJSON
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out)
	}
	if doc.Schema != "ticfac.factory-wait-deployed.v1" || doc.State != agentStateDone || doc.SHA != commitB || doc.DeployedCommit != commitC[:12] {
		t.Errorf("document = %+v", doc)
	}
	if f.polls != 4 {
		t.Errorf("polls = %d, want 4 (waited until the factory reported the newer commit)", f.polls)
	}
	if !strings.Contains(errOut, "run 3") {
		t.Errorf("progress does not name the in-flight run:\n%s", errOut)
	}
}

func TestWaitDeployedFailsFastWhenTheCarryingDeployFailed(t *testing.T) {
	f := &fakeDeployWait{
		history:  []string{commitA, commitB, commitC},
		versions: []string{versionOf(commitA)},
		deploys: func(int) []workflowRun {
			return []workflowRun{
				{ID: 77, HeadSHA: commitC, Status: "completed", Conclusion: "failure", CreatedAt: at(12), URL: "https://github.invalid/run/77"},
				{ID: 76, HeadSHA: commitB, Status: "completed", Conclusion: "cancelled", CreatedAt: at(11)},
				{ID: 70, HeadSHA: commitA, Status: "completed", Conclusion: "failure", CreatedAt: at(1)},
			}
		},
		cis: func(int) []workflowRun {
			return []workflowRun{{ID: 9, HeadSHA: commitC, Status: "completed", Conclusion: "success", CreatedAt: at(5)}}
		},
	}
	useFakeDeployWait(t, f)

	code, out, errOut := runWaitDeployed(t, commitB)
	if code != exitGeneric {
		t.Fatalf("exit %d, want 1\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(errOut, "run 77") || !strings.Contains(errOut, "failure") {
		t.Errorf("the refusal does not name the run and its conclusion:\n%s", errOut)
	}
	if f.polls != 1 {
		t.Errorf("polls = %d, want 1 (fail fast)", f.polls)
	}
}

// A failed deploy is not the last word while a CI run that could carry the
// sha is still going: its deploy may yet come. With nothing changing, the
// wait runs out and exits the running class.
func TestWaitDeployedKeepsWaitingWhileSomethingCouldStillCarryIt(t *testing.T) {
	f := &fakeDeployWait{
		history:  []string{commitA, commitB, commitC},
		versions: []string{versionOf(commitA)},
		deploys: func(int) []workflowRun {
			return []workflowRun{{ID: 77, HeadSHA: commitB, Status: "completed", Conclusion: "failure", CreatedAt: at(12)}}
		},
		cis: func(int) []workflowRun {
			return []workflowRun{{ID: 9, HeadSHA: commitC, Status: "in_progress", CreatedAt: at(13)}}
		},
	}
	useFakeDeployWait(t, f)

	code, out, errOut := runWaitDeployed(t, "--timeout", "50ms", "--json", commitB)
	if code != exitRunning {
		t.Fatalf("exit %d, want 5 (timed out)\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	var doc factoryWaitDeployedJSON
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out)
	}
	if doc.State != agentStateRunning || !strings.Contains(doc.Reason, "run 9") {
		t.Errorf("document = %+v", doc)
	}
	if f.polls < 2 {
		t.Errorf("polls = %d, want it to have kept polling", f.polls)
	}
}

func TestWaitDeployedFailsWhenCIOnMainFailedForTheCarryingCommit(t *testing.T) {
	f := &fakeDeployWait{
		history:  []string{commitA, commitB},
		versions: []string{versionOf(commitA)},
		deploys: func(int) []workflowRun {
			return []workflowRun{{ID: 5, HeadSHA: commitB, Status: "completed", Conclusion: "skipped", CreatedAt: at(12)}}
		},
		cis: func(int) []workflowRun {
			return []workflowRun{{ID: 4, HeadSHA: commitB, Status: "completed", Conclusion: "failure", CreatedAt: at(2)}}
		},
	}
	useFakeDeployWait(t, f)

	code, _, errOut := runWaitDeployed(t, commitB)
	if code != exitGeneric || !strings.Contains(errOut, "CI on main") || !strings.Contains(errOut, "run 4") {
		t.Fatalf("exit %d, want 1 naming the failed CI run\nstderr: %s", code, errOut)
	}
}

// A sha that changes nothing the factory ships needs no deploy: the workflow's
// own path check skips it, and the factory already serves its tree.
func TestWaitDeployedSucceedsWhenNothingShippedChanged(t *testing.T) {
	f := &fakeDeployWait{
		history:  []string{commitA, commitB},
		versions: []string{versionOf(commitA)},
		shipped:  map[string][]string{commitA[:12] + ".." + commitB[:12]: nil},
	}
	useFakeDeployWait(t, f)

	code, out, errOut := runWaitDeployed(t, commitB)
	if code != exitSuccess || !strings.Contains(out, "nothing it ships changed") {
		t.Fatalf("exit %d, want 0 with the nothing-shipped reason\nstdout: %s\nstderr: %s", code, out, errOut)
	}
}

func TestWaitDeployedRefusals(t *testing.T) {
	f := &fakeDeployWait{history: []string{commitA}, versions: []string{versionOf(commitA)}}
	useFakeDeployWait(t, f)

	if code, _, errOut := runWaitDeployed(t, "deadbeef"); code != exitNotFound {
		t.Errorf("unknown sha: exit %d, want 4\n%s", code, errOut)
	}
	if code, _, errOut := runWaitDeployed(t); code != exitUsage {
		t.Errorf("no sha: exit %d, want 2\n%s", code, errOut)
	}
	f.deployed = factory.ErrNoFactory
	if code, _, errOut := runWaitDeployed(t, commitA); code != exitGeneric || !strings.Contains(errOut, "no factory is configured") {
		t.Errorf("no factory: exit %d, want 1 naming the missing configuration\n%s", code, errOut)
	}
}

// The shipped-paths list is deploy-factory.yml's, so "nothing shipped
// changed" here means exactly what the workflow's skip means.
func TestFactoryShippedPathsMatchTheWorkflow(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "deploy-factory.yml"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)paths=\((.*?)\n\s*\)`).FindStringSubmatch(string(raw))
	if block == nil {
		t.Fatal("deploy-factory.yml has no `paths=(` block")
	}
	var workflow []string
	for _, field := range strings.Fields(block[1]) {
		workflow = append(workflow, strings.Trim(field, "'"))
	}
	if strings.Join(workflow, " ") != strings.Join(factoryShippedPaths, " ") {
		t.Errorf("factoryShippedPaths drifted from deploy-factory.yml:\nworkflow: %q\ngo:       %q", workflow, factoryShippedPaths)
	}
}
