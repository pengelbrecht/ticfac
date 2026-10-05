package cli

// The dashboard's colour accents (tick 5ba): colour carries meaning,
// sparingly, never decoration — and the frame tests here read the colour
// the way a terminal does. A rendered frame is a byte stream of words and
// SGR sequences; the grid parser below eats it the way a terminal eats it,
// leaving one cell per display column carrying the attribute state it was
// drawn in, so a test asserts the colour of the state, not the spelling of
// an escape sequence at one width by accident.
//
// The palette is the tick's semantic one, ANSI 16-colour names so terminal
// themes map them: green = done/closed/passed/healthy; amber = in
// flight/working/stall warning; red = failed/rejected/held/needs-you (the
// needs-you line the most prominent thing on screen when non-empty:
// red+bold); cyan = tick ids and key hints; dim/grey = secondary text
// (closed groups, provenance, the feed tail's timestamps, "not metered");
// bold for section headers. At most those four hues, no background fills,
// and NO_COLOR and TERM=dumb render plain text with an identical layout —
// colour never changes widths.

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// atTime parses one timestamp the feed fixtures lean on, refusing to guess
// at a clock a test cannot read back.
func atTime(t *testing.T, stamp string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("the fixture stamp %q does not parse: %v", stamp, err)
	}
	return at
}

// gridCell is one grid cell after a terminal ate the frame's SGR
// sequences: the glyph it shows and the attribute state it shows it in.
type gridCell struct {
	text string
	fg   string // "", or the hue: red, green, amber, cyan
	dim  bool
	bold bool
}

// screenGrid eats one rendered frame the way a terminal eats it: every SGR
// sequence changes the attribute state, every other rune becomes one cell
// carrying the state it was drawn in. The frame's own cursor-movement
// sequences never appear inside a line, so a line-by-line eat is the
// emulator's whole answer here.
func screenGrid(frame []string) [][]gridCell {
	grid := make([][]gridCell, len(frame))
	for i, line := range frame {
		row := []gridCell{}
		fg, dim, bold := "", false, false
		for j := 0; j < len(line); {
			if line[j] == '\x1b' && j+1 < len(line) && line[j+1] == '[' {
				end := j + 2
				for end < len(line) && line[end] != 'm' {
					end++
				}
				for _, param := range strings.Split(line[j+2:end], ";") {
					switch param {
					case "0", "":
						fg, dim, bold = "", false, false
					case "1":
						bold = true
					case "2":
						dim = true
					case "31":
						fg = "red"
					case "32":
						fg = "green"
					case "33":
						fg = "amber"
					case "36":
						fg = "cyan"
					}
				}
				j = end + 1
				continue
			}
			r, size := utf8.DecodeRuneInString(line[j:])
			row = append(row, gridCell{text: string(r), fg: fg, dim: dim, bold: bold})
			j += size
		}
		grid[i] = row
	}
	return grid
}

// gridCells is the cells one rendered word occupies, found across the
// frame by its own text — the assertion reads "the cells the word ✓ 2
// closed was drawn in", never "row 14, column 3".
func gridCells(grid [][]gridCell, what string) []gridCell {
	runes := []rune(what)
	for _, row := range grid {
		if cells := gridRowCells(row, runes); cells != nil {
			return cells
		}
	}
	return nil
}

// gridRowCells is the cells `what` occupies inside one row — the row
// scoped search the state assertions need, so "the ● of t2's pipeline"
// never matches the health verdict's ● first.
func gridRowCells(row []gridCell, runes []rune) []gridCell {
	if len(row) < len(runes) {
		return nil
	}
	for i := 0; i+len(runes) <= len(row); i++ {
		match := true
		for k, r := range runes {
			if []rune(row[i+k].text)[0] != r {
				match = false
				break
			}
		}
		if match {
			return row[i : i+len(runes)]
		}
	}
	return nil
}

// gridRowOf is the one row a marker was drawn in — the scoping handle the
// per-state assertions pass back in.
func gridRowOf(grid [][]gridCell, marker string) []gridCell {
	for _, row := range grid {
		if gridRowCells(row, []rune(marker)) != nil {
			return row
		}
	}
	return nil
}

