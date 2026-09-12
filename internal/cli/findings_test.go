package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The findings triage surface (tick 7vn): the commands a person uses to decide
// the drafts a run has filed. What has to hold:
//
//   - `findings` lists the drafts with their keys and states, and says how to
//     triage each — the refusal message the close gate prints names these
//     exact commands;
//   - `finding` records ONE attributed decision, promoting into the repository
//     the finding targets — a routed finding promoted elsewhere is refused,
//     because that is the routing being dropped;
//   - a decision is never made twice, and the second call is not an error.

// newFindingsRepo seeds a repository whose integration branch already exists,
// the way a run's does, and returns the checkout to point --repo at.
func newFindingsRepo(t *testing.T) (repo string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the path")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	repo = filepath.Join(root, "checkout")

	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
		}
		return string(out)
	}
	run(root, "init", "--quiet", "--bare", "-b", "main", bare)
	run(root, "init", "--quiet", "-b", "epic/qeu", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("the target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(seed, "add", "-A")
	run(seed, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--quiet", "-m", "seed")
	run(seed, "push", "--quiet", bare, "epic/qeu")
	run(root, "clone", "--quiet", bare, repo)
	return repo
}

func seedFinding(t *testing.T, repo string, finding runstate.Finding) {
	t.Helper()
	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: "epic/qeu", RunID: "epic-qeu"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutFinding(finding); err != nil {
		t.Fatal(err)
	}
}

func testDraftFinding(key, target string) runstate.Finding {
	kind := "proposed-tick"
	if target != "" {
		kind = "upstream-tick"
	}
	p := runstate.Provenance{
		RunID:          "epic-qeu",
		TickID:         runstate.Ptr("a1"),
		Attempt:        runstate.Ptr(1),
		SourceRef:      "refs/heads/epic/qeu",
		SourceSHA:      "acb08b9493dd8647918efbebac27079c64339946",
		IntegrationRef: runstate.Ptr("refs/heads/epic/qeu"),
		Phase:          runstate.PhaseWorker,
		Executor:       runstate.Ptr("local-subprocess"),
		Role:           runstate.Ptr("implement-tick"),
	}
	return runstate.Finding{
		Key:            key,
		Source:         "ticfac-worker",
		DiscoveredFrom: "run-epic-qeu/tick-a1/attempt-1",
		Kind:           kind,
		Title:          "A finding the surface lists",
		Body:           "",
		Severity:       "high",
		Target:         target,
		TickID:         "a1",
		Attempt:        1,
		Status:         runstate.FindingProposed,
		ProposedAt:     "2026-09-11T18:00:00Z",
		Provenance:     p,
	}
}

func TestFindingsListsTheDraftsAndHowToTriageThem(t *testing.T) {
	repo := newFindingsRepo(t)
	seedFinding(t, repo, testDraftFinding("d34db33f", ""))
	seedFinding(t, repo, testDraftFinding("c0ffee00", "pengelbrecht/ticks"))

	var stdout, stderr bytes.Buffer
	code := Run([]string{"findings", "--repo", repo, "qeu"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"d34db33f", "proposed", "proposed-tick", "high", "this repository", "A finding the surface lists",
		"c0ffee00", "upstream-tick", "pengelbrecht/ticks",
		"discovered by run-epic-qeu/tick-a1/attempt-1",
		"ticfac finding qeu d34db33f --promote-as <tick>",
		"waiting for a person",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not carry %q:\n%s", want, out)
		}
	}
}

func TestFindingsWithNothingDraftedSaysSo(t *testing.T) {
	repo := newFindingsRepo(t)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"findings", "--repo", repo, "qeu"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no findings drafted") {
		t.Errorf("stdout %q", stdout.String())
	}
}

func TestAFindingIsPromotedWithProvenanceAndRouting(t *testing.T) {
	repo := newFindingsRepo(t)
	seedFinding(t, repo, testDraftFinding("d34db33f", ""))
	seedFinding(t, repo, testDraftFinding("c0ffee00", "pengelbrecht/ticks"))

	// A routed finding promoted as a LOCAL tick is refused: the routing the
	// finding carried is exactly what a misfiled promotion drops.
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"finding", "--repo", repo, "--promote-as", "zz9", "--by", "the operator", "qeu", "c0ffee00"},
		&stdout, &stderr); code != 1 {
		t.Fatalf("misfiled promotion exit %d, want 1: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "routed to pengelbrecht/ticks") {
		t.Errorf("stderr %q does not name the target", stderr.String())
	}

	// A local finding promoted as a foreign tick is refused too.
	stderr.Reset()
	if code := Run([]string{"finding", "--repo", repo, "--promote-as", "pengelbrecht/ticks:zz9", "--by", "the operator", "qeu", "d34db33f"},
		&stdout, &stderr); code != 1 {
		t.Fatalf("misrouted promotion exit %d, want 1: %s", code, stderr.String())
	}

	// The right promotions: the local one as a bare tick, the routed one
	// carrying the repository it targets.
	stdout.Reset()
	if code := Run([]string{"finding", "--repo", repo, "--promote-as", "zz9", "--by", "the operator", "qeu", "d34db33f"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("promote exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "discovered_from run-epic-qeu/tick-a1/attempt-1") {
		t.Errorf("stdout %q does not carry the discovered_from the promoted tick must be filed under", stdout.String())
	}
	if code := Run([]string{"finding", "--repo", repo,
		"--promote-as", "pengelbrecht/ticks:of9", "--by", "the operator", "qeu", "c0ffee00"}, &stdout, &stderr); code != 0 {
		t.Fatalf("routed promote exit %d: %s", code, stderr.String())
	}

	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: "epic/qeu", RunID: "epic-qeu"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"d34db33f": "zz9", "c0ffee00": "pengelbrecht/ticks:of9"} {
		finding, ok, err := store.Finding(key)
		if err != nil || !ok {
			t.Fatalf("read %s back: %v %v", key, ok, err)
		}
		if finding.Status != runstate.FindingPromoted || finding.PromotedAs != want {
			t.Errorf("%s is %s as %q, want promoted as %q", key, finding.Status, finding.PromotedAs, want)
		}
		if finding.TriagedBy != "the operator" {
			t.Errorf("%s triaged_by %q", key, finding.TriagedBy)
		}
	}
}

func TestADiscardIsAttributedAndNeverMadeTwice(t *testing.T) {
	repo := newFindingsRepo(t)
	seedFinding(t, repo, testDraftFinding("d34db33f", ""))

	// Unattributed: refused, for the same reason a settlement's release is.
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"finding", "--repo", repo, "--discard", "qeu", "d34db33f"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unattributed discard exit %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--by names who is triaging") {
		t.Errorf("stderr %q", stderr.String())
	}

	if code := Run([]string{"finding", "--repo", repo, "--discard", "--by", "the operator", "qeu", "d34db33f"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("discard exit %d: %s", code, stderr.String())
	}

	// The second decision on the same draft is not an error, and it is not a
	// decision either: whatever the human did with the original is what a
	// repeat finding must not reopen.
	stdout.Reset()
	if code := Run([]string{"finding", "--repo", repo, "--promote-as", "zz9",
		"--by", "someone else", "qeu", "d34db33f"}, &stdout, &stderr); code != 0 {
		t.Fatalf("re-triage exit %d, want 0: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already discarded") || !strings.Contains(stdout.String(), "never made twice") {
		t.Errorf("stdout %q does not report the standing decision", stdout.String())
	}
}

func TestAFindingKeyThatDoesNotExistIsAUsageFailure(t *testing.T) {
	repo := newFindingsRepo(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"finding", "--repo", repo, "--discard", "--by", "who", "qeu", "nope"},
		&stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no findings draft nope") {
		t.Errorf("stderr %q", stderr.String())
	}
}
