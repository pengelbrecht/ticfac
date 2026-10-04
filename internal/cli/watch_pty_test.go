package cli

// The live view on a REAL terminal (tick r3x). The seams the in-process
// tests fake — watchIsTerminal, watchTerminalSize, watchAttachKeys — are
// exactly the facts this tick's defect hid behind: with the keyboard's raw
// mode on, the terminal's line discipline no longer returns the carriage
// after a newline, so every line the live view writes while the keys are
// attached must end \r\n, and the watch's closing words must be written
// after the terminal is restored. A buffer with the seams faked cannot see
// either, because the seam is the thing that was wrong. So this test
// re-execs the test binary with a pty on stdin, stdout and stderr — a real
// terminal at a real size, keys attached for real — and reads the master's
// byte stream: every newline that reaches it must carry its own carriage,
// every line the watch drew must fit the pane, and the closing message must
// arrive after the restore, its lines starting at column 0 like every other
// line.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
)

// ptyStream is the master's byte stream, collected by a reader goroutine so
// the parent can wait on what the terminal actually received.
type ptyStream struct {
	mu  sync.Mutex
	buf []byte
}

func (s *ptyStream) write(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
}

func (s *ptyStream) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.buf)
}

// ptyWaitsFor polls the stream until `holds` or the deadline: a condition
// wait over the observable (what the terminal received), never a guess
// about the child's timing.
func ptyWaitsFor(t *testing.T, stream *ptyStream, what string, holds func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if holds(stream.String()) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; the terminal received:\n%s", what, ansi.Strip(stream.String()))
}

// TestWatchOnAPtyRendersWithoutAStaircase (tick r3x): the dashboard on a
// real terminal with the keys on, at the two sizes the acceptance names.
// Every newline the watch writes reaches the master carrying its carriage
// (\r\n) — no line starts where the previous one ended — every line the
// watch drew fits the pane, the keys really are on (j moves the cursor),
// and the closing message arrives after the terminal was restored.
//
// short: one pty scenario per size, a few seconds each; the only end-to-end
// proof a real terminal's raw mode can carry, and the tick's acceptance.
func TestWatchOnAPtyRendersWithoutAStaircase(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
	}{
		{"120x40", 120, 40},
		{"80x24", 80, 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			watchOnAPty(t, tc.width, tc.height)
		})
	}
}

