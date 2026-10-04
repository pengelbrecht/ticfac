package reconcile

import "fmt"

// SettleReleaseCommand is the release command a hold for a person names: the
// one imperative that lets a stopped attempt go, spelled once so the
// reconciler's own refusals and every surface that renders them — the watch's
// hold alert, the status model's needs-you (whose SettleCommandForCurrentRun
// reads this rule) — cannot drift apart. It is addressed by
// the epic, the tick and the RUN-WIDE dispatch number, and by the run whose
// store carries the attempt whenever that run's id is NOT the spelling a
// settle without --run-id opens (epic-<epic-id>): a cloud run's records live
// under the factory's run_<hex>, so the command without the flag addresses a
// store that carries no such attempt and refuses — a hold the header points
// at that its own command cannot clear (tick ulw fixed the model surface;
// tick qxj the prose and the alert). The command the flag spells is one a
// person types on a host that never ran the attempt, so its release is
// answered by the FACTORY that did — the store the flag opens carries the
// marker, and the factory's record of the worker is what the release rules
// on when this host holds none of its state (tick bd5). The empty run id
// says the caller does not know it; either way the flag is left off and the
// spelling every local run's prose has always carried stands.
func SettleReleaseCommand(epicID, tickID string, attempt int, runID string) string {
	if runID == "" || runID == "epic-"+epicID {
		return fmt.Sprintf("ticfac settle %s %s %d --release \"<who>\"", epicID, tickID, attempt)
	}
	return fmt.Sprintf("ticfac settle %s %s %d --run-id %s --release \"<who>\"", epicID, tickID, attempt, runID)
}

// settleCommand is the release command of THIS run's attempt: what every
// refusal prose interpolates, so the address a hold's prose names is always
// the store this run writes its records under.
func (r *Reconciler) settleCommand(tick string, attempt int) string {
	return SettleReleaseCommand(r.opts.EpicID, tick, attempt, r.opts.RunID)
}
