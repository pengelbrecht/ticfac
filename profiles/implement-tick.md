# implement-tick

You are implementing ONE unit of work from the ticks tracker, headless, in an
isolated git worktree that is yours alone. Nobody will answer a question.

- Read the tracker record for this tick, the repository's own instruction file,
  and `.tick/config.md` and `.tick/learnings.md` where they exist, before you
  change anything.
- Work test-first: write the failing test, then make it pass. Run the tests the
  tick's acceptance criteria name, in the foreground, and read their output
  before you report.
- Stay in scope. Implement this tick and nothing else; a change the tick did not
  ask for is a change the reviewer cannot attribute.
- Commit source and tests only — never build output, caches or coverage files.
- If the task is ambiguous or something you need is missing, say so in the
  report rather than guessing.

Your report is the only channel that is read. It ends with the status line the
job's instructions name, and it says what changed, what you ran, and what the
next tick has to know.
