package cli

// The watch's exit must not stall on a terminal query (tick jmf). When the
// live view returns a non-zero exit, fang's error path asks the terminal for
// its colours (mustColorscheme -> lipgloss.HasDarkBackground: OSC 11 written
// to the terminal, stdin read for the reply) — but the key reader goroutine
// the live view left behind is still parked in a read of the same stdin, so
// the reply is swallowed by it and the query parks on the read lock it never
// gets: the process hangs until another byte arrives (measured: exit 10s
// after q in one run, still alive 30s+ after q in another), and on a
// terminal that never answers the query, it hangs forever. The fix routes a
// printedExit — a body that already said its own refusal — out of fang's
// error path entirely, so no query happens at all; this test is the
// end-to-end proof on a real pty, the only place the defect can be seen.
//
// The choreography is the real one: the terminal ANSWERS the colourscheme
// query, and the answer is the exact byte the stalled exit waits for. A
// pty master that answers the way a terminal answers turns the hidden
// stall — "hangs until another byte arrives" — into a bounded, observable
// one: the reply arrives, the key reader eats it, and the process stays
// alive, still reading a stdin whose lock it will hand back to nobody.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
)

// The colourscheme query the exit path writes, and a terminal's answer to
// it: an OSC 11 reply naming a background colour. This is what iTerm, a
// herdr pane and most terminal emulators put on the wire.
const (
	watchExitOSCQuery = "\x1b]11;?"
	watchExitOSCReply = "\x1b]11;rgb:1c1c/1c1c/1c1c\x1b\\"
)

// TestWatchOnAPtyExitsWhileTheKeysStillHoldStdin (tick jmf): a watch whose
// run ended holding is closed with q, the terminal answers the colourscheme
// query the exit path writes, and the process must still EXIT — promptly,
// with the held class the ending owns — because the exit path never asks a
// terminal it no longer owns for its colours.
//
// short: one pty scenario, a couple of seconds; the only end-to-end proof
// the exit path's terminal query stalls the process, and the tick's
// acceptance.
func TestWatchOnAPtyExitsWhileTheKeysStillHoldStdin(t *testing.T) {
	now := time.Now()
	repo, home := modelFixture(t, now)
	runID := "epic-rmod"

	// A hold stands from the first frame, and the run ends still holding
	// it: the same scenario the staircase test ends by — q closes the
	// standing dashboard through the run's own ending, the held class (3).
	two := 2
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(now, runID, "t2", &two,
		reconcile.StageRunHeld, "attempt_unaddressed: nobody can say whether the attempt is running"))

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("open a pty: %v", err)
	}
	t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatalf("size the pty: %v", err)
	}

	// This binary, re-exec'd, on a pty for all three streams — the same
	// helper the staircase test drives, so the watch's own seams answer
	// for a real terminal and the keys are attached for real.
	cmd := exec.Command(os.Args[0], "-test.run=TestWatchPtyHelperProcess$")
	cmd.Env = append(os.Environ(),
		"NO_COLOR=1",
		"TICFAC_WATCH_PTY_HELPER=1",
		"TICFAC_WATCH_PTY_REPO="+repo,
		"TICFAC_WATCH_PTY_HOME="+home,
		"TICFAC_WATCH_PTY_RUN="+runID,
	)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the watch on the pty: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	stream := &ptyStream{}
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				stream.write(buf[:n])
			}
			if err != nil {
				return // the slave's last fd is closed: nothing more to read
			}
		}
	}()

	// The dashboard is up and the keys are attached.
	ptyWaitsFor(t, stream, "the dashboard's tick rows", func(s string) bool {
		return strings.Contains(s, "the second tick") && strings.Contains(s, "the fifth tick")
	})

	// The run ends, still holding: the dashboard stands open for drill-in,
	// and q closes it through the run's own ending.
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "", nil,
		reconcile.StageRunFinished, "failed: t2 did not pass"))
	life.Release("ended holding")
	ptyWaitsFor(t, stream, "the end kept above the block", func(s string) bool {
		return strings.Contains(s, "has ended — the dashboard stays open")
	})
	if _, err := master.Write([]byte("q")); err != nil {
		t.Fatalf("press q on the pty: %v", err)
	}
	// The closing message is the watch body's own last word — the words of
	// the ending, in cooked output. Everything after it on the exit path is
	// the wrapper's, and that is where the defect waits.
	ptyWaitsFor(t, stream, "the closing message", func(s string) bool {
		return strings.Contains(s, "ended holding something for a person")
	})

	// The condition wait: the exit, bounded. Along the way, the choreography
	// the defect needs is fed for real — when the exit path's colourscheme
	// query reaches the pty, it is answered the way a terminal answers. A
	// fixed exit path writes no query at all, so no reply is ever fed; a
	// broken one writes the query, gets its reply eaten by the key reader
	// still parked on stdin, and never exits.
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	exited := false
	fedReply := false
	deadline := time.Now().Add(3 * time.Second)
	for !exited && time.Now().Before(deadline) {
		select {
		case <-waitDone:
			exited = true
			continue
		default:
		}
		if !fedReply && strings.Contains(stream.String(), watchExitOSCQuery) {
			fedReply = true
			if _, err := master.Write([]byte(watchExitOSCReply)); err != nil {
				t.Fatalf("answer the colourscheme query the way a terminal does: %v", err)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !exited {
		_ = cmd.Process.Kill()
		<-waitDone
		t.Fatalf("the watch did not exit within 3s of q — the exit path asked the terminal for its colours "+
			"(query seen on the wire: %v, reply answered: %v) while the key reader still holds stdin; "+
			"the process was still alive reading a reply it will never be given:\n%s",
			strings.Contains(stream.String(), watchExitOSCQuery), fedReply, stream.String())
	}
	if fedReply {
		t.Errorf("the exit path queried the terminal for its colours after the watch returned — " +
			"the query reached the pty while the watch's key reader still held stdin")
	}
	// The exit class itself is the helper's own assertion: it checks Run's
	// code in-process (the held class) before its process can exit, the same
	// contract the staircase test's helper pins.
}
