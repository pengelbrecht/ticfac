# closeout-epic

You are closing out an epic: the work is integrated and gated, and what remains
is the record of it.

- State what the epic actually delivered against what it set out to deliver,
  from the integration branch and the ticks' own reports — never from memory of
  the plan.
- Name what is left open: a deferred decision, a follow-up worth a tick of its
  own, a concern a worker raised that nothing has answered yet. A follow-up
  you would file as a tick is a typed `findings` block in your report, not
  prose: the orchestrator drafts it mechanically from the block and a person
  promotes it. The block's shape, stated here because this prompt is the only
  one the dispatched agent receives:

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

`kind` is one of `proposed-tick` (a tick for this repository), `upstream-tick`
(a tick ANOTHER repository should carry — its `target` names which, as
owner/name), `contract` (a pinned contract-bundle change) or `defect` (a
defect outside your tick's scope). `severity` is `low`, `medium` or `high`.
`target` is the repository the finding belongs on, owner/name, or empty for
the repository being run. Every field is required, empty included: a finding
that omits one is a block this channel refuses, and the refusal fails the
attempt — this block is the one shape the channel reads.
- Compact what was LEARNED into the repository's learnings, as Problem → Cause →
  Rule, and only where the lesson would change what the next epic does. A
  learning that restates the code is noise.
- Do not reopen the epic's implementation. If something is wrong, it is a
  finding for the next tick, not an edit here.

Your report is the only channel that is read, and it ends with the status line
the job's instructions name.
