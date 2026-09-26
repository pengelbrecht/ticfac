package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The close-out admission precondition (tick 0iz): a target repository may
// declare, in its own `.tick/config.md`, that an epic integrates through a
// PR + CI gate — the orchestrator pushes the epic branch and opens a PR, and
// the epic close-out may not complete until CI is green on that PR. This
// file makes that rule a precondition the RUN enforces mechanically, rather
// than diligence a close-out worker performs by reading the config and
// reasoning correctly — which is exactly the "an orchestrator should
// remember it from a prompt" failure this repository keeps rejecting. The
// production run that routed this tick in had the rule declared, no PR ever
// opened, and two close-out attempts that each had to rediscover the same
// unmet precondition from the config themselves; and because the CI workflow
// triggered on pull_request only, a push to the epic branch ran nothing at
// all — the precondition was unsatisfiable rather than merely unmet.
//
// The order is the one every other precondition in this package keeps, with
// the forge where the gate would be:
//
//	read the rule  ->  PR exists?  ->  open one if not  ->  CI green?
//	->  ADMIT the close-out, or refuse typed, naming which half is unmet
//
// Both facts are recorded: the PR in the checkpoint's reason the moment it is
// opened or found (so a resumed run reads where the PR lives rather than
// asking the forge again from scratch), and CI at every state it passes
// through — pending while the run waits, the failing job's NAME in the
// refusal and the checkpoint's reason when it is red, the admission when it
// is green. CI state is deliberately re-derived at every admission rather
// than trusted from the checkpoint: CI's answer changes with every push, and
// a checkpointed "green" is stale evidence — the same reason the gate's
// evidence is keyed by the commit it ran on.
//
// The admission is the FIRST of this file's two CI gates. The second is the
// close's own (tick sqx, gateCloseoutClose): the admission covers the head
// as it stands when the close-out STARTS, and the close-out's own commits —
// the very thing the phase exists to write — then move that head, so the
// CLOSE re-derives CI from the PR before it happens rather than trusting the
// admission's green. One rule, two gates, one gap.

// ------------------------------------------------------- the rule itself ---

// CloseoutRule is what the target repository's own config declares about how
// an epic integrates, read from `.tick/config.md` the way the gate reads
// `runners.toml` — from the repository, through a reader that recognizes only
// what it knows, rather than a rule hardcoded into this binary.
type CloseoutRule struct {
	// Declared says the repository states the PR + CI close-out rule.
	Declared bool
	// CIWorkflow is the CI workflow the rule names (or the default
	// `.github/workflows/ci.yml`), carried into every message about the
	// precondition so the repair points at a file.
	CIWorkflow string
	// Stated is the rule's own line, verbatim, for the messages a person
	// reads: the repository's words, not this package's paraphrase of them.
	Stated string
}

// DefaultCIWorkflow is the workflow the rule names when it names none. It is
// GitHub's own convention, and the one both repositories that declare this
// rule use.
const DefaultCIWorkflow = ".github/workflows/ci.yml"

// ruleAnchor is the phrase that declares the rule. The config's Rules
// section is human prose, and a reader that fuzzy-matched it would fail open
// on every paraphrase; so the phrase "PR + CI gate" — the rule's own stable
// vocabulary, present verbatim in every declaration of it — is the anchor,
// and this comment is the contract a repository buys into by using it.
const ruleAnchor = "pr + ci gate"

// workflowPattern pulls the workflow path the rule may name in parentheses,
// e.g. "the CI workflow (.github/workflows/ci.yml) is green".
var workflowPattern = regexp.MustCompile(`\.github/workflows/[A-Za-z0-9._/-]+\.ya?ml`)

// ReadCloseoutRule reads the PR + CI close-out rule from a repository's
// `.tick/config.md`.
//
// A missing file declares nothing, and that is not an error: most target
// repositories carry no config.md at all, and a run against one of them is
// exactly as correct as it was before this rule existed. A file that IS
// there but cannot be read is an error, for the same reason as the gate
// reader's: the run was pointed at it.
func ReadCloseoutRule(path string) (CloseoutRule, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CloseoutRule{}, nil
		}
		return CloseoutRule{}, fmt.Errorf("read the repository config at %s: %w", path, err)
	}
	return parseCloseoutRule(string(raw))
}

