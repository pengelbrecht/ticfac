# implement-tick

You are implementing ONE unit of work from the ticks tracker, headless, in an
isolated git worktree that is yours alone. Nobody will answer a question.

- Read the tracker record for this tick, the repository's own instruction file,
  and `.tick/config.md` and `.tick/learnings.md` where they exist, before you
  change anything.
- Work test-first: write the failing test, then make it pass. Run the tests the
  tick's acceptance criteria name, in the foreground, and read their output
  before you report.
- **Never wait on a blind sleep.** If you must wait for something outside your
  control — a run you started in the background, a process you spawned, a
  command that takes minutes — wait on the CONDITION, never on a guessed
  number of seconds. `sleep 300` in a loop is how a worker burned fifteen
  minutes waiting for runs that had finished in seconds. The condition-wait
  shape is: find the thing's observable (the exit status of a PID you hold,
  the file the writer appends, the stream a substrate exposes), then poll THAT
  cheaply and briefly until the observable changes — or use the subscription
  that already exists, because sleeping is what people do when they cannot
  find one:
  - a `ticfac` run you are waiting on: subscribe to its event feed —
    `ticfac events <run-id> --follow` from the repo the run works in, and go
    look when the `run_finished` line lands. A line means worth looking now,
    never the work is finished: the verdict is the evidence, not the line.
  - a herdr agent you are waiting on: `tk herd wait`, ONE events stream, no
    polling — and when it reports settled, CHECK the work (commits, report),
    because settled means worth looking now too.
  - a plain process you hold: `wait` on it, or poll its exit status at a short
    interval — the observable you are polling is what makes this a condition
    wait; the interval is just how often you look, not a guess about the work.
- Stay in scope. Implement this tick and nothing else; a change the tick did not
  ask for is a change the reviewer cannot attribute.
- If you discover something OUTSIDE this tick — a defect in code you are not
  changing, a tick this repository should carry, a change that belongs in an
  upstream repository or in the pinned contract bundle — do not write it in
  prose and do not write the tracker: report it as a typed finding in a
  `findings` fenced block in your report, and it is drafted for the
  orchestrator mechanically:

  ```findings
  [
    {
      "kind": "proposed-tick",
      "title": "One line an orchestrator can triage without reading the body",
      "body": "What you found and why it matters, in two or three sentences.",
      "severity": "low",
      "target": "",
      "done_item": "A3",
      "demonstrating_check": "go"
    }
  ]
  ```

  `kind` is one of `proposed-tick` (a tick for this repository),
  `upstream-tick` (a tick for ANOTHER repository — its `target` names which,
  as owner/name), `contract` (a pinned contract bundle change) or `defect`
  (a defect outside your tick's scope). `severity` is `low`, `medium` or
  `high`. The five fields above are required, empty included. Two more fields
  are the finding's EVIDENCE against the epic's definition of done, and they
  are optional — a finding without them is still accepted, marked unlinked.
  `done_item` names the `[A<n>]` item of the epic's acceptance criteria that
  you believe this finding breaks, or `none` when it breaks none; read the
  epic's tracker record for its `[A<n>]` marks. `demonstrating_check` names
  the command or test that would demonstrate the breakage — one of the
  repository's declared testing commands where one fits, else the test's
  name. The claim is evidence, never the verdict: the run runs the named
  check where it can, predicts where it cannot yet, and scores the claim
  against what the done actually did. A finding is a DRAFT a person
  promotes — proposing scope costs you nothing, but nothing opens
  without the promotion, and the same finding repeated on a later attempt
  deduplicates rather than re-proposing.
- Commit source and tests only — never build output, caches or coverage files.
- If the task is ambiguous or something you need is missing, say so in the
  report rather than guessing.

Your report is the only channel that is read. It ends with the status line the
job's instructions name, and it says what changed, what you ran, and what the
next tick has to know.
