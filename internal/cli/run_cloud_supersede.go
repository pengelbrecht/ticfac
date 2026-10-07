package cli

// A held cloud run's rerun supersedes it (tick kk7, epic ilz 2026-10-07).
//
// WHAT WAS WRONG. ilz's cloud run held land_review_not_ready, and the hold
// told the operator to fix what the review named and "run the epic again with
// `ticfac run ilz --cloud`". The orchestrator had halted; its Workflow had
// not ended — it was still looking at a container that had not exited — so
// the factory answered `running`, and this command ATTACHED, replaying the
// old halt. Getting past it took `ticfac cloud stop --now`, waiting out the
// stop's grace window, and running the command again.
//
// THE RULE. A live run whose own feed says it ended holding for a person —
// a run_held line its supervisor then halted on, with no continuation after
// it — is a run nothing will advance until a person acts, and the person
// acting is the one running this command. So it supersedes the parked run: a
// clean stop, a wait for the factory to say the run has ended (the stop's
// grace window is the factory's to keep), and the epic resumed as a new
// submission, which re-reads the review over the tree as it now stands. A
// live run that holds nothing is attached to exactly as before.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// cloudSupersedePoll and cloudSupersedeBound pace and bound the wait for a
// stopped held run to end before the resume is submitted. The bound covers
// the Workflow's next look plus the stop's grace window (five minutes by
// default) with room to spare. Variables, so the tests wait in milliseconds.
var (
	cloudSupersedePoll  = 10 * time.Second
	cloudSupersedeBound = 20 * time.Minute
)

// cloudHeldTailEvents is how many of a feed's last events the held question
// reads: the hold, the run's last word and the supervisor's halt are its last
// few lines, and the factory's own boot lines may follow them.
const cloudHeldTailEvents = 64

// cloudHeldIfAlive asks the held question only of a run the liveness answer
// calls alive: a run already over is resumed anyway, and its feed need not be
// read for that.
func cloudHeldIfAlive(ctx context.Context, client *cloudClient, epicID, runID string, liveness cloudLiveness) (bool, string) {
	if !liveness.Alive {
		return false, ""
	}
	return cloudRunEndedHolding(ctx, client, epicID, runID)
}

// cloudRunEndedHolding reads the end of a cloud run's feed and answers whether
// the run ended holding for a person, and the hold's reason class. A feed that
// cannot be read answers no: the run is attached to, as it always was.
func cloudRunEndedHolding(ctx context.Context, client *cloudClient, epicID, runID string) (bool, string) {
	source := &cloudFeedSource{client: client, runID: runID, epic: epicID, warn: io.Discard}
	lines, _, err := feedTail(ctx, source, cloudHeldTailEvents)
	if err != nil {
		return false, ""
	}
	return endedHoldingForAPerson(lines)
}

// endedHoldingForAPerson is the held question over a feed's lines: a run_held
// line, then the supervisor's halt, and no continuation of the run after
// either. The halt is the orchestrator's last word on the run (the Workflow
// keys on the same line, supervisionHaltOf); a hold the supervisor continued
// past is not one.
func endedHoldingForAPerson(lines []runfeed.Located) (bool, string) {
	held, halted := "", false
	for _, line := range lines {
		switch line.Stage {
		case reconcile.StageResumedAutomatically, reconcile.StageResumed:
			held, halted = "", false
		case reconcile.StageRunHeld:
			held, halted = line.Detail, false
		case reconcile.StageSupervisionHalted:
			halted = held != ""
		}
	}
	if !halted {
		return false, ""
	}
	if reason := statusmodel.HoldReason(held); reason != "" {
		return true, reason
	}
	return true, "a hold"
}

// readCloudRunStatus is one run's whole answer from the factory: the record
// and the project's lease beside it.
func readCloudRunStatus(ctx context.Context, client *cloudClient, runID string) (cloudStatusResponse, error) {
	data, err := client.request(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(runID), nil)
	if err != nil {
		return cloudStatusResponse{}, err
	}
	var response cloudStatusResponse
	err = decodeCloudJSON(data, &response)
	return response, err
}

// supersedeHeldCloudRun stops a held cloud run cleanly and waits for the
// factory to say it has ended. It answers "" when the run has ended and the
// resume may be submitted, else the note the command ends on: the run is
// stopping, and running the command again once it has ended resumes the
// epic — an interrupted wait submits nothing, so it never makes two runs.
func supersedeHeldCloudRun(ctx context.Context, client *cloudClient, epicID, runID string, prose io.Writer) string {
	path := "/api/runs/" + url.PathEscape(runID) + "/stop"
	if _, err := client.request(ctx, http.MethodPost, path, map[string]string{
		"requested_by": cloudRequestedBy(),
		"mode":         "clean",
	}); err != nil {
		return fmt.Sprintf("the held run %s could not be stopped (%v); nothing was submitted — "+
			"`ticfac run %s --cloud` asks again", runID, err, epicID)
	}
	fmt.Fprintf(prose, "cloud run %s is stopping — waiting for its Workflow to end (the stop's grace window "+
		"included) before the resume is submitted; Ctrl-C leaves it stopping and submits nothing\n", runID)
	deadline := time.Now().Add(cloudSupersedeBound)
	for {
		// Ended is the run's record past its last state AND the project's
		// dispatch lease no longer the run's: the factory writes the state
		// before it releases the lease, and a resume submitted between the
		// two is refused lease_held.
		if status, err := readCloudRunStatus(ctx, client, runID); err == nil &&
			!cloudRunLiveness(ctx, runID, status.Run.State).Alive &&
			(status.Lease == nil || status.Lease.RunID != runID) {
			fmt.Fprintf(prose, "cloud run %s has ended (%s)\n", runID, status.Run.State)
			return ""
		}
		if time.Now().After(deadline) {
			return fmt.Sprintf("the held run %s is stopping but had not ended after %s; `ticfac run %s --cloud` "+
				"resumes the epic once it has (`ticfac status %s` says when)", runID, cloudSupersedeBound, epicID,
				runID)
		}
		select {
		case <-ctx.Done():
			return fmt.Sprintf("interrupted while the held run %s was stopping; nothing was submitted — "+
				"`ticfac run %s --cloud` resumes the epic once it has ended", runID, epicID)
		case <-time.After(cloudSupersedePoll):
		}
	}
}
