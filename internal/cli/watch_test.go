package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// `ticfac watch` is the consumer the run event feed was built for (tick 0z0):
// the feed says a run ended holding an attempt, and nothing read it — the
// operator noticed by looking. Watch subscribes like `events --follow`, and
// when the run stops holding something for a person it SAYS SO to a human:
// which tick, which attempt, why, and the command that moves it on. Its exit
// code is the condition an orchestrator waits on, so the stall becomes an
// answer instead of silence.

func TestWatchSurfacesARunThatEndedHoldingAnAttempt(t *testing.T) {
	repo := t.TempDir()
	attempt := 3
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), "r-1", "nkf", &attempt, "dispatched", "attempt 3 started as run-x/tick-nkf/attempt-3"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC), "r-1", "nkf", &attempt, reconcile.StageRunHeld,
		"attempt_unaddressed: nobody can say whether the attempt is running"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 4, 0, time.UTC), "r-1", "", nil, "run_finished",
		"failed: nkf did not pass"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d for a run that ended holding an attempt; stderr %q", code, ExitHeld, stderr.String())
	}
	// What surfaces names WHICH TICK and WHY — the acceptance criterion —
	// and says what moves the hold on, carry and all. The settle command is
	// addressed by the run's own epic id — a placeholder a person would
	// still have to fill in is not a command.
	for _, want := range []string{"nkf try 1 (run dispatch #3)", "settle r-1 nkf 3 ", "attempt_unaddressed", "--carry-work", "settle",
		// The release command names the run whose store carries the attempt
		// (tick qxj): this run's records live under r-1, not under the epic
		// spelling a settle without --run-id opens.
		"--run-id r-1 --release"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the alert does not name %q: %q", want, stderr.String())
		}
	}
	// The events themselves are still printed, so a person watching sees the
	// whole run, and the hold is visible on stdout too.
	if !strings.Contains(stdout.String(), reconcile.StageRunHeld) {
		t.Errorf("the held line never printed on stdout: %q", stdout.String())
	}
}

// The settle command the hold alert names is addressed by the run's OWN
// epic id, never a `<epic-id>` placeholder: the alert exists so a person
// can copy one command, and a placeholder is a second thing to look up.
// The run's id spells the epic (`epic-<id>`), so the command reads the
// epic out of it.
func TestWatchHoldAlertNamesTheEpicNotAPlaceholder(t *testing.T) {
	repo := t.TempDir()
	attempt := 2
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC), "epic-2jn", "t1", &attempt, "dispatched", "t1 try 1 dispatched"))
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC), "epic-2jn", "t1", &attempt, reconcile.StageRunHeld,
		"attempt_struck_out: the report names no status"))
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "epic-2jn", "", nil, "run_finished",
		"failed: t1 did not pass"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-2jn"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ticfac settle 2jn t1 2 --release") {
		t.Errorf("the alert does not name the settle command addressed by the epic id: %q", stderr.String())
	}
	// A run under the epic spelling is the one run the bare command already
	// addresses: its alert spells no --run-id (tick qxj).
	if strings.Contains(stderr.String(), "--run-id") {
		t.Errorf("the alert adds --run-id to a command that already defaults to this run's store: %q",
			stderr.String())
	}
	if strings.Contains(stderr.String(), "<epic-id>") {
		t.Errorf("the alert still prints a placeholder instead of the epic id: %q", stderr.String())
	}
}