// parseCloseoutRule recognises the rule in the config's own Rules section.
// Only the `## Rules` section declares it — a prompt file or a standing
// order mentioning a PR is not a rule a run enforces, and treating it as one
// would be the same failure this tick fixes, pointed the other way.
func parseCloseoutRule(document string) (CloseoutRule, error) {
	section := ""
	for _, line := range strings.Split(document, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// A heading — any depth — starts a new section. The Rules section
			// is `## Rules`; a deeper heading inside it ends it.
			section = strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
			continue
		}
		if section != "rules" {
			continue
		}
		if !strings.Contains(strings.ToLower(trimmed), ruleAnchor) {
			continue
		}
		rule := CloseoutRule{Declared: true, CIWorkflow: DefaultCIWorkflow, Stated: trimmed}
		if found := workflowPattern.FindString(trimmed); found != "" {
			rule.CIWorkflow = found
		}
		return rule, nil
	}
	return CloseoutRule{}, nil
}

// ------------------------------------------------- the admission itself ---

// gateCloseoutOnOpenChildren is the close-out's definition-of-done precondition
// (tick 3h0): the close-out does not START while any child of the epic other
// than itself is open.
//
// The production incident this gate exists for was observed on epic-yoh,
// 2026-09-24: while the run was in its final review, four ticks were absorbed
// into the epic and the close-out was made blocked-by each of them — and the
// live run, which never re-admitted the new ticks nor re-read the edges, went
// straight from the last tick's close into the close-out and OPENED THE EPIC
// PR over four open blockers. The blocker edges were the incident's shape;
// the gate is deliberately wider than they are, because blocked_by is the
// plan's sequencing vocabulary and a close-out must not start while the
// epic's own children stand open however they got that way: a tick created
// mid-run the plan never picked up, a child a person reopened after the run
// closed it, or an edge nobody drew at all.
//
// The incident's own timing is why the refusal is not returned flat. The
// absorbed ticks landed while the REVIEW ran, and a role job settles inline
// — its close is not a window-held attempt's close, so nothing replans
// between the review and the close-out. A flat refusal would make every
// mid-review absorption a failed run and a person's re-run, which is the
// thing the tick exists to remove. So the gate hands its refusal to the
// run's own sequencing instead (settleBeforeDispatch returns a blockedTickErr
// carrying it, and requeueBlocked decides): the open children the fresh
// graph offers as work this run has not done are WORKED first — admitted by
// the re-derivation, dispatched, closed — and the refusal that finally
// stands names only the children this run could do nothing about.
//
// It answers the open children and a refusal naming them when a child is
// open, nothing when the close-out may start, and a plain error when the
// tracker cannot answer — the same rule every graph read keeps: a graph the
// tracker cannot answer for stops the run rather than being guessed at.
//
// The refusal is a FAILURE, not a hold: the next actor can be another run.
// Once the children close — by a person, by whatever absorbed them — a re-run
// of the epic under this run id resumes from the graph as it stands, plans
// what it finds open, and reaches this gate again. What the run must never do
// is the thing the old code did: keep going as though the epic's definition
// of done were a fact about the plan rather than about the tracker.
func (r *Reconciler) gateCloseoutOnOpenChildren(ctx context.Context, entry planEntry) ([]string, *Refusal, error) {
	graph, err := r.tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		return nil, nil, fmt.Errorf("reconcile: read the epic graph of %s before admitting the close-out: %w",
			r.opts.EpicID, err)
	}
	var open []string
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.ID != entry.TickID && task.Status != "closed" {
				open = append(open, task.ID)
			}
		}
	}
	if len(open) == 0 {
		return nil, nil, nil
	}
	return open, r.refuse(RefusedCloseoutChildrenOpen, entry.TickID,
		"the close-out of %s does not start while any child of the epic other than itself is open: %s %s still "+
			"open, so the epic's definition of done is not met and this run refuses to close over it — a close-out that "+
			"runs past an open child could hand over an epic whose goal nobody reached. The child(ren) may be ticks "+
			"created under the epic after this run planned it, or ones reopened since it closed them; re-run the epic "+
			"under this run id once they close and the resume re-derives the plan from the tracker as it stands",
		r.opts.EpicID, strings.Join(open, ", "), plural(len(open), "is", "are")), nil
}

