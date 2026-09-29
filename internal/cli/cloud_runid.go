package cli

// Run id resolution, ported verbatim from ticks' cmd/tk/cmd/cloud_runid.go.
// A truncated run id is resolved against the factory's run index before any
// run-scoped read is made, so a prefix is never answered with a confident
// negative that is true of the prefix and false of the run it names (tick c5i).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// A run id is "run_" followed by a UUID with its dashes removed — 32 hex
// characters (newRunID in cloudflare/src/runs.ts). That shape is what makes
// a truncation recognisable rather than guessed at: "run_62c289d1" is hex and
// too short, so it cannot be an id and can only be the head of one.
const (
	cloudRunIDMarker = "run_"
	cloudRunIDDigits = 32
)

// cloudRunIndexLimit is MAX_RUN_LIMIT in cloudflare/src/runs.ts: the widest
// window the run index will serve. A prefix is resolved against as many runs as
// the factory will name, because the cost of a narrow window is telling an
// operator their run is unknown when it is merely older than the window.
const cloudRunIndexLimit = 200

// isCloudRunIDPrefix reports whether an argument can only be the head of a run
// id. A whole id is not a prefix of itself, and anything that is not "run_"
// plus hex is not a run id at all — both pass through untouched, so this
// changes nothing for an argument that was never truncated.
func isCloudRunIDPrefix(id string) bool {
	body, ok := strings.CutPrefix(id, cloudRunIDMarker)
	if !ok || body == "" || len(body) >= cloudRunIDDigits {
		return false
	}
	for _, digit := range body {
		switch {
		case digit >= '0' && digit <= '9':
		case digit >= 'a' && digit <= 'f':
		case digit >= 'A' && digit <= 'F':
		default:
			return false
		}
	}
	return true
}

// resolveCloudRunID turns the head of a run id into the run id itself, or
// refuses — it never lets a prefix reach a lookup that would answer it.
//
// The trap this closes (tick c5i): every run-scoped read takes a run id, and a
// truncated one is what an operator actually has to hand after a wrapped
// terminal line or a copy that stopped at a word boundary. Answered literally,
// each of those reads returns a negative that is true of the prefix and false
// of the run — "No AI Gateway calls are stamped with run run_62c289d1" reads as
// "this run produced no telemetry", which is the opposite of what the command
// exists to say. A confident negative about a prefix is always wrong, so a
// prefix is resolved against the runs the factory knows about and the
// resolution is reported, or it is refused for being a prefix.
//
// The second return value is that report: a diagnostic line for stderr, so a
// --json read stays parseable.
func resolveCloudRunID(ctx context.Context, id string) (string, string, error) {
	if !isCloudRunIDPrefix(id) {
		return id, "", nil
	}
	client, err := newCloudClient()
	if err != nil {
		return "", "", fmt.Errorf(
			"%s is the head of a run id, not a run id, and only the factory can say which run it names: %w",
			id, err)
	}
	data, err := client.request(ctx, http.MethodGet,
		fmt.Sprintf("/api/runs?limit=%d", cloudRunIndexLimit), nil)
	if err != nil {
		return "", "", fmt.Errorf(
			"%s is the head of a run id, and the factory's run index could not be read to resolve it: %w",
			id, err)
	}
	var response cloudStatusResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return "", "", fmt.Errorf("%s is the head of a run id, and the factory's run index could not be read: %w", id, err)
	}

	matches := cloudRunIDMatches(response, id)
	switch len(matches) {
	case 1:
		return matches[0], fmt.Sprintf("# %s is the head of a run id; resolved to %s", id, matches[0]), nil
	case 0:
		// Deliberately not "no such run": the index is a bounded window, so a
		// miss is a fact about the window. Saying otherwise would reintroduce
		// the confident negative one layer down.
		return "", "", fmt.Errorf(
			"%s is the head of a run id, and no run in the factory's index (its %d most recent) begins with it; "+
				"that is a statement about the index, not about the run — pass the full run id, "+
				"or list the runs with 'ticfac cloud status'",
			id, cloudRunIndexLimit)
	default:
		return "", "", fmt.Errorf("%s is the head of %d run ids — %s; pass the full run id",
			id, len(matches), strings.Join(matches, ", "))
	}
}

// cloudRunIDMatches collects every run id the index named that begins with the
// prefix. The lease holder and the queue are included: a live run is the most
// likely thing to have a half-copied id, and it is not always in the run list.
func cloudRunIDMatches(response cloudStatusResponse, prefix string) []string {
	seen := make(map[string]bool)
	matches := make([]string, 0)
	add := func(id string) {
		if id == "" || !strings.HasPrefix(id, prefix) || seen[id] {
			return
		}
		seen[id] = true
		matches = append(matches, id)
	}
	for _, run := range response.Runs {
		add(run.RunID)
	}
	if response.Lease != nil {
		add(response.Lease.RunID)
	}
	for _, queued := range response.Queued {
		add(queued.RunID)
	}
	for _, project := range response.Projects {
		if project.Lease != nil {
			add(project.Lease.RunID)
		}
		for _, queued := range project.Queued {
			add(queued.RunID)
		}
	}
	sort.Strings(matches)
	return matches
}

// cloudRunIDArg is what the run-scoped commands call: resolve the argument,
// report any resolution on stderr, and hand back the id to look up.
func cloudRunIDArg(ctx context.Context, id string, stderr io.Writer) (string, error) {
	resolved, note, err := resolveCloudRunID(ctx, id)
	if err != nil {
		return "", newExitError(exitGeneric, "%v", err)
	}
	if note != "" {
		fmt.Fprintln(stderr, note)
	}
	return resolved, nil
}