// writeRunCheckpoint writes one run's durable checkpoint record into a
// checkout's working tree — the directory `statusRecords` reads when the
// run id spells no epic hint — carrying an epic id the run id itself does
// not spell. It is how a run started as `ticfac run <epic> --run-id <other>`
// keeps its epic findable: the checkpoint names it, not the id.
func writeRunCheckpoint(t *testing.T, repo, runID, epicID string) {
	t.Helper()
	dir := filepath.Join(repo, runstate.Root, "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(runstate.Checkpoint{
		SchemaVersion: runstate.SchemaVersion,
		RunID:         runID,
		EpicID:        epicID,
		Sequence:      1,
		State:         runstate.StateRunning,
		Reason:        "t1 is dispatched",
		UpdatedAt:     time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC).UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		t.Fatalf("marshal the checkpoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checkpoint.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The stream path's hold alert reads the run's CHECKPOINT for the epic a
// non-epic-shaped local run id hides (tick fub): a run started as
// `ticfac run <epic> --run-id run-p` spells its epic nowhere in its id, and
// an alert that guesses the id spells `ticfac settle run-p …` — a command
// that refuses (no branch, no epic run-p) while the model path, reading the
// same checkpoint, spells the real epic. Two renderers naming two epics is
// exactly what the one-model rule (hn6 rule 8) forbids.
func TestWatchHoldAlertReadsTheCheckpointForANonEpicRunID(t *testing.T) {
	repo := t.TempDir()
	writeRunCheckpoint(t, repo, "run-p", "pip")
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	attempt := 2
	writeFeedEvent(t, repo, "run-p", runfeed.NewEvent(
		at, "run-p", "t1", &attempt, "dispatched", "t1 try 1 dispatched"))
	writeFeedEvent(t, repo, "run-p", runfeed.NewEvent(
		at.Add(time.Second), "run-p", "t1", &attempt, reconcile.StageRunHeld,
		"attempt_unaddressed: nobody can say whether the attempt is running"))
	writeFeedEvent(t, repo, "run-p", runfeed.NewEvent(
		at.Add(2*time.Second), "run-p", "", nil, reconcile.StageRunFinished,
		"failed: t1 did not pass"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "run-p"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	// The release command is addressed by the CHECKPOINT's epic — the one
	// the model path spells — with the run's own store beside it.
	want := statusmodel.SettleCommandForCurrentRun("pip", "t1", 2, "run-p")
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("the alert does not name the checkpoint's epic %q: %q", want, stderr.String())
	}
	guessed := statusmodel.SettleCommandForCurrentRun("run-p", "t1", 2, "run-p")
	if strings.Contains(stderr.String(), guessed) {
		t.Errorf("the alert still guesses the epic from the run id, spelling the command that refuses: %q",
			stderr.String())
	}
}

// The same read serves the whole stream path's clearing commands: the
// untriaged-findings hold's triage and a failed end's resume are addressed
// by the checkpoint's epic too, or the one fix leaves two of its three
// commands guessing (tick fub).
func TestWatchStreamReadsTheCheckpointForEveryCommandANonEpicRunIDNeeds(t *testing.T) {
	for _, why := range []struct {
		hold   bool
		detail string
	}{
		{true, "finding_untriaged: 1 finding(s) this run drafted are still waiting for a person"},
		{false, "failed: t1 did not pass: the integrated gate refused the work"},
	} {
		repo := t.TempDir()
		writeRunCheckpoint(t, repo, "run-p", "pip")
		at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
		if why.hold {
			writeFeedEvent(t, repo, "run-p", runfeed.NewEvent(
				at, "run-p", "rrl", nil, reconcile.StageRunHeld, why.detail))
		}
		writeFeedEvent(t, repo, "run-p", runfeed.NewEvent(
			at.Add(time.Second), "run-p", "", nil, reconcile.StageRunFinished, why.detail))

		var stdout, stderr syncBuffer
		code := Run([]string{"watch", "--repo", repo, "run-p"}, &stdout, &stderr)
		want := ExitHeld
		if !why.hold {
			want = exitGeneric
		}
		if code != want {
			t.Fatalf("%s: exit code %d, want %d; stderr %q", why.detail, code, want, stderr.String())
		}
		var command string
		if why.hold {
			command = statusmodel.TriageCommandForCurrentRun("pip", "run-p")
		} else {
			command = statusmodel.ResumeCommand(statusmodel.HostLocal, "pip")
		}
		if !strings.Contains(stderr.String(), command) {
			t.Errorf("%s: the command is not addressed by the checkpoint's epic %q: %q",
				why.detail, command, stderr.String())
		}
		guessed := "run-p"
		if why.hold {
			guessed = statusmodel.TriageCommandForCurrentRun("run-p", "run-p")
		} else {
			guessed = statusmodel.ResumeCommand(statusmodel.HostLocal, "run-p")
		}
		if strings.Contains(stderr.String(), guessed) {
			t.Errorf("%s: the command is still addressed by the guessed id %q: %q",
				why.detail, guessed, stderr.String())
		}
	}
}

// The close-out's untriaged-findings hold is cleared by triage, so the
// alert names `ticfac triage <epic>` — never `settle`, which releases an
// attempt and would refuse this one.
func TestWatchHoldAlertNamesTriageForAFindingHold(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC), "epic-2jn", "rrl", nil, reconcile.StageRunHeld,
		"finding_untriaged: 1 finding(s) this run drafted are still waiting for a person"))
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "epic-2jn", "", nil, "run_finished",
		"holding: the close-out waits on untriaged findings"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-2jn"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ticfac triage 2jn") {
		t.Errorf("the finding hold's alert does not name the triage command: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "ticfac settle") {
		t.Errorf("the finding hold's alert names settle, a command that releases an attempt and refuses this hold: %q",
			stderr.String())
	}
}

// The alert addresses the run whose store carries the drafts (tick q8m):
// a cloud run is named by the factory's run_<hex>, and its untriaged
// findings live in ITS store — the bare command's default (epic-<epic-id>,
// the local id) names a store a cloud run never wrote, so a person
// following it finds nothing while the hold stands.
func TestWatchHoldAlertForACloudRunNamesTheRunItsStoreLivesAt(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	cloudRun := cloudRunIDOf("e777")

	at := time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC)
	feed := feedLine(t, runfeed.NewEvent(at, cloudRun, "rrl", nil, reconcile.StageRunHeld,
		"finding_untriaged: 1 finding(s) this run drafted are still waiting for a person"))
	feed += feedLine(t, runfeed.NewEvent(at.Add(time.Second), cloudRun, "", nil, reconcile.StageRunFinished,
		"holding: the close-out waits on untriaged findings"))
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "completed",
			}}}
		case "/api/runs/" + cloudRun:
			return 200, map[string]any{"run": map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "completed",
			}}
		case "/api/runs/" + cloudRun + "/events":
			return 200, map[string]any{
				"run_id": cloudRun, "state": "completed",
				"text": feed, "bytes": len(feed), "total_bytes": len(feed),
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, cloudRun}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ticfac triage epic1 --run-id "+cloudRun) {
		t.Errorf("the cloud finding hold's alert does not name the triage addressed to the run's own store: %q",
			stderr.String())
	}
}