// plural is the one word a count has to agree with, for the message above.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// admitCloseout is the close-out phase's admission precondition: the rule
// the target repo declares, enforced by the run before the close-out job is
// claimed or dispatched — never left to the close-out worker's diligence.
//
// It returns nil when the close-out is admitted (the repo declares no rule,
// or the PR is open and CI is green on it), and a typed refusal otherwise,
// each reason naming which half of the precondition is unmet, because the
// halves send the next repair somewhere different: a missing surface at the
// host, an unopenable PR at the forge's credential, a red CI at the failing
// job the message names, a pending CI at the clock — and a CI that never
// APPEARS at the workflow's triggers, but only after the run has waited
// its bound for the check runs to show up (tick ox0), because a PR this run
// just opened has none yet by construction.
func (r *Reconciler) admitCloseout(ctx context.Context, entry planEntry) error {
	if !r.closeoutRule.Declared {
		return nil
	}
	tick := entry.TickID
	if r.opts.PullRequests == nil {
		// Defensive: New refuses this configuration before anything is
		// claimed, so this is the embedder that built Options by hand. It
		// still fails closed, and typed.
		return r.refuse(RefusedCloseoutForge, tick,
			"the repository declares the PR + CI close-out rule, and this build has no code-hosting surface "+
				"configured to open or read the epic PR: %s", r.closeoutRule.Stated)
	}

	// The PR half. Find before open, so a resumed run cut between the two
	// finds the PR the previous incarnation opened rather than opening a
	// second one. The body the PR carries is composed BEFORE the branch: the
	// same record composes it either way, and the open hands it to the PR the
	// moment it exists while the found branch rewrites it — the write is an
	// overwrite, so the two paths are one idempotence argument (tick 4sb).
	head, base := r.branch, r.prBase()
	body, findings, err := r.closeoutPRBody()
	if err != nil {
		return r.refuse(RefusedCloseoutPRBody, tick,
			"the epic PR cannot carry the final review's verdict and the run's findings: the run's own record "+
				"could not be read to compose them: %v. The rule the repository declares is: %s",
			err, r.closeoutRule.Stated)
	}
	pr, err := r.opts.PullRequests.Find(ctx, head, base)
	if err != nil {
		return r.refuse(RefusedCloseoutPR, tick,
			"the close-out cannot be admitted: the code-hosting surface could not say whether a PR exists for "+
				"%s (→ %s): %v. The rule the repository declares is: %s", head, base, err, r.closeoutRule.Stated)
	}
	if pr == nil {
		title := fmt.Sprintf("epic %s: integrate %s", r.opts.EpicID, head)
		opened, openErr := r.opts.PullRequests.Open(ctx, head, base, title, body)
		if openErr != nil {
			return r.refuse(RefusedCloseoutPR, tick,
				"no PR exists for %s (→ %s) and opening one failed, so the close-out's precondition is unmet and "+
					"cannot be satisfied by this run: %v. The rule the repository declares is: %s",
				head, base, openErr, r.closeoutRule.Stated)
		}
		pr = opened
		r.record(tick, StagePROpened, "the epic PR #%d (%s → %s) is open: %s", pr.Number, head, base, pr.URL)
		// The composed body went to the PR with its open, so the write and
		// the existence are one call: no window where the PR exists and
		// carries nothing. The stage is the same one the rewrite records,
		// because the invariant is the same one (tick 4sb).
		r.recordPRBody(tick, pr.Number, findings)
	} else {
		// The PR exists — this run's earlier incarnation, or an older
		// ticfac, opened it — so the body it carries is REWRITTEN from the
		// record: the found path is the resumed-close-out half of the write,
		// and the rewrite is what keeps a resume from appending.
		if err := r.carryOntoThePR(ctx, tick, *pr, body, findings); err != nil {
			return err
		}
		r.record(tick, StagePROpened, "the epic PR #%d (%s → %s) already exists: %s", pr.Number, head, base, pr.URL)
	}
	// The PR fact lands in the checkpoint the moment it is known, so a
	// resumed run — and a person reading the run's record — sees where the
	// PR lives rather than rediscovering it. This is a state change, not an
	// observation: the reason moves.
	if _, err := r.checkpoint(runstate.StateRunning,
		fmt.Sprintf("the epic PR #%d (%s → %s) is open; close-out of %s waits on CI", pr.Number, head, base,
			r.opts.EpicID)); err != nil {
		return err
	}

	// The CI half. Every state CI can leave is first a wait the run bounds
	// against the same clock a gate command gets, because a CI run on the PR
	// is a gate this run waits on rather than one it runs — pending is a
	// wait for an answer, and NOTHING reported is a wait for the question
	// itself to exist (tick ox0): a PR this run just opened has no check
	// runs yet BY CONSTRUCTION, and refusing at their first momentary
	// absence is the "unsatisfiable by waiting" verdict the production run
	// handed an operator about a workflow that was correct. What survives
	// the bound is a typed refusal naming which state it was and what was
	// checked, never a cause guessed at the first ask.
	deadline := r.now().Add(r.opts.GateTimeout)
	for {
		report, ciSHA, ciIsHead, ciErr := r.ciForTree(ctx, pr)
		if ciErr != nil {
			return r.refuse(RefusedCloseoutPR, tick,
				"the close-out cannot be admitted: CI on the epic PR #%d could not be read: %v. The rule the "+
					"repository declares is: %s", pr.Number, ciErr, r.closeoutRule.Stated)
		}
		if report.State == forge.CIRed && r.rerunRedCIOnce(ctx, tick, pr, report) {
			report.State = forge.CIPending
		}
		switch report.State {
		case forge.CIGreen:
			r.record(tick, StageCloseoutAdmitted, "CI is green on %s; the close-out is admitted",
				ciSubject(ciSHA, ciIsHead, pr))
			if _, err := r.checkpoint(runstate.StateRunning,
				fmt.Sprintf("CI is green on the epic PR #%d; the close-out of %s is admitted", pr.Number,
					r.opts.EpicID)); err != nil {
				return err
			}
			return nil
		case forge.CIRed:
			r.record(tick, StageCloseoutHeld, "CI on the epic PR #%d is red: %s failed", pr.Number,
				strings.Join(report.Failing, ", "))
			return r.refuse(RefusedCloseoutCI, tick,
				"CI is red on the epic PR #%d (%s): the failing job is %s. The close-out of %s is not admitted "+
					"until CI is green on the PR, and the rule the repository declares is: %s",
				pr.Number, pr.URL, strings.Join(report.Failing, ", "), r.opts.EpicID, r.closeoutRule.Stated)
		case forge.CINone:
			// Absence right after creation is "not yet", never "never" (tick
			// ox0): the production run opened PR #11 and refused it the same
			// second — "unsatisfiable by waiting", volunteering a cause that
			// sent whoever read it to edit a workflow file that was correct,
			// while the check runs appeared 45s later and went green. So a
			// silent CI waits against the same bound a pending one does, and
			// the feed says it is waiting for CI to APPEAR while it does — the
			// "not yet" wording, kept apart from the "never" only the bound
			// can earn, because the two have opposite repairs: one is a clock,
			// the other is a workflow file.
			if now := r.now(); now.After(deadline) {
				return r.refuse(RefusedCloseoutCIAbsent, tick,
					"no CI has appeared on the epic PR #%d (%s): this run waited %s for %s to produce check runs on "+
						"the PR's head, asking the forge every %s, and none appeared in that time, so the close-out's "+
						"precondition is unmet. What was checked: the code-hosting surface's check runs on the head of "+
						"PR #%d. Only now may the run suggest a cause: the workflow may not trigger on pull_request at "+
						"all — check what %s answers for other PRs before editing it. The rule the repository declares "+
						"is: %s",
					pr.Number, pr.URL, r.opts.GateTimeout, r.closeoutRule.CIWorkflow, r.pollInterval, pr.Number,
					r.closeoutRule.CIWorkflow, r.closeoutRule.Stated)
			}
			r.record(tick, StageCloseoutHeld, "no check runs yet on the epic PR #%d: waiting for CI to appear", pr.Number)
			if _, err := r.checkpoint(runstate.StateRunning,
				fmt.Sprintf("no check runs yet on the epic PR #%d; the close-out of %s waits for CI to appear", pr.Number,
					r.opts.EpicID)); err != nil {
				return err
			}
			r.sleep(r.pollInterval)
		case forge.CIPending:
			if now := r.now(); now.After(deadline) {
				return r.refuse(RefusedCloseoutCIPending, tick,
					"CI on the epic PR #%d was still pending %s after the close-out began waiting on it, so this "+
						"run does not admit the close-out: re-run the epic once CI concludes, and this admission "+
						"is re-derived from the PR, not rediscovered. The rule the repository declares is: %s",
					pr.Number, r.opts.GateTimeout, r.closeoutRule.Stated)
			}
			r.record(tick, StageCloseoutHeld, "CI on the epic PR #%d is pending; the close-out is held", pr.Number)
			if _, err := r.checkpoint(runstate.StateRunning,
				fmt.Sprintf("CI on the epic PR #%d is pending; the close-out of %s is held", pr.Number,
					r.opts.EpicID)); err != nil {
				return err
			}
			r.sleep(r.pollInterval)
		}
	}
}

