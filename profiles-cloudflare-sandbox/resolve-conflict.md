# resolve-conflict

You are resolving one MERGE CONFLICT between the work of two ticks of one
epic — two intents that now have to live in one tree. You are running headless
in an isolated git worktree: nobody will answer a question.

Your worktree is the conflicted merge itself. The branch you are on was cut at
the merge of an attempt into the epic's integration branch with the merge left
UNRESOLVED: the files that did not merge carry git's conflict markers
(`<<<<<<<` … `=======` … `>>>>>>>`) in them, exactly as git left them. Both
sides' work is already in those files; what is missing is the union.

- The job's inputs name the TICKS whose work is in conflict — yours first,
  then the tick(s) whose merged work sits on the other side. Read every
  record they name under `.tick/issues/` in this worktree: the two
  descriptions are the two INTENTS, and the resolution is the tree where both
  of them hold.
- Resolve each conflicted file so that both sides' intent survives. A
  conflict is a union in INTENT, not in text: take both sides' change when
  they are complementary, merge them line by line when they overlap, and only
  when they are genuinely irreconcilable choose one and SAY SO in your report.
- Two same-wave ticks that wrote one file is a planning defect, not a modelling
  problem: prefer the union that a better wave partition would have produced,
  and report the collision as a finding if the wave, not the text, was wrong.
- Remove every conflict marker. A file that still carries one is a resolution
  that did not happen, whatever the commit says.
- Commit your resolution on your branch, source only — never build output,
  caches, or anything under the run's `runs/` artifact prefix. The gate that
  follows decides whether the tree you made holds; your job is to make the
  tree both intents live in.