// The settle branch of the same alert gets its own test against a cloud run
// id (tick v2f, the finding of q8m): an attempt hold's release is addressed
// by the run whose store carries the attempt — the factory's run_<hex> —
// spelled as the flag, because the bare command's default (epic-<epic-id>)
// opens a store a cloud run never wrote, and the attempt it names is not
// there, so the command a person copies refuses while the hold stands. The
// epic id comes off the factory's record, never a placeholder.
func TestWatchHoldAlertForACloudAttemptNamesTheRunItsStoreLivesAt(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	cloudRun := cloudRunIDOf("e778")

	at := time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC)
	attempt := 2
	feed := feedLine(t, runfeed.NewEvent(at, cloudRun, "rrl", &attempt, "dispatched",
		"attempt 2 started as run-x/tick-rrl/attempt-2"))
	feed += feedLine(t, runfeed.NewEvent(at.Add(time.Second), cloudRun, "rrl", &attempt, reconcile.StageRunHeld,
		"attempt_unaddressed: nobody can say whether the attempt is running"))
	feed += feedLine(t, runfeed.NewEvent(at.Add(2*time.Second), cloudRun, "", nil, reconcile.StageRunFinished,
		"failed: rrl did not pass"))
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "completed",
			}}}
		case "/api/runs/" + cloudRun:
			return 200, map[string]any{"run": map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "completed",
			}}
		case "/api/runs/" + cloudRun + "/events":
			return 200, map[string]any{
				"run_id": cloudRun, "state": "completed",
				"text": feed, "bytes": len(feed), "total_bytes": len(feed),
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, cloudRun}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	// The release command carries the factory's own run id: the attempt lives
	// in ITS store, not in the one the bare spelling defaults to.
	release := fmt.Sprintf("ticfac settle epic1 rrl 2 --run-id %s --release", cloudRun)
	if !strings.Contains(stderr.String(), release) {
		t.Errorf("the cloud attempt hold's alert does not name the release addressed to the run's own store: %q",
			stderr.String())
	}
	// The bare spelling is the refusal the alert exists to avoid: without the
	// flag the command defaults to the epic's local store, and there is no
	// such attempt in it.
	if strings.Contains(stderr.String(), "ticfac settle epic1 rrl 2 --release") {
		t.Errorf("the cloud attempt hold's alert names the bare settle, a command addressed to a store the "+
			"attempt was never recorded under: %q", stderr.String())
	}
	// The sentence still leads with the tick's try and says why it is held.
	if !strings.Contains(stderr.String(), "rrl try 1 (run dispatch #2)") ||
		!strings.Contains(stderr.String(), "attempt_unaddressed") {
		t.Errorf("the alert does not name the held attempt and why: %q", stderr.String())
	}
}

// The holds that fire before the tick's first dispatch — the width, a
// foreign claim, the absorption bound — carry a NULL attempt, and the
// alert's settle branch used to fall to its inline format over that dash
// and name `ticfac settle <epic> <tick> - --release "<who>"`, a command
// the settle CLI refuses ("-" is not an attempt number). Worse than the
// refusal, it was the wrong verb: these holds are facts about the world and
// the bound, not attempts a person releases — a foreign claim clears when
// the holder's tick closes, the width when a slot frees, the bound when a
// person judges the chain. Each names the command that actually moves it
// on (tick gf0), spelled by the one shared decision the model's needs-you
// reads too — never a settle addressed by a dash.
func TestWatchHoldAlertForAWidthHoldNamesTheRunAgainCommand(t *testing.T) {
	repo := t.TempDir()
	// The finding's own repro: tick w9b, null attempt.
	writeFeedEvent(t, repo, "epic-wne", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC), "epic-wne", "w9b", nil, reconcile.StageRunHeld,
		"claim_width: the width wne declares is already full of claims this run does not hold: the graph counts 3 "+
			"in dispatch.in_flight_ids (0ju, mrn, keh)"))
	writeFeedEvent(t, repo, "epic-wne", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "epic-wne", "", nil, reconcile.StageRunFinished,
		"failed: w9b did not pass"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-wne"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	// The width hold clears when a slot frees and the run again re-derives —
	// the one command the alert names, addressed by the epic.
	if !strings.Contains(stderr.String(), "ticfac run-epic wne") {
		t.Errorf("the width hold's alert does not name the run-again command: %q", stderr.String())
	}
	// And never a settle: no attempt stands behind the hold, so the command
	// the old inline fallback spelled would refuse — and a release would not
	// clear the hold anyway.
	if strings.Contains(stderr.String(), "ticfac settle") {
		t.Errorf("the width hold's alert names a settle command for a hold no attempt stands behind: %q",
			stderr.String())
	}
	// The hold's own reason and the tick are read off the line's fields.
	if !strings.Contains(stderr.String(), "claim_width") || !strings.Contains(stderr.String(), "tick w9b") {
		t.Errorf("the alert does not name the hold's reason and tick: %q", stderr.String())
	}
}

// The foreign-claim hold is the width's twin with its own clearing sentence:
// the claim ends when the HOLDER's tick closes (or the holder's run stops),
// and the run again re-derives and proceeds the moment it does — never a
// release, which is what makes it a hold at all.
func TestWatchHoldAlertForAForeignClaimHoldNamesTheRunAgainCommand(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "epic-wne", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC), "epic-wne", "w9b", nil, reconcile.StageRunHeld,
		"foreign_claim: w9b is claimed by run run-epic-xte, whose records on the integration branch do not "+
			"read finished, and which is alive"))
	writeFeedEvent(t, repo, "epic-wne", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "epic-wne", "", nil, reconcile.StageRunFinished,
		"failed: w9b did not pass"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-wne"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ticfac run-epic wne") {
		t.Errorf("the foreign-claim hold's alert does not name the run-again command: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "ticfac settle") {
		t.Errorf("the foreign-claim hold's alert names a settle command for a hold no attempt stands behind: %q",
			stderr.String())
	}
}

