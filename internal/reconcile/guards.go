package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The five lifecycle invariants the local subprocess executor names as not its
// own, implemented where they belong: in the thing that drives it.
//
// Each is a real mechanism the reconciler uses in Run — a step cap the wait
// loop is spread across, a poll that is the keepalive, a hold that is read
// before every dispatch, a budget that is clamped before it is reported, and
// an evidence fingerprint that publication checks against the current target.
// Each also carries the fixture's guard name, so lifecycle_test.go can turn
// exactly one off and watch the rule stop being kept.

// ------------------------------------------------------------------- A3 ---

// StepOutcome is the answer to a spend, in the fixture's vocabulary.
type StepOutcome string

const (
	WithinCap   StepOutcome = "within_cap"
	ExceededCap StepOutcome = "exceeded_cap"
)

// Step is one bounded leg of a long wait. The host caps how long a step may
// execute for, so a wait longer than the cap becomes two legs — and the second
// is a FRESH step that re-reads what it needs rather than a continuation that
// remembers it.
type Step struct {
	cap     time.Duration
	spent   time.Duration
	guarded bool
}

// OpenStep begins a leg. Opening a step discards the previous leg's spend,
// which is the whole difference between a step cap and a total budget.
func (r *Reconciler) OpenStep(cap time.Duration) *Step {
	step := &Step{cap: cap, guarded: r.guarded(guardStepCap)}
	r.step = step
	return step
}

// Spend asks to execute for d inside this step. A refused spend changes
// nothing: on the real host it would not have been allowed to happen, so the
// caller's answer is "open another step", never "spend it anyway".
func (s *Step) Spend(d time.Duration) StepOutcome {
	if s.guarded && s.spent+d > s.cap {
		return ExceededCap
	}
	s.spent += d
	return WithinCap
}

// Spent is how long this leg has executed for.
func (s *Step) Spent() time.Duration { return s.spent }

// ------------------------------------------------------------------- A4 ---

// PollOutcome is the answer to addressing a job.
type PollOutcome string

const (
	Polled PollOutcome = "polled"
	Wiped  PollOutcome = "wiped"
)

// Poll addresses one job. There is no separate heartbeat: the poll IS the
// keepalive, and the only thing that makes it one is that its interval stays
// under the substrate's wipe threshold. A job that has not been addressed
// within the threshold is gone, and the reconciler learns that here rather
// than by waiting for a job that no longer exists.
func (r *Reconciler) Poll(job string) PollOutcome {
	now := r.now()
	last, seen := r.lastPolled[job]
	r.lastPolled[job] = now
	if r.guarded(guardPollUnderWipe) && seen && now.Sub(last) > r.wipeThreshold {
		r.liveness[job] = "dead"
		return Wiped
	}
	return Polled
}

// Liveness is what the last poll concluded about a job.
func (r *Reconciler) Liveness(job string) string { return r.liveness[job] }

// noteAlive records a job as live at the moment it was booted, so the first
// poll has something to be an interval from.
func (r *Reconciler) noteAlive(job string) {
	r.lastPolled[job] = r.now()
	r.liveness[job] = "live"
}

// ------------------------------------------------------------------ A11 ---

// HoldOutcome is the answer at the read site.
type HoldOutcome string

const (
	Held      HoldOutcome = "held"
	Permitted HoldOutcome = "permitted"
)

type hold struct {
	reason     string
	struck     bool
	releasedBy string
}

// StrikeOut holds a unit — a repository, a branch, a tick — out of dispatch.
func (r *Reconciler) StrikeOut(unit, reason string) {
	r.holds[unit] = &hold{reason: reason, struck: true}
}

// ClockRelease is what a rolling window would do, and it is refused. A window
// bounds the WINDOW, not the subject: time passing is not new information
// about why the unit was struck out.
func (r *Reconciler) ClockRelease(unit string) string {
	if r.guarded(guardReleaseByPerson) {
		return "refused_clock_release"
	}
	if h, ok := r.holds[unit]; ok {
		h.struck, h.releasedBy = false, "clock"
	}
	return "released"
}

// PersonRelease is the only release. `by` is recorded because a release with
// no author is a release the clock could have made.
func (r *Reconciler) PersonRelease(unit, by string) string {
	if h, ok := r.holds[unit]; ok {
		h.struck, h.releasedBy = false, by
	}
	return "released"
}

// MayDispatch is the READ site, asked before every dispatch. A table with
// writes and no reads is not a guard.
func (r *Reconciler) MayDispatch(unit string) HoldOutcome {
	if h, ok := r.holds[unit]; ok && h.struck {
		return Held
	}
	return Permitted
}

// ReleasedBy reports who released each unit, for an operator asking why a
// held unit is dispatching again.
func (r *Reconciler) ReleasedBy() map[string]string {
	out := map[string]string{}
	for unit, h := range r.holds {
		if h.releasedBy != "" {
			out[unit] = h.releasedBy
		}
	}
	return out
}

// ------------------------------------------------------------------ A12 ---

// Budget is an operator's number and the number that will actually govern.
type Budget struct {
	Requested float64 `json:"requested"`
	Ceiling   float64 `json:"ceiling"`
	Effective float64 `json:"effective"`
	Reported  float64 `json:"reported"`
	Clamped   bool    `json:"clamped"`
}

