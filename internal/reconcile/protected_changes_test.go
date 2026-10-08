package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The epic-v5t incident (ticks tda, yck): a finding whose deliverable is an
// edit of .tick/runners.cloud.toml — a file the worker boundary refuses —
// was absorbed as a tick no worker could do, every worker answered BLOCKED
// with a sed for the operator, and the run ended failed waiting for a person.

// The run channel: the finding carries the exact change, and the run applies
// it itself — after the close-out's reads, as a labelled commit on the epic
// branch — lists it with its diff in the epic PR for the merger, and absorbs
// no tick. The run completes.
func TestAFindingCarryingAProtectedChangeIsAppliedAfterTheCloseOutNotAbsorbed(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	forge := &fakeForge{}
	opts := fixtureOptions{mode: "protected_change_finding", pullRequests: forge}
	f := newFixture(t, opts)
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")
	declareCloseoutRule(t, f.Repo)

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v — a protected change must never wait for a person",
			result.State, result.Reason, result.Failure)
	}

	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	if records, err := store.Absorptions(); err != nil {
		t.Fatal(err)
	} else if len(records) != 0 {
		t.Errorf("the finding was absorbed (%+v): its fix is a protected change no worker can make", records)
	}
	findings, err := store.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("%d finding draft(s), want 1: %+v", len(findings), findings)
	}
	finding := findings[0]
	if finding.ProtectedPath != ".tick/runners.cloud.toml" || finding.ProtectedAppend == "" {
		t.Errorf("the draft does not carry the proposed change: %+v", finding)
	}
	if finding.Status != runstate.FindingFixed || !sha40.MatchString(finding.FixedAs) {
		t.Fatalf("the finding is %s fixed as %q, want fixed as the commit that applied the change",
			finding.Status, finding.FixedAs)
	}

	// The commit is the run's own, labelled, and carries the change.
	branch := r.IntegrationBranch()
	mustRun(t, f.Repo.Dir, "git", "fetch", "--quiet", "origin", branch)
	subject := mustRun(t, f.Repo.Dir, "git", "log", "-1", "--format=%s", finding.FixedAs)
	if !strings.Contains(subject, protectedCommitSubject) {
		t.Errorf("the commit's subject is %q, want it labelled %q", subject, protectedCommitSubject)
	}
	file := mustRun(t, f.Repo.Dir, "git", "show", "origin/"+branch+":.tick/runners.cloud.toml")
	if !strings.Contains(file, "[configs.claude]") {
		t.Errorf("the epic branch's runners.cloud.toml does not carry the change:\n%s", file)
	}
	if !strings.Contains(mustRun(t, f.Repo.Dir, "git", "branch", "-r", "--contains", finding.FixedAs), branch) {
		t.Errorf("the epic branch does not carry the commit %s", finding.FixedAs)
	}

	// AFTER the close-out's reads: the close-out's attempt and its integrated
	// gate are behind before the change is applied.
	gated, applied := -1, -1
	for i, event := range r.Journal() {
		if event.Tick == "co" && event.Stage == StageGatePassed && gated < 0 {
			gated = i
		}
		if event.Stage == StageProtectedChangeApplied {
			applied = i
		}
		if event.Stage == StageAbsorbed || event.Stage == StageBacklogged {
			t.Errorf("a tick was made of the finding: %s", event.Detail)
		}
	}
	if applied < 0 {
		t.Fatalf("no %s line on the feed", StageProtectedChangeApplied)
	}
	if gated < 0 || applied < gated {
		t.Errorf("the change was applied at feed line %d, before the close-out's gate passed (line %d): it must "+
			"come after everything this run reads", applied, gated)
	}

	// The PR lists it, with its diff, for the merger.
	body := forge.body()
	for _, want := range []string{"## Protected changes for the merger", ".tick/runners.cloud.toml",
		"+[configs.claude]", "applied as " + short(finding.FixedAs)} {
		if !strings.Contains(body, want) {
			t.Errorf("the PR body does not carry %q:\n%s", want, body)
		}
	}
}

