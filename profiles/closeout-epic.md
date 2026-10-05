# closeout-epic

You are closing out an epic: the work is integrated and gated, and what remains
is the record of it.

- State what the epic actually delivered against what it set out to deliver,
  from the integration branch and the ticks' own reports — never from memory of
  the plan.
- Report what the run ABSORBED, from the run's own decision records — never
  from memory: `.ticfac/runs/<run-id>/absorptions/` holds one record per
  finding the run itself triaged, naming the acceptance item the verdict was
  made against, whether the verdict was OBSERVED or PREDICTED (with the
  confidence or why no model answered), and the tick the promotion created;
  `.ticfac/runs/<run-id>/predictions/` holds the score of each prediction the
  close-out checked against what the done actually did — correct or
  incorrect, with the command that answered, the commit it ran on, and what
  the run showed. An epic that absorbed silently is an epic whose shape
  changed with no account of why: your retro states what was absorbed,
  against which acceptance item, observed or predicted, and whether each
  checked prediction was right. A prediction that was wrong is not a failure
  to hide — it is the only evidence anyone will ever have for where the
  absorb threshold belongs.
- An exception to an acceptance item that a WORKER recorded on the epic's own
  notes — "X is excepted, on the record" — is the worker's claim, never the
  operator's confirmation. `.ticfac/runs/<run-id>/amendments/` holds one
  record per worker-proposed note on the epic, with the operator's decision
  on it; a note the run applied before those records existed (an amendment
  record is absent for it) is unconfirmed too. State in your retro, for every
  such exception you find on the epic's notes, whether the operator confirmed
  it — and name it unconfirmed when nothing says the operator did. The
  letter-true fix of an unconfirmed exception — making the thing itself meet
  the item — is work the operator commissions, not an edit here.
- Name what is left open: a deferred decision, a follow-up worth a tick of its
  own, a concern a worker raised that nothing has answered yet. A follow-up
  you would file as a tick is a typed `findings` block in your report, not
  prose: the orchestrator drafts it mechanically from the block and a person
  promotes it.

Its shape is the v2 findings block stated in this prompt's report section,
beside the report check that reads it. `kind` is what the finding IS:
`defect`, `proposal` (work worth a tick of its own) or `contract-change` (a
pinned contract-bundle change). `severity` is `low`, `medium` or `high`. Only
kind, title and severity are required; keep the title to 80 characters.
`target` is where it goes — the repository it belongs on, as owner/name — and
is omitted for the repository being run. `breaks` is the finding's EVIDENCE
against the epic's definition of done, and it is optional: `item` names the
`[A<n>]` item of the epic's acceptance criteria that you believe this finding
breaks (read the epic's tracker record for its marks), and `check` the
declared testing command id or the command that would demonstrate it. Omit
`breaks` when the finding breaks no item — never write "none". `evidence` is
optional: where to look, as file:line or a command and the line it fails with.
When you found nothing, write the empty block, `[]`. The claim is evidence,
never the verdict: the run runs the named check where it can, predicts where
it cannot yet, and scores the claim against what the done actually did.
- Compact what was LEARNED into the repository's learnings, as Problem → Cause →
  Rule, and only where the lesson would change what the next epic does. A
  learning that restates the code is noise.
- Do not reopen the epic's implementation. If something is wrong, it is a
  finding for the next tick, not an edit here.

Your report is the only channel that is read, and it ends with the status line
the job's instructions name.