// The absorption bound's hold asks a person to judge the CHAIN the line
// carries: the finding the bound refused to absorb is still theirs to
// decide — so the alert names the triage, the one command that decides it —
// and says the raise the line's own message names as the other road.
func TestWatchHoldAlertForAnAbsorptionBoundHoldNamesTheFindingsDecision(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC), "epic-2jn", "v2f", nil, reconcile.StageRunHeld,
		"absorption_depth_exceeded: absorbing the finding \"fb1910\" would be the 4th absorption of ONE chain "+
			"that already carries 3 and the bound is 3 (tick qjj). Raise the bound with --absorption-depth and "+
			"run the epic again instead"))
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "epic-2jn", "", nil, reconcile.StageRunFinished,
		"failed: v2f did not pass"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-2jn"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ticfac triage 2jn") {
		t.Errorf("the absorption bound's alert does not name the command that decides the finding: %q",
			stderr.String())
	}
	if strings.Contains(stderr.String(), "ticfac settle") {
		t.Errorf("the absorption bound's alert names a settle command for a hold no attempt stands behind: %q",
			stderr.String())
	}
	// The raise is the other road, and the alert says it — the flag the
	// refusal's own escape hatch names.
	if !strings.Contains(stderr.String(), "--absorption-depth") {
		t.Errorf("the absorption bound's alert does not name the raise the line itself offers: %q", stderr.String())
	}
}

// The final-review hold (tick quz) is the one hold that fires AFTER an
// attempt was dispatched — the close-out's — so its line CARRIES an attempt
// and the alert's default branch used to name the settle that attempt
// addresses. Releasing it clears nothing: the hold is the review's NOT READY
// verdict recorded on the PR, which the next resume re-reads and holds on
// again. The moves are the refusal's own — fix what the review names and run
// the epic again, merge the PR by hand to accept it (a re-run then finds it
// merged), or close it — so the alert names the run again, never a settle.
func TestWatchHoldAlertForAFinalReviewHoldNamesTheRunAgainCommand(t *testing.T) {
	repo := t.TempDir()
	// The hold's own shape: the close-out tick, its attempt carried on the
	// line — the number a settle would happily take and the release of which
	// answers nothing.
	attempt := 2
	writeFeedEvent(t, repo, "epic-qeu", runfeed.NewEvent(
		time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), "epic-qeu", "co7", &attempt, reconcile.StageRunHeld,
		"land_review_not_ready: the run does not merge the epic qeu: its final review (decision 3) still judges it "+
			"NOT READY after 2 review round(s), the bound being 2. What the review says would make it ready: the Phase 4 "+
			"gate still never ran. The verdict is on the epic PR, and accepting work the run's own review rejected is a "+
			"person's judgement: fix what it names and run the epic again, merge the PR by hand to accept it (a re-run "+
			"then finds it merged), or close it"))
	writeFeedEvent(t, repo, "epic-qeu", runfeed.NewEvent(
		time.Date(2026, 10, 4, 12, 0, 1, 0, time.UTC), "epic-qeu", "", nil, reconcile.StageRunFinished,
		"failed: the epic PR is not ready"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-qeu"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	// The run again is the one command the alert names: a resume is the move
	// after every one of the refusal's own roads — the fix, the hand-merge
	// (a re-run then finds it merged) and the close all end at running the
	// epic again or never needing to.
	if !strings.Contains(stderr.String(), "ticfac run-epic qeu") {
		t.Errorf("the final-review hold's alert does not name the run-again command: %q", stderr.String())
	}
	// And never a settle: the attempt the line carries is the close-out's,
	// and releasing it clears nothing — the next resume re-reads the verdict
	// off the PR and holds again on it.
	if strings.Contains(stderr.String(), "ticfac settle") {
		t.Errorf("the final-review hold's alert names a settle for an attempt whose release clears nothing: %q",
			stderr.String())
	}
	// The hold's own reason and the attempt it happens to carry are read off
	// the line's fields.
	if !strings.Contains(stderr.String(), "land_review_not_ready") ||
		!strings.Contains(stderr.String(), "co7") {
		t.Errorf("the alert does not name the hold's reason and tick: %q", stderr.String())
	}
}

// A hold the closed set does not know — no reason it recognises, no attempt
// behind it — answers with no command rather than a wrong one: the dash
// settle the inline fallback used to spell is exactly a command a person
// copies and the CLI refuses. The alert still says the hold and why.
func TestWatchHoldAlertForAnUnrecognisedHoldWithNoAttemptNamesNoCommand(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC), "epic-2jn", "t1", nil, reconcile.StageRunHeld,
		"some_future_hold: a hold kind the alert's decision does not know"))
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "epic-2jn", "", nil, reconcile.StageRunFinished,
		"failed: t1 did not pass"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-2jn"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	if strings.Contains(stderr.String(), "ticfac settle") || strings.Contains(stderr.String(), "ticfac triage") {
		t.Errorf("the unrecognised hold's alert names a command the decision does not carry: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "some_future_hold") {
		t.Errorf("the alert does not say the hold's own reason: %q", stderr.String())
	}
}

