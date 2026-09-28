package reconcile

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The epic PR's body, written for its REVIEWER (operator, 2026-09-28).
//
// The common way an epic reaches ticfac is handed over from interactive work
// with a frontier agent, and the next touch point is the PR: the person reviews
// it, with their agent, and decides. ticfac has already reviewed it with the
// configured tier — so the body's job is to make the person's review quick and
// pointed: where to look first, what the epic did, its done and the evidence
// for each item, then the record's detail (the verdict, the findings and where
// each went, what was absorbed) and the readiness the run checked. Every
// section is composed from the run's durable record and the tracker, like the
// rest of the body (closeout_body.go), so a rewrite states the same facts once.

// maxEpicDescription bounds the epic's own description in the body: it is the
// intent the reviewer checks the diff against, and a description that is a
// design document is linked by the tracker, not pasted whole.
const maxEpicDescription = 2000

// lookFirst is the reviewer's short list: the facts that most change what a
// review should look at, most urgent first — a NOT READY verdict, untriaged
// findings, the changes the run made that no tick asked for (repairs and
// resolutions), what it absorbed, and what it could not do at all.
func (r *Reconciler) lookFirst(decisions []runstate.Decision, final int, findings []runstate.Finding,
	absorptions []runstate.Absorption) string {

	var items []string
	if final >= 0 && reviewVerdictOf(decisions[final].Response) == subprocess.ReviewVerdictNotReady {
		items = append(items, "**The final review judged the epic NOT READY.** Read its verdict below before the "+
			"diff: it says what would make the epic ready.")
	}
	// Findings are named by tick and key here, never by title: each title is
	// on the PR exactly once, under its tick below, and the close-out's
	// read-back check counts on that (gateCloseoutCarriesFindings).
	var untriaged []string
	for _, finding := range findings {
		if finding.Status == runstate.FindingProposed {
			untriaged = append(untriaged, fmt.Sprintf("%s (tick %s)", short(finding.Key), finding.TickID))
		}
	}
	if len(untriaged) > 0 {
		items = append(items, fmt.Sprintf("%d finding(s) are untriaged — %s — listed with their full text under "+
			"the findings below.", len(untriaged), strings.Join(untriaged, ", ")))
	}
	var repairs, resolves []string
	for _, decision := range decisions {
		status, _ := decision.Response["status"].(string)
		if status != "merged" {
			continue
		}
		switch decision.Role {
		case RoleRepairGate:
			repairs = append(repairs, decisionTick(decision))
		case RoleResolveConflict:
			resolves = append(resolves, decisionTick(decision))
		}
	}
	if len(repairs) > 0 {
		items = append(items, fmt.Sprintf("The run REPAIRED a failed gate or CI by itself for %s: those commits "+
			"(\"repair: …\") are changes no tick asked for — read them as the run's own judgement.",
			strings.Join(dedupe(repairs), ", ")))
	}
	if len(resolves) > 0 {
		items = append(items, fmt.Sprintf("The run RESOLVED merge conflicts by itself for %s (commits "+
			"\"resolve-conflict: …\"): check each resolution keeps both sides' intent.",
			strings.Join(dedupe(resolves), ", ")))
	}
	var absorbed, liveRun []string
	for _, record := range absorptions {
		switch {
		case record.Gating:
			absorbed = append(absorbed, record.TickID)
		case isLiveRun(record):
			liveRun = append(liveRun, record.TickID)
		}
	}
	if len(absorbed) > 0 {
		items = append(items, fmt.Sprintf("The run ABSORBED %d tick(s) into the epic mid-run, fixes its own "+
			"findings demanded: %s.", len(absorbed), strings.Join(absorbed, ", ")))
	}
	if len(liveRun) > 0 {
		items = append(items, fmt.Sprintf("%d finding(s) need a live run of an epic that no worker inside this "+
			"one can do; they are backlog ticks for the next epic run to satisfy: %s.", len(liveRun),
			strings.Join(liveRun, ", ")))
	}
	if len(items) == 0 {
		items = append(items, "Nothing the run flagged: the review answered READY, no finding is untriaged, and the "+
			"run made no change of its own. Start with the ticks below — each closed behind the integrated gate.")
	}
	var b strings.Builder
	for i, item := range items {
		fmt.Fprintf(&b, "%d. %s\n", i+1, item)
	}
	return b.String()
}

