package cli

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The triage surface (tick sg5): the everyday half of the findings channel.
// `ticfac findings` lists the drafts a run filed; `ticfac finding` settles
// ONE — but its everyday shape demands what the operator's own complaint
// names: a 64-hex key typed by hand, a --by on every call, and a tick the
// operator must create BEFORE the command can record the promotion.
//
// `ticfac triage` removes all three for the decisions a person (or an agent)
// makes every day:
//
//   - every draft is addressed by a SHORT KEY PREFIX — unambiguous is the
//     only rule, and an ambiguous prefix is refused naming the drafts it
//     matches — or not addressed at all: with no decisions passed, the
//     command walks each untriaged finding, showing kind, severity, title,
//     body and the discovering tick, and reads the verdict one word at a
//     time;
//   - the actor defaults from git config (user.name, then user.email), the
//     identity the checkout already attributes the person's commits with —
//     and a READ needs no author (decided 2026-09-27): only a decision that
//     records one resolves an actor, so a listing answers on a checkout that
//     names nobody, and a settling action on one is still refused naming --by;
//   - ABSORB and FILE create the tick themselves — the same durable
//     promotion writes the run's own absorption makes (npq), on the branch
//     the run owns — so the promotion's mechanism is no longer the
//     operator's to perform by hand. Absorb files the tick UNDER THE EPIC:
//     an open child of the epic is what blocks the close-out (3h0's gate),
//     and the run works it. File records a backlog tick — no parent, owned
//     by the actor — for a finding the done is reachable with standing.
//
// FIXED and DISCARD record the same verdicts `ticfac finding` does, and the
// old command remains for what it alone can do: promoting a tick that already
// exists, into another repository (the finding the `ticfac finding` routing
// demands), on the operator's own key.
//
// `--json` is the agent half of the same surface: the untriaged findings
// listed as JSON — everything the interactive walk shows — and, when
// decisions are passed, what each decision did, so the loop list → decide →
// read back runs without parsing prose.

// triageStdin is where the interactive walk reads the person's decisions
// from: the terminal in production. A seam for exactly the tests that script
// the person, the same one-var shape newTracker is.
var triageStdin io.Reader = os.Stdin

// triageStdinIsTerminal reports whether the walk's input is a terminal a
// person is deciding on. The walk is for a person AT a terminal: anything
// else — a redirected file, /dev/null, an open pipe nobody will write a
// verdict into — can only look like a walk that decided nothing (or one that
// never ends), so the walk refuses it and names the two halves that work
// without a terminal: the scripted decisions and --json. A seam for exactly
// the tests that script the person, the same one-var shape triageStdin is.
var triageStdinIsTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// The decisions the surface settles, as the person and the agent both spell
// them. The words are the epic's own (absorb / file / fixed / discard); the
// letters are their interactive shorthands.
const (
	triageAbsorb  = "absorb"
	triageFile    = "file"
	triageFixed   = "fixed"
	triageDiscard = "discard"
	triageSkip    = "skip"
	triageQuit    = "quit"
)

// newTriageCommand builds the cobra command for `triage`.
func newTriageCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "triage <epic-id> [<key-prefix>=<decision>...]",
		Short: "settle the untriaged findings of an epic without the 64-hex keys",
		Long: `Settle every finding a run left waiting for a person — the gate that holds
the epic's close-out while one is untriaged — without typing a 64-hex key.

With no decisions passed, each untriaged finding is walked interactively:
kind, severity, title, body and the discovering tick, then one word — absorb,
file, fixed <commit>, discard, skip or quit. The walk reads a person's
verdicts from a terminal; without one it refuses and names the two halves
that work without — the decisions below, or --json.

With decisions passed, each is <key-prefix>=<decision>, settled in order:
d34=absorb (creates the tick under the epic, so the close-out waits on it),
d34=file (a backlog tick, owned by you), d34=fixed:<commit> (repaired inside
the epic — the commit must exist in this repository, so the claim is checked,
not asserted), d34=discard. A prefix that matches more than one draft is refused
naming them.

The actor defaults from git config (user.name, then user.email), and only a
decision that records one needs it — the listings record nothing and need no
actor. A finding
routed to another repository never holds a run — the run files it there, or
backlogs it here naming the target — and one still listed keeps its routing:
discard it here, or promote it with ticfac finding (into the target, or as a
local tick that names the target).

--json lists the untriaged findings as JSON when no decisions are passed, and
reports what each decision did as JSON when they are — the loop an agent
runs without parsing prose.`,
	}
	fs := flag.NewFlagSet("triage", flag.ContinueOnError)
	repo, remote, branch, runID := findingRunOptions(fs)
	by := fs.String("by", "", "the person triaging (default: git config user.name, then user.email)")
	asJSON := fs.Bool("json", false, "list the untriaged findings, and report what each decision did, as JSON")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(triageCommand(args, repo, remote, branch, runID, by, asJSON, stdout, stderr))
	}
	return cmd
}