// The '<tick>#<n>' prefix is the tick's own TRY (tick h58), not the run-wide
// dispatch number the line's `attempt` field carries. The operator's run: 0ju
// was dispatch 1, mrn 2, and w9b 3, 4 and 5 — so dispatch 5 is w9b#3, and a
// prefix that said w9b#5 read as w9b's fifth try.
func TestWatchPrefixShowsTheTicksTry(t *testing.T) {
	repo := t.TempDir()
	at := time.Date(2026, 9, 22, 17, 0, 0, 0, time.UTC)
	for i, tick := range []string{"0ju", "mrn", "w9b", "w9b", "w9b"} {
		n := i + 1
		writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(at.Add(time.Duration(i)*time.Minute), "r-1", tick, &n,
			"dispatched", fmt.Sprintf("dispatch %d", n)))
	}
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(at.Add(time.Hour), "r-1", "", nil, "run_finished", "completed"))

	var stdout, stderr syncBuffer
	if code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d; stderr %q", code, stderr.String())
	}
	for _, want := range []string{"0ju#1 ", "mrn#1 ", "w9b#1 ", "w9b#2 ", "w9b#3 "} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the watch never printed the prefix %q: %q", want, stdout.String())
		}
	}
	for _, unwanted := range []string{"w9b#4", "w9b#5", "mrn#2"} {
		if strings.Contains(stdout.String(), unwanted) {
			t.Errorf("the watch prefixed a line with the run dispatch number %q: %q", unwanted, stdout.String())
		}
	}
}

func TestWatchReportsARunThatEndedOnItsOwn(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), "r-1", "", nil, "budget_set", "the effective budget is $8.00"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC), "r-1", "", nil, "run_finished", "completed: every tick closed"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d for a run that ended without holding anything; stderr %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a run that held nothing raised the hold alert: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the terminal line never printed: %q", stdout.String())
	}
}

func TestWatchNeedsExactlyOneRunID(t *testing.T) {
	for _, args := range [][]string{{"watch"}, {"watch", "a", "b"}, {"watch", ""}} {
		var stdout, stderr syncBuffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
	}
}

func TestWatchNamesARunThatNeverRanHere(t *testing.T) {
	// The run's directory stands — a claim made it and released it — but
	// no line was ever written and nothing claims the run now. An id with
	// no directory at all is a different answer (runid_test.go).
	repo := t.TempDir()
	if err := os.MkdirAll(runlife.Dir(repo, "r-none"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-none"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("a run with no feed and no live claim was watched without complaint")
	}
	if !strings.Contains(stderr.String(), "no feed for run r-none") {
		t.Errorf("stderr %q does not say what a missing feed means", stderr.String())
	}
}

func TestWatchFollowsUntilTheRunEnds(t *testing.T) {
	repo := t.TempDir()
	attempt := 2
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, "dispatched", "attempt 2 started"))

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	}()

	// The watch prints the line that stands, then follows. No interval is
	// named anywhere: the lines arrive because the subscription is open, and
	// this waits on the CONDITION (the printed line), never on a guess.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), "dispatched") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), "dispatched") {
		t.Fatalf("the standing line never printed: %q", stdout.String())
	}
	// The terminal line ends the watch on its own: a watcher must not outlive
	// the run it watches.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, reconcile.StageRunHeld, "attempt_unaddressed: nobody can say whether it is running"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "failed: a1 did not pass"))

	var got int
	select {
	case got = <-code:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	if got != ExitHeld {
		t.Fatalf("exit code %d, want %d", got, ExitHeld)
	}
	if !strings.Contains(stderr.String(), "HOLDING a1 try 1 (run dispatch #2)") {
		t.Errorf("the alert does not name the held tick: %q", stderr.String())
	}
}

