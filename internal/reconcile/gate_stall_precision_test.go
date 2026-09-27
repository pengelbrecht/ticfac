package reconcile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The gate's stall warning compared ROUNDED durations against its threshold:
// idle and elapsed were rounded to the second for the feed line and then
// tested against StallWarnAfter. Any silence of 500ms or more rounds to 1s, so
// under a sub-second threshold a gate printing every 100ms was warned about
// whenever one gap reached 500ms — TestAQuietGateIsWarnedAboutAndATalkativeOne
// IsNot failed on CI that way (PR #77). The threshold is compared against what
// was measured; rounding is for the words only.
// short: one call on in-memory state and two temp files
func TestTheGateStallThresholdIsComparedUnrounded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, errPath := filepath.Join(dir, "out"), filepath.Join(dir, "err")
	if err := os.WriteFile(out, []byte("working\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(errPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Round(0)
	// Output 600ms ago: silent for less than the 900ms threshold, but 600ms
	// rounds to a second.
	if err := os.Chtimes(out, now.Add(-600*time.Millisecond), now.Add(-600*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(errPath, now.Add(-3*time.Second), now.Add(-3*time.Second)); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{now: func() time.Time { return now }, opts: Options{StallWarnAfter: 900 * time.Millisecond}}
	g := &gateCommand{command: GateCommand{Name: "test"}, shell: &gateShell{
		outPath: out, errPath: errPath, startedAt: now.Add(-3 * time.Second), deadline: time.Now().Add(time.Minute)}}
	r.announceGate(g, attemptHandle{TickID: "a1"}, merge{})
	for _, event := range r.Journal() {
		if event.Stage == StageGateStalled {
			t.Fatalf("a gate silent for 600ms was warned about under a 900ms threshold: %s", event.Detail)
		}
	}
}