// gridCellsInRow is the cells `what` occupies in the one row `marker`
// names — the form the state assertions read: the colour of t2's pipeline
// ●, read on t2's own row, never on the first ● the frame happens to carry.
func gridCellsInRow(grid [][]gridCell, marker, what string) []gridCell {
	row := gridRowOf(grid, marker)
	if row == nil {
		return nil
	}
	return gridRowCells(row, []rune(what))
}

// assertGridAttr says the cells a word was drawn in carry exactly the
// attribute state the palette names — one hue at most, no decoration a
// state did not ask for.
func assertGridAttr(t *testing.T, cells []gridCell, what, fg string, dim, bold bool) {
	t.Helper()
	if cells == nil {
		t.Fatalf("%q is not on the screen", what)
	}
	for i, c := range cells {
		if c.fg != fg || c.dim != dim || c.bold != bold {
			t.Errorf("%q's cell %d (%q) carries fg=%q dim=%v bold=%v, want fg=%q dim=%v bold=%v",
				what, i, c.text, c.fg, c.dim, c.bold, fg, dim, bold)
		}
	}
}

// colourGrid renders the fixture with the ANSI style set and returns the
// terminal's grid of it.
func colourGrid(t *testing.T, m statusmodel.Model, width, height int) [][]gridCell {
	t.Helper()
	return screenGrid(renderWatchFrame(m, ansiWatchStyles(), width, height, ""))
}

