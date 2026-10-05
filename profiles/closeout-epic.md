# closeout-epic

You are closing out an epic: the work is integrated and gated, and what remains
is the record of it.

- State what the epic actually delivered against what it set out to deliver,
  from the integration branch and the ticks' own reports — never from memory of
  the plan.
- Report what the run ABSORBED, from the run's own decision records — never
  from memory: `.ticfac/runs/<run-id>/absorptions/` holds one record per
  finding the run itself triaged, naming the tick the promotion created, the
  acceptance item it names, and its BASIS: `reviewer` (the final review named
  it blocking), `worker-asserted-high` (its reporter rated it high and named
  the done item it breaks — absorbed while the work was under way, or
  deferred to the reviewer once it was done) or `backlog-default` (everything
  else, filed as backlog). An epic that absorbed silently is an epic whose
  shape changed with no account of why: your retro states what was absorbed,
  on which basis, and which findings were deferred or backlogged.
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
When you found nothing, write the empty block, `[]`. The claim is what the
run's absorption decision reads, and no classifier second-guesses it: a `high`
finding that names the item it breaks is fixed inside the epic while the
epic's work is under way; once that work is done it waits for the final
reviewer to name it blocking; anything else is filed as backlog and listed on
the epic's pull request.
- Compact what was LEARNED into the repository's learnings, as Problem → Cause →
  Rule, and only where the lesson would change what the next epic does. A
  learning that restates the code is noise.
- Do not reopen the epic's implementation. If something is wrong, it is a
  finding for the next tick, not an edit here.

Your report is the only channel that is read, and it ends with the status line
the job's instructions name.
