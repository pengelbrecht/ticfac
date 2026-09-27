package reconcile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// A finding routed to ANOTHER repository never holds a run (the epic-2jn
// close-out stall, 2026-09-27).
//
// The close-out of epic-2jn held on five findings "for pengelbrecht/ticks":
// every finding routed to the run's own repository had been decided by the
// run itself, but a routed one had no mechanical path — the run cannot absorb
// another repository's work, so it waited for a person, and the close-out's
// untriaged-findings hold fired on it. Some of those findings also claimed to
// break done items of the epic, which this run can never fix.
//
// THE CASES:
//
//  1. the target is one the repository's runners.toml lets the run file into
//     ([findings.route."owner/name"] file = true): the run files the finding
//     as a tick in the TARGET's own tracker, on its default branch, and the
//     draft is promoted as "<owner/name>:<id>";
//  2. no allowlist entry: the finding becomes a backlog tick HERE, titled with
//     the target and carrying the finding verbatim, and the draft is promoted
//     to it;
//  3. the allowlisted filing fails terminally: the same local backlog tick.
//
// In every case the run COMPLETES — no hold — and the finding gates nothing
// here, whatever done item it claims, with the reasoning on the record.

const routedTarget = "pengelbrecht/ticks"

// routedGate is passingGate plus the [findings] route for the target, filed
// through the given remote.
func routedGate(remote string) string {
	return passingGate + "\n[findings.route.\"" + routedTarget + "\"]\nfile = true\nremote = \"" + remote + "\"\n"
}