// TestWatchColourGridPerState: the frame tests the acceptance names, read
// off a terminal-emulator screen grid — the colour attribute per state.
func TestWatchColourGridPerState(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	grid := colourGrid(t, m, 120, 0)
	const t2row = "the second tick's gloss"

	// Tick ids are cyan — the identity a person scans for.
	assertGridAttr(t, gridCellsInRow(grid, t2row, "t2"), "t2", "cyan", false, false)
	assertGridAttr(t, gridCellsInRow(grid, "a repair child", "t1c"), "t1c", "cyan", false, false)

	// An in-flight pipeline stage is amber, in words and in glyphs.
	assertGridAttr(t, gridCellsInRow(grid, t2row, "●gate"), "●gate", "amber", false, false)
	narrow := colourGrid(t, m, 99, 0)
	assertGridAttr(t, gridCellsInRow(narrow, t2row, "●"), "●", "amber", false, false)

	// Section headers are bold: the table's, the workers panel's, the
	// recent rule's.
	for _, header := range []string{"TICK", "WORKERS", "recent"} {
		assertGridAttr(t, gridCells(grid, header), header, "", false, true)
	}

	// Key hints are cyan.
	assertGridAttr(t, gridCells(grid, "[e] events"), "[e] events", "cyan", false, false)

	// The feed tail's timestamps are dim, and its tick ids cyan.
	assertGridAttr(t, gridCells(grid, "18:58:02"), "18:58:02", "", true, false)
	assertGridAttr(t, gridCells(grid, "t2#2"), "t2#2", "cyan", false, false)

	// The health verdict carries its state's hue, and so does a passed CI
	// check; "not metered" is secondary text, dim.
	assertGridAttr(t, gridCells(grid, "● healthy"), "● healthy", "green", false, false)
	assertGridAttr(t, gridCellsInRow(grid, "CI #98", "✓"), "✓", "green", false, false)
	assertGridAttr(t, gridCells(grid, "not metered"), "not metered", "", true, false)

	// A failed/held row is red: the pipeline stage the tick is stuck at,
	// refused.
	held := dashboardFixture()
	t2 := &(*held.Waves)[1].Ticks[0]
	t2.State = "rejected"
	t2.Pipeline = []statusmodel.PipelineStage{
		{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
		{Stage: statusmodel.StageWork, State: statusmodel.StageStateFailed},
		{Stage: statusmodel.StageGate, State: statusmodel.StageStatePending},
		{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
	}
	heldGrid := colourGrid(t, held, 120, 0)
	assertGridAttr(t, gridCellsInRow(heldGrid, t2row, "✗work"), "✗work", "red", false, false)

	// Needs-you is the most prominent thing on screen when non-empty:
	// red and bold, the whole line.
	held.Attention = []statusmodel.Attention{{
		Kind:           statusmodel.WaitHeldForPerson,
		What:           "attempt 2 of t2 struck out: the refusal the run recorded",
		NeedsPerson:    true,
		UnblockCommand: ptr("ticfac settle rmod t2 2 --release"),
	}}
	heldGrid = colourGrid(t, held, 120, 0)
	assertGridAttr(t, gridCells(heldGrid, "needs you: attempt 2 of t2 struck out"),
		"needs you: attempt 2 of t2 struck out", "red", false, true)

	// A collapsed closed group is dim: history, quietly.
	full := renderWatchFrame(m, plainStyles(), 120, 0, "")
	collapsed := screenGrid(renderWatchFrame(m, ansiWatchStyles(), 120, len(full)-1, ""))
	assertGridAttr(t, gridCells(collapsed, "✓ 2 closed"), "✓ 2 closed", "", true, false)
}

// TestWatchColourHonoursNoColor: NO_COLOR and TERM=dumb render plain text
// — no SGR colour codes at all — with an identical layout, because colour
// never changes widths and a terminal that cannot show a hue still shows
// every word.
func TestWatchColourHonoursNoColor(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{"NO_COLOR", map[string]string{"NO_COLOR": "1", "TERM": "xterm-256color"}},
		{"NO_COLOR empty is not off", map[string]string{"NO_COLOR": "", "TERM": "xterm-256color"}},
		{"TERM=dumb", map[string]string{"NO_COLOR": "", "TERM": "dumb"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			st := watchStylesForTerminal()
			m := dashboardFixture()
			if tc.name == "NO_COLOR empty is not off" {
				// An empty NO_COLOR is a NO_COLOR nobody set: colour stays on.
				if st.green("x") == "x" && st.dim("x") == "x" {
					// colour on — the identity set would answer plain; assert
					// the ANSI set did not come back plain.
					t.Fatalf("an empty NO_COLOR turned the colour off:\n%#v", st)
				}
				return
			}
			frame := renderWatchFrame(m, st, 120, 40, "")
			plain := renderWatchFrame(m, plainStyles(), 120, 40, "")
			for i, line := range frame {
				if strings.Contains(line, "\x1b[") {
					t.Errorf("%s: line %d still carries an SGR sequence: %q", tc.name, i, line)
				}
				if line != plain[i] {
					t.Errorf("%s: line %d changed layout without colour:\n got: %q\nwant: %q",
						tc.name, i, line, plain[i])
				}
			}
			// The drill-in and feed views hold the same rule.
			tick := renderTickView(m, "t2", st, 120, 40)
			tickPlain := renderTickView(m, "t2", plainStyles(), 120, 40)
			for i, line := range tick {
				if strings.Contains(line, "\x1b[") || line != tickPlain[i] {
					t.Errorf("%s: the tick view's line %d is coloured or moved: %q", tc.name, i, line)
				}
			}
			events := []runfeed.Event{runfeed.NewEvent(atTime(t, "2026-09-28T18:49:00Z"), "epic-rmod",
				"t2", ptr(2), reconcile.StageRejected, "the gate refused attempt 2 of t2")}
			feed := renderFeedView(events, &runfeed.Tries{}, nil, 0, 120, 40, st)
			feedPlain := renderFeedView(events, &runfeed.Tries{}, nil, 0, 120, 40, plainStyles())
			for i, line := range feed {
				if strings.Contains(line, "\x1b[") || line != feedPlain[i] {
					t.Errorf("%s: the feed view's line %d is coloured or moved: %q", tc.name, i, line)
				}
			}
		})
	}
}

// TestWatchColourNeverChangesWidths: the styled frame and the plain frame
// are the same layout — strip the SGR codes off one and it equals the other
// byte for byte, at the wide, the narrow and the wordless widths alike.
func TestWatchColourNeverChangesWidths(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	for _, width := range []int{120, 100, 99, 80, 64, 60, 47, 30} {
		coloured := renderWatchFrame(m, ansiWatchStyles(), width, 0, "")
		plain := renderWatchFrame(m, plainStyles(), width, 0, "")
		if len(coloured) != len(plain) {
			t.Fatalf("width %d: the styled frame holds %d lines, the plain one %d",
				width, len(coloured), len(plain))
		}
		for i := range coloured {
			if stripSGR(coloured[i]) != plain[i] {
				t.Errorf("width %d: the styled line %d changed the layout:\n got: %q\nwant: %q",
					width, i, stripSGR(coloured[i]), plain[i])
			}
		}
	}
	// The drill-in and feed views hold the same property at their widths.
	for _, width := range []int{120, 80, 47} {
		coloured := renderTickView(m, "t2", ansiWatchStyles(), width, 0)
		plain := renderTickView(m, "t2", plainStyles(), width, 0)
		for i := range coloured {
			if stripSGR(coloured[i]) != plain[i] {
				t.Errorf("width %d: the styled tick view's line %d changed the layout:\n got: %q\nwant: %q",
					width, i, stripSGR(coloured[i]), plain[i])
			}
		}
	}
}

// stripSGR removes the SGR sequences one line carries, leaving the words.
func stripSGR(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] == '\x1b' && i+1 < len(line) && line[i+1] == '[' {
			end := i + 2
			for end < len(line) && line[end] != 'm' {
				end++
			}
			i = end + 1
			continue
		}
		b.WriteByte(line[i])
		i++
	}
	return b.String()
}

