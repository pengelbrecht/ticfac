#!/bin/sh
# seeded-groups-reordered.sh — the seeded bug for the grouped-order
# property's OTHER claim: the ticks grouped by state, each group's rows in
# the plan's own order, but the groups themselves standing in an order the
# design never drew (UP NEXT before DONE, where the redesign draws NOW,
# DONE, UP NEXT, HELD). A person reading the dashboard reads the groups as
# a shape — work that is left before work that is done — so a dashboard
# that reorders them is a different answer, not a different layout.
# rows-reordered.sh is the same property's other seeded program: it breaks
# the rows within a group, this one the order of the groups, so each of the
# property's two claims is shown to fail on its own.
printf '! ticfac watch: run epic-hld is holding for a person: finding_untriaged: a finding waits for\n'
printf 'a person — move it on: ticfac triage hld\n'
printf 'a fixture epic for the terminal property suite (hld)                     local · failed\n'
printf '━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n'
printf '┌────────────────────────────────────────────────────────────┐\n'
printf '│ Needs you: finding_untriaged: a finding waits for a person │\n'
printf '│ clear with: ticfac triage hld                              │\n'
printf '└────────────────────────────────────────────────────────────┘\n'
printf '● stopped: no process holds this run: the last one released it · 2 of 4 done\n'
printf 'Building ──── Reviewing ──── Closing out ──── PR & CI ──── Merged\n'
printf '▲ here\n'
printf 'UP NEXT (2)   t03 second wave tick three · t04 second wave tick four\n'
printf '              then: PR & CI → Merged\n'
printf 'DONE (2)\n'
printf '  t01  first wave tick one       merged          22m\n'
printf '  t02  first wave tick two       merged          34m\n'
printf '─ latest ────────────────────────────────────────────────────────────────────────────────────\n'
printf '09:48:55  t1#1  paused: needs you\n'
printf '09:49:55  run  run finished: the run holds a finding for a person\n'
printf '                                                     [enter] details  [e] all events  [q] quit\n'
sleep 120
