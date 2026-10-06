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
//   - `findings` lists the drafts with their keys and states, and says how
//     to triage each with `ticfac triage` (tick 8yn) — the same command the
//     close gate's refusal names, each draft addressed by a short key prefix;
//   - `finding` records ONE attributed decision, promoting into the repository
//     the finding targets — a routed finding promoted elsewhere is refused,
//     because that is the routing being dropped;
//   - a decision is never made twice, and the second call is not an error.

// newFindingsRepo seeds a repository whose integration branch already exists,
// the way a run's does, and returns the checkout to point --repo at.
//
// The fixture is isolated from the HOST's git config — GIT_CONFIG_GLOBAL and
// GIT_CONFIG_SYSTEM point at os.DevNull for the whole test — and names
// somebody in the fixture checkout itself, so a test's git reads are about
// THIS checkout, whatever machine runs it: a CI runner with no git identity
// sees the same fixture this Mac does, and a test that wants a checkout
// naming nobody unsets the name itself (the walk's own
// TestTheTriageActorDefaultsFromGitConfig does exactly that).
func newFindingsRepo(t *testing.T) (repo string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the path")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
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
	run(repo, "config", "user.name", "t")
	run(repo, "config", "user.email", "t@example.com")
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
	// The first draft carries its DONE EVIDENCE (tick nfo); the routed one
	// carries none — one listing, both marks: the person deciding sees which
	// acceptance item the reporter says is broken, and which finding made no
	// claim at all.
	linked := testDraftFinding("d34db33f", "")
	linked.DoneItem, linked.DemonstratingCheck = "A2", "go"
	seedFinding(t, repo, linked)
	seedFinding(t, repo, testDraftFinding("c0ffee00", "pengelbrecht/ticks"))

	var stdout, stderr bytes.Buffer
	code := Run([]string{"findings", "--repo", repo, "qeu"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	// The pointer teaches the everyday path (tick 8yn): `ticfac triage`,
	// each draft addressed by the shortest key prefix that names it alone —
	// never the old `ticfac finding <epic> <64-hex> --promote-as ...` shape,
	// which is the friction the triage surface exists to remove.
	for _, want := range []string{
		"d34db33f", "proposed", "proposed-tick", "high", "this repository", "A finding the surface lists",
		"c0ffee00", "upstream-tick", "pengelbrecht/ticks",
		"discovered by run-epic-qeu/tick-a1/attempt-1",
		"ticfac triage qeu d34=absorb|file|fixed:<commit>|discard",
		"ticfac triage qeu c0f=discard — or promote it into pengelbrecht/ticks with ticfac finding",
		"ticfac triage qeu settles each by short key prefix",
		"waiting for a person",
		`breaks done item A2 (demonstrated by "go")`,
		"unlinked: names no done item",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not carry %q:\n%s", want, out)
		}
	}
	// The old shape is gone from the pointer: no command carrying the key in
	// tow, no --promote-as, no --by on every call. A person following the
	// listing must not be taught to type what the triage surface settled.
	for _, old := range []string{
		"ticfac finding qeu d34db33f",
		"ticfac finding qeu c0ffee00",
		"--promote-as",
		`--by "<who>"`,
	} {
		if strings.Contains(out, old) {
			t.Errorf("stdout still teaches the old 64-hex triage %q:\n%s", old, out)
		}
	}
}

// The listing's triage hints address the store the listing was opened for
// (tick q8m): when the drafts were read from a non-default run id — a
// cloud run's factory run_<hex> — the hints must repeat that address, or
// a person copying them runs triage against the local default and finds
// nothing while the close-out stays held.
func TestTheFindingsListingHintsAddressTheRunTheyWereOpenedFor(t *testing.T) {
	repo := newFindingsRepo(t)
	cloudRun := "run_a1b2c3d4e5"
	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: "epic/qeu", RunID: cloudRun})
	if err != nil {
		t.Fatal(err)
	}
	finding := testDraftFinding("d34db33f", "")
	finding.Provenance.RunID = cloudRun
	if _, err := store.PutFinding(finding); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"findings", "--repo", repo, "qeu", "--run-id", cloudRun}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"ticfac triage qeu --run-id " + cloudRun + " d34=absorb|file|fixed:<commit>|discard",
		"ticfac triage qeu --run-id " + cloudRun + " settles each by short key prefix",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing does not address triage to the run it was opened for (%q):\n%s", want, out)
		}
	}
	// The default spelling keeps the bare command it has always carried.
	if strings.Contains(out, "--run-id") && !strings.Contains(out, "--run-id "+cloudRun) {
		t.Errorf("the listing names some other run id than the one it was opened for:\n%s", out)
	}
}

