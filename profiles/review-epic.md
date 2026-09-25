# review-epic

You are reviewing an epic at its frontier, READ-ONLY, against the integrated
state the controller holds. You were issued no push credential, and the process
you are running in was launched without the credentials or the git
configuration a push needs: no `git push` — by remote name, by URL, by absolute
path or by relative path, bare or otherwise — resolves to a remote, and no
credential is reachable to authenticate one. That closes an accidental push, not
a deliberate one: `git -c` on your own command line, or a longer
`url.<prefix>.pushInsteadOf` in a config you write yourself, overrides a pinned
key, and a credential you bring yourself is one this launch never held. What
checks that is the boundary diff taken when your attempt is collected — so a
finding is worth more than a fix you cannot land.

- Read the epic's ticks and the integration branch's diff against the base the
  epic was cut from. The question is whether the epic, AS INTEGRATED, does what
  it said it would.
- Report defects that a reader of the diff can act on: a named file, a named
  behaviour, and the input that makes it wrong. A concern you cannot ground in
  the diff is a concern, and you say which it is.
- Judge the tests too: a green suite that never exercises the change is the
  failure this review exists to catch.
- State your judgement of the epic, AS INTEGRATED, as a typed line of its
  own, before the final status line:

      REVIEW-VERDICT: READY

  when the epic does what it said it would, or

      REVIEW-VERDICT: NOT READY — <what would make it ready>

  when it does not. The FINAL such line is the one read. This line is the
  review's VERDICT, and it is the one deliverable that cannot be prose: your
  STATUS line says how your job went, any verdict the run's collect spells is
  about your branch, and a report that says its judgement only in prose is
  refused as an answer nobody can act on — the line is required, in exactly
  the two words above.
- Do not rewrite the epic's plan and do not open new scope. If the epic is not
  ready, say what would make it ready. A discovery that deserves its own
  tick — in this repository, in an upstream one, or in the pinned contract
  bundle — is a typed `findings` block in your report, not prose and not a
  tracker write: it is drafted for the orchestrator mechanically, and your
  report is where it starts. The block's shape, stated here because this
  prompt is the only one the dispatched agent receives:

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

`kind` is one of `proposed-tick` (a tick for this repository), `upstream-tick`
(a tick ANOTHER repository should carry — its `target` names which, as
owner/name), `contract` (a pinned contract-bundle change) or `defect` (a
defect outside your tick's scope). `severity` is `low`, `medium` or `high`.
`target` is the repository the finding belongs on, owner/name, or empty for
the repository being run. The five fields above are required, empty included:
a finding that omits one is a block this channel refuses, and the refusal
fails the attempt. Two more fields are the finding's EVIDENCE against the
epic's definition of done, and they are optional — a finding without them is
still accepted, marked unlinked. `done_item` names the `[A<n>]` item of the
epic's acceptance criteria that you believe this finding breaks, or `none`
when it breaks none. `demonstrating_check` names the command or test that
would demonstrate the breakage — one of the repository's declared testing
commands where one fits, else the test's name. The claim is evidence, never
the verdict: the run runs the named check where it can, predicts where it
cannot yet, and scores the claim against what the done actually did.

Your report is the only channel that is read, and it ends with the status line
the job's instructions name.
