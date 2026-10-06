package cli

// The epic-across-runs wiring (hn6, tick gmo): the gathering passes the
// status model every earlier run's records for the same epic — the Sources
// are the only place the wire is observable, the same rule the wave-1
// readers' wiring test holds.

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// TestStatusWiringPassesPriorRecords: a LOCAL run's gathering hands the
// model every earlier run's records beside its own, oldest first, from the
// sibling run directories the integration branch carries.
func TestStatusWiringPassesPriorRecords(t *testing.T) {
	repo := newFindingsRepo(t)
	captured := captureStatusSources(t)

	// The prior run's records, as an earlier run pushed them.
	priorStore, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: "epic/qeu", RunID: "run_before"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", repo, "rev-parse", "refs/remotes/origin/epic/qeu").Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(out))
	provenance := func(runID string) runstate.Provenance {
		return runstate.Provenance{RunID: runID, SourceRef: "refs/heads/epic/qeu",
			SourceSHA: sha, IntegrationRef: runstate.Ptr("refs/heads/epic/qeu"),
			Phase: runstate.PhaseWorker}
	}
	if _, err := priorStore.PutCheckpoint(runstate.Checkpoint{
		SchemaVersion: runstate.SchemaVersion,
		RunID:         "run_before",
		EpicID:        "qeu",
		Sequence:      1,
		State:         runstate.StateCompleted,
		Reason:        "the fixture's earlier run",
		UpdatedAt:     "2026-09-26T10:00:00Z",
		Provenance:    provenance("run_before"),
	}); err != nil {
		t.Fatal(err)
	}
	// The gathered run's own records, the newest layer.
	ownStore, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: "epic/qeu", RunID: "epic-qeu"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownStore.PutCheckpoint(runstate.Checkpoint{
		SchemaVersion: runstate.SchemaVersion,
		RunID:         "epic-qeu",
		EpicID:        "qeu",
		Sequence:      2,
		State:         runstate.StateRunning,
		Reason:        "the fixture's live run",
		UpdatedAt:     "2026-09-28T10:00:00Z",
		Provenance:    provenance("epic-qeu"),
	}); err != nil {
		t.Fatal(err)
	}

	// The run whose model is gathered, alive so the gather answers a live
	// run's Sources.
	life, err := runlife.Claim(repo, "epic-qeu")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	localStatusModel(context.Background(), repo, "epic-qeu", runlife.Status{
		State: runlife.Alive, Reason: "the fixture holds the pid",
	}, modelGatherers{
		graph: func(context.Context, string, string) *tk.Graph { return nil },
		ci:    func(context.Context, string, string) (*statusmodel.CIInput, error) { return nil, nil },
	})

	if captured.Records == nil || captured.Records.Checkpoint == nil ||
		captured.Records.Checkpoint.RunID != "epic-qeu" {
		t.Fatalf("the gathering passed the run's own records %+v, want epic-qeu's", captured.Records)
	}
	if len(captured.PriorRecords) != 1 || captured.PriorRecords[0].Checkpoint == nil ||
		captured.PriorRecords[0].Checkpoint.RunID != "run_before" {
		t.Fatalf("the gathering passed prior records %+v, want run_before's, oldest first", captured.PriorRecords)
	}
}