// The prefix the pointer shows names ONE draft: two drafts sharing a prefix
// get a longer one, because a person who copies the pointer's prefix into
// `ticfac triage` must land on the draft they read, not on an ambiguity the
// walk then refuses (tick 8yn).
func TestTheFindingsPointerNamesOneDraft(t *testing.T) {
	repo := newFindingsRepo(t)
	seedFinding(t, repo, testDraftFinding("d34db33f", ""))
	seedFinding(t, repo, testDraftFinding("d34dcafe", ""))

	var stdout, stderr bytes.Buffer
	code := Run([]string{"findings", "--repo", repo, "qeu"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"ticfac triage qeu d34db=absorb|file|fixed:<commit>|discard",
		"ticfac triage qeu d34dc=absorb|file|fixed:<commit>|discard",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not carry the unambiguous pointer %q:\n%s", want, out)
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

// A routed finding promoted as a LOCAL tracking tick that names its target is
// not "elsewhere": it is the tick the run itself files when it may not file
// into the target, and the routing stays visible in the tracker a person
// reads. A local tick that does not name the target is still refused.
func TestARoutedFindingMayBePromotedAsALocalTickThatNamesItsTarget(t *testing.T) {
	repo := newFindingsRepo(t)
	seedFinding(t, repo, testDraftFinding("c0ffee00", "pengelbrecht/ticks"))
	issues := filepath.Join(repo, ".tick", "issues")
	if err := os.MkdirAll(issues, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, title := range map[string]string{
		"trk": "For pengelbrecht/ticks: A finding the surface lists",
		"oth": "A tick about something else",
	} {
		record := `{"id":"` + id + `","title":"` + title + `","status":"open","priority":2,"type":"task",` +
			`"owner":"o","created_by":"o","created_at":"2026-09-27T00:00:00Z","updated_at":"2026-09-27T00:00:00Z"}`
		if err := os.WriteFile(filepath.Join(issues, id+".json"), []byte(record), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"finding", "--repo", repo, "--promote-as", "oth", "--by", "the operator", "qeu", "c0ffee00"},
		&stdout, &stderr); code != 1 {
		t.Fatalf("a local tick that does not name the target was accepted (exit %d): %s", code, stdout.String())
	}
	stderr.Reset()
	if code := Run([]string{"finding", "--repo", repo, "--promote-as", "trk", "--by", "the operator", "qeu", "c0ffee00"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("a local tracking tick naming the target was refused (exit %d): %s", code, stderr.String())
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

// The fixed verdict (tick her): a finding repaired inside the epic is triaged
// as fixed, naming the commit that repaired it. It is ONE decision like the
// other two — exactly one verdict per call, attributed — and the listing shows
// the commit, so the claim stays checkable.
func TestAFindingIsTriagedAsFixedNamingTheCommit(t *testing.T) {
	repo := newFindingsRepo(t)
	seedFinding(t, repo, testDraftFinding("d34db33f", ""))

	// Exactly one verdict: fixed alongside promote, discard, or alone-with-no
	// commit is a usage failure.
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"finding", "--repo", repo, "--fixed-as", "338bbf8b", "--promote-as", "zz9",
		"--by", "the operator", "qeu", "d34db33f"}, &stdout, &stderr); code != 2 {
		t.Fatalf("fixed plus promote exit %d, want 2: %s", code, stderr.String())
	}
	stderr.Reset()
	if code := Run([]string{"finding", "--repo", repo, "--fixed-as", "338bbf8b", "--discard",
		"--by", "the operator", "qeu", "d34db33f"}, &stdout, &stderr); code != 2 {
		t.Fatalf("fixed plus discard exit %d, want 2: %s", code, stderr.String())
	}
	stderr.Reset()
	if code := Run([]string{"finding", "--repo", repo, "--fixed-as", "338bbf8b",
		"--by", "the operator", "qeu", "d34db33f"}, &stdout, &stderr); code != 0 {
		t.Fatalf("fixed exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "338bbf8b") {
		t.Errorf("stdout %q does not name the commit the verdict records", stdout.String())
	}
	if !strings.Contains(stdout.String(), "is not suppressed") {
		t.Errorf("stdout %q does not say a later report of the same finding is not suppressed", stdout.String())
	}

	// The record on origin carries the commit, and the listing shows the fix.
	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: "epic/qeu", RunID: "epic-qeu"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	finding, ok, err := store.Finding("d34db33f")
	if err != nil || !ok {
		t.Fatalf("read the draft back: %v %v", ok, err)
	}
	if finding.Status != runstate.FindingFixed || finding.FixedAs != "338bbf8b" || finding.TriagedBy != "the operator" {
		t.Fatalf("finding %+v, want fixed as 338bbf8b by the operator", finding)
	}
	stdout.Reset()
	if code := Run([]string{"findings", "--repo", repo, "qeu"}, &stdout, &stderr); code != 0 {
		t.Fatalf("findings exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{"fixed", "338bbf8b", "none waiting for a person"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout does not carry %q:\n%s", want, stdout.String())
		}
	}

	// The second decision on the decided draft is not a decision either, and
	// names the standing fix.
	stdout.Reset()
	if code := Run([]string{"finding", "--repo", repo, "--discard", "--by", "someone else", "qeu", "d34db33f"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("re-triage exit %d, want 0: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already fixed") || !strings.Contains(stdout.String(), "338bbf8b") {
		t.Errorf("stdout %q does not report the standing fix", stdout.String())
	}
}
