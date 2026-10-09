#!/bin/sh
# seeded-needs-you-nothing.sh — the seeded bug for the watch dashboard's
# needs-you property: a run that ended holding a finding for a person, whose
# frame says "needs you: nothing". The first question the dashboard exists
# to answer, answered with a lie.
printf '! ticfac watch: run epic-hld is holding for a person: finding_untriaged waits\r\n'
printf 'hld a fixture epic for the terminal property suite    local - failed\r\n'
printf 'needs you: nothing\r\n'
printf '\r\n'
printf ' TICK  WHAT            TIER PIPELINE TIME ATTEMPTS\r\n'
printf ' t01   first wave tick one     ...             0\r\n'
printf ' t02   first wave tick two     ...             0\r\n'
printf ' t03   second wave tick three  ...             0\r\n'
printf ' t04   second wave tick four   ...             0\r\n'
printf 'cost not metered\r\n'
printf '            [e] events  [enter] tick\r\n'
sleep 120