// cloudRunArg is what every run-scoped cloud command (status, logs, trace,
// supervisor, stop) resolves its argument through. It takes the same two
// spellings `ticfac status|events|watch` take (runid.go): a cloud run id —
// `run_` plus hex, whole or its head — or the epic id the run was started
// for, bare or as `epic-<id>`.
//
// The trap it closes: `ticfac run hn6 --cloud` hands the operator an epic id,
// never the factory's run id, and the cloud commands used to pass whatever
// they were given straight to the factory or the AI Gateway. `ticfac cloud
// trace r5i` — a TICK of a live run with 112 model calls — answered "No AI
// Gateway calls are stamped with run r5i", a confident negative about a run
// nobody asked about. An epic id now names the factory's current (else its
// latest) run for that epic, and an id that names no run and no epic but is a
// tick of a live run is refused with the command that does answer.
//
// tickFlag says the command takes --tick, so the refusal for a tick id can
// point at the tick's own stream rather than only at its run.
func cloudRunArg(ctx context.Context, command, arg string, tickFlag bool, stderr io.Writer) (string, error) {
	if strings.HasPrefix(arg, cloudRunIDMarker) {
		return cloudRunIDArg(ctx, arg, stderr)
	}
	epicID := epicIDOfArg(arg)
	if epicID == "" {
		return "", newExitError(exitUsage, "%s names neither a cloud run nor an epic", arg)
	}
	client, err := newCloudClient()
	if err != nil {
		return "", newExitError(exitGeneric,
			"%s is not a cloud run id (run_…), and only the factory can say which run epic %s names: %v",
			arg, epicID, err)
	}
	// This checkout's project, when it names one, narrows the index to the
	// runs that are its; outside a checkout every project's runs are asked.
	project := ""
	if root, err := cloudRepoRoot(); err == nil {
		if named, err := cloudProjectOf(root); err == nil {
			project = named
		}
	}
	runs, err := readCloudRunIndex(ctx, client, project)
	if err != nil {
		return "", newExitError(exitGeneric,
			"%s is not a cloud run id, and the factory's run index could not be read to resolve it as an epic: %v",
			arg, err)
	}

	if run := cloudRunOfEpic(runs, epicID); run != nil {
		fmt.Fprintf(stderr, "# %s is an epic id; answering for its cloud run %s (%s)\n",
			arg, run.RunID, stateOrUnknown(run.State))
		return run.RunID, nil
	}

	// Not a run and not an epic: a tick of a live run is the likeliest thing
	// an operator reading a board or a worker stream has to hand.
	if run := cloudLiveRunWithTick(ctx, client, runs, arg); run != nil {
		try := fmt.Sprintf("ticfac cloud %s %s", command, run.Epic)
		if tickFlag {
			try += " --tick " + arg
		}
		return "", newExitError(exitGeneric,
			"%s is a tick of epic %s (cloud run %s), not a run — try: %s", arg, run.Epic, run.RunID, try)
	}

	scope := "the factory's index"
	if project != "" {
		scope = fmt.Sprintf("the factory's index for %s", project)
	}
	return "", newExitError(exitGeneric,
		"no cloud run answers to %s: it is not a run id (run_…), no run in %s (its %d most recent) is for epic %s, "+
			"and no live run there has a tick %s. List the runs with 'ticfac cloud status'",
		arg, scope, cloudRunIndexLimit, epicID, arg)
}

// readCloudRunIndex reads the factory's run index, newest first, narrowed to one
// project when one is named.
func readCloudRunIndex(ctx context.Context, client *cloudClient, project string) ([]cloudRunRecord, error) {
	path := fmt.Sprintf("/api/runs?limit=%d", cloudRunIndexLimit)
	if project != "" {
		path += "&project=" + url.QueryEscape(project)
	}
	data, err := client.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var response cloudStatusResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return nil, err
	}
	return response.Runs, nil
}

// cloudRunOfEpic is the run an epic id names: its live run when it has one —
// the one an operator following the epic means — else its newest.
func cloudRunOfEpic(runs []cloudRunRecord, epicID string) *cloudRunRecord {
	var newest *cloudRunRecord
	for i := range runs {
		run := &runs[i]
		if strings.TrimSpace(run.Epic) != epicID {
			continue
		}
		if !isFinishedCloudRun(run.State) {
			return run
		}
		if newest == nil {
			newest = run
		}
	}
	return newest
}

// cloudLiveRunWithTick is the live run whose worker streams include tickID —
// the factory's own record of which ticks a run dispatched. Only live runs
// are asked: a tick id is resolved to be HELPFUL, and asking every finished
// run in the index for its streams would make a typo cost two hundred reads.
func cloudLiveRunWithTick(ctx context.Context, client *cloudClient, runs []cloudRunRecord, tickID string) *cloudRunRecord {
	for i := range runs {
		run := &runs[i]
		if isFinishedCloudRun(run.State) || run.RunID == "" || strings.TrimSpace(run.Epic) == "" {
			continue
		}
		data, err := client.request(ctx, http.MethodGet,
			"/api/runs/"+url.PathEscape(run.RunID)+"/logs?tick="+url.QueryEscape(tickID), nil)
		if err != nil {
			continue
		}
		var response cloudLogsResponse
		if err := decodeCloudJSON(data, &response); err != nil {
			continue
		}
		for _, stream := range response.Streams {
			if stream.TickID == tickID {
				return run
			}
		}
	}
	return nil
}