// The resumed-run case (ticfac tick usx): the feed is append-only per RUN
// ID, so a resumed run appends to a file a previous, failed incarnation of
// the same run id already ended — and the previous incarnation ended
// HOLDING, which is the worst case: a watch that replays the standing feed
// from offset zero reads that run_finished FIRST, exits at once and raises
// the hold alert for a hold the release already settled. That is the same
// defect `events --follow` carried (ticfac tick 55i), in the command built
// to be alerted by it. The resumed run is LIVE — it claims the run — so the
// watch joins the CURRENT incarnation: the previous ending is history, and
// only the current incarnation's own terminal line ends the watch.
func TestWatchOnAResumedRunDoesNotExitOnThePreviousIncarnationsTerminalLine(t *testing.T) {
	repo := t.TempDir()
	attempt := 1
	// The previous incarnation: it held tick a1 for a person, and said so
	// on its way to failing — the exact line the old watch replayed as if it
	// were happening now.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, "dispatched", "attempt 1 started as run-x/tick-a1/attempt-1"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, reconcile.StageRunHeld,
		"attempt_unaddressed: nobody can say whether the attempt is running"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "failed: a1 did not pass"))

	// The resumed run is in flight: this process claims it, the way the
	// real second incarnation's own process does at startup.
	life, err := runlife.Claim(repo, "r-1")
	if err != nil {
		t.Fatalf("claim the resumed run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	}()

	// The subscription is live, and it joined the current incarnation: the
	// resumed run's first line reaches the watch, and the previous
	// incarnation's ending does not end it first. This waits on the CONDITION
	// (the watch printing the resumed run's own line), never on a guess about
	// time — on the old code the watch has already returned and this never
	// comes true.
	syncWatchMarker(t, repo, &stdout)
	select {
	case got := <-code:
		t.Fatalf("the watch returned %d before the resumed run said anything — it exited on the previous incarnation's ending; stderr %q", got, stderr.String())
	default:
	}

	// The resumed run ends its own way, and the watch ends on THAT line.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "completed: every tick closed behind the gate"))
	var got int
	select {
	case got = <-code:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never returned after the resumed run ended")
	}
	if got != 0 {
		t.Fatalf("exit code %d, want 0 for a resumed run that ended clean; stderr %q", got, stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("the watch raised the hold alert for the hold the previous incarnation already had settled: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "failed: a1 did not pass") {
		t.Errorf("the watch replayed the previous incarnation's terminal line: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "every tick closed behind the gate") {
		t.Errorf("the current incarnation's own terminal line never printed: %q", stdout.String())
	}
}

// The decided run_held semantics (ticfac tick usx): a watch started while the
// run is ALREADY holding an attempt reports the hold it joined — the line
// landed before the watch did, so starting from now would miss the one line
// the command exists for. The run is live here, so this is the joined hold of
// the CURRENT incarnation, not the replayed hold of a previous one.
func TestWatchStartedWhileTheRunHoldsReportsTheHoldItJoined(t *testing.T) {
	repo := t.TempDir()
	first := 1
	// The previous incarnation failed cleanly — no hold of its own — so
	// anything the watch says about a hold can only come from the current
	// incarnation.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &first, "dispatched", "attempt 1 started as run-x/tick-a1/attempt-1"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "failed: attempt 1 of a1 is missing-result"))

	// The current incarnation: live, and already holding before the watch
	// starts.
	second := 2
	life, err := runlife.Claim(repo, "r-1")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a2", &second, "dispatched", "attempt 2 started as run-x/tick-a2/attempt-2"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a2", &second, reconcile.StageRunHeld,
		"attempt_unaddressed: nobody can say whether the attempt is running"))

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	}()

	// The hold the watch joined is reported — on stderr, where a person
	// reads it, without waiting for the run to end first: the alert is the
	// whole point, and it must not wait for the terminal line to say it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), "HOLDING a2 try 1 (run dispatch #2)") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "HOLDING a2 try 1 (run dispatch #2)") {
		t.Fatalf("the watch never reported the hold it joined: stderr %q stdout %q", stderr.String(), stdout.String())
	}
	if !strings.Contains(stderr.String(), "settle r-1 a2 2 ") || !strings.Contains(stderr.String(), "attempt_unaddressed") {
		t.Errorf("the alert does not name the held attempt and why: %q", stderr.String())
	}

	// And the run's own last word still ends the watch, holding.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "failed: a2 did not pass"))
	var got int
	select {
	case got = <-code:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	if got != ExitHeld {
		t.Fatalf("exit code %d, want %d for a run that ended holding an attempt; stderr %q", got, ExitHeld, stderr.String())
	}
	if !strings.Contains(stdout.String(), reconcile.StageRunHeld) {
		t.Errorf("the held line never printed on stdout: %q", stdout.String())
	}
	// The hold reported is the CURRENT incarnation's, from the current
	// incarnation's lines: the previous incarnation's ending is not replayed
	// into the stream the watch is now following (on the old code this line
	// is what fails — the joined hold read as a replayed history).
	if strings.Contains(stdout.String(), "failed: attempt 1 of a1 is missing-result") {
		t.Errorf("the watch replayed the previous incarnation's terminal line: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "attempt 1 started as run-x/tick-a1/attempt-1") {
		t.Errorf("the watch replayed the previous incarnation's dispatch line: %q", stdout.String())
	}
}

// syncWatchMarker proves the watch's subscription is live and joined the
// current incarnation BEFORE anything is asserted about what it did not
// replay: the resumed run writes a marker, the watch prints it, and only a
// watch that did not exit on the previous incarnation's ending can. It
// waits on that condition, never on a guessed interval.
func syncWatchMarker(t *testing.T, repo string, stdout *syncBuffer) {
	t.Helper()
	for i := 0; i < 40; i++ {
		writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
			time.Now(), "r-1", "a2", nil, "resumed", fmt.Sprintf("subscription sync marker %d", i)))
		deadline := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(deadline) {
			if strings.Contains(stdout.String(), "subscription sync marker") {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatalf("the watch never showed a sync marker; it did not join the resumed run's feed: %q", stdout.String())
}

// The exit table's failed class (tick bot, epic 2jn's A4): a run whose own
// terminal line says it FAILED — the integrated gate refused the work, a
// worker answered BLOCKED, the run stopped rather than integrating over an
// unproven change — ended the watch with exit 0, indistinguishable from a
// run that closed every tick behind the gate. The failure is in the
// terminal line's own LEADING state word (the runstate word the reconciler
// checkpointed: "failed: nkf did not pass"), so the watch classifies on
// that word — never on the absence of a run_held, which says only that
// nothing waits for a person's release, not that the run succeeded.
func TestWatchExitsFailedWhenTheRunEndedFailed(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 40, 0, 0, time.UTC), "r-1", "nkf", nil,
		reconcile.StageGateFailed, "the integrated gate refused the work: go test failed"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 4, 0, time.UTC), "r-1", "", nil, reconcile.StageRunFinished,
		"failed: nkf did not pass: the integrated gate refused the work"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit code %d, want %d (the failed class) for a run whose own last line says failed; stderr %q", code, exitGeneric, stderr.String())
	}
	// The failed end is said to the person reading, and it is NOT a hold:
	// nothing waits for a release, the work has to be fixed and the epic
	// run again.
	if !strings.Contains(stderr.String(), "ended FAILED") {
		t.Errorf("the failed end is not said to the person reading the stream: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a run that failed holding nothing raised the hold alert: %q", stderr.String())
	}
	// The resume it names is a command a person can paste (tick gtk): the
	// one statusmodel spells for the run's host, addressed by a real id —
	// never a `<epic-id>` placeholder.
	if strings.Contains(stderr.String(), "<epic-id>") {
		t.Errorf("the failed end names a placeholder, not a command: %q", stderr.String())
	}
	if want := statusmodel.ResumeCommand(statusmodel.HostLocal, "r-1"); !strings.Contains(stderr.String(), want) {
		t.Errorf("the failed end does not name the resume %q: %q", want, stderr.String())
	}
	// The terminal line still prints — the last line says why, and the exit
	// code says which class of ending it was.
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the terminal line never printed: %q", stdout.String())
	}
}

