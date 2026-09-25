package gating

import (
	"context"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The oracle half of gvc's two-tier decision (tick pzp). Where the Predictor
// (tick bse) guesses over the items that cannot be checked yet, the Oracle
// RUNS the items that can — and the rule this file exists to establish, and
// nothing later may weaken it: WHERE AN ITEM CAN BE RUN, RUNNING WINS. No
// classifier overrides an observation. A prediction that disagrees with a run
// is the prediction being wrong, and that is the whole reason the oracle is
// built first.
//
// The rule is enforced structurally, not by instruction: the Predictor never
// offers a runnable item to its classifier (see Predict), and every verdict
// this file writes carries BasisObserved — a typed field, never a convention —
// so a record a classifier guessed at and a record a command answered can
// never drift into indistinguishable prose.

// Result is what one run of a declared command adds up to, in the gate's own
// vocabulary (runstate.CheckResults): `pass` and `fail` are verdicts about the
// item the command proves; `error` and `skipped` are NOT — a command that
// could not run or was skipped has produced no evidence about the item at
// all, and the oracle refuses to read either as the other.
type Result string

const (
	// ResultPass: the command ran to zero and the item it proves is
	// demonstrated, whatever else is true.
	ResultPass Result = "pass"
	// ResultFail: the command ran to non-zero and the item it proves is
	// observed broken — the done is not reachable with the finding standing,
	// and no judgement is needed.
	ResultFail Result = "fail"
	// ResultError: the command could not run — killed at a bound, refused by
	// the host, dead before it answered. Not a fail: the item is unresolved.
	ResultError Result = "error"
	// ResultSkipped: the command was not run, by the runner's own decision.
	// Not a pass: no evidence about the item exists.
	ResultSkipped Result = "skipped"
)

// maxInlineOutput bounds what a run's output puts in the record, at the gate's
// own bound. Evidence is read by people and by machines; a record that drowns
// in a failing command's output is a record nobody reads.
const maxInlineOutput = 16 << 10

// Runner is the seam to the thing that runs a declared command: the caller
// that owns the worktree, the pin and the shell hands the oracle a runner, so
// this package never learns where commands run or how a commit is checked out.
//
// The seam's contract is command IDS, never shell: [evidence.acceptance]
// binds an item to the ID of the one command that proves it, the table in
// .tick/runners.toml is what authorises shell, and this package never sees a
// command line — the same rule internal/acceptance keeps, for the same
// reason: a package that never resolves an id to shell cannot run anything the
// repository did not declare.
//
// The runner reports the commit each run ran ON. The thing that runs is the
// thing that knows what it ran on; the oracle stamps the verdict with the
// commit it is HANDED and refuses one without it, because an observed record
// keyed by nothing says "once, at some point" — the timestamped opinion the
// key exists to prevent. A test fakes the seam; the real runner is the
// reconciler's, on a worktree pinned to the commit it reports.
type Runner interface {
	// Run executes the declared command with the given id and reports what
	// the run says about itself: the commit, the result, the exit status, the
	// output, and when it began and ended.
	Run(ctx context.Context, command string) (Run, error)
}

// Run is what one executed command reports about itself, in the terms the
// gate's evidence record already takes: when it started and finished, what it
// wrote, how it exited, and what that adds up to — plus the one thing the
// oracle adds to the shape, the commit it ran on.
type Run struct {
	// Commit is the commit the command ran on — the key of the verdict the
	// oracle builds from this run. A run with no commit is a violated seam.
	Commit string
	// Result is what the run adds up to: pass, fail, error or skipped, in
	// the gate's own vocabulary. Anything else is a violated seam.
	Result Result
	// ExitCode is the command's exit status. Zero for pass, the command's
	// own non-zero for fail; meaningless for a run that never answered.
	ExitCode int
	// Stdout and Stderr are what the command wrote. The oracle bounds them
	// at the gate's own bound before they enter the record; redacting host
	// paths out of them is the recording caller's, who knows the host.
	Stdout, Stderr string
	// StartedAt and FinishedAt are when the run began and ended, RFC3339, as
	// the runner observed them.
	StartedAt, FinishedAt string
}

// Evidence is the command's evidence, in the shape the gate's evidence record
// already takes: what ran (check), when (started/finished), how it exited
// (exit_code), what it wrote (output, inline and bounded at the gate's own
// bound), and what that adds up to (result). It reuses runstate's own check
// and output types so the oracle's record and the gate's cannot drift apart
// into two shapes a reader has to translate.
//
// What it deliberately does not carry is the gate record's provenance and
// keying — run id, tick, attempt, persistence URI. Those name WHO recorded
// the evidence and where; this package writes nothing anywhere, and the
// caller that records the verdict on the run branch adds them, the same way
// the reconciler does for the gate's own records.
type Evidence struct {
	Check      runstate.Check        `json:"check"`
	StartedAt  string                `json:"started_at,omitempty"`
	FinishedAt string                `json:"finished_at,omitempty"`
	ExitCode   int                   `json:"exit_code"`
	Output     runstate.InlineOutput `json:"output"`
	Result     string                `json:"result"`
}

// Observation is one runnable item's answer: the item's id, whether the
// command showed the item broken (fail) or demonstrated (pass), and the
// command's evidence. An observation is never produced for a command that
// answered error or skipped, or could not run at all — those produce
// [Unresolved] entries instead, because an observation IS a verdict and a
// verdict about nothing is not one.
type Observation struct {
	// ItemID is the acceptance item the observation is about.
	ItemID string `json:"item_id"`
	// Gating says what the command showed: true — the item is observed broken
	// while the finding stands; false — the item is demonstrated.
	Gating bool `json:"gating"`
	// Evidence is the command's evidence in the gate record's shape.
	Evidence Evidence `json:"evidence"`
}

// Unresolved is an item this verdict refuses to decide, with the reason it
// cannot be. Two kinds of item land here, and neither may be read as safe or
// as broken: an item bound to no command (UNVERIFIED — the classifier's to
// predict, never this verdict's), and an item whose command could not run or
// was skipped (no evidence exists — not the oracle's to guess, and not the
// classifier's either, because a runnable item is never offered to one).
//
// Unresolved is the state that keeps the oracle honest: an oracle that
// silently treated an unrunnable item as demonstrated would manufacture
// exactly the false negative — an epic closed with its goal unmet — that the
// gvc epic exists to prevent, and one that treated it as broken would absorb
// on a guess. It comes back as a named neither, and the decision it feeds
// (the absorption, npq) owns what to do with it.
type Unresolved struct {
	// ItemID is the unresolved acceptance item.
	ItemID string `json:"item_id"`
	// Reason says why the item is unresolved — never what it is worth.
	Reason string `json:"reason"`
}

// Observed is the oracle's whole answer: the finding-level verdict in the
// SAME SHAPE as the predictor's (the two share [Verdict], so a retro reads
// one shape and tells a guess from a measurement by the Basis field), keyed
// by the commit every command ran on, with every runnable item's observation
// beneath it and every item it refuses to decide named as unresolved.
//
// The verdict's scope is what ran: a NOT GATING observed verdict says the
// done is reachable AS FAR AS ANYTHING CAN OBSERVE, and names the unresolved
// items it does not decide rather than letting them be read as safe. The
// absorption (npq) combines this with the prediction tier's; neither half
// decides alone.
type Observed struct {
	Verdict
	// Commit is the commit every command ran on — the record's key, so it
	// means something on a re-derivation rather than being a timestamped
	// opinion. Every run in one observation must name the same commit: a
	// verdict about two trees is not a verdict at all.
	Commit string `json:"commit"`
	// Items is every runnable item's observation, in document order. An
	// observation exists for every command that answered pass or fail — even
	// after one fails, the rest still run, because the record is what the
	// retro reads, not only the verdict.
	Items []Observation `json:"items"`
	// Unresolved is every item this verdict refuses to decide: unverified
	// items, and runs that produced no evidence.
	Unresolved []Unresolved `json:"unresolved,omitempty"`
}

// Oracle decides gating where the done CAN be run: it runs every runnable
// item's bound command and answers whether the done is reachable with the
// finding standing. Items that are not runnable are NOT its answer — they
// come back unresolved and the predictor's to predict. An oracle that guesses
// is not an oracle.
type Oracle struct {
	runner Runner
}

// NewOracle builds an Oracle on the given runner. A nil runner is the same
// no-observation as a runner that cannot run anything: every runnable item
// stays unresolved and the decision falls to the prediction tier's documented
// fallback, which errs toward absorbing — the safe direction by the stated
// asymmetry. It is never a stop: an unattended run has no actor to stop for.
func NewOracle(runner Runner) *Oracle {
	return &Oracle{runner: runner}
}

// Observe runs every RUNNABLE item of the done and answers whether the done
// is reachable with the finding standing, as an OBSERVED verdict naming the
// item and its evidence.
//
// The three returns, and every caller takes all three:
//
//   - (*Observed, "", nil): observations exist — every runnable item's
//     command ran and answered pass or fail, and the verdict says what they
//     showed. The verdict's Basis is always BasisObserved.
//   - (nil, reason, nil): nothing was observed — the done carries no items
//     (the refusal owns it, klq), no item is runnable (each is the
//     classifier's to predict, bse), no runner is configured, or no command
//     could run at all. The reason says whose the decision is; the oracle
//     does not guess where it could not observe.
//   - (nil, "", err): a caller defect or a violated seam — a finding with no
//     id cannot be keyed, a runner reporting a run with no commit or two
//     different commits, a runner answering a result outside the gate's own
//     vocabulary, or a cancelled context. Guessing past any of them would be
//     a decision wearing a defect's costume.
//
// THE COMMANDS RUN ONE AT A TIME, in document order, even after one fails:
// the oracle's verdict is about one tree, and two commands racing on it would
// be two verdicts about different moments of it — the same rule the gate
// runs under, for the same reason. And they run to the END: a second broken
// item behind a first is part of what the finding did, and the absorption
// and the retro read the record, not only the verdict.
func (o *Oracle) Observe(ctx context.Context, finding Finding, done acceptance.Done) (*Observed, string, error) {
	if strings.TrimSpace(finding.ID) == "" {
		return nil, "", fmt.Errorf(
			"a finding with no id cannot be keyed into the observed verdict, so its gating cannot be observed")
	}
	if len(done.Items) == 0 {
		// Never a command: an acceptance with no items is the refusal's to
		// answer for (klq), and standing in for the refusal with an empty
		// observation that reads as "nothing found" would be the exact
		// failure the refusal exists to prevent.
		return nil, "the done carries no acceptance items to observe against: an unenumerated acceptance is a " +
			"refusal's to answer for, not an observation's to guess at — mark the acceptance into [A<n>] items " +
			"first, then decide absorption against them", nil
	}

	var runnable []acceptance.Resolved
	for _, item := range done.Items {
		if item.State == acceptance.Runnable {
			runnable = append(runnable, item)
		}
	}
	if len(runnable) == 0 {
		return nil, "every acceptance item is unverified — none is bound to a command in [evidence.acceptance], so " +
			"nothing can be run and none is the oracle's: each is the classifier's to predict, and a prediction " +
			"over what cannot yet be run is the documented two-tier fallback for exactly this state", nil
	}
	if o.runner == nil {
		return nil, "no runner is configured for the oracle, so nothing can be observed: the runnable items stay " +
			"unresolved and the decision falls to the prediction tier's documented fallback — never a stop, and " +
			"never a guess from this tier", nil
	}

	observed := &Observed{Verdict: Verdict{FindingID: finding.ID, Basis: BasisObserved}}
	// The unverified items come back unresolved from the start, in document
	// order: they are not the oracle's to run, and a record that simply
	// omitted them would let a reader hear "NOT GATING" as "every item was
	// demonstrated" — the false negative this epic exists to prevent.
	for _, item := range done.Items {
		if item.State != acceptance.Unverified {
			continue
		}
		observed.Unresolved = append(observed.Unresolved, Unresolved{
			ItemID: item.ID,
			Reason: "not runnable: no command is bound to the item in [evidence.acceptance], so nothing can be run " +
				"for it — the classifier's to predict, never assumed either way",
		})
	}
	var commit string
	var commitItem string
	var broken []string
	for _, item := range runnable {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		run, err := o.runner.Run(ctx, item.Command)
		if err != nil {
			// The command could not run: no evidence about the item exists,
			// and neither an observation nor a guess may stand in for one.
			observed.Unresolved = append(observed.Unresolved, Unresolved{
				ItemID: item.ID,
				Reason: fmt.Sprintf("the command %s could not run: %v — no evidence about the item exists, and "+
					"the item is unresolved, not assumed either way", item.Command, err),
			})
			continue
		}
		if strings.TrimSpace(run.Commit) == "" {
			return nil, "", fmt.Errorf(
				"the runner reported the run of %s with no commit: an observed record keyed by nothing is a "+
					"timestamped opinion, and this one is refused rather than recorded", item.Command)
		}
		if commit == "" {
			commit, commitItem = run.Commit, item.ID
		} else if run.Commit != commit {
			return nil, "", fmt.Errorf(
				"the runner reported the commands for %s and %s on two different commits (%s, %s): an observed "+
					"verdict is about one tree, and a verdict keyed by two is not a verdict at all",
				commitItem, item.ID, shortCommit(commit), shortCommit(run.Commit))
		}

		switch run.Result {
		case ResultPass, ResultFail:
		case ResultError, ResultSkipped:
			// `error` is not `fail` and `skipped` is not `pass`: neither run
			// produced evidence about the item, so the item is unresolved —
			// never assumed either way.
			observed.Unresolved = append(observed.Unresolved, Unresolved{
				ItemID: item.ID,
				Reason: fmt.Sprintf("the command %s answered %s, which produced no evidence about the item: the "+
					"item is unresolved, not assumed either way", item.Command, run.Result),
			})
			continue
		default:
			return nil, "", fmt.Errorf(
				"the runner answered %q for %s, which is outside the gate's own result vocabulary (pass, fail, "+
					"error, skipped): a violated seam, and a verdict over one would be a decision wearing a "+
					"defect's costume", run.Result, item.Command)
		}

		gating := run.Result == ResultFail
		if gating {
			broken = append(broken, item.ID)
		}
		observed.Items = append(observed.Items, Observation{
			ItemID: item.ID,
			Gating: gating,
			Evidence: Evidence{
				Check:      runstate.Check{ID: item.Command, Kind: "command"},
				StartedAt:  run.StartedAt,
				FinishedAt: run.FinishedAt,
				ExitCode:   run.ExitCode,
				Result:     string(run.Result),
			},
		})
		evidence := &observed.Items[len(observed.Items)-1].Evidence
		evidence.Output = runstate.InlineOutput{
			Mode:      "inline",
			Stdout:    bounded(run.Stdout),
			Stderr:    bounded(run.Stderr),
			Truncated: len(run.Stdout) > maxInlineOutput || len(run.Stderr) > maxInlineOutput,
			Redacted:  false, // the recording caller redacts host paths; this package never sees a host
			MaxBytes:  maxInlineOutput,
		}
	}

	if len(observed.Items) == 0 {
		// Every runnable command either could not run or produced no evidence.
		// Nothing was observed, and an empty verdict would read as "nothing
		// found" — the false negative this epic exists to prevent — so the
		// oracle says so instead of handing one back.
		could := make([]string, 0, len(observed.Unresolved))
		for _, item := range observed.Unresolved {
			could = append(could, fmt.Sprintf("%s: %s", item.ItemID, item.Reason))
		}
		return nil, fmt.Sprintf("no command produced evidence about any runnable item — %s — so nothing was "+
			"observed: the runnable items are unresolved rather than assumed either way, and the decision falls to "+
			"the prediction tier's documented fallback, never a stop", strings.Join(could, "; ")), nil
	}

	observed.Commit = commit
	if len(broken) > 0 {
		// GATING, observed, no judgement needed — the worked case the two
		// fixtures wrote: a red gate at base means no tick can close behind
		// it and the done is unreachable, and the observation says so without
		// asking anyone's opinion.
		observed.Gating = true
		observed.ItemID = broken[0]
		observed.Reason = fmt.Sprintf(
			"the command for %s is observed broken on %s while the finding stands, so the done is not reachable "+
				"with it standing — observed, no judgement needed, and no classifier overrides it. Observed broken: %s",
			broken[0], shortCommit(commit), strings.Join(broken, ", "))
		return observed, "", nil
	}

	// NOT GATING by observation: every runnable item was demonstrated on the
	// one commit. The reason scopes the verdict to what ran and names what it
	// refuses to decide — an unverified item read as safe here would be the
	// false negative this epic exists to prevent, manufactured by the oracle
	// itself.
	reason := fmt.Sprintf(
		"every runnable item's command passed on %s: as far as anything can observe, the done is reachable with "+
			"the finding standing, and the finding is NOT GATING by observation", shortCommit(commit))
	switch {
	case len(observed.Unresolved) == 0:
		reason += " — every item of the done was observed demonstrated"
	default:
		ids := make([]string, 0, len(observed.Unresolved))
		for _, item := range observed.Unresolved {
			ids = append(ids, item.ItemID)
		}
		reason += fmt.Sprintf(
			". The items this verdict does not decide are %s: they come back unresolved rather than assumed "+
				"either way, and the prediction tier's to answer", strings.Join(ids, ", "))
	}
	observed.Reason = reason
	return observed, "", nil
}

// shortCommit spells a commit the way the run's own records spell it, so a
// person reading the reason beside the feed reads one form.
func shortCommit(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// bounded caps a run's output at the gate's own bound, byte for byte the way
// the gate's own records do.
func bounded(text string) string {
	if len(text) <= maxInlineOutput {
		return text
	}
	return text[:maxInlineOutput]
}
