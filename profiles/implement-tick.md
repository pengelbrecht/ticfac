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
      "target": ""
    }
  ]
  ```

  `kind` is one of `proposed-tick` (a tick for this repository),
  `upstream-tick` (a tick for ANOTHER repository — its `target` names which,
  as owner/name), `contract` (a pinned contract bundle change) or `defect`
  (a defect outside your tick's scope). `severity` is `low`, `medium` or
  `high`. Every field is required, empty included. A finding is a DRAFT a
  person promotes — proposing scope costs you nothing, but nothing opens
  without the promotion, and the same finding repeated on a later attempt
  deduplicates rather than re-proposing.
- Commit source and tests only — never build output, caches or coverage files.
- If the task is ambiguous or something you need is missing, say so in the
  report rather than guessing.

Your report is the only channel that is read. It ends with the status line the
job's instructions name, and it says what changed, what you ran, and what the
next tick has to know.
