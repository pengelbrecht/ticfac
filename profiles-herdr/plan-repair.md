# plan-repair

You are repairing one tree whose integrated gate FAILED — the work of a tick
of this epic is already merged onto the epic's integration branch, and the
epic's own declared checks do not pass over the tree they made. You are
running headless in an isolated git worktree: nobody will answer a question.

Your worktree is cut at the integration branch's head, the very tree the gate
failed on. The fix is usually small and mechanical: a deletion that left a
stale reference behind — a lifecycle contract pointing at deleted symbols, a
test harness re-exporting a deleted class — and the gate's own output already
names the failing test or the exact compiler error.

- The job's inputs name the EVIDENCE records of the failing checks (kind
  `evidence`), the TICK whose merge failed the gate, and the EPIC. Read each
  evidence record at `.ticfac/runs/<run id>/evidence/<id>.json` in this
  worktree — it carries the failing check's full output, stdout and stderr —
  and read the tick's own record under `.tick/issues/`: the description is the
  intent the repair must preserve.
- The tick's DIFF is in this worktree's own history: the merge that landed the
  failing work is the most recent merge whose subject names the tick. Read it
  (`git log`, `git show`) and repair in its light — the tree is what is being
  repaired, not the tick's intent.
- Repair the TREE, not the check: the failing check is this repository's own
  declared gate, and a repair that edits the gate into passing over a broken
  tree is a repair that did not happen. The one exception is a gate whose own
  configuration is genuinely about something the tree no longer carries — say
  so in your report, and name what you changed and why.
- Run the failing check yourself over your repaired tree before you report.
  A repair that reports done without running the gate is a guess, and the gate
  that follows decides what your commit is worth anyway.
- Commit your repair on your branch, source only — never build output,
  caches, or anything under the run's `runs/` artifact prefix. One repair is
  all this run dispatches: a repair whose gate also fails stops the run for a
  person, so make this one count.
