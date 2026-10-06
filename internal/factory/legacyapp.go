package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// The 0.x container application, deleted from the account.
//
// Until tick dax, the factory's containers all lived in one application,
// `ticks-orchestrator` — the Sandbox SDK 0.x class on the `default` scheduling
// policy, whose image every deploy rebuilt and rolled out application-wide.
// The cutover moved every container to `ticks-factory-sandbox`
// (FactorySandbox, `durable_object`), deleted the rollout machinery the old
// policy required, and removes the old application itself here, on the first
// deploy after the last pre-cutover run has ended.
//
// The application is deleted only when it is provably unused. A running
// container of a pre-cutover run is still serving from it, and deleting the
// application under such a run is the deploy-side container death the whole
// migration exists to make impossible. So the deploy reads its own D1 first:
// a run that is live and has no substrate row (every pre-cutover run, and any
// explicit 0.x ask) holds the application open. Anything unreadable holds it
// open too — an account whose state cannot be read does not get its
// application deleted on a guess. The deploy reports exactly what held it,
// and the next deploy after the runs end finishes the deletion.

// liveLegacyRunsSQL counts the runs the 0.x application is still serving: a
// live run with no substrate row (migration 0023; a row exists exactly for a
// run on the durable_object application, and the runs states are the ones
// ACTIVE_RUN_STATES names in cloudflare/src/runs.ts).
const liveLegacyRunsSQL = `SELECT COUNT(*) AS n FROM runs r WHERE r.state IN ('starting','running','stopping') ` +
	`AND NOT EXISTS (SELECT 1 FROM run_substrate s WHERE s.run_id = r.run_id)`

// legacyRunsHolding reads the live-run count. ok is false when the answer
// cannot be read — which must hold the application open, never release it.
func legacyRunsHolding(ctx context.Context, w *wrangler, database string) (n int, ok bool) {
	out, err := w.run(ctx, "", "d1", "execute", database, "--remote", "--json", "--command", liveLegacyRunsSQL)
	if err != nil {
		return 0, false
	}
	start := strings.Index(out, "[")
	end := strings.LastIndex(out, "]")
	if start < 0 || end < start {
		return 0, false
	}
	var results []struct {
		Results []struct {
			N int `json:"n"`
		} `json:"results"`
		Success *bool `json:"success"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &results); err != nil || len(results) == 0 {
		return 0, false
	}
	for _, r := range results {
		if r.Success != nil && !*r.Success {
			return 0, false
		}
		for _, row := range r.Results {
			n += row.N
		}
	}
	return n, true
}

// deleteLegacyContainerApp deletes the 0.x container application and its
// images once no live run is on them. It never fails the deploy: every
// refusal or error is a report, and the next deploy retries — the deletion is
// a one-time cleanup, not a condition the deploy's success depends on.
func deleteLegacyContainerApp(ctx context.Context, w *wrangler, out io.Writer, opts Options, database string) {
	apps, err := w.listContainerApps(ctx)
	if err != nil {
		fmt.Fprintf(out, "the %s application: left in place (the application list could not be read: %v)\n",
			LegacyContainerAppName, err)
		return
	}
	app, ok := findContainerApp(apps, LegacyContainerAppName)
	if !ok {
		// Nothing to delete — the normal case once the cleanup has run.
		return
	}
	holders, ok := legacyRunsHolding(ctx, w, database)
	if !ok {
		fmt.Fprintf(out, "the %s application: left in place (the live runs could not be read, "+
			"so it is not deleted on a guess)\n", LegacyContainerAppName)
		return
	}
	if holders > 0 {
		fmt.Fprintf(out, "the %s application: left in place — %d live run(s) are still on it "+
			"(runs started before the cutover keep their containers). Run `ticfac factory deploy` "+
			"again once they have ended\n", LegacyContainerAppName, holders)
		return
	}
	if _, err := w.run(ctx, "", "containers", "delete", app.ID); err != nil {
		fmt.Fprintf(out, "WARNING: the %s application could not be deleted: %v\n", LegacyContainerAppName, err)
		return
	}
	fmt.Fprintf(out, "deleted the %s container application (%s): every container runs on %s, "+
		"the durable_object application\n", LegacyContainerAppName, app.ID, FactorySandboxAppName)
	// The application's images: with no application left, none is protected.
	// The 0.x repository held the bulk of the account's image storage.
	deleteRepositoryImages(ctx, w, out, opts, LegacyContainerAppName)
}