// scriptedDecision is one decision passed on the command line: the draft,
// addressed by key prefix, and the verb, with the commit a fixed verdict
// names.
type scriptedDecision struct {
	prefix string
	verb   string
	commit string
}

// triageResult is what one decision did, as --json reports it: the decision,
// attributed, with the tick an absorb or file created, and the error a
// refusal carried. An error means the decision settled nothing.
type triageResult struct {
	Key      string `json:"key"`
	Decision string `json:"decision"`
	By       string `json:"by"`
	Tick     string `json:"tick,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Note     string `json:"note,omitempty"`
	Error    string `json:"error,omitempty"`
}

// triageFindingJSON is one untriaged finding as an agent reads it: everything
// the interactive walk shows, plus the full key the decision addresses by
// prefix. This listing is the half of the round-trip an agent starts from.
type triageFindingJSON struct {
	Key                string `json:"key"`
	Kind               string `json:"kind"`
	Severity           string `json:"severity"`
	Title              string `json:"title"`
	Body               string `json:"body"`
	Target             string `json:"target"`
	DiscoveredFrom     string `json:"discovered_from"`
	TickID             string `json:"tick_id"`
	Attempt            int    `json:"attempt"`
	DoneItem           string `json:"done_item,omitempty"`
	DemonstratingCheck string `json:"demonstrating_check,omitempty"`
	Linkage            string `json:"linkage"`
	Status             string `json:"status"`
}

func newTriageFindingJSON(f runstate.Finding) triageFindingJSON {
	return triageFindingJSON{
		Key:                f.Key,
		Kind:               f.Kind,
		Severity:           f.Severity,
		Title:              f.Title,
		Body:               f.Body,
		Target:             f.Target,
		DiscoveredFrom:     f.DiscoveredFrom,
		TickID:             f.TickID,
		Attempt:            f.Attempt,
		DoneItem:           f.DoneItem,
		DemonstratingCheck: f.DemonstratingCheck,
		Linkage:            f.LinkageText(),
		Status:             f.Status,
	}
}

func triageCommand(args []string, repo, remote, branch, runID, by *string, asJSON *bool, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] == "" {
		fmt.Fprintf(stderr, "ticfac triage: exactly one epic id is required\n")
		return exitUsage
	}
	epicID := args[0]
	// The epic id is accepted with its own `epic-` prefix everywhere — a
	// prefix with nothing behind it names no epic.
	if epicIDOfArg(epicID) == "" {
		fmt.Fprintf(stderr, "ticfac triage %q names no epic\n", epicID)
		return exitUsage
	}
	decisions := make([]scriptedDecision, 0, len(args)-1)
	for _, arg := range args[1:] {
		decision, err := parseScriptedDecision(arg)
		if err != nil {
			fmt.Fprintf(stderr, "ticfac triage %s: %v\n", epicID, err)
			return exitUsage
		}
		decisions = append(decisions, decision)
	}
	if parseOnly {
		return exitSuccess
	}

	// The run's address, resolved once for the drafts and the promotions: the
	// same defaults `run-epic` derives, so the two surfaces name the same
	// branch for the same epic — under the epic's CANONICAL id, because the
	// tick an absorb files names the epic the tracker knows.
	epicID, repoDir, remoteName, branchName, runName, err := resolveFindingsRun(epicID, *repo, *remote, *branch, *runID)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac triage %s: %v\n", epicID, err)
		return exitGeneric
	}

	store, _, err := openFindingsStore(epicID, repoDir, remoteName, branchName, runName)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac triage %s: %v\n", epicID, err)
		return exitGeneric
	}
	findings, err := store.Findings()
	if err != nil {
		fmt.Fprintf(stderr, "ticfac triage %s: %v\n", epicID, err)
		return exitGeneric
	}
	waiting := []runstate.Finding{}
	for _, finding := range findings {
		if finding.Status == runstate.FindingProposed {
			waiting = append(waiting, finding)
		}
	}

	// No decisions: the listing half. A READ needs no author (decided
	// 2026-09-27): a listing records nothing, so it resolves no actor — a
	// checkout that names nobody still gets its drafts, on a CI runner with
	// no git identity as much as this Mac. --json answers for an agent — one
	// VERSIONED document (tick 8v3), the schema id first so a reader can
	// refuse an unknown shape; otherwise the person walks the drafts, and
	// the walk RECORDS verdicts — its actor is resolved before the first
	// finding is shown, so the refusal still belongs ahead of a finding's
	// text, not between it and its verdict.
	if len(decisions) == 0 {
		if *asJSON {
			listed := make([]triageFindingJSON, 0, len(waiting))
			for _, finding := range waiting {
				listed = append(listed, newTriageFindingJSON(finding))
			}
			doc := triageListingJSON{
				agentDoc: agentDoc{Schema: agentSchemaID("triage"), State: agentStateDone},
				RunID:    store.RunID(),
				EpicID:   epicID,
				Findings: listed,
			}
			if err := emitAgentJSON(stdout, doc); err != nil {
				fmt.Fprintf(stderr, "ticfac triage %s: %v\n", epicID, err)
				return exitGeneric
			}
			return exitSuccess
		}
		if len(waiting) == 0 {
			fmt.Fprintf(stdout, "run %s has no findings waiting for triage.\n", store.RunID())
			return exitSuccess
		}
		actor, err := triageActor(*by, repoDir)
		if err != nil {
			fmt.Fprintf(stderr, "ticfac triage %s: %v\n", epicID, err)
			return exitUsage
		}
		return triageWalk(store, epicID, actor, waiting, repoDir, remoteName, branchName, runName,
			triageStdin, stdout, stderr)
	}

	// Decisions: settled in the order they were passed, each against the
	// draft its prefix addresses. A decision nobody can attribute is one
	// nobody can audit, so the actor is resolved before the first one is
	// settled.
	actor, err := triageActor(*by, repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac triage %s: %v\n", epicID, err)
		return exitUsage
	}

	// The promoter is opened lazily, on the
	// first absorb or file — a verdict (fixed, discard) never builds a
	// worktree.
	promo := &lazyPromotions{opts: reconcile.PromotionOptions{
		Repo: repoDir, Remote: remoteName, Branch: branchName, RunID: runName,
	}}
	defer promo.close()
	results := make([]triageResult, 0, len(decisions))
	failed := false
	for _, decision := range decisions {
		finding, err := resolveFindingPrefix(epicID, findings, decision.prefix)
		if err != nil {
			results = append(results, triageResult{
				Key: decision.prefix, Decision: decision.verb, By: actor, Error: err.Error(),
			})
			failed = true
			if !*asJSON {
				fmt.Fprintln(stderr, "ticfac triage "+epicID+": "+err.Error())
			}
			continue
		}
		result := settleTriage(store, promo, epicID, actor, *finding, decision.verb, decision.commit, repoDir,
			!*asJSON, stdout)
		results = append(results, result)
		if result.Error != "" {
			failed = true
			if !*asJSON {
				fmt.Fprintln(stderr, "ticfac triage "+epicID+": "+result.Error)
			}
		}
	}
	if *asJSON {
		doc := triageDecisionsJSON{
			agentDoc:  agentDoc{Schema: agentSchemaID("triage")},
			EpicID:    epicID,
			Decisions: results,
		}
		if failed {
			doc.State = agentStateFailed
		} else {
			doc.State = agentStateDone
		}
		if err := emitAgentJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "ticfac triage %s: %v\n", epicID, err)
			return exitGeneric
		}
	}
	if failed {
		return exitGeneric
	}
	return exitSuccess
}

// triageListingJSON is `triage --json`'s listing half, ticfac.triage.v1:
// the untriaged findings an agent decides from, one document with the
// schema id first.
type triageListingJSON struct {
	agentDoc
	RunID    string              `json:"run_id"`
	EpicID   string              `json:"epic_id"`
	Findings []triageFindingJSON `json:"findings"`
}

// triageDecisionsJSON is `triage --json`'s decision half: what each passed
// decision did, attributed, with the error a refusal carried — and the
// state word agreeing with the exit code (failed when any decision failed).
type triageDecisionsJSON struct {
	agentDoc
	EpicID    string         `json:"epic_id"`
	Decisions []triageResult `json:"decisions"`
}

// triageWalk settles the drafts a person reads one at a time: the finding's
// own text, then its verdict, one word per draft. The walk is for a person AT
// a terminal — anything else is refused, naming the halves that work without
// one — and a skip and a closed stream are honest stops, not failures: the
// finding stays proposed, the walk says so, and the close-out keeps holding
// the hand-over. A settle error is not an honest stop: it settled nothing, so
// the walk ends failed, never reporting the gate clear while a draft still
// holds it.
func triageWalk(store *runstate.Store, epicID, actor string, waiting []runstate.Finding,
	repoDir, remoteName, branchName, runName string, in io.Reader, stdout, stderr io.Writer) int {
	if !triageStdinIsTerminal(in) {
		fmt.Fprintf(stderr, "ticfac triage %s: the interactive walk reads a person's verdicts from a terminal, "+
			"and this input is not one.\n", epicID)
		fmt.Fprintf(stderr, "settle the drafts by short key prefix — ticfac triage %s <key-prefix>=absorb|file|fixed:<commit>|discard "+
			"— or list them with ticfac triage %s --json.\n", epicID, epicID)
		return exitUsage
	}
	reader := bufio.NewReader(in)
	promo := &lazyPromotions{opts: reconcile.PromotionOptions{
		Repo: repoDir, Remote: remoteName, Branch: branchName, RunID: runName,
	}}
	defer promo.close()

	settled, left, failed := 0, len(waiting), false
	// unfinished is every stop that leaves drafts waiting: the summary the
	// person reads, and the exit code that agrees with what actually happened
	// — failed when any decision settled nothing.
	unfinished := func() int {
		fmt.Fprintf(stdout, "run %s: %d settled, %d still waiting for a person; the epic's close-out does not "+
			"hand over while a finding is untriaged.\n", store.RunID(), settled, left)
		if failed {
			return exitGeneric
		}
		return exitSuccess
	}
	for i := range waiting {
		finding := waiting[i]
		printTriageFinding(stdout, i+1, len(waiting), finding)
		var verb, commit string
		for {
			fmt.Fprint(stdout, triagePrompt(finding))
			line, readErr := reader.ReadString('\n')
			if readErr != nil && strings.TrimRight(line, "\r\n") == "" {
				// The stream is closed with nothing more to read. What was not
				// decided stays exactly as it was — waiting, and gating the
				// close-out — and the walk says so rather than guessing.
				fmt.Fprintf(stdout, "\nno more decisions to read.\n")
				return unfinished()
			}
			parsedVerb, parsedCommit, parseErr := parseInteractiveDecision(line)
			if parseErr != nil {
				fmt.Fprintln(stdout, parseErr)
				if readErr != nil {
					// A last line the walk cannot parse, and nothing behind it:
					// the honest stop, not a re-prompt nobody can answer.
					fmt.Fprintf(stdout, "\nno more decisions to read.\n")
					return unfinished()
				}
				continue
			}
			if parsedVerb == triageQuit {
				return unfinished()
			}
			verb, commit = parsedVerb, parsedCommit
			break
		}
		if verb == triageSkip {
			fmt.Fprintf(stdout, "%s left as proposed — still waiting for a person.\n", shortTriageKey(finding.Key))
			continue
		}
		result := settleTriage(store, promo, epicID, actor, finding, verb, commit, repoDir, true, stdout)
		if result.Error != "" {
			fmt.Fprintln(stderr, "ticfac triage "+epicID+": "+result.Error)
			failed = true
			continue
		}
		if result.Note == "" {
			settled++
			left--
		}
	}
	if left == 0 {
		fmt.Fprintf(stdout, "run %s: %d settled, none waiting; the triage gate is down.\n", store.RunID(), settled)
		return exitSuccess
	}
	return unfinished()
}

// triagePrompt is the one line each decision is read on. A finding routed to
// another repository is not offered absorb or file: the tick it would create
// lives in the repository the finding targets, and creating it here would be
// the routing the finding carried, silently undone.
func triagePrompt(finding runstate.Finding) string {
	if finding.Target != "" {
		return fmt.Sprintf("decide %s… [fixed <commit>|discard|skip|quit] (routed to %s): ",
			shortTriageKey(finding.Key), finding.Target)
	}
	return fmt.Sprintf("decide %s… [absorb|file|fixed <commit>|discard|skip|quit]: ", shortTriageKey(finding.Key))
}

// printTriageFinding is the finding as the person deciding reads it: the
// discovery in full — kind, severity, title, body, target, discovering
// attempt, the claim against the done — because a verdict on a summary is a
// guess with confidence.
func printTriageFinding(w io.Writer, index, of int, finding runstate.Finding) {
	fmt.Fprintf(w, "[%d/%d] %s  %s, %s — %s\n", index, of, shortTriageKey(finding.Key),
		finding.Kind, finding.Severity, finding.Title)
	fmt.Fprintf(w, "    for %s\n", findingTarget(finding.Target))
	fmt.Fprintf(w, "    discovered by %s (tick %s, run dispatch #%d)\n",
		finding.DiscoveredFrom, finding.TickID, finding.Attempt)
	fmt.Fprintf(w, "    %s\n", finding.LinkageText())
	if body := strings.TrimRight(finding.Body, "\n"); body != "" {
		for _, line := range strings.Split(body, "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
}

// shortTriageKey is the prefix the surface shows a person: enough of the key
// to recognise the draft, never the 64 hexes nobody types.
func shortTriageKey(key string) string {
	const shown = 8
	if len(key) <= shown {
		return key
	}
	return key[:shown]
}

// parseInteractiveDecision reads one word of the person's verdict: the word
// the epic's own shape names, or its first letter. An empty line is a skip.
func parseInteractiveDecision(line string) (verb, commit string, err error) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return triageSkip, "", nil
	}
	switch fields[0] {
	case "absorb", "a":
		return triageAbsorb, "", nil
	case "file", "backlog", "b":
		return triageFile, "", nil
	case "fixed", "f":
		if len(fields) != 2 {
			return "", "", fmt.Errorf("the fixed verdict names the commit that repaired it: `fixed <commit>`")
		}
		if !looksLikeCommit(fields[1]) {
			return "", "", fmt.Errorf("%q is not a commit id", fields[1])
		}
		return triageFixed, fields[1], nil
	case "discard", "d":
		return triageDiscard, "", nil
	case "skip", "s":
		return triageSkip, "", nil
	case "quit", "q":
		return triageQuit, "", nil
	default:
		return "", "", fmt.Errorf("%q is not a decision: absorb, file, fixed <commit>, discard, skip or quit", fields[0])
	}
}

// parseScriptedDecision reads one <key-prefix>=<decision> argument. The
// scripted form is colon-separated where the interactive one is
// space-separated, because an argument is one token a shell will not split.
func parseScriptedDecision(arg string) (scriptedDecision, error) {
	prefix, verb, ok := strings.Cut(arg, "=")
	if !ok || prefix == "" {
		return scriptedDecision{}, fmt.Errorf("a decision is <key-prefix>=absorb|file|fixed:<commit>|discard, not %q", arg)
	}
	switch {
	case verb == triageAbsorb:
		return scriptedDecision{prefix: prefix, verb: triageAbsorb}, nil
	case verb == triageFile || verb == "backlog":
		return scriptedDecision{prefix: prefix, verb: triageFile}, nil
	case verb == triageDiscard:
		return scriptedDecision{prefix: prefix, verb: triageDiscard}, nil
	case verb == triageFixed:
		return scriptedDecision{}, fmt.Errorf("the scripted form of the fixed verdict is fixed:<commit>, naming the commit that repaired it")
	case strings.HasPrefix(verb, triageFixed+":"):
		commit := strings.TrimPrefix(verb, triageFixed+":")
		if !looksLikeCommit(commit) {
			return scriptedDecision{}, fmt.Errorf("%q is not a commit id: the fixed verdict names the commit that repaired "+
				"it, so the claim is checkable rather than asserted", commit)
		}
		return scriptedDecision{prefix: prefix, verb: triageFixed, commit: commit}, nil
	default:
		return scriptedDecision{}, fmt.Errorf("%q is not a decision: absorb, file, fixed:<commit> or discard", verb)
	}
}

// looksLikeCommit reports whether s is shaped like a commit id — hexadecimal,
// abbreviated or full length. Shape is the PARSE-time check, so a decision
// typed on a command line or at a prompt is refused before anything is
// settled; existence is checked at settle time, in the repository the run
// works in (commitExists) — and for a finding routed to another repository
// the shape is all this surface can check, because the commit lives where it
// cannot read. runstate's own validator re-reads the verdict the triage
// records.
func looksLikeCommit(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// commitExists reports whether commit names a commit the repository the run
// works in can resolve — full or abbreviated, reachable or merely present.
// The check is git's own resolution, so what the checkout holds is what counts:
// a repair the run's durable branch carries resolves, and an invented id
// names nothing.
func commitExists(repo, commit string) bool {
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", commit+"^{commit}")
	return cmd.Run() == nil
}

// resolveFindingPrefix addresses one draft by the short key prefix an agent
// passes: unambiguous is the only rule, so a prefix matching nothing is a
// lookup failure naming the listing command, and one matching several is
// refused naming every draft it matched.
func resolveFindingPrefix(epicID string, findings []runstate.Finding, prefix string) (*runstate.Finding, error) {
	matches := []*runstate.Finding{}
	for i := range findings {
		if strings.HasPrefix(findings[i].Key, prefix) {
			matches = append(matches, &findings[i])
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no findings draft starts with %q: `ticfac triage %s --json` lists the drafts that exist",
			prefix, epicID)
	default:
		keys := make([]string, 0, len(matches))
		for _, finding := range matches {
			keys = append(keys, finding.Key)
		}
		return nil, fmt.Errorf("the prefix %q matches %d drafts — say more of the key: %s",
			prefix, len(matches), strings.Join(keys, ", "))
	}
}

// settleTriage records ONE decision on ONE draft — the shared body of the
// interactive walk and the scripted pass, so the two cannot drift. The result
// carries what happened; prose goes to stdout only when a person is reading
// (quiet is --json's pass). repoDir is the repository the run works in, where
// the fixed verdict's commit must be resolvable — the claim is checked, not
// asserted.
func settleTriage(store *runstate.Store, promo *lazyPromotions, epicID, actor string,
	finding runstate.Finding, verb, commit, repoDir string, prose bool, stdout io.Writer) triageResult {
	result := triageResult{Key: finding.Key, Decision: verb, By: actor, Commit: commit}

	// A decision is never made twice: the standing triage is the person's
	// (or the run's) and nothing this surface does rewrites it.
	if finding.Status != runstate.FindingProposed {
		result.Note = standingDecisionNote(finding)
		if prose {
			fmt.Fprintln(stdout, result.Note)
		}
		return result
	}

	var triage runstate.Triage
	switch verb {
	case triageAbsorb, triageFile:
		if finding.Target != "" {
			// The finding is routed: the tick an absorb or file would create
			// belongs in the repository it targets, and creating it here
			// would be the routing silently undone. The tick already exists
			// there — the one command that can record it is the old one.
			result.Error = fmt.Sprintf("the finding is routed to %s: only a person can file it there — "+
				"`ticfac finding %s %s --promote-as \"%s:<tick-id>\" --by \"<who>\"` (or --promote-as a "+
				"local tick whose title or description names %s)",
				finding.Target, epicID, finding.Key, finding.Target, finding.Target)
			return result
		}
		tickID, err := promo.mint()
		if err != nil {
			result.Error = err.Error()
			return result
		}
		if _, err := promo.create(triagedTickRecord(epicID, actor, finding, tickID, verb == triageFile)); err != nil {
			result.Error = err.Error()
			return result
		}
		result.Tick = tickID
		triage = runstate.Triage{Status: runstate.FindingPromoted, By: actor, PromotedAs: tickID}
	case triageFixed:
		// The verdict's value is that the claim is checkable: the commit must
		// exist in the repository the run works in — an id that names nothing
		// is an assertion wearing the verdict's clothes. A finding routed to
		// another repository keeps the shape check alone: its commit lives
		// where this surface cannot read.
		if finding.Target == "" && !commitExists(repoDir, commit) {
			result.Error = fmt.Sprintf("%q names no commit in this repository: the fixed verdict names the commit "+
				"that repaired it, so the claim is checked, not asserted", commit)
			return result
		}
		triage = runstate.Triage{Status: runstate.FindingFixed, By: actor, FixedAs: commit}
	case triageDiscard:
		triage = runstate.Triage{Status: runstate.FindingDiscarded, By: actor}
	case triageSkip:
		result.Note = "skipped: left as proposed, still waiting for a person"
		return result
	default:
		result.Error = fmt.Sprintf("%q is not a decision this surface settles", verb)
		return result
	}

	outcome, decided, err := store.TriageFinding(finding.Key, triage)
	if err != nil {
		if verb == triageAbsorb || verb == triageFile {
			// The tick is on the branch and the draft still stands: the same
			// decision re-run reaches the same records, because the create
			// is create-if-absent — the resume npq built for killed runs is
			// the person's retry too.
			result.Error = fmt.Sprintf("the tick %s is on the branch and the draft %s still stands: %v — "+
				"re-run the same decision; the create is idempotent", result.Tick, finding.Key, err)
		} else {
			result.Error = err.Error()
		}
		return result
	}
	if outcome.IsConflict() {
		// The compare-and-swap refused: the draft moved under this triage in
		// a way the store's re-read does not answer (a missing base, an
		// unclassifiable refusal). Whatever the conflict is, this decision
		// settled nothing — and for absorb and file, the tick this one created
		// is a real record no draft points at, so the note says so rather than
		// leaving it to be discovered.
		result.Note = fmt.Sprintf("finding %s could not be decided: the draft moved under the triage (%s)",
			finding.Key, outcome)
		if decided.TriagedBy != "" {
			result.Note = fmt.Sprintf("finding %s was decided while you were deciding it: it is %s (by %s) — "+
				"their decision stands", finding.Key, decided.Status, decided.TriagedBy)
		}
		if result.Tick != "" {
			result.Note += fmt.Sprintf("; the tick %s you created is on the branch and must be worked or closed by hand",
				result.Tick)
		}
		if prose {
			fmt.Fprintln(stdout, result.Note)
		}
		return result
	}
	if outcome == runstate.NoChange {
		// Somebody decided first — between this walk's read and its write, or
		// before it began. Theirs is the decision; and for absorb and file,
		// the tick this one created is a real record no draft points at now,
		// so the note says so rather than leaving it to be discovered.
		result.Note = standingDecisionNote(decided)
		if result.Tick != "" && decided.PromotedAs != result.Tick {
			result.Note += fmt.Sprintf(" The tick %s you created is on the branch and must be worked or closed by hand",
				result.Tick)
		}
		if prose {
			fmt.Fprintln(stdout, result.Note)
		}
		return result
	}

	if prose {
		switch verb {
		case triageAbsorb:
			fmt.Fprintf(stdout, "finding %s is absorbed into epic %s as tick %s, %s's decision of run %s.\n"+
				"The tick is an open child of the epic: the close-out does not hand over while it is, and the run "+
				"works it before the final review.\n",
				finding.Key, epicID, result.Tick, actor, store.RunID())
		case triageFile:
			fmt.Fprintf(stdout, "finding %s is filed as the backlog tick %s, %s's decision of run %s.\n"+
				"It is not the epic's to absorb: the done is reachable with the finding standing, and the tick "+
				"waits for %s.\n",
				finding.Key, result.Tick, actor, store.RunID(), actor)
		case triageFixed:
			fmt.Fprintf(stdout, "finding %s is recorded as fixed, repaired as %s, %s's decision of run %s.\n"+
				"Ticks of the run whose findings are all triaged can now close. The same finding reported again is not "+
				"suppressed: if it comes back, the fix did not hold, and that is exactly when the run must hear it.\n",
				finding.Key, commit, actor, store.RunID())
		case triageDiscard:
			fmt.Fprintf(stdout, "finding %s is discarded, recorded as %s's decision of run %s.\n"+
				"The same finding reported again proposes nothing new, whatever was decided here.\n",
				finding.Key, actor, store.RunID())
		}
	}
	return result
}