// reconciler-decision:D32:begin:prbase — the local close-out RESOLVES the
// PR base from the refs git holds; the Workflow host takes it named by the
// submitter (decisions/reconciler-parity.json, D32).
// prBase is the ref the epic PR asks to merge into: the default branch of
// the remote, resolved from the refs git holds rather than guessed — the
// remote's own HEAD first, then the checkout's current branch, then the
// operator's --base when it named a ref, and `main` as the documented
// convention when nothing else resolves.
func (r *Reconciler) prBase() string {
	if branch := r.remoteDefaultBranch(); branch != "" {
		return branch
	}
	if out, err := r.git.run("", "symbolic-ref", "--short", "HEAD"); err == nil && out != "" {
		return out
	}
	if r.opts.BaseRef != "" && r.opts.BaseRef != "HEAD" {
		return r.opts.BaseRef
	}
	return "main"
}

// reconciler-decision:D32:end:prbase

// gateCloseoutClose is the close-out's OTHER CI gate (tick sqx): the one
// over the head the admission's green CI is not evidence about. The
// admission checks the epic PR's head as it stands when the close-out
// STARTS; the close-out then writes — a retro, the learnings it compacts —
// and those integrate onto the epic branch, which IS the PR's head, so CI
// runs again on a tree nobody gated. The pwp run is why the gap is not
// theoretical: its close-out committed records a public-repo guard then
// failed on, the admission stayed green, and the close stood behind
// evidence about a head that no longer existed.
//
// It runs at the CLOSE — after the integrated gate, before the tick closes —
// and re-derives CI from the PR the way the admission does, never from a
// checkpoint, because CI's answer changes with every push and this push was
// the close-out's own. A repo that declares no rule has no PR to ask and no
// CI to wait on, so the gate is a no-op there, exactly as the admission is.
//
// One deliberate symmetry with the admission's loop (tick ox0): CI
// reporting NOTHING on the head is a wait here for the same reason it is a
// wait at the admission — between a push and the forge's first check run
// there is a window where nothing has reported yet, and here the run
// itself just pushed the head CI is asked about, so the window is
// guaranteed; at the admission a PR the run opened is in the same window,
// which is why refusing at the first absent answer was the same race there.
// So `none` holds against the same clock the admission's pending wait uses,
// and only a `none` that survives that bound is the workflow's failure again.
func (r *Reconciler) gateCloseoutClose(ctx context.Context, marker attemptHandle, merged merge) error {
	tick := marker.TickID
	if !r.closeoutRule.Declared {
		// No PR, no CI — but the findings gate still runs (tick aqm): the hold
		// the per-tick close carried moved HERE, not away, and a repository
		// that declares no rule hands over through the same close-out this
		// gate protects. The durable record under .ticfac/ is the view the
		// person reads, listed by `ticfac findings`.
		if refusal, err := r.gateCloseoutOnFindings(tick, 0); err != nil {
			return err
		} else if refusal != nil {
			return refusal
		}
		return nil
	}
	if r.opts.PullRequests == nil {
		// Defensive, and the same fail-closed answer the admission keeps: New
		// refuses this configuration before anything is claimed, and the
		// admission for this very tick already needed the surface to pass.
		return r.refuse(RefusedCloseoutForge, tick,
			"the repository declares the PR + CI close-out rule, and this build has no code-hosting surface "+
				"to gate the close-out's close on: %s", r.closeoutRule.Stated)
	}

	// The PR is looked up rather than remembered from the admission, for the
	// same reason the admission looks before it opens: the forge is the PR's
	// authority the way origin is the run's, and a PR a resumed run — or a
	// person — closed or reopened in between must be read as it stands.
	head, base := r.branch, r.prBase()
	pr, err := r.opts.PullRequests.Find(ctx, head, base)
	if err != nil {
		return r.refuse(RefusedCloseoutPR, tick,
			"the close-out of %s cannot be gated on CI: the code-hosting surface could not say whether the epic PR "+
				"still exists for %s (→ %s): %v. The rule the repository declares is: %s",
			tick, head, base, err, r.closeoutRule.Stated)
	}
	if pr == nil {
		return r.refuse(RefusedCloseoutPR, tick,
			"the epic PR for %s (→ %s) is gone: the close-out's commits are merged onto %s, but a close-out "+
				"whose rule declares a PR does not close behind a PR that no longer exists. Re-run the epic: the "+
				"admission re-opens the PR and the close is gated again", head, base, r.branch)
	}

	// The CI wait, with the admission's own bound: a CI run on the PR is a
	// gate this run waits on, and the same clock that bounds the admission's
	// wait bounds the close's.
	deadline := r.now().Add(r.opts.GateTimeout)
	for {
		report, ciSHA, ciIsHead, ciErr := r.ciForTree(ctx, pr)
		if ciErr != nil {
			return r.refuse(RefusedCloseoutPR, tick,
				"the close-out of %s cannot be gated on CI: CI on the epic PR #%d could not be read: %v. "+
					"The rule the repository declares is: %s", tick, pr.Number, ciErr, r.closeoutRule.Stated)
		}
		if report.State == forge.CIRed && r.rerunRedCIOnce(ctx, tick, pr, report) {
			report.State = forge.CIPending
		}
		switch report.State {
		case forge.CIGreen:
			// The last write the run owns (tick 4sb): the close gate is the last
			// moment the run holds the PR, and the close-out's OWN attempt —
			// dispatched since the admission — can have drafted a finding the
			// admission's body predated, or a person can have triaged one since.
			// The body the person merges behind is recomposed from the FINAL
			// records, and a forge that cannot take it refuses the close: green
			// CI beside an empty body is the silent merge this write exists to
			// remove. The write is an overwrite, so a resumed close-out that
			// reaches this gate again rewrites the same view and adds nothing.
			body, findings, bodyErr := r.closeoutPRBody()
			if bodyErr != nil {
				return r.refuse(RefusedCloseoutPRBody, tick,
					"the epic PR #%d cannot carry the final review's verdict and the run's findings at the close-out's "+
						"close: the run's own record could not be read to compose them: %v. The rule the repository "+
						"declares is: %s", pr.Number, bodyErr, r.closeoutRule.Stated)
			}
			if err := r.carryOntoThePR(ctx, tick, *pr, body, findings); err != nil {
				return err
			}
			// The integrity check that replaces the per-tick hold (tick aqm): a
			// finding the run filed that does not appear on the PR it is about
			// to hand over is a finding on the floor whatever the write just
			// claimed — so the PR is READ BACK from the forge and checked against
			// every filed finding before the hand-over. This, not a hold per tick,
			// is what stops one falling on the floor — and it belongs at the one
			// gate a person actually reads.
			if err := r.gateCloseoutCarriesFindings(ctx, tick, head, base); err != nil {
				return err
			}
			// The hold the per-tick gate became (tick aqm): the close-out does
			// not hand over while any finding of the run is untriaged. The body
			// was rewritten from the final records immediately above, so the
			// person this hold asks for reads every finding's full text on the
			// PR they are about to judge — one decision point, at the end,
			// instead of one per tick mid-run.
			if refusal, err := r.gateCloseoutOnFindings(tick, pr.Number); err != nil {
				return err
			} else if refusal != nil {
				return refusal
			}
			r.record(tick, StageCloseoutCloseGated,
				"CI is green on %s, which carries the close-out's own commits; the close proceeds",
				ciSubject(ciSHA, ciIsHead, pr))
			return nil
		case forge.CIRed:
			// The refusal is typed apart from the admission's (tick sqx):
			// both are red CI, but the repairs point at different writers —
			// the epic's tree at the admission, the close-out's own writes
			// here — and a person reading the run's record reads WHICH red
			// CI stopped the run from the reason alone.
			r.setTick(tick, "rejected")
			r.record(tick, StageCloseoutHeld,
				"CI on the epic PR #%d is red on the head that includes the close-out's own commits: %s failed",
				pr.Number, strings.Join(report.Failing, ", "))
			return r.refuse(RefusedCloseoutCIOnClose, tick,
				"CI is red on the epic PR #%d (%s) on the head that includes the close-out's own commits (merged as %s): "+
					"the failing job is %s. The close-out of %s is NOT closed behind it, and the rule the repository "+
					"declares is: %s. The close-out's commits are already on %s, so the repair is what the failing job "+
					"names — the retro, the learnings or the records the close-out itself wrote: fix them, push to %s, "+
					"and run the epic again under this run id — the close gate re-derives CI from the PR, it does not "+
					"trust the admission's green", pr.Number, pr.URL, short(merged.GateSHA),
				strings.Join(report.Failing, ", "), r.opts.EpicID, r.closeoutRule.Stated, r.branch, r.branch)
		case forge.CINone, forge.CIPending:
			// A pending CI — and a silent one, in the window after this run's
			// own push — is a hold, not a failure; see the comment above the
			// loop for why `none` waits here where the admission refuses it.
			if now := r.now(); now.After(deadline) {
				reason := RefusedCloseoutCIPending
				what := "was still pending"
				if report.State == forge.CINone {
					reason = RefusedCloseoutCIAbsent
					what = "had still produced no check runs"
				}
				r.setTick(tick, "rejected")
				r.record(tick, StageCloseoutHeld, "CI on the epic PR #%d %s on the head that includes the "+
					"close-out's own commits when the run's bound fired", pr.Number, what)
				return r.refuse(reason, tick,
					"CI on the epic PR #%d (%s) %s %s after the close-out began waiting on the head that includes "+
						"its own commits, so this run does not close the close-out of %s: re-run the epic once CI "+
						"concludes, and this gate is re-derived from the PR, not rediscovered. The rule the repository "+
						"declares is: %s", pr.Number, pr.URL, what, r.opts.GateTimeout, r.opts.EpicID, r.closeoutRule.Stated)
			}
			r.record(tick, StageCloseoutHeld,
				"CI on the epic PR #%d is %s on the head that includes the close-out's own commits; the close is held",
				pr.Number, report.State)
			if _, err := r.checkpoint(runstate.StateRunning,
				fmt.Sprintf("CI on the epic PR #%d is %s on the head that includes the close-out's own commits; "+
					"the close-out of %s is held", pr.Number, report.State, r.opts.EpicID)); err != nil {
				return err
			}
			r.sleep(r.pollInterval)
		}
	}
}