// epicSummary is what the epic did: its own statement of intent, and every
// child with its state — the map a reviewer reads the diff against.
func (r *Reconciler) epicSummary() string {
	if r.tracker == nil {
		return "The tracker could not be read for the epic's own record.\n"
	}
	ctx := context.Background()
	var b strings.Builder
	if epic, err := r.tracker.Show(ctx, r.opts.EpicID); err == nil {
		fmt.Fprintf(&b, "**%s** (epic %s)\n", strings.TrimSpace(epic.Title), r.opts.EpicID)
		if description := strings.TrimSpace(epic.Description); description != "" {
			if len(description) > maxEpicDescription {
				description = description[:maxEpicDescription] + " …(the rest is in .tick/issues/" +
					r.opts.EpicID + ".json)"
			}
			fmt.Fprintf(&b, "\n%s\n", quote(description))
		}
	} else {
		fmt.Fprintf(&b, "Epic %s (its record could not be read: %v)\n", r.opts.EpicID, err)
	}
	graph, err := r.tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		fmt.Fprintf(&b, "\nIts ticks could not be read: %v\n", err)
		return b.String()
	}
	b.WriteString("\nIts ticks, in the order the plan ran them:\n\n")
	seen := map[string]bool{}
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if seen[task.ID] {
				continue
			}
			seen[task.ID] = true
			role := RoleOf(task)
			label := ""
			if role != "implement-tick" {
				label = " _(" + role + ")_"
			}
			fmt.Fprintf(&b, "- `%s` %s%s — %s\n", task.ID, strings.TrimSpace(task.Title), label, task.Status)
		}
	}
	return b.String()
}

// doneEvidence is the epic's definition of done, item by item, each with what
// proves it: the command [evidence.acceptance] binds it to and the last
// verdict of that command the run recorded, or the plain statement that no
// command proves it and the review is its evidence.
func (r *Reconciler) doneEvidence() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Every tick closed behind the integrated gate (%s).\n\n", r.gate)
	if r.tracker == nil {
		return b.String()
	}
	epic, err := r.tracker.Show(context.Background(), r.opts.EpicID)
	if err != nil {
		fmt.Fprintf(&b, "The epic's acceptance could not be read: %v\n", err)
		return b.String()
	}
	items, err := acceptance.Parse(epic.AcceptanceCriteria)
	if err != nil || len(items) == 0 {
		if strings.TrimSpace(epic.AcceptanceCriteria) == "" {
			b.WriteString("The epic states no acceptance criteria: the review's verdict below is the evidence " +
				"that it is done.\n")
		} else {
			fmt.Fprintf(&b, "The epic states its done as prose, not [A<n>] items, so no command is bound to it and "+
				"the review's verdict below is its evidence:\n\n%s\n", quote(strings.TrimSpace(epic.AcceptanceCriteria)))
		}
		return b.String()
	}
	bindings, commands, err := r.evidenceTable()
	if err != nil {
		bindings, commands = map[string]string{}, map[string]string{}
	}
	latest := r.latestEvidenceByCheck()
	for _, resolved := range acceptance.Resolve(items, bindings) {
		fmt.Fprintf(&b, "- **[%s]** %s\n", resolved.ID, strings.TrimSpace(resolved.Text))
		if resolved.State != acceptance.Runnable {
			b.WriteString("  - no command proves it: the review assessed it\n")
			continue
		}
		fmt.Fprintf(&b, "  - proved by `%s`", resolved.Command)
		if text := commands[resolved.Command]; text != "" {
			fmt.Fprintf(&b, " (`%s`)", text)
		}
		if record, ok := latest[resolved.Command]; ok {
			fmt.Fprintf(&b, ": last ran %s on %s", record.Result, short(record.Provenance.SourceSHA))
		} else {
			b.WriteString(": the run recorded no verdict of it")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// latestEvidenceByCheck is the last evidence record the run holds for each
// check id, by the time it finished.
func (r *Reconciler) latestEvidenceByCheck() map[string]runstate.Evidence {
	out := map[string]runstate.Evidence{}
	if r.store == nil {
		return out
	}
	keys := r.store.EvidenceKeys()
	sort.Strings(keys)
	for _, key := range keys {
		record, ok, err := r.store.Evidence(key)
		if err != nil || !ok {
			continue
		}
		if prior, seen := out[record.Check.ID]; !seen || record.FinishedAt >= prior.FinishedAt {
			out[record.Check.ID] = *record
		}
	}
	return out
}

// quote is a block of text as a markdown quote.
func quote(text string) string {
	return "> " + strings.ReplaceAll(text, "\n", "\n> ")
}

// decisionTick is the tick a role job's decision was about, as its request
// or provenance names it — the epic itself for a base fold's resolve.
func decisionTick(decision runstate.Decision) string {
	if tick, _ := decision.Request["tick_id"].(string); tick != "" {
		return tick
	}
	if decision.Provenance.TickID != nil && *decision.Provenance.TickID != "" {
		return *decision.Provenance.TickID
	}
	return "the epic"
}

// dedupe keeps the first of each value, in order.
func dedupe(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
