#!/bin/sh
# seeded-rows-reordered.sh — the seeded bug for the rows-never-reorder
# property: the tick table's rows in an order the plan never had. The
# dashboard's rows are a person's place-keeping; a table that reshuffles on
# every frame is a table nobody can read.
printf '! ticfac watch: run epic-hld is holding for a person: finding_untriaged waits\r\n'
printf 'needs you: finding_untriaged: a finding waits for a person - ticfac triage hld\r\n'
printf '\r\n'
printf ' TICK  WHAT            TIER PIPELINE TIME ATTEMPTS\r\n'
printf ' t03   second wave tick three  ...             0\r\n'
printf ' t01   first wave tick one     ...             0\r\n'
printf ' t04   second wave tick four   ...             0\r\n'
printf ' t02   first wave tick two     ...             0\r\n'
printf 'cost not metered\r\n'
printf '            [e] events  [enter] tick\r\n'
sleep 120