// newTargetTracker is the other repository: a bare origin on main whose tree
// carries a tracker with one tick already in it — the index a filing must mint
// around, and the tree a filing must not disturb.
func newTargetTracker(t *testing.T, root string) string {
	t.Helper()
	bare := filepath.Join(root, "ticks-origin.git")
	mustRun(t, root, "git", "init", "--quiet", "--bare", "-b", "main", bare)
	mustRun(t, bare, "git", "config", "maintenance.auto", "false")
	seed := filepath.Join(root, "ticks-seed")
	mustRun(t, root, "git", "init", "--quiet", "-b", "main", seed)
	configure(t, seed)
	if err := os.MkdirAll(filepath.Join(seed, ".tick", "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(seed, "README.md"), "# ticks\n")
	write(t, filepath.Join(seed, ".tick", "issues", "old.json"),
		`{"id":"old","title":"A tick already there","status":"open","priority":2,"type":"task","owner":"someone",`+
			`"created_by":"someone","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-01T00:00:00Z"}`+"\n")
	mustRun(t, seed, "git", "add", "-A")
	mustRun(t, seed, "git", "commit", "--quiet", "-m", "seed")
	mustRun(t, seed, "git", "remote", "add", "origin", bare)
	mustRun(t, seed, "git", "push", "--quiet", "origin", "main")
	return bare
}

// routedEpic is the 2jn shape of the epic's done: enumerated, the item the
// routed finding claims to break unverified — a finding that WOULD have gone
// to the predictor, and absorbed by the fallback, had it been this
// repository's.
func routedEpic(t *testing.T, f *fixture) {
	t.Helper()
	setEpicAcceptance(t, f, "[A1] Every tick closes behind a green gate.")
}

// routedFinding reads the run's one finding draft and one decision record.
func routedFinding(t *testing.T, f *fixture, r *Reconciler) (runstate.Finding, runstate.Absorption) {
	t.Helper()
	record := absorbingAbsorption(t, f.Repo, r)
	store := openRunStore(t, f.Repo.Dir, r.IntegrationBranch(), r.RunID())
	finding, ok, err := store.Finding(record.Key)
	if err != nil || !ok {
		t.Fatalf("read the routed finding's draft: %v %v", ok, err)
	}
	if finding.Target != routedTarget {
		t.Fatalf("the draft's target is %q, want %s", finding.Target, routedTarget)
	}
	return *finding, record
}

// assertRuleDecided is what every routed disposition shares: the run's RULE
// decided — never a prediction, never an observation — the finding gates
// nothing here although it claimed A1, and the record says why.
func assertRuleDecided(t *testing.T, finding runstate.Finding, record runstate.Absorption) {
	t.Helper()
	if record.Gating || record.ItemID != "" {
		t.Errorf("a finding routed to another repository was judged gating (%+v): this run cannot fix it", record)
	}
	if record.Basis != runstate.AbsorptionRule {
		t.Errorf("the basis is %q, want %q: nobody predicted or observed anything", record.Basis, runstate.AbsorptionRule)
	}
	if record.Target != routedTarget {
		t.Errorf("the record's target is %q, want %s", record.Target, routedTarget)
	}
	if !strings.Contains(record.Reason, "cannot fix another repository") || !strings.Contains(record.Reason, "A1") {
		t.Errorf("the decision does not say why the claim on A1 gates nothing here: %q", record.Reason)
	}
	if finding.Status != runstate.FindingPromoted || finding.PromotedAs != record.TickID {
		t.Errorf("the finding is %s promoted as %q, want promoted as %s", finding.Status, finding.PromotedAs, record.TickID)
	}
}

// 1. THE ALLOWLISTED PATH: the run files the finding in the target's own
// tracker, and the close-out hands over without a person.
func TestAFindingRoutedToAFileableRepositoryIsFiledThereAndHoldsNothing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := newTargetTracker(t, root)
	repo := newRepo(t, root, "repo", routedGate(target))
	forge := &fakeForge{}
	f := newFixture(t, fixtureOptions{mode: "finding_routed", repo: repo, pullRequests: forge})
	declareCloseoutRule(t, f.Repo)
	routedEpic(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_routed", pullRequests: forge})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a finding routed to another repository must never hold a run",
			result.State, result.Failure)
	}

	finding, record := routedFinding(t, f, r)
	assertRuleDecided(t, finding, record)
	if record.Placement != runstate.AbsorptionRouted {
		t.Fatalf("the placement is %q, want %q: the target is fileable", record.Placement, runstate.AbsorptionRouted)
	}
	id, ok := strings.CutPrefix(record.TickID, routedTarget+":")
	if !ok || id == "" {
		t.Fatalf("the filed tick is %q, want %s:<id>", record.TickID, routedTarget)
	}

	// THE TICK EXISTS WHERE EXPECTED: on the target's default branch, in its
	// tracker's own layout, carrying the finding verbatim and where it came
	// from — and the tick already there is untouched.
	raw := mustRun(t, target, "git", "show", "main:.tick/issues/"+id+".json")
	var filed tk.Tick
	if err := json.Unmarshal([]byte(raw), &filed); err != nil {
		t.Fatalf("the filed record does not read back as a tick: %v\n%s", err, raw)
	}
	if filed.ID != id || filed.Title != finding.Title || filed.Status != "open" {
		t.Errorf("the filed tick is %+v, want id %s, the finding's title, open", filed, id)
	}
	if !strings.Contains(filed.Description, "The upstream half, reported verbatim.") {
		t.Errorf("the filed tick does not carry the finding's own text: %q", filed.Description)
	}
	if filed.ExternalRef != findingSource+":"+finding.Key {
		t.Errorf("the filed tick's external_ref is %q, want %s:%s — the key a repeat filing is deduplicated on",
			filed.ExternalRef, findingSource, finding.Key)
	}
	if !strings.HasSuffix(raw, "}\n") || !strings.Contains(raw, "\n  \"id\"") {
		t.Errorf("the filed record is not in the tracker's pinned layout (two-space indent, trailing newline):\n%s", raw)
	}
	if old := mustRun(t, target, "git", "show", "main:.tick/issues/old.json"); !strings.Contains(old, "A tick already there") {
		t.Errorf("the tick already in the target's tracker was disturbed: %s", old)
	}

	// Nothing was filed here: the finding's tick is the target's, not ours.
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	for tickID, tick := range state.Ticks {
		if tick.Title == finding.Title {
			t.Errorf("the routed finding was also filed here as %s", tickID)
		}
	}

	// The epic PR lists the cross-repository finding and where it went.
	body := forge.body()
	if !strings.Contains(body, "routed to other repositories") || !strings.Contains(body, record.TickID) {
		t.Errorf("the epic PR does not list the routed finding and the tick it was filed as:\n%s", body)
	}
}

// 2. THE FALLBACK: no allowlist entry, so the finding becomes a backlog tick
// in THIS repository naming the target — promoted, not gating — and the run
// completes.
func TestAFindingRoutedToAnUnlistedRepositoryBecomesALocalBacklogTickAndHoldsNothing(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{mode: "finding_routed"})
	routedEpic(t, f)
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_routed"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a finding routed to another repository must never hold a run",
			result.State, result.Failure)
	}
	finding, record := routedFinding(t, f, r)
	assertRuleDecided(t, finding, record)
	assertLocalTrackingTick(t, f, finding, record, "no [findings.route")
}

