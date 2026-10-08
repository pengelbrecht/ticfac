#!/bin/sh
# seeded-never-frames.sh — the seeded bug for the first-frame bound (#112):
# a watch that draws nothing for far longer than its bound, the way the
# loaded-host's five subprocesses once kept the follow's first frame from
# ever landing (the defect status_firstframe_test.go pins in process, seen
# here from outside).
sleep 120