// standingDecisionNote reports a draft somebody already decided — the funnel's
// rule, unchanged from the finding command: whatever the human did with the
// original is what a repeat finding must not reopen.
func standingDecisionNote(finding runstate.Finding) string {
	note := fmt.Sprintf("finding %s is already %s", finding.Key, finding.Status)
	if finding.TriagedBy != "" {
		note += fmt.Sprintf(" (by %s at %s)", finding.TriagedBy, finding.TriagedAt)
	}
	if finding.PromotedAs != "" {
		note += fmt.Sprintf(", as %s", finding.PromotedAs)
	}
	if finding.FixedAs != "" {
		note += fmt.Sprintf(", fixed as %s", finding.FixedAs)
	}
	return note + ": a decision is never made twice, and a repeat finding proposes nothing new."
}

// triagedTickRecord is the tick a person's absorb or file creates: the
// finding's own title and body, the attempt that discovered it, and a
// description that says WHO settled the finding into this tick — the
// provenance the 9t0 shape lacks (a tick filed with no provenance and no
// review). The owner is the RUN's for an absorbed child — the run works it,
// exactly the owner the run's own absorbed ticks carry — and the ACTOR's for
// a backlog tick, which waits for the person who filed it.
func triagedTickRecord(epicID, actor string, finding runstate.Finding, tickID string, backlog bool) tk.Tick {
	description := strings.TrimSpace(finding.Body)
	if description == "" {
		description = finding.Title
	}
	how := fmt.Sprintf(
		"ticfac triage: %s settled the finding %s (reported by %s) by absorbing it into the epic %s. "+
			"The tick is an open child of the epic: the close-out does not hand over while it is, and the run works it.",
		actor, finding.Key, finding.DiscoveredFrom, epicID)
	if backlog {
		how = fmt.Sprintf(
			"ticfac triage: %s settled the finding %s (reported by %s) by filing it as a backlog tick: "+
				"the done is reachable with the finding standing, so it is not the epic's to absorb. The tick waits for %s.",
			actor, finding.Key, finding.DiscoveredFrom, actor)
	}
	at := time.Now().UTC().Format(time.RFC3339)
	tick := tk.Tick{
		ID:             tickID,
		Title:          finding.Title,
		Description:    strings.TrimSpace(description + "\n\n" + how),
		Status:         "open",
		Priority:       2,
		Type:           "task",
		Owner:          "ticfac",
		Parent:         epicID,
		DiscoveredFrom: finding.DiscoveredFrom,
		CreatedBy:      actor,
		CreatedAt:      at,
		UpdatedAt:      at,
	}
	if backlog {
		tick.Owner = actor
		tick.Parent = ""
	}
	return tick
}

