package cli

// The key handling of the live watch (epic hn6, wave 4 — tick c2u), pinned
// headless: parseKeys maps the terminal's raw bytes to key names, and the
// watchUI reducer walks the dashboard's rows and opens the drill-in views.
// No pty and no herdr here — a byte slice in, a state out.

import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// TestWatchKeysParse: the byte sequences the tick names map to the key names
// the reducer reads, several keys ride in one chunk when a person types
// fast, and a complete-but-unknown escape sequence is ignored whole — a
// terminal that sends sequences this watch does not answer must not have
// them leak in as movement.
func TestWatchKeysParse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{"enter", "\r", []string{"enter"}},
		{"escape", "\x1b", []string{"esc"}},
		{"arrow up", "\x1b[A", []string{"up"}},
		{"arrow down", "\x1b[B", []string{"down"}},
		{"application-mode arrows", "\x1bOA\x1bOB", []string{"up", "down"}},
		{"q", "q", []string{"q"}},
		{"e", "e", []string{"e"}},
		{"j", "j", []string{"j"}},
		{"k", "k", []string{"k"}},
		{"ctrl-c", "\x03", []string{"ctrl-c"}},
		{"a typed word", "jek\x03", []string{"j", "e", "k", "ctrl-c"}},
		{"unknown escape ignored", "\x1b[Z", nil},
		{"unknown escape among keys", "j\x1b[Zk", []string{"j", "k"}},
		{"unmapped bytes ignored", "x\n y", nil},
	} {
		got := parseKeys([]byte(tc.raw))
		if len(got) != len(tc.want) {
			t.Errorf("%s: parseKeys(%q) = %v, want %v", tc.name, tc.raw, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: parseKeys(%q) = %v, want %v", tc.name, tc.raw, got, tc.want)
				break
			}
		}
	}
}

// TestWatchKeysNavigate: j and k walk the plan order and clamp at both ends
// — the first and last row are walls; enter with nothing under the cursor
// opens the first in-flight tick; e opens the feed and esc or q comes back
// down. The cursor is the dashboard's own "▸" row, so the state it names
// must be a tick the plan carries.
func TestWatchKeysNavigate(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	// Plan order, the waves in order and the tracker's order inside each
	// wave, the absorbed child under its parent: t1, t1c, t2, t3, t4.
	const order = "t1 t1c t2 t3 t4"

	u := watchUI{}
	if got, want := u.key("j", m).selected, "t1"; got != want {
		t.Errorf("j with no cursor selected %q, want the plan's first row %q", got, want)
	}
	// k with no cursor has no previous row to move to: it stays no cursor.
	if got := (watchUI{}).key("k", m).selected; got != "" {
		t.Errorf("k with no cursor selected %q, want no cursor", got)
	}

	// Down to the end and clamp; back up to the top and clamp.
	u = watchUI{}
	for _, want := range []string{"t1", "t1c", "t2", "t3", "t4", "t4"} {
		u = u.key("j", m)
		if u.selected != want {
			t.Fatalf("j walked to %q, want %q", u.selected, want)
		}
	}
	for _, want := range []string{"t3", "t2", "t1c", "t1", "t1"} {
		u = u.key("k", m)
		if u.selected != want {
			t.Fatalf("k walked to %q, want %q", u.selected, want)
		}
	}

	// enter with nothing under the cursor opens the first in-flight tick —
	// t2 stands dispatched on its second try; t1 and t1c closed before it.
	u = watchUI{}
	u = u.key("enter", m)
	if u.view != watchViewTick || u.selected != "t2" {
		t.Errorf("enter with no cursor opened view %q for %q, want the tick view for the in-flight t2", u.view, u.selected)
	}
	// esc comes back down, the cursor staying where it was.
	u = u.key("esc", m)
	if u.view != watchViewDashboard || u.selected != "t2" {
		t.Errorf("esc landed on view %q with cursor %q, want the dashboard with t2", u.view, u.selected)
	}
	// ...and enter now opens the tick the cursor is on.
	if u = u.key("enter", m); u.view != watchViewTick {
		t.Errorf("enter under a cursor stayed on %q", u.view)
	}

	// e opens the feed from the dashboard; esc and q both come back.
	u = watchUI{}
	if u = u.key("e", m); u.view != watchViewFeed {
		t.Errorf("e did not open the feed (view %q)", u.view)
	}
	if u = u.key("esc", m); u.view != watchViewDashboard {
		t.Errorf("esc did not come back from the feed (view %q)", u.view)
	}
	u = u.key("e", m)
	u = u.key("q", m)
	if u.view != watchViewDashboard {
		t.Errorf("q in a sub-view did not come back (view %q)", u.view)
	}

	// The arrows alias j and k.
	u = watchUI{}
	if u = u.key("down", m); u.selected != "t1" {
		t.Errorf("the down arrow selected %q, want t1", u.selected)
	}
	if u = u.key("up", m); u.selected != "t1" {
		t.Errorf("the up arrow at the first row moved to %q, want the clamp", u.selected)
	}

	// The feed view scrolls: j reveals newer lines until the bottom, k goes
	// back up. The feed's own length is the wiring's clamp; the reducer's
	// own clamp is only the bottom.
	u = watchUI{view: watchViewFeed, scroll: 2}
	u = u.key("j", m)
	if u.scroll != 1 {
		t.Errorf("j in the feed scrolled to %d, want 1", u.scroll)
	}
	u = u.key("j", m)
	if u.scroll != 0 {
		t.Errorf("j at the feed's bottom scrolled to %d, want 0", u.scroll)
	}
	if u = u.key("k", m); u.scroll != 1 {
		t.Errorf("k in the feed scrolled to %d, want 1", u.scroll)
	}
	if u = u.key("esc", m); u.view != watchViewDashboard {
		t.Errorf("esc did not come back from a scrolled feed (view %q)", u.view)
	}

	// A selection the plan no longer carries is no selection: a tick
	// absorbed or replanned away mid-run must not leave the cursor pointing
	// at a row that is gone.
	u = watchUI{selected: "zz"}
	if u = u.key("j", m); u.selected != "t1" {
		t.Errorf("a dangling cursor survived a key: %q, want the plan's first row", u.selected)
	}
	u = watchUI{view: watchViewTick, selected: "zz"}
	if u = u.key("j", m); u.view != watchViewDashboard {
		t.Errorf("a dangling cursor's tick view stayed up (view %q)", u.view)
	}
}

// TestWatchKeysEnterHasNowhereToGo: a model whose shape could not be read
// has no rows to walk and no tick to open — the keys do nothing, and the
// dashboard stays up rather than opening a view of nothing.
func TestWatchKeysEnterHasNowhereToGo(t *testing.T) {
	t.Parallel()
	m := statusmodel.Model{}
	u := watchUI{}
	u = u.key("j", m)
	if u.selected != "" || u.view != watchViewDashboard {
		t.Errorf("j on an unreadable epic selected %q in view %q", u.selected, u.view)
	}
	u = u.key("enter", m)
	if u.view != watchViewDashboard {
		t.Errorf("enter on an unreadable epic opened %q", u.view)
	}
}