// A run that DIED without its own run_finished — the process erred, panicked
// or was signalled — is the same failed class on the same seam: run_died
// exists precisely so a death never reads as an ordinary success, and the
// watch must not read it as done either (tick bot).
func TestWatchExitsFailedWhenTheRunDied(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 4, 0, time.UTC), "r-1", "", nil, reconcile.StageRunDied,
		"run-epic: the reconciler returned an operational error"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit code %d, want %d (the failed class) for a run that died without its own run_finished; stderr %q", code, exitGeneric, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ended FAILED") {
		t.Errorf("the death is not said to the person reading the stream: %q", stderr.String())
	}
}

// TestWatchByEpicIDFollowsTheCloudRunTheEpicHasInTheFactory: `ticfac watch
// <epic-id>` follows the run the factory holds for this checkout's project
// when nothing runs here (tick nyi) — the same one command the operator
// uses for a local run, because the run's id alone should not decide which
// host answers. Before the fix the epic id never looked in the factory and
// the watch refused with "no feed".
func TestWatchByEpicIDFollowsTheCloudRunTheEpicHasInTheFactory(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	cloudRun := cloudRunIDOf("e999")

	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	attempt := 1
	feed := feedLine(t, runfeed.NewEvent(at, cloudRun, "t1", &attempt, "dispatched", "t1 try 1 dispatched"))
	feed += feedLine(t, runfeed.NewEvent(at.Add(time.Minute), cloudRun, "", nil,
		reconcile.StageRunFinished, "completed: every tick of epic1 is closed behind the integrated gate"))
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Path == "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "completed",
			}}}
		case request.Path == "/api/runs/"+cloudRun:
			return 200, map[string]any{"run": map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "completed",
			}}
		case request.Path == "/api/runs/"+cloudRun+"/events":
			return 200, map[string]any{
				"run_id": cloudRun, "state": "completed",
				"text": feed, "bytes": len(feed), "total_bytes": len(feed),
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-epic1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d watching the factory's run for epic1, want 0:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the stream never printed the run's own terminal word:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), cloudRun) {
		t.Errorf("stderr does not name the resolution from epic id to the factory's run:\n%s", stderr.String())
	}
}

// A watch that reached the factory's run by EPIC id (tick nyi) names the
// clearing commands by that epic, never by the factory's `run_` plus hex id
// it resolved to (tick gtk): the resolved run's source carries the epic the
// operator typed, so the failed end's resume is one a person can paste.
func TestWatchByEpicIDNamesTheResumeByTheEpicNotTheFactoryRunID(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	cloudRun := cloudRunIDOf("e998")

	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	feed := feedLine(t, runfeed.NewEvent(at, cloudRun, "", nil,
		reconcile.StageRunFinished, "failed: t1 did not pass: the integrated gate refused the work"))
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Path == "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "failed",
			}}}
		case request.Path == "/api/runs/"+cloudRun:
			return 200, map[string]any{"run": map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "failed",
			}}
		case request.Path == "/api/runs/"+cloudRun+"/events":
			return 200, map[string]any{
				"run_id": cloudRun, "state": "failed",
				"text": feed, "bytes": len(feed), "total_bytes": len(feed),
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-epic1"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit %d for the factory's failed run, want %d:\n%s\n%s", code, exitGeneric, stdout.String(), stderr.String())
	}
	if want := statusmodel.ResumeCommand(statusmodel.HostCloud, "epic1"); !strings.Contains(stderr.String(), want) {
		t.Errorf("the failed end does not name the resume %q by the epic:\n%s", want, stderr.String())
	}
	if bad := statusmodel.ResumeCommand(statusmodel.HostCloud, cloudRun); strings.Contains(stderr.String(), bad) {
		t.Errorf("the failed end names the resume by the factory's run id %q:\n%s", bad, stderr.String())
	}
}

// A run whose own terminal line says CANCELLED (tick rix): runstate's
// cancelled word was vocabulary only, but the resume path's already-terminal
// branch writes run_finished for it and the failed classifier deliberately
// classifies only the failed word — so a cancelled end read as done/0 from
// the watch, indistinguishable from a run that closed every tick behind the
// gate. Cancelled is its own class now, with its own code: the run was
// stopped deliberately, the work is neither done nor failed, and nothing is
// held for a person.
func TestWatchExitsCancelledWhenTheRunEndedCancelled(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 40, 0, 0, time.UTC), "r-1", "a1", nil, "dispatched", "attempt 1 started"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "r-1", "", nil, reconcile.StageRunFinished,
		"cancelled: the operator stopped the run: stop requested"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != exitCancelled {
		t.Fatalf("exit code %d, want %d (the cancelled class) for a run whose own last line says cancelled; stderr %q", code, exitCancelled, stderr.String())
	}
	// The cancelled end is said to the person reading the stream — and it is
	// neither a failure to fix nor a hold to release.
	if !strings.Contains(stderr.String(), "ended CANCELLED") {
		t.Errorf("the cancelled end is not said to the person reading the stream: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "FAILED") {
		t.Errorf("a cancelled run was spoken of as a failure to fix: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a cancelled run raised the hold alert: %q", stderr.String())
	}
	// The terminal line still prints — the last line says why the run was
	// stopped, and the exit code says which class of ending it was.
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the terminal line never printed: %q", stdout.String())
	}
}