func watchOnAPty(t *testing.T, width, height int) {
	t.Helper()
	now := time.Now()
	repo, home := modelFixture(t, now)
	runID := "epic-rmod"

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	// A hold stands from the first frame — the same words the in-process
	// hold test uses — so the kept alert above the block is part of the
	// scenario, and the run ends still holding it: the closing message the
	// acceptance names is the hold's.
	two := 2
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(now, runID, "t2", &two,
		reconcile.StageRunHeld, "attempt_unaddressed: nobody can say whether the attempt is running"))

	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("open a pty: %v", err)
	}
	t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })
	if err := pty.Setsize(slave, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)}); err != nil {
		t.Fatalf("size the pty to %dx%d: %v", width, height, err)
	}

	// This binary, re-exec'd: the child sees the pty on all three streams,
	// so the watch's own seams — IsTerminal, the size, raw mode — answer for
	// a real terminal, which is the fact the staircase hid behind.
	cmd := exec.Command(os.Args[0], "-test.run=TestWatchPtyHelperProcess$")
	cmd.Env = append(os.Environ(),
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
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
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

	// The dashboard renders from the run's own records: the epic's rows are
	// on the terminal.
	ptyWaitsFor(t, stream, "the dashboard's tick rows", func(s string) bool {
		return strings.Contains(s, "the second tick") && strings.Contains(s, "the fifth tick")
	})
	// The keys are on for real: j moves the drill-in cursor onto the first
	// row, and the frame drawn after it carries the marker.
	if _, err := master.Write([]byte("j")); err != nil {
		t.Fatalf("press j on the pty: %v", err)
	}
	ptyWaitsFor(t, stream, "the cursor marker j moved", func(s string) bool {
		return strings.Contains(s, "▸t1")
	})

	// The run ends, still holding: the watch ends 3, and the closing message
	// is the end-of-watch message the acceptance covers.
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "", nil,
		reconcile.StageRunFinished, "failed: t2 did not pass"))
	life.Release("ended holding")

	if err := cmd.Wait(); err != nil {
		t.Fatalf("the watch on the pty did not end clean: %v\nthe terminal received:\n%s",
			err, ansi.Strip(stream.String()))
	}
	// The parent's copy of the slave was the last one open: closing it ends
	// the master's reads, and the reader drains what the child wrote before
	// it exited.
	_ = slave.Close()
	<-readerDone
	got := stream.String()

	// No staircase: every newline on the wire carries its carriage. While
	// the keyboard's raw mode was on, the watch wrote its own \r; after the
	// restore, the line discipline's ONLCR added it — so a bare \n here is a
	// line that starts where the previous one ended, whatever wrote it.
	for i := 0; i < len(got); i++ {
		if got[i] == '\n' && (i == 0 || got[i-1] != '\r') {
			t.Fatalf("a bare \\n reached the terminal at byte %d — the staircase, in the bytes:\n%q",
				i, got[max(0, i-120):min(len(got), i+120)])
		}
	}

	// The end-of-watch message: the run ended holding, the message names
	// what and the command that moves it on, and it rode the same wire the
	// frames did — so the bare-newline check above covers its lines too.
	for _, want := range []string{
		"ended holding something for a person:",
		"attempt_unaddressed: nobody can say whether the attempt is running",
		"ticfac settle rmod t2 2 --release",
		"TICFAC-PTY-PROBE plain-newline-after-the-watch",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the terminal never received %q; it received:\n%s", want, ansi.Strip(got))
		}
	}

	// Every line the watch DREW fits the pane: the frames, the kept alert
	// above the block and the run's own last word — everything before the
	// closing message, which is ordinary cooked output a terminal wraps.
	closing := strings.Index(got, "ended holding something for a person:")
	if closing < 0 {
		t.Fatalf("the closing message never arrived; the terminal received:\n%s", ansi.Strip(got))
	}
	for i, line := range strings.Split(got[:closing], "\r\n") {
		if w := ansi.StringWidth(ansi.Strip(line)); w > width {
			t.Errorf("line %d is %d cells wide in a %d-column pane: %q", i, w, width, ansi.Strip(line))
		}
	}
}

// TestWatchPtyHelperProcess is the pty test's other end: this binary
// re-exec'd with the pty's slave on stdin, stdout and stderr, so the watch
// sees a real terminal — raw mode, a real size — the one thing the seams
// cannot fake in-process. It runs the watch over the fixture the parent
// built, then writes one probe line with a plain \n: after the watch has
// restored the terminal, the line discipline translates that newline
// itself, and a watch that forgot the restore leaves it bare on the wire —
// the staircase, caught on the probe.
func TestWatchPtyHelperProcess(t *testing.T) {
	if os.Getenv("TICFAC_WATCH_PTY_HELPER") == "" {
		t.Skip("the pty test's helper: re-exec'd with a pty on stdin, stdout and stderr")
	}
	repo, runID := os.Getenv("TICFAC_WATCH_PTY_REPO"), os.Getenv("TICFAC_WATCH_PTY_RUN")
	home := os.Getenv("TICFAC_WATCH_PTY_HOME")
	if repo == "" || runID == "" {
		t.Fatal("the helper was started without its fixture")
	}
	// The tracker is faked in the helper's own process: the seam crosses no
	// process boundary, and the epic's shape is the graph the parent's
	// scenario renders.
	fakeTheTracker(t, threeWaveGraph())
	t.Setenv("HOME", home)
	code := Run([]string{"watch", "--repo", repo, "--interval", "150ms", runID}, os.Stdout, os.Stderr)
	// The watch has returned: whatever mode it left the terminal in, this
	// probe line is written the ordinary way — cooked, into the mode the
	// watch restored.
	fmt.Fprintf(os.Stdout, "TICFAC-PTY-PROBE plain-newline-after-the-watch\n")
	if code != ExitHeld {
		t.Fatalf("the watch on the pty exited %d, want %d (the run ended holding for a person)", code, ExitHeld)
	}
}