// repoConfigPath is where the run reads the target repository's own config:
// beside the runners.toml the gate reads, in the same `.tick/`.
func repoConfigPath(repo string) string {
	return filepath.Join(repo, ".tick", "config.md")
}

// ------------------------------------------------- the findings gates (aqm) ---

// The per-tick untriaged-findings hold (tick 7vn) is gone, and these two
// gates at the close-out are what replaced it (tick aqm — the operator's
// 2026-09-19 decision, after epic-ncv stopped five times for nothing but
// untriaged findings):
//
//   - a tick whose findings are untriaged CLOSES, the run continues, and the
//     finding rides — durably, under .ticfac/runs/<run-id>/findings/, and on
//     the epic PR when the repository declares the rule;
//   - the CLOSE-OUT does not hand over while any finding of the run is
//     untriaged, naming them: one decision point at the end, where a person
//     is already being asked to look, instead of one per tick mid-run —
//     and unlike the old hold it cannot re-dispatch the review that reported
//     the finding, so a thorough reviewer is never punished with another
//     round;
//   - the close-out REFUSES the hand-over when a filed finding does not
//     appear on the PR, read back from the forge: the PR is the one gate a
//     person actually reads, and a filed finding missing from it is a
//     finding on the floor whatever green CI says beside it.

// filedFindings is every finding the run drafted, whatever its triage state.
// Read from ORIGIN, fetched first, for the same reason the untriaged read
// is: the carried check is a comparison between the durable record and the
// PR as it stands NOW, not between a memory and a write.
func (r *Reconciler) filedFindings() ([]runstate.Finding, error) {
	if r.store == nil {
		return nil, nil
	}
	if _, err := r.store.Fetch(); err != nil {
		return nil, err
	}
	return r.store.Findings()
}