// Every word the run's own terminal line can spell a deliberate stop with
// is the cancelled class — or the decided answer is a word nothing
// recognizes. The resume path's already-terminal branch replays a cancelled
// checkpoint as "the run is already cancelled: …" (not the state-led word),
// and the cloud's own vocabulary spells its deliberate stop "stopped" —
// the word the overview already classifies one run's row by.
func TestWatchClassifiesEveryCancelledWordTheRunWrites(t *testing.T) {
	for detail, why := range map[string]string{
		"cancelled: the operator stopped the run":          "the state-led word a cancelling reconciler writes",
		"the run is already cancelled: operator cancelled": "the resume path's already-terminal replay",
		"stopped: stop requested by the operator":          "the cloud factory's own word for a deliberate stop",
	} {
		repo := t.TempDir()
		writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
			time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "r-1", "", nil, reconcile.StageRunFinished, detail))
		var stdout, stderr syncBuffer
		code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
		if code != exitCancelled {
			t.Errorf("%s: exit code %d, want %d; stderr %q", why, code, exitCancelled, stderr.String())
		}
	}
	// The control the classifier must not swallow: the resume path's
	// already-terminal replay of a COMPLETED run is the done class, and so
	// is the ordinary state-led "completed" word — an over-eager classifier
	// would make every ended run cancelled.
	for _, detail := range []string{
		"completed: every tick closed behind the gate",
		"the run is already completed: every tick closed behind the gate",
	} {
		repo := t.TempDir()
		writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
			time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "r-1", "", nil, reconcile.StageRunFinished, detail))
		var stdout, stderr syncBuffer
		code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
		if code != exitSuccess {
			t.Errorf("a completed run's ending %q exited %d, want %d (done)", detail, code, exitSuccess)
		}
	}
}

// A person's SIGINT stop of a foreground run-epic (tick vqc): the signal
// handler writes run_died for it, and before this tick the watch classified
// every run_died as the failed class — "the work has to be fixed and the
// epic run again" — for a stop a person chose to make. A deliberate stop is
// the cancelled class's own case, the same distinction rix made for the
// run_finished cancelled words: the handler leads the death line with the
// cancelled state word, and the classifier reads the word, never the prose.
func TestWatchExitsCancelledWhenAPersonStoppedTheRun(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 27, 13, 5, 0, 0, time.UTC), "r-1", "a1", nil, "dispatched", "attempt 1 started"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 27, 13, 6, 2, 0, time.UTC), "r-1", "", nil, reconcile.StageRunDied,
		"cancelled: stopped by a signal (interrupt) before the run finished"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != exitCancelled {
		t.Fatalf("exit code %d, want %d (the cancelled class) for a run a person stopped with Ctrl-C; stderr %q",
			code, exitCancelled, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ended CANCELLED") {
		t.Errorf("the deliberate stop is not said to the person reading the stream: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "FAILED") {
		t.Errorf("a person's stop was spoken of as a failure to fix: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a person's stop raised the hold alert: %q", stderr.String())
	}
	// The terminal line still prints — the last line says how the run was
	// stopped, and the exit code says which class of ending it was.
	if !strings.Contains(stdout.String(), "run_died") {
		t.Errorf("the terminal line never printed: %q", stdout.String())
	}
}

// The control for the split (tick vqc): a SIGTERM is the platform saying
// the container is going away — an eviction, a death like a panic, not a
// person's stop — so its death line carries no state word and stays the
// failed class. Only the interrupt is the cancelled class's own case.
func TestWatchKeepsTheEvictionADeath(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 27, 13, 6, 2, 0, time.UTC), "r-1", "", nil, reconcile.StageRunDied,
		"stopped by a signal (terminated) before the run finished; evacuated: pushed the integration branch"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit code %d, want %d (the failed class) for a run the platform evicted; stderr %q",
			code, exitGeneric, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ended FAILED") {
		t.Errorf("the eviction is not said to the person reading the stream: %q", stderr.String())
	}
}

// The death line's two spellings, straight from the handler (tick vqc):
// SIGINT — a person at a terminal — is led by the cancelled state word, the
// word the classifier branches on, exactly as run_finished's details are
// state-led; SIGTERM — the platform's eviction — keeps the plain death
// sentence, because a death is what it is.
func TestSignalStopDetailLeadsOnlyThePersonStopWithTheCancelledWord(t *testing.T) {
	if got := signalStopDetail(syscall.SIGINT); !strings.HasPrefix(got, string(runstate.StateCancelled)+":") {
		t.Errorf("the SIGINT death line %q is not led by the cancelled state word", got)
	}
	if got := signalStopDetail(syscall.SIGTERM); strings.HasPrefix(got, string(runstate.StateCancelled)+":") {
		t.Errorf("the SIGTERM death line %q is led by the cancelled state word — an eviction is a death, not a stop", got)
	}
	if !strings.Contains(signalStopDetail(syscall.SIGINT), "stopped by a signal") {
		t.Errorf("the SIGINT death line lost the sentence that says what happened: %q", signalStopDetail(syscall.SIGINT))
	}
}
