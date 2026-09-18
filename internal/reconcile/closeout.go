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

// admitCloseout is the close-out phase's admission precondition: the rule
// the target repo declares, enforced by the run before the close-out job is
// claimed or dispatched — never left to the close-out worker's diligence.
//
// It returns nil when the close-out is admitted (the repo declares no rule,
// or the PR is open and CI is green on it), and a typed refusal otherwise,
// each reason naming which half of the precondition is unmet, because the
// halves send the next repair somewhere different: a missing surface at the
// host, an unopenable PR at the forge's credential, an absent CI at the
// workflow's triggers, a red CI at the failing job the message names, a
// pending CI at the clock.
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
	// second one.
	head, base := r.branch, r.prBase()
	pr, err := r.opts.PullRequests.Find(ctx, head, base)
	if err != nil {
		return r.refuse(RefusedCloseoutPR, tick,
			"the close-out cannot be admitted: the code-hosting surface could not say whether a PR exists for "+
				"%s (→ %s): %v. The rule the repository declares is: %s", head, base, err, r.closeoutRule.Stated)
	}
	if pr == nil {
		title := fmt.Sprintf("epic %s: integrate %s", r.opts.EpicID, head)
		body := fmt.Sprintf("ticfac run %s opened this PR because the repository declares the PR + CI close-out rule "+
			"in .tick/config.md: the epic close-out may not complete until CI (%s) is green on this PR.", r.runID,
			r.closeoutRule.CIWorkflow)
		opened, openErr := r.opts.PullRequests.Open(ctx, head, base, title, body)
		if openErr != nil {
			return r.refuse(RefusedCloseoutPR, tick,
				"no PR exists for %s (→ %s) and opening one failed, so the close-out's precondition is unmet and "+
					"cannot be satisfied by this run: %v. The rule the repository declares is: %s",
				head, base, openErr, r.closeoutRule.Stated)
		}
		pr = opened
		r.record(tick, StagePROpened, "the epic PR #%d (%s → %s) is open: %s", pr.Number, head, base, pr.URL)
	} else {
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

	// The CI half. A pending CI is a wait the run bounds — the same bound a
	// gate command gets, because a CI run on the PR is a gate this run waits
	// on rather than one it runs. A state CI leaves (green, red, nothing at
	// all) is a typed refusal naming which one it was.
	deadline := r.now().Add(r.opts.GateTimeout)
	for {
		report, ciErr := r.opts.PullRequests.CI(ctx, *pr)
		if ciErr != nil {
			return r.refuse(RefusedCloseoutPR, tick,
				"the close-out cannot be admitted: CI on the epic PR #%d could not be read: %v. The rule the "+
					"repository declares is: %s", pr.Number, ciErr, r.closeoutRule.Stated)
		}
		switch report.State {
		case forge.CIGreen:
			r.record(tick, StageCloseoutAdmitted, "CI is green on the epic PR #%d; the close-out is admitted", pr.Number)
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
			return r.refuse(RefusedCloseoutCIAbsent, tick,
				"no CI has run on the epic PR #%d (%s): %s has produced no check runs on its head, so the "+
					"close-out's precondition is unsatisfiable by waiting — the workflow may not trigger on "+
					"pull_request at all. The rule the repository declares is: %s",
				pr.Number, pr.URL, r.closeoutRule.CIWorkflow, r.closeoutRule.Stated)
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

// prBase is the ref the epic PR asks to merge into: the default branch of
// the remote, resolved from the refs git holds rather than guessed — the
// remote's own HEAD first, then the checkout's current branch, then the
// operator's --base when it named a ref, and `main` as the documented
// convention when nothing else resolves.
func (r *Reconciler) prBase() string {
	if out, err := r.git.run("", "symbolic-ref", "--short", "refs/remotes/"+r.opts.Remote+"/HEAD"); err == nil && out != "" {
		return strings.TrimPrefix(out, r.opts.Remote+"/")
	}
	if out, err := r.git.run("", "symbolic-ref", "--short", "HEAD"); err == nil && out != "" {
		return out
	}
	if r.opts.BaseRef != "" && r.opts.BaseRef != "HEAD" {
		return r.opts.BaseRef
	}
	return "main"
}

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
// One deliberate difference from the admission's loop: CI reporting
// NOTHING on the head is a wait here rather than an immediate refusal. At
// the admission, `none` means the workflow does not trigger on
// pull_request at all — unsatisfiable by waiting. At the close, the run
// itself just pushed the head CI is asked about, and between that push and
// the forge's first check run there is a window where nothing has reported
// yet: refusing it as unsatisfiable would end a healthy run on a race. So
// it is held against the same clock the admission's pending wait uses, and
// only a `none` that survives that bound is the workflow's failure again.
func (r *Reconciler) gateCloseoutClose(ctx context.Context, marker attemptHandle, merged merge) error {
	tick := marker.TickID
	if !r.closeoutRule.Declared {
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
		report, ciErr := r.opts.PullRequests.CI(ctx, *pr)
		if ciErr != nil {
			return r.refuse(RefusedCloseoutPR, tick,
				"the close-out of %s cannot be gated on CI: CI on the epic PR #%d could not be read: %v. "+
					"The rule the repository declares is: %s", tick, pr.Number, ciErr, r.closeoutRule.Stated)
		}
		switch report.State {
		case forge.CIGreen:
			r.record(tick, StageCloseoutCloseGated,
				"CI is green on the epic PR #%d on the head that includes the close-out's own commits (%s); the close proceeds",
				pr.Number, short(merged.GateSHA))
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