// SetBudget clamps a requested budget to the deployment ceiling. The clamp is
// correct and stays; what A12 is about is which number gets REPORTED.
func (r *Reconciler) SetBudget(requested, ceiling float64) string {
	r.budget = Budget{Requested: requested, Ceiling: ceiling, Effective: requested}
	if ceiling > 0 && requested > ceiling {
		r.budget.Effective, r.budget.Clamped = ceiling, true
		return "clamped"
	}
	return "as_requested"
}

// ReportBudget states the number that will govern, at submission, while the
// run can still be cancelled cheaply. An operator's 40 under an 8 ceiling is
// told 8 — and a 5 under a 25 ceiling is told 5, because the rule is "say what
// will govern", not "always say the ceiling".
func (r *Reconciler) ReportBudget() string {
	if r.guarded(guardReportAfterClamping) {
		r.budget.Reported = r.budget.Effective
		return "reported_effective"
	}
	r.budget.Reported = r.budget.Requested
	return "reported_requested"
}

// Budget is the run's budget as it stands.
func (r *Reconciler) Budget() Budget { return r.budget }

// ------------------------------------------------------------------ A13 ---

// FingerprintFields are Appendix A #13's four, spelled as
// contracts/job-protocol.json's `$defs.provenance` spells them. The mapping is
// contracts/lifecycle-invariants.json's, and it is followed rather than
// re-derived.
var FingerprintFields = []string{
	"source_sha", "integration_ref", "context_manifest_digest", "profile_digest",
}

// Fingerprint says what a piece of evidence evaluated.
type Fingerprint map[string]string

// Complete reports whether every fingerprint field is stated. A record that
// omits one cannot say what it evaluated, and "omitted" and "null" are
// different claims — only the second is evidence.
func (f Fingerprint) Complete() bool {
	for _, field := range FingerprintFields {
		if f[field] == "" {
			return false
		}
	}
	return true
}

// Digest is a stable identity for a fingerprint, for a caller that wants one
// value rather than four.
func (f Fingerprint) Digest() string {
	fields := make([]string, 0, len(f))
	for name := range f {
		fields = append(fields, name)
	}
	sort.Strings(fields)
	sum := sha256.New()
	for _, name := range fields {
		fmt.Fprintf(sum, "%s=%s\n", name, f[name])
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil))
}

// RecordEvidence keeps one fingerprinted record in this reconciler's index of
// what it has evaluated. It refuses a record that cannot say what it
// evaluated: all four fields or none of it.
func (r *Reconciler) RecordEvidence(key string, fingerprint Fingerprint) string {
	if r.guarded(guardEvidenceFingerprinted) && !fingerprint.Complete() {
		return "refused_unfingerprinted"
	}
	r.evidence[key] = fingerprint
	return "recorded"
}

// PublishEvidence checks the record against the CURRENT target before its
// verdict is acted on. The target moved between the check and the publication
// means the record is still true about what it evaluated and is no longer true
// about what is being published.
func (r *Reconciler) PublishEvidence(key string, target Fingerprint) string {
	record, ok := r.evidence[key]
	if !ok {
		return "refused_stale"
	}
	if r.guarded(guardPublicationChecksFreshness) {
		for _, field := range FingerprintFields {
			if record[field] != target[field] {
				return "refused_stale"
			}
		}
	}
	r.published = append(r.published, key)
	return "published"
}

// EvidenceKeys are the records this reconciler has fingerprinted, sorted.
func (r *Reconciler) EvidenceKeys() []string {
	out := make([]string, 0, len(r.evidence))
	for key := range r.evidence {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// PublishedKeys are the records whose verdicts were published, in order.
func (r *Reconciler) PublishedKeys() []string { return append([]string{}, r.published...) }

// ------------------------------------------------------------ guard names ---

// The guard names are contracts/lifecycle-invariants.json's own. They are
// constants rather than literals so that a fixture rename is a compile error
// here and not a test that quietly stops turning anything off.
const (
	guardStepCap                    = "step_cap"
	guardPollUnderWipe              = "poll_under_wipe"
	guardReleaseByPerson            = "release_by_person"
	guardReportAfterClamping        = "report_after_clamping"
	guardEvidenceFingerprinted      = "evidence_fingerprinted"
	guardPublicationChecksFreshness = "publication_checks_freshness"
	guardNeverRedispatchLive        = "never_redispatch_live"
	guardSettleFromEvidence         = "settle_from_evidence"
	guardSubstrateEnforcesBoundary  = "substrate_enforces_boundary"
	guardDistinctFailureClasses     = "distinct_failure_classes"
)

func (r *Reconciler) guarded(name string) bool { return !r.guardsOff[name] }

// digestOf is the config and profile digest: one stable value over the strings
// that decide what a check evaluated.
func digestOf(parts ...string) string {
	sum := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(sum, "%d:%s\n", len(part), part)
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil))[:32]
}

// commandDigest is the digest of the declared gate: the checks, in name order,
// with their command lines. A gate whose commands changed evaluated something
// else, and evidence carrying this digest says so.
func (g GateCommands) Digest() string {
	parts := make([]string, 0, len(g)*2)
	for _, command := range g {
		parts = append(parts, command.Name, command.Command)
	}
	return digestOf(append([]string{"testing.commands"}, parts...)...)
}

// String renders the gate for a message.
func (g GateCommands) String() string {
	names := make([]string, 0, len(g))
	for _, command := range g {
		names = append(names, command.Name)
	}
	return strings.Join(names, ", ")
}