// 3. A TERMINAL FILING FAILURE falls back the same way: the target is
// allowlisted but cannot be reached as a repository at all, which no retry
// cures — the finding becomes the local backlog tick, and the run completes.
func TestAFindingWhoseFilingFailsTerminallyBecomesALocalBacklogTick(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := newRepo(t, root, "repo", routedGate(filepath.Join(root, "no-such-repository.git")))
	f := newFixture(t, fixtureOptions{mode: "finding_routed", repo: repo})
	routedEpic(t, f)
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_routed"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a filing that failed must fall back, not hold", result.State, result.Failure)
	}
	finding, record := routedFinding(t, f, r)
	assertRuleDecided(t, finding, record)
	assertLocalTrackingTick(t, f, finding, record, "could not be filed")
}

// 4. THE LIVE 2jn SHAPE: a routed finding already PROPOSED when the close-out
// gates — left by an incarnation older than this rule, exactly the five
// drafts the epic-2jn close-out held on. The close-out disposes of it before
// it gates and hands over: the untriaged hold never fires on a routed
// finding, whoever drafted it.
func TestAProposedRoutedFindingIsDisposedOfByTheCloseOutNotHeldOn(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	routedEpic(t, f)
	stopped := fixtureOptions{stopAfter: stopAt("rv", StageClosed)}
	// The run is killed the moment the review closes: the close-out has not
	// gated yet, so the next incarnation's close-out is the one under test.
	_, _, _ = f.run(f.Repo, stopped)

	// The draft an older incarnation left: proposed, routed, claiming A1.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	attempts, err := store.Attempts()
	if err != nil || len(attempts) == 0 {
		t.Fatalf("read the run's attempts: %v %d", err, len(attempts))
	}
	reported := subprocess.Finding{Kind: "upstream-tick", Title: "An upstream finding an older run left proposed",
		Body: "Left for a person by a run before the rule.", Severity: "low", Target: routedTarget}
	draft := runstate.Finding{
		Key: findingKey(reported), Source: findingSource, DiscoveredFrom: "run-r-fixture/tick-a1/attempt-1",
		Kind: reported.Kind, Title: reported.Title, Body: reported.Body, Severity: reported.Severity,
		Target: reported.Target, DoneItem: "A1", TickID: attempts[0].TickID, Attempt: attempts[0].Attempt,
		Status: runstate.FindingProposed, ProposedAt: "2026-09-27T20:00:00Z", Provenance: attempts[0].Provenance,
	}
	if _, err := store.PutFinding(draft); err != nil {
		t.Fatalf("draft the proposed routed finding: %v", err)
	}

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s (%+v): the close-out must dispose of a routed finding, not hold on it",
			result.State, result.Failure)
	}
	finding, record := routedFinding(t, f, r)
	assertRuleDecided(t, finding, record)
	if record.Placement != runstate.AbsorptionBacklog {
		t.Errorf("the placement is %q, want the local backlog: nothing allows filing into %s here",
			record.Placement, routedTarget)
	}
}

// assertLocalTrackingTick is the fallback's shape: a backlog tick here — no
// parent, the epic's owner, never dispatched — titled with the target and
// carrying the finding verbatim and "for <owner/name>".
func assertLocalTrackingTick(t *testing.T, f *fixture, finding runstate.Finding, record runstate.Absorption, why string) {
	t.Helper()
	if record.Placement != runstate.AbsorptionBacklog {
		t.Fatalf("the placement is %q, want %q", record.Placement, runstate.AbsorptionBacklog)
	}
	if strings.Contains(record.TickID, ":") {
		t.Fatalf("the local tracking tick is named %q, want a bare id", record.TickID)
	}
	if !strings.Contains(record.Reason, why) {
		t.Errorf("the decision does not say why the finding was not filed in %s (want %q): %q", routedTarget, why, record.Reason)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	tick, ok := state.Ticks[record.TickID]
	if !ok {
		t.Fatalf("the local tracking tick %s does not exist", record.TickID)
	}
	if !strings.Contains(tick.Title, routedTarget) || !strings.Contains(tick.Title, finding.Title) {
		t.Errorf("the tracking tick's title %q does not name the target and the finding", tick.Title)
	}
	if !strings.Contains(tick.Description, "The upstream half, reported verbatim.") ||
		!strings.Contains(tick.Description, "for "+routedTarget) {
		t.Errorf("the tracking tick does not carry the finding verbatim and name its target: %q", tick.Description)
	}
	if tick.Parent != "" || tick.Status != "open" || tick.Owner != "operator@example.com" {
		t.Errorf("the tracking tick is parent %q, %s, owner %q: want a backlog tick, open, the epic's owner",
			tick.Parent, tick.Status, tick.Owner)
	}
}
