package reconcile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// hn6's log and qrl: before cross-run dedup held (#164), run_ee8e absorbed
// two findings run_6d88 had already absorbed (as oro and yjq), so each
// finding stands in the tracker as TWO ticks, the later one open and gating
// the final review. Closing it by hand is a person doing the run's job. The
// next run closes the later tick as a duplicate of the earlier — noted,
// recorded on the feed — before it plans, so it is never dispatched.
func TestARunClosesALaterTickAFindingWasPromotedToTwiceBeforeScheduling(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f := newFixture(t, fixtureOptions{mode: "finding_local"})
	setEpicAcceptance(t, f, "[A2] A cloud run dispatches on the model the gateway names.")
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "finding_local", stopAfter: stopAt("a1", StageAbsorbed)})
	killedAfter(t, err, "a1", StageAbsorbed)
	f.stopEverything()
	holder := "r-fixture"

	decided, err := openRunStore(t, f.Repo.Dir, "epic/qeu", holder).Absorptions()
	if err != nil || len(decided) != 1 {
		t.Fatalf("the dead run left %d absorption decision(s) (%v), want one", len(decided), err)
	}
	first := decided[0]

	// The duplicate, as run_ee8e left it: another run's absorption of the
	// SAME finding key, a later decision, as a second open child of the epic
	// that the final review is blocked by.
	const dup, dupRun = "dupx", "r-dup"
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "dup-edit"))
	mustRun(t, clone.Dir, "git", "checkout", "--quiet", "epic/qeu")
	raw, err := os.ReadFile(filepath.Join(clone.Dir, runstate.AbsorptionPath(holder, first.Key)))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record["tick_id"] = dup
	record["decided_at"] = "2999-01-01T00:00:00Z"
	record["provenance"].(map[string]any)["run_id"] = dupRun
	edited, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(clone.Dir, runstate.AbsorptionPath(dupRun, first.Key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, path, string(edited)+"\n")
	mustRun(t, clone.Dir, "git", "add", "-A")
	mustRun(t, clone.Dir, "git", "commit", "--quiet", "-m", "a second run absorbed the same finding")
	mustRun(t, clone.Dir, "git", "push", "--quiet", "origin", "epic/qeu")
	ctx := context.Background()
	if _, err := f.Tracker.CreateTick(ctx, tk.Tick{ID: dup, Title: "the same finding, absorbed again",
		Parent: "qeu"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Tracker.BlockOn(ctx, "rv", dup); err != nil {
		t.Fatal(err)
	}

	host := &hostSays{run: holder, verdict: HolderDead}
	r, result, err := f.run(f.Repo, fixtureOptions{runID: "r-next", mode: "finding_local", claimHolder: host.ask})
	if err != nil {
		t.Fatalf("the new run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the new run ended %s (failure %+v)", result.State, result.Failure)
	}
	for _, event := range r.Journal() {
		if event.Tick == dup && (event.Stage == StageDispatched || event.Stage == StageClaimed) {
			t.Errorf("the duplicate %s was %s: it is closed before scheduling, never worked: %s", dup, event.Stage,
				event.Detail)
		}
	}
	line, ok := journalLine(r, dup, StageDuplicateClosed)
	if !ok {
		t.Fatalf("no %s line for %s\n%s", StageDuplicateClosed, dup, journalText(r))
	}
	for _, want := range []string{first.TickID, first.Key, holder, dupRun} {
		if !strings.Contains(line, want) {
			t.Errorf("the %s line does not name %q: %s", StageDuplicateClosed, want, line)
		}
	}
	shown, err := f.Tracker.Show(ctx, dup)
	if err != nil {
		t.Fatal(err)
	}
	if shown.Status != "closed" || !strings.Contains(shown.Notes, "duplicate of "+first.TickID) {
		t.Errorf("%s is %s with notes %q, want closed and noted as a duplicate of %s", dup, shown.Status,
			shown.Notes, first.TickID)
	}
	if got := f.Tracker.count("close:" + first.TickID); got != 1 {
		t.Errorf("the earlier tick %s closed %d times, want once: it is the finding's, and it is worked", first.TickID, got)
	}
}
