#!/bin/sh
# seeded-needs-you-nothing.sh — the seeded bug for the watch dashboard's
# needs-you property: a run that ended holding a finding for a person, whose
# frame says "Needs you: nothing". The first question the dashboard exists
# to answer, answered with a lie. The screen is the redesigned dashboard's
# own layout (2026-10): key hints last, the ticks grouped by state, the
# quiet answer beside the health line — every element the real frame draws,
# so the property it violates is live, not vacuous.
printf '! ticfac watch: run epic-hld is holding for a person: finding_untriaged: a finding waits for\n'
printf 'a person — move it on: ticfac triage hld\n'
printf 'a fixture epic for the terminal property suite (hld)                     local · failed\n'
printf '━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n'
printf 'Needs you: nothing         ● stopped: no process holds this run · 2 of 4 done\n'
printf 'Building ──── Reviewing ──── Closing out ──── PR & CI ──── Merged\n'
printf '▲ here\n'
printf 'UP NEXT (4)   t01 first wave tick one · t02 first wave tick two · t03 second wave tick three …\n'
printf '              then: PR & CI → Merged\n'
printf '─ latest ────────────────────────────────────────────────────────────────────────────────────\n'
printf '09:48:55  t1#1  paused: needs you\n'
printf '09:49:55  run  run finished: the run holds a finding for a person\n'
printf '                                                     [enter] details  [e] all events  [q] quit\n'
sleep 120
