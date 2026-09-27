---
name: ticfac
description: Run, watch and triage epic runs with ticfac — the execution half of the ticks workflow. Use when the user asks to run an epic, start or resume a run, watch or follow a run's progress, check the overview or a run's status, triage a run's findings, or drive an epic from start to close. Triggers include "run the epic", "ticfac run", "watch the run", "what is the run doing", "triage findings", "epic status", "resume the run".
---

# ticfac — running epics

Boundary with the ticks skill: ticks owns PLANNING — creating ticks and epics,
breaking work down, tick shape and tracker state. ticfac owns EXECUTION —
starting, following, triaging and finishing an epic run. Plan with `tk`; run
with `ticfac`. Never contradict the ticks skill: when a task is "plan this"
or "break this down", use ticks; when it is "run this epic" or "what is the
run doing", use ticfac.

Install or update this skill with `ticfac skills install ticfac` — the same
one command as the ticks skill's `tk skills install ticks`.

## The loop

1. `ticfac init` — make the repository runnable (writes `.tick/runners.toml`,
   guesses the testing gate). Flags answer every question:
   `--substrate local|cloud|both --runner claude|pi --model <m> --gate '<cmd>'`;
   `--yes` takes every default.
2. `ticfac doctor` — is the machine ready? Exit 0 yes; 1 names each missing
   thing with the one command that fixes it.
3. `ticfac run <epic>` — start the epic in the background and attach to the
   live view. Ctrl-C detaches; the run keeps going. Run it again to attach to
   a live run or resume a stopped one. `epic-<id>` is accepted everywhere.
4. `ticfac` with no arguments — every run, attention first; each held or
   failed run names the one command that clears it.
5. `ticfac status <run-id>` — is it alive; `ticfac events <run-id> --follow`
   — what it is doing, as it does it; `ticfac watch <run-id>` — the whole
   epic at a glance, live.
6. `ticfac triage <epic> <key-prefix>=absorb|file|fixed:<commit>|discard
   --by <who>` — settle findings by short key prefix;
   `ticfac triage <epic> --json` lists what waits.
7. Clear what a stop names, then `ticfac run <epic>` again, until the run
   completes.

Every command takes `--json` — one versioned document, the schema named in
it, nothing the document needs on stderr — and exits the documented table:
0 done, 1 failed, 2 usage, 3 the run ended holding something only a person
can move (the reason class is in the line), 4 a lookup that came back empty,
5 the command ended while the run is still going (detached — nothing is
wrong), 6 io. `ticfac status` is the one exception: it exits the run's
liveness, 0 alive, 1 not.

The epic finishes when its run completes: every tick gated and merged onto
the epic branch, the PR opened, CI green on it. The MERGE is a person's.
A run that ends holding something for a person exits 3 and names the wait
kind; clear it (triage, settle, the fix it names) and run again.