// TestWatchDrillAndFeedUseThePalette: the same palette the dashboard
// renders with applies to the drill-in and feed views — the tick view's id
// cyan, its try outcomes carrying their own state's hue, the footer's key
// hint cyan; the feed view's timestamps dim, its tick ids cyan, and a
// refusal's stage red.
func TestWatchDrillAndFeedUseThePalette(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Gates = []statusmodel.Gate{{
		TickID: ptr("t2"), Check: "go", Result: "pass", Head: ptr("9f2ab6e0e8f96fc3fdc87c2f681519bb0d191a7"),
	}}
	grid := screenGrid(renderTickView(m, "t2", ansiWatchStyles(), 120, 0))
	assertGridAttr(t, gridCellsInRow(grid, "the second tick's gloss", "t2"), "t2", "cyan", false, false)
	assertGridAttr(t, gridCellsInRow(grid, "try 1", "rejected"), "rejected", "red", false, false)
	assertGridAttr(t, gridCellsInRow(grid, "try 2", "in-flight"), "in-flight", "amber", false, false)
	assertGridAttr(t, gridCells(grid, "[esc] back"), "[esc] back", "cyan", false, false)
	// The gate evidence keeps its own answer's hue.
	assertGridAttr(t, gridCellsInRow(grid, "9f2ab6e0", "pass"), "pass", "green", false, false)

	at := atTime(t, "2026-09-28T18:49:00Z")
	events := []runfeed.Event{
		runfeed.NewEvent(at, "epic-rmod", "t2", ptr(2), reconcile.StageRejected,
			"the integrated gate refused attempt 2 of t2 (go)"),
		runfeed.NewEvent(at, "epic-rmod", "t2", ptr(3), reconcile.StageDispatched,
			"t2 try 3 dispatched"),
	}
	var tries runfeed.Tries
	for _, e := range events {
		tries.Observe(e)
	}
	feed := screenGrid(renderFeedView(events, &tries, nil, 0, 200, 0, ansiWatchStyles()))
	assertGridAttr(t, gridCells(feed, "18:49:00"), "18:49:00", "", true, false)
	assertGridAttr(t, gridCells(feed, "t2#1"), "t2#1", "cyan", false, false)
	assertGridAttr(t, gridCells(feed, "rejected"), "rejected", "red", false, false)
	assertGridAttr(t, gridCells(feed, "dispatched"), "dispatched", "", false, false)
}