// Epic ex6's 2pn: a tick whose WHOLE deliverable is a protected change — the
// cloud's boundary refuses .tick/runners.toml — commits nothing and carries
// the change. That is a delivery, not an empty branch: the tick closes, the
// run applies the change after the close-out, and the run completes rather
// than holding the tick for a person.
func TestATickWhoseOnlyDeliverableIsAProtectedChangeClosesAndTheRunAppliesIt(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	opts := fixtureOptions{mode: "protected_change_only"}
	f := newFixture(t, opts)
	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v — a protected-change delivery must never wait for a person",
			result.State, result.Reason, result.Failure)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Ticks["b1"].Status; got != "closed" {
		t.Errorf("b1 is %s, want closed over its protected-change delivery", got)
	}
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	findings, err := store.Findings()
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Status != runstate.FindingFixed {
		t.Fatalf("findings %+v, want the one, fixed by the run's commit", findings)
	}
	branch := r.IntegrationBranch()
	mustRun(t, f.Repo.Dir, "git", "fetch", "--quiet", "origin", branch)
	if file := mustRun(t, f.Repo.Dir, "git", "show", "origin/"+branch+":.tick/runners.toml"); !strings.Contains(file, "# lint: make lint") {
		t.Errorf("the epic branch's runners.toml does not carry the change:\n%s", file)
	}
}

// The incident's own shape: the finding names the protected edit in prose
// only. It is never absorbed into the epic as a worker's tick — it is a
// backlog tick outside the epic, gating nothing — and the run completes.
func TestAFindingWhoseDeliverableIsAProtectedEditIsNotAbsorbedAsAWorkersTick(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	opts := fixtureOptions{mode: "protected_edit_prose"}
	f := newFixture(t, opts)
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s: %+v", result.State, result.Reason, result.Failure)
	}
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	records, err := store.Absorptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("%d absorption record(s), want the one decision: %+v", len(records), records)
	}
	record := records[0]
	if record.Gating || record.Placement != runstate.AbsorptionBacklog || record.Basis != runstate.AbsorptionRule {
		t.Errorf("the decision is gating=%v placed %s on basis %s, want a backlog tick by rule", record.Gating,
			record.Placement, record.Basis)
	}
	if !strings.Contains(record.Reason, ".tick/runners.cloud.toml") {
		t.Errorf("the reason does not name the protected file: %s", record.Reason)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if tick, ok := state.Ticks[record.TickID]; !ok || tick.Parent == "qeu" {
		t.Errorf("the backlog tick %s is %+v, want a tick outside the epic", record.TickID, tick)
	}
}

// The boundary stays: a worker that writes the protected file in its own
// commits is still refused, whatever channel now exists for the change.
//
// short: pure functions; no processes.
func TestAWorkerWritingAProtectedFileIsStillRefused(t *testing.T) {
	t.Parallel()
	for _, path := range []string{".tick/runners.cloud.toml", ".tick/runners.local.toml"} {
		if got := subprocess.BoundaryViolations([]string{"src/a.go", path}); len(got) != 1 || got[0] != path {
			t.Errorf("BoundaryViolations(%s) = %v, want the write refused", path, got)
		}
		if !subprocess.ProposableProtectedPath(path) {
			t.Errorf("%s is not proposable: the run channel must take it", path)
		}
	}
	for _, path := range []string{"src/a.go", ".tick/issues/x.json", ".ticfac/runs/r/findings/k.json"} {
		if subprocess.ProposableProtectedPath(path) {
			t.Errorf("%s is proposable: only configuration directly in .tick/ is", path)
		}
	}
	// The cloud substrate refuses all of .tick/ (tick 9sy, epic ex6's 2pn), so
	// the file the local boundary exempts is proposable — and unwritable there.
	if !subprocess.ProposableProtectedPath(".tick/runners.toml") ||
		!subprocess.UnwritableOn(".tick/runners.toml", true) || subprocess.UnwritableOn(".tick/runners.toml", false) {
		t.Error(".tick/runners.toml: want proposable, unwritable on the cloud, writable locally")
	}
}

// short: pure functions; no processes.
func TestProtectedDeliverableReadsTheTitleOrAnUnwritableBody(t *testing.T) {
	t.Parallel()
	cases := []struct {
		title, body string
		cloud, want bool
	}{
		{"Append the two named cloud configs to .tick/runners.cloud.toml", "", false, true},
		{"Cloud configs are missing", "The block must go in .tick/runners.cloud.toml. This must be done by the " +
			"operator or the run, not a worker.", false, true},
		{"The router misreads a cell", "selectRunConfig reads .tick/runners.cloud.toml and drops the tier.", false, false},
		{"Fix the parser", "", false, false},
		// Epic ex6's 2pn: a runners.toml cell is a local worker's to write,
		// and no cloud worker's.
		{"Declare the lint command in .tick/runners.toml", "", false, false},
		{"Declare the lint command in .tick/runners.toml", "", true, true},
	}
	for _, c := range cases {
		if got := len(protectedDeliverable(c.title, c.body, c.cloud)) > 0; got != c.want {
			t.Errorf("protectedDeliverable(%q, %q, cloud=%v) = %v, want %v", c.title, c.body, c.cloud, got, c.want)
		}
	}
}