// lazyPromotions opens the durable promotion seam on the first decision that
// needs it — absorb or file — so recording a verdict (fixed, discard) never
// builds a worktree, and a walk that decides nothing costs nothing.
type lazyPromotions struct {
	opts reconcile.PromotionOptions
	open *reconcile.Promotions
	err  error
}

func (l *lazyPromotions) promotions() (*reconcile.Promotions, error) {
	if l.open == nil && l.err == nil {
		l.open, l.err = reconcile.OpenPromotions(l.opts)
	}
	return l.open, l.err
}

func (l *lazyPromotions) mint() (string, error) {
	promo, err := l.promotions()
	if err != nil {
		return "", err
	}
	return promo.MintTickID()
}

func (l *lazyPromotions) create(tick tk.Tick) (string, error) {
	promo, err := l.promotions()
	if err != nil {
		return "", err
	}
	created, err := promo.CreateTick(tick)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

func (l *lazyPromotions) close() {
	if l.open != nil {
		l.open.Close()
	}
}

// triageActor is who settles the drafts: the flag when the person names
// themselves, else the identity the checkout already attributes their commits
// with — user.name first, user.email when no name is set. An identity that
// resolves to nothing is a refusal naming the flag, because a decision nobody
// can attribute is one nobody can audit.
func triageActor(by, repo string) (string, error) {
	if by != "" {
		return by, nil
	}
	for _, key := range []string{"user.name", "user.email"} {
		out, err := execGitConfig(repo, key)
		if err == nil {
			return out, nil
		}
	}
	return "", fmt.Errorf("--by names who is triaging, and this checkout's git config names nobody " +
		"(neither user.name nor user.email is set): a decision nobody can attribute is one nobody can audit")
}

// execGitConfig reads one git config value the way the person's own git
// reads it — host config included, because the actor IS the host's identity
// (the same read the factory's GitHub surface makes of user.email).
func execGitConfig(repo, key string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "config", key).Output()
	if err != nil {
		return "", fmt.Errorf("git config %s: %w", key, err)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return "", fmt.Errorf("git config %s is empty", key)
	}
	return value, nil
}
