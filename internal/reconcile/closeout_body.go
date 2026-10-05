package reconcile

import (
	"context"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The epic PR's body (tick 4sb): the write half of the PR + CI close-out rule.
//
// Until this tick the forge could find, open and read the epic PR but never
// write to one, so the PR the rule demanded was a page that carried CI status
// and nothing else — not the review's verdict, not one finding. A person
// merging an epic would have pressed the merge button behind a green check
// while the review's answer sat in `.ticfac/`, unread by exactly the person
// the merge belongs to. On an epic whose review said NOT READY that is not a
// missing nicety; it is the safety net failing silently.
//
// The write is an EDIT of the PR's body, never a comment, and the choice is
// load-bearing (the seam's own doc says the other half): the durable record
// is the run's state — the decision the review's validated answer landed as,
// the findings drafts a worker's reports became — and the body is a VIEW of
// that record, recomposed and overwritten every time the close-out writes it.
// A comment would APPEND, and no resume survives an append honestly: a run
// cut after one write and resumed into a second posts the findings twice, and
// the second copy looks as authoritative as the first while being noise. An
// overwrite is idempotent by construction — the same record composes the
// same body — so nothing has to remember whether the body was already
// written, and the record the PR must agree with stays the one record there
// is.
//
// The close-out composes the body at two moments, both of them its own:
//
//   - the ADMISSION, where the PR is opened (or found) — so the verdict and
//     the findings are on the PR from the moment it exists;
//   - the CLOSE gate, the last moment the run owns the PR — because the
//     close-out's own attempt can draft a finding the admission's body
//     predated, and a person must not merge behind a body the run's final
//     state contradicts.
//
// This is the hard prerequisite for aqm (the operator's 2026-09-19 decision
// that untriaged findings are carried to the epic PR instead of holding the
// run): carrying anything anywhere is a write, and until this file existed
// the forge had none.

// closeoutPRBody composes the body the epic PR carries: the rule the
// repository declares — the sentence the PR was always opened under, kept as
// the opening so the PR still says why it exists — then the final review's
// verdict, then every finding the run drafted, with each finding's own text
// and triage state. It reads only the run's durable state, so a resumed
// close-out composes the same body from the same record: that is the whole
// idempotence argument, and it is why the composition takes no parameters —
// there is nothing to pass that the record does not already hold.
//
// It returns the body and the number of findings it carries, for the feed
// line the write leaves.
func (r *Reconciler) closeoutPRBody() (string, int, error) {
	return r.composePRBody("")
}

// composePRBody is closeoutPRBody with one section the record does not hold
// appended before the closing line: the readying's account of what it checked
// the PR against (land.go). Everything else is recomposed from the record.
func (r *Reconciler) composePRBody(readinessSection string) (string, int, error) {
	if _, err := r.store.Fetch(); err != nil {
		return "", 0, fmt.Errorf("read the run state on %s: %w", r.branch, err)
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return "", 0, fmt.Errorf("read the run's decisions: %w", err)
	}
	// The FINAL review: the last review decision the run recorded. Decisions
	// come back in decision order, so the last one wins; a re-run never buys
	// the answer again (recordDecision is create-if-absent), so "final" is
	// stable across resumes however many there were.
	final := -1
	for i := range decisions {
		if decisions[i].Role == "review-epic" {
			final = i
		}
	}
	findings, err := r.store.Findings()
	if err != nil {
		return "", 0, fmt.Errorf("read the run's findings: %w", err)
	}

	var body strings.Builder
	fmt.Fprintf(&body,
		"ticfac run %s opened this PR because the repository declares the PR + CI close-out rule "+
			"in .tick/config.md: the epic close-out may not complete until CI (%s) is green on this PR.",
		r.runID, r.closeoutRule.CIWorkflow)

	// For the REVIEWER (operator, 2026-09-28): the PR is the touch point an
	// epic handed over to ticfac comes back through, read by a person and
	// their own agent. So the body opens with what they need first — where
	// to look, what the epic did, and its done — before the record's detail.
	absorptions, err := r.store.Absorptions()
	if err != nil {
		return "", 0, fmt.Errorf("read the run's absorption decisions: %w", err)
	}
	// The worker-proposed amendments to the epic's OWN record (tick 7sn) —
	// read here so the "where to look first" opening and the amendments
	// section below compose from one fetch of the same durable records.
	amendments, err := r.store.Amendments()
	if err != nil {
		return "", 0, fmt.Errorf("read the run's amendments: %w", err)
	}
	body.WriteString("\n\n## Where to look first\n\n")
	body.WriteString(r.lookFirst(decisions, final, findings, absorptions, amendments))
	body.WriteString("\n## What this epic did\n\n")
	body.WriteString(r.epicSummary())
	body.WriteString("\n## Definition of done\n\n")
	body.WriteString(r.doneEvidence())

	// The workers that stopped to ask (tick tyd): the questions that hold for
	// a person first — the PR is where they are asked — then how the run
	// answered the others. Absent when no worker asked anything.
	body.WriteString(r.blockedHoldsSection())

	body.WriteString("\n## The review's verdict\n\n")
	if final < 0 {
		// The body states the absence rather than staying silent about it:
		// a PR that says nothing about the review reads as "no review found
		// anything", which is a verdict nobody gave.
		body.WriteString("No review decision is recorded for this run: the epic reached its " +
			"close-out without a review whose validated answer landed in the run's state.\n")
	} else {
		// The typed verdict first (tick b50): a person merging reads a
		// verdict, not prose — and the record this paragraph composes from can
		// no longer spell a NOT READY review as ready-to-merge, because the
		// review's answer carries its own review_verdict and not the collect
		// vocabulary's word.
		reviewVerdict(&body, decisions[final])
	}

	body.WriteString("\n## Findings this run drafted\n\n")
	if len(findings) == 0 {
		body.WriteString("This run drafted no findings.\n")
	} else {
		// Grouped by the tick whose attempt reported the finding (tick aqm):
		// a person triaging at the close-out — or judging the merge — reads
		// what each tick's work discovered, beside that tick's own record,
		// rather than one flat pile. Full text, not a count, because a link
		// nobody opens is a finding on the floor.
		body.WriteString("Every finding this run drafted, grouped by the tick whose attempt reported it, each with " +
			"its own text and triage state.\n")
		var order []string
		byTick := make(map[string][]runstate.Finding)
		for _, finding := range findings {
			if _, seen := byTick[finding.TickID]; !seen {
				order = append(order, finding.TickID)
			}
			byTick[finding.TickID] = append(byTick[finding.TickID], finding)
		}
		for _, tick := range order {
			fmt.Fprintf(&body, "\n### Tick %s\n\n", tick)
			for i, finding := range byTick[tick] {
				fmt.Fprintf(&body, "%d. %s — %s (%s, for %s), triaged %s\n",
					i+1, finding.Kind, finding.Title, finding.Severity, targetName(finding.Target), finding.Status)
				// The linkage mark (tick nfo): the claim against the epic's
				// definition of done — which [A<n>] item the reporter says is
				// broken, demonstrated by what — or the unlinked mark. The PR
				// is where a person decides, and a claim the decision cannot
				// see is evidence the absorption decision does not have.
				fmt.Fprintf(&body, "   %s\n", finding.LinkageText())
				if finding.Body != "" {
					// The finding's own text, indented under its identity: "every
					// finding's text" is the acceptance, not just every title.
					fmt.Fprintf(&body, "\n   %s\n", strings.ReplaceAll(finding.Body, "\n", "\n   "))
				}
			}
		}
	}

	// FINDINGS ROUTED TO OTHER REPOSITORIES (the epic-2jn close-out stall): a
	// cross-repository finding never gates this epic and never holds it, so
	// the PR is where a person sees that it exists and where it went — filed
	// in the target's tracker, or a backlog tick here naming the target.
	var routed []runstate.Finding
	for _, finding := range findings {
		if finding.Target != "" {
			routed = append(routed, finding)
		}
	}
	if len(routed) > 0 {
		body.WriteString("\n## Findings routed to other repositories\n\n")
		body.WriteString("This run cannot fix another repository, so none of these gates this epic whatever it " +
			"claims; each was filed in its target's tracker or backlogged here naming the target. Each one's " +
			"full text is under the tick that reported it above.\n\n")
		for _, finding := range routed {
			// The key, not the title: the title is listed once, above, and
			// the PR's read-back integrity check counts on that.
			where := finding.Status
			if finding.PromotedAs != "" {
				where = "promoted as " + finding.PromotedAs
			}
			fmt.Fprintf(&body, "- finding %s (tick %s) → %s: %s\n", short(finding.Key), finding.TickID,
				finding.Target, where)
		}
	}

	// WHAT THIS EPIC ABSORBED (tick jlv): the close-out must be able to say,
	// for this epic, what was absorbed, against which acceptance item,
	// whether the verdict was observed or predicted, by WHICH MODEL it was
	// predicted, and — for each prediction later checked — whether it was
	// right. An epic that absorbed silently is an epic whose shape changed
	// with no account of why, so the body carries every absorption decision
	// with its item id, its basis, its answering model and the score of each
	// checked prediction, composed from the same durable records the retro
	// reads — never from memory of what the run intended. The write is an
	// overwrite like the whole body is, so a resumed close-out composed after
	// a second scoring pass carries each fact once.
	scored := map[string]runstate.PredictionScore{}
	if scores, err := r.store.PredictionScores(); err != nil {
		return "", 0, fmt.Errorf("read the run's checked predictions: %w", err)
	} else {
		for _, score := range scores {
			scored[score.Key] = score
		}
	}
	body.WriteString("\n## What this epic absorbed\n\n")
	if len(absorptions) == 0 {
		body.WriteString("This run decided no finding's absorption: nothing was absorbed into " +
			"the epic, and nothing was backlogged by the run itself.\n")
	} else {
		body.WriteString("Every finding the run itself triaged, from the decision records under " +
			"`.ticfac/runs/" + r.runID + "/absorptions/`, with the score of each prediction the " +
			"close-out checked against what the done actually did.\n")
		for _, record := range absorptions {
			fmt.Fprintf(&body, "\n- tick %s — ", record.TickID)
			if record.Basis == runstate.AbsorptionRule {
				fmt.Fprintf(&body, "%s; %s", verdictLine(record), placementLine(record))
			} else if record.Gating {
				fmt.Fprintf(&body, "absorbed into the running epic: %s, basis %s%s%s; %s",
					verdictLine(record), record.Basis, confidenceLine(record), modelLine(record), placementLine(record))
			} else {
				fmt.Fprintf(&body, "promoted to a backlog tick with an owner: %s, basis %s%s%s",
					verdictLine(record), record.Basis, confidenceLine(record), modelLine(record))
			}
			if score, checked := scored[record.Key]; checked {
				fmt.Fprintf(&body, ". The prediction was later CHECKED: the command %s answered %s on %s — the tree the finding was made on, not the one carrying the absorbed fix — and the prediction was %s",
					score.Check.ID, score.Result, short(score.Commit), score.Score)
			} else if record.Basis == runstate.AbsorptionPredicted && record.Gating {
				if record.ItemID != "" {
					body.WriteString(". The prediction was not checked: no command for the item produced evidence before the close-out")
				} else {
					body.WriteString(". The prediction was never checkable by one command: it named no single item (the fallback named every item at risk in its reason)")
				}
			}
			body.WriteString("\n")
		}
	}

	// AMENDMENTS TO THE EPIC'S OWN RECORD (tick 7sn): every note a WORKER
	// proposed to the record the close-out scores the acceptance from, each
	// with the operator's decision on it — a pending or rejected one is what
	// the close-out's amendments gate holds the hand-over on, and this is the
	// text the operator reads to decide. Full text, like the findings: a
	// decision made against a summary is a decision made against prose
	// nobody scored.
	body.WriteString("\n## Amendments to the epic's record\n\n")
	if len(amendments) == 0 {
		// The absence is stated from the RECORD's view, and says so: a worker's
		// note applied by an older run — before these records existed — is
		// outside them, and the close-out's retro names such exceptions from
		// the epic's notes themselves. An absence that read as "no worker ever
		// wrote to the record" would be a claim the record cannot make.
		body.WriteString("No worker-proposed amendment to the epic's record is filed with this run. A note a " +
			"worker applied in an earlier run — before the amendment records existed — is outside these " +
			"records, and the close-out's retro names it from the epic's notes.\n")
	} else {
		body.WriteString("Every note a worker proposed to the epic's own record — the one the acceptance is scored " +
			"from — applied by the run, and waiting for or carrying the operator's decision. A worker's words on " +
			"it are a claim, never the operator's word: the close-out does not hand over while one is undecided " +
			"(confirm lets it stand as the operator's own, reject disowns it).\n")
		for _, amendment := range amendments {
			switch amendment.Status {
			case runstate.AmendmentConfirmed:
				fmt.Fprintf(&body, "\n- CONFIRMED — %q (key %s, %s on the epic, proposed by %s): the operator let it "+
					"stand, by %s at %s\n", amendment.FirstLine(), short(amendment.Key), amendment.Field,
					r.attemptName(amendment.ProposedBy, amendment.Attempt), amendment.DecidedBy, amendment.DecidedAt)
			case runstate.AmendmentRejected:
				fmt.Fprintf(&body, "\n- REJECTED — %q (key %s, %s on the epic, proposed by %s): the operator disowned "+
					"it, by %s at %s, and the close-out does not hand over while the record still carries it\n",
					amendment.FirstLine(), short(amendment.Key), amendment.Field,
					r.attemptName(amendment.ProposedBy, amendment.Attempt), amendment.DecidedBy, amendment.DecidedAt)
			default:
				fmt.Fprintf(&body, "\n- PENDING — %q (key %s, %s on the epic, proposed by %s): waiting for the "+
					"operator — confirm with `ticfac amendment %s %s --confirm --by <who>`, reject with --reject\n",
					amendment.FirstLine(), amendment.Key, amendment.Field,
					r.attemptName(amendment.ProposedBy, amendment.Attempt), r.opts.EpicID, amendment.Key)
			}
			fmt.Fprintf(&body, "\n  %s\n", strings.ReplaceAll(amendment.Value, "\n", "\n  "))
		}
	}

	body.WriteString(readinessSection)

	fmt.Fprintf(&body,
		"\nThe durable record is the run's own state under .ticfac/runs/%s/ on %s; this body is the "+
			"view of it, recomposed from the record and overwritten — never appended to — so a resumed "+
			"close-out carries these facts once, however many times it writes.\n",
		r.runID, r.branch)
	return body.String(), len(findings), nil
}

// reviewVerdict writes the PR body's statement of the final review's
// verdict: the typed verdict first — a person merging reads a verdict, not
// prose — then the stated rule for a NOT READY (see reviewverdict.go for
// why the carry, not the hold, is the rule), then the review's own summary.
// A decision recorded before the typed field existed states what it has
// rather than nothing, because a PR that says nothing about the review reads
// as "no review found anything", which is a verdict nobody gave.
func reviewVerdict(b *strings.Builder, decision runstate.Decision) {
	b.WriteString(reviewVerdictParagraph(decision))
}

// carryOntoThePR writes the composed body onto the epic PR: the record the
// person merging reads, beside the CI status the rule already put there.
//
// It is the one write path the close-out has, called from the admission and
// from the close gate, so the invariant holds at both moments with the same
// code: the PR carries the final review's verdict and every finding the run
// has drafted as of the moment it is written. The write is an overwrite (see
// the seam), so a resumed close-out that reaches this again rewrites the same
// view and duplicates nothing.
//
// A body the forge refuses to write is a typed refusal, not a warning: a
// close-out admitted — or closed — behind a PR that carries nothing is the
// silent merge this tick exists to stop, whatever green CI says beside it.
func (r *Reconciler) carryOntoThePR(ctx context.Context, tick string, pr forge.PullRequest, body string, findings int) error {
	if err := r.opts.PullRequests.UpdateBody(ctx, pr, body); err != nil {
		return r.refuse(RefusedCloseoutPRBody, tick,
			"the epic PR #%d (%s) could not be given the body that carries the final review's verdict and the "+
				"run's findings: %v. The close-out is not admitted behind it: a person merging on CI status "+
				"alone — reading no verdict, no finding — is exactly the failure the rule's write half exists "+
				"to remove. The rule the repository declares is: %s",
			pr.Number, pr.URL, err, r.closeoutRule.Stated)
	}
	r.recordPRBody(tick, pr.Number, findings)
	return nil
}

// recordPRBody is the one feed line the write leaves, in the one shape both
// write sites use — the open, the found-path rewrite and the close gate's
// rewrite — so "the PR carries the record" reads identically wherever it
// happened from.
func (r *Reconciler) recordPRBody(tick string, number, findings int) {
	r.record(tick, StagePRBodyWritten,
		"the epic PR #%d carries the final review's verdict and the %d finding(s) this run drafted",
		number, findings)
}