// gateCloseoutCarriesFindings is the integrity check that replaces the
// per-tick hold (tick aqm): every finding the run filed must appear on the
// epic PR the close-out is about to hand over. The PR is read BACK from the
// forge — the body the forge answers with, never the body this process
// believes it wrote — because the check exists to catch the write that lied,
// the composition that omitted, and the PR body somebody stripped a finding
// from; checking the run's own memory of the write would catch none of them.
//
// A run that filed no findings checks nothing and touches the forge not at
// all: the gate is about findings, and a run that found nothing must not pay
// a round trip to prove it.
func (r *Reconciler) gateCloseoutCarriesFindings(ctx context.Context, tick, head, base string) error {
	findings, err := r.filedFindings()
	if err != nil {
		return fmt.Errorf("read the run's findings to check the epic PR carries them: %w", err)
	}
	if len(findings) == 0 {
		return nil
	}
	pr, err := r.opts.PullRequests.Find(ctx, head, base)
	if err != nil {
		return r.refuse(RefusedCloseoutPRFindings, tick,
			"the close-out cannot say whether the epic PR carries every filed finding, so it does not hand "+
				"over: the code-hosting surface could not read the PR for %s (→ %s) back: %v. The rule the repository "+
				"declares is: %s", head, base, err, r.closeoutRule.Stated)
	}
	if pr == nil {
		return r.refuse(RefusedCloseoutPRFindings, tick,
			"the epic PR for %s (→ %s) is gone between the close gate's write and the check that the findings it "+
				"carries are on it, so the close-out does not hand over behind a PR it cannot read: re-run the epic and "+
				"the admission re-opens the PR. The rule the repository declares is: %s", head, base, r.closeoutRule.Stated)
	}
	var missing []string
	for _, finding := range findings {
		if !strings.Contains(pr.Body, finding.Title) {
			missing = append(missing, fmt.Sprintf("%q (key %s, %s, severity %s, tick %s)",
				finding.Title, finding.Key, finding.Kind, finding.Severity, finding.TickID))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return r.refuse(RefusedCloseoutPRFindings, tick,
		"the epic PR #%d (%s) does not carry %d finding(s) the run filed — %s — and the close-out does not hand "+
			"over behind a PR a filed finding is missing from: the PR is the one gate a person actually reads, and a "+
			"finding that is not on it is a finding on the floor whatever green CI says beside it. The close gate just "+
			"wrote the body, so the repair is whatever dropped the finding — the forge's write, the body's composition, "+
			"or a PR body somebody stripped — and re-running the epic under this run id recomposes and rewrites the "+
			"body from the record before checking again. The rule the repository declares is: %s",
		pr.Number, pr.URL, len(missing), strings.Join(missing, "; "), r.closeoutRule.Stated)
}

// rerunRedCIOnce re-runs the failed CI jobs behind a red report ONCE, and
// answers whether it did - in which case the caller holds as pending and the
// ordinary bounded wait takes over.
//
// Why at all: on 2026-09-23 four epic close-outs stopped on red CI that passed
// on a plain re-run of the same head (ticfac 3cq, a load-dependent timing
// failure in the TypeScript suite). Each stop needed a person to resume the
// run, which an unattended factory cannot wait for.
//
// Why only once, and why it is recorded: a re-run can hide a real
// intermittent failure. GitHub's own run_attempt makes 'once' durable - a
// restarted run cannot retry again - so a job red twice still refuses the
// close-out, and the feed says a re-run happened.
func (r *Reconciler) rerunRedCIOnce(ctx context.Context, tick string, pr *forge.PullRequest, report forge.CIReport) bool {
	rerunner, ok := r.opts.PullRequests.(forge.CIRerunner)
	if !ok || len(report.FailingRuns) == 0 {
		return false
	}
	rerun, err := rerunner.RerunFailedOnce(ctx, report.FailingRuns)
	if err != nil {
		r.record(tick, StageCloseoutHeld, "CI on the epic PR #%d is red (%s) and its failed jobs could not be re-run: %v",
			pr.Number, strings.Join(report.Failing, ", "), err)
	}
	if len(rerun) == 0 {
		return false
	}
	r.record(tick, StageCloseoutHeld,
		"CI on the epic PR #%d is red (%s); the failed jobs of workflow run(s) %v were re-run ONCE - an automatic "+
			"intervention, recorded because a re-run can hide a real intermittent failure. A second red refuses the "+
			"close-out", pr.Number, strings.Join(report.Failing, ", "), rerun)
	return true
}
