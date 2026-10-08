// gatejoiner is the late member of a gate's process group: the outcome of
// the kernel race in which a fork in flight joins the group after killpg's
// walk has already passed it, and so never receives the signal. See
// internal/reconcile/gate_unix.go for the measurement on this host, and
// internal/exec/subprocess/kill_repeat_test.go for the same race on attempts.
//
// It starts IN the gate's group, like any child the gate's shell forks, then
// leaves it — so the gate's first group kill cannot reach it — and rejoins
// after that kill has been delivered. The shell that starts it passes the
// gate slot's lock as fd 3, which this helper never closes: it holds the slot
// from its first instruction to its death, exactly as the missed member of a
// killed gate does.
//
// Usage: gatejoiner OUT GO
//
// OUT is written once the helper is out of the group — the observable a test
// waits on before killing the gate, so the kill can never land before the
// leave. GO is the marker written when the collector is about to collect;
// the rejoin happens a grace after GO, so the first kill has been delivered
// by then. A member that rejoined before the kill would be IN the walk, and
// there would be no miss to reproduce.
package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

const (
	// pollGo is how often to look for GO, and giveUpAfter is how long to
	// keep looking before leaving — a test that never wrote GO was itself
	// collected, and the group this helper is a member of dies on its own.
	pollGo       = 50 * time.Millisecond
	giveUpAfter  = 30 * time.Second
	rejoinGrace  = 500 * time.Millisecond
	holdForAfter = 2 * time.Minute
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: gatejoiner OUT GO")
		os.Exit(2)
	}
	out, goMark := os.Args[1], os.Args[2]

	// The gate's group, recorded while still a member of it.
	pgid := syscall.Getpgrp()
	// Out: a group of its own, which the gate's group kill does not reach.
	if err := syscall.Setpgid(0, 0); err != nil {
		fmt.Fprintf(os.Stderr, "gatejoiner: leave the gate's group: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, []byte("out\n"), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "gatejoiner: mark that it is out: %v\n", err)
		os.Exit(1)
	}

	// Wait for the collector to be about to collect.
	deadline := time.Now().Add(giveUpAfter)
	for {
		if _, err := os.Stat(goMark); err == nil {
			break
		}
		if time.Now().After(deadline) {
			// Nobody came for this gate: hold nothing further.
			return
		}
		time.Sleep(pollGo)
	}
	time.Sleep(rejoinGrace)

	// The rejoin — the whole point. It is the state the kernel race leaves:
	// a member of the group that the group's kill never reached, holding the
	// slot's lock (fd 3). It FAILS when the group is gone — a collector that
	// kills once and reaps immediately has destroyed it — and that failure is
	// the verdict on such a collector: its missed member goes on holding the
	// slot with nothing left that can reach it.
	_ = syscall.Setpgid(0, pgid)

	// And hold the slot until killed, as the missed member would.
	time.Sleep(holdForAfter)
}
