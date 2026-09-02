package reconcile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The profile side of a dispatch: which profile a role is dispatched under,
// what that profile is allowed to be, and what it decides about the job.
//
// A profile is exactly {executor, runner, model, prompt} (SPEC §4.5, Phase 1),
// and this is where those four meet the protocol. The runner is executor
// CONFIGURATION — the JobSpec's records are closed, so a runner field invented
// on this side would be a field the executor's contract does not have. The
// model and the prompt are carried in the profile's digest and in the
// provenance of every record the dispatch produces, which is what makes "under
// which profile was this decided" answerable after the fact.

// profileFor is the profile one role is dispatched under. A role this phase
// ships no profile for is implement-tick's — the same rule RoleOf applies to a
// task nobody classified: unclassified work is work.
func (r *Reconciler) profileFor(role string) *profile.Profile {
	if p, ok := r.profiles[role]; ok && p != nil {
		return p
	}
	return r.profiles["implement-tick"]
}

// usableProfile refuses a profile this build cannot honour, at construction.
//
// Both refusals are about the same thing: a profile is only worth recording if
// the run actually happened under it. A profile naming `herdr` that ran on the
// local subprocess executor would be provenance that lies, and a runner nothing
// here can launch is a dispatch that fails after the tracker has been claimed.
func usableProfile(p *profile.Profile) error {
	if p == nil {
		return fmt.Errorf("no profile resolved: nothing says which executor, runner, model and prompt a job is dispatched with")
	}
	if p.Executor != subprocess.ExecutorName {
		return fmt.Errorf("profile %s names executor %q and this phase has one, %s: a record naming an executor the "+
			"run did not use is provenance that lies", p.Role, p.Executor, subprocess.ExecutorName)
	}
	known := subprocess.KnownRunners()
	for _, name := range known {
		if name == p.Runner {
			return nil
		}
	}
	return fmt.Errorf("profile %s names runner %q, which is not one of %s: a dispatch that discovers this after the "+
		"tick is claimed has claimed a tick nothing will work on", p.Role, p.Runner, strings.Join(known, ", "))
}

// profileSetDigest is the digest of ALL the profiles a run was made under. A
// checkpoint is not about one role, so it names the set rather than picking one
// role's profile to stand for the run.
func profileSetDigest(profiles map[string]*profile.Profile) string {
	roles := make([]string, 0, len(profiles))
	for role := range profiles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	parts := make([]string, 0, len(roles)*2)
	for _, role := range roles {
		parts = append(parts, role, profiles[role].Digest)
	}
	return digestOf(append([]string{"profiles"}, parts...)...)
}

// ------------------------------------------------------------ role jobs ---

// isRoleJob reports whether a tick's deliverable is an ANSWER rather than a
// change: review and closeout are jobs like any other, run through the same
// executor, but what the reconciler acts on is the role-result envelope they
// return and not a branch it merges.
func isRoleJob(role string) bool {
	return role == "review-epic" || role == "closeout-epic"
}

// sourceGradeFor is the grade the host issues source access at.
//
// SPEC §6.3 puts review-epic at the epic boundary, running READ-ONLY against
// the integrated ref: the executor issues it no push credential at all, so a
// review that tried to advance a ref is refused by the issuer rather than by
// the model's good manners. A closeout writes — a retro and the learnings it
// compacts are its output — so it is dispatched at the write grade and its work
// is integrated and gated like any other.
func sourceGradeFor(role string) string {
	if role == "review-epic" {
		return "read-only"
	}
	return "write"
}

// outputSchemaFor is the role-specific contract the job's role_result.result
// must satisfy. It is named in the SPEC so that the reconciler validates
// against what it ASKED for rather than against what came back.
func outputSchemaFor(role string) string {
	return "ticfac.job-result." + role + ".v1"
}

// controllerBase is the state a role job is dispatched at: the integration
// branch as origin has it NOW, not the base the run started from. A review of
// an epic that reads the epic's pre-run state reviews something nobody
// integrated.
func (r *Reconciler) controllerBase() string {
	if err := r.git.fetch(r.branch); err != nil {
		return r.base
	}
	head, err := r.git.remoteHead(r.branch)
	if err != nil || head == "" {
		return r.base
	}
	if _, err := r.git.resolve(head); err != nil {
		return r.base
	}
	return head
}

// ------------------------------------------------------- the provenance ---

// attemptProvenance is what a dispatch's records say they were produced under.
//
// Three fields the run-level provenance leaves null are stated here, and each
// is a claim the contract has a slot for precisely because a record that cannot
// make it is not evidence: the ROLE that was dispatched, the MODEL the profile
// routed to, and the digest of THAT role's profile rather than of the run's set.
func (r *Reconciler) attemptProvenance(d Dispatch) runstate.Provenance {
	provenance := r.provenance(&d.TickID, &d.Attempt, phaseFor(d.Role), d.BaseSHA)
	role := d.Role
	provenance.Role = &role
	if d.Profile != nil {
		model, digest := d.Profile.Model, d.Profile.Digest
		provenance.Model = &model
		provenance.ProfileDigest = &digest
	}
	return provenance
}

// phaseFor is the gate vocabulary's phase for a dispatch. The same command
// against the same ref at a different phase answers a different question, so a
// review's records say `review` and a closeout's say `closeout` rather than all
// three saying `worker`.
func phaseFor(role string) runstate.Phase {
	switch role {
	case "review-epic":
		return runstate.PhaseReview
	case "closeout-epic":
		return runstate.PhaseCloseout
	default:
		return runstate.PhaseWorker
	}
}
