package cli

// The live view's interaction (epic hn6, wave 4 — tick c2u): the dashboard
// answers the glance, and the keys drill in. The reducer is a pure function
// of one key, the current state and the model — no terminal, no pty, no
// herdr — so the key handling is testable headless with byte slices, and
// the wiring (raw mode, the reader goroutine, the redraw) stays in watch.go
// where every exit path restores the terminal.

import (
	"bytes"
	"context"
	"os"
	"slices"

	"golang.org/x/term"

	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// The three views the live loop can be showing. The dashboard is the frame
// the watch opens on; the feed and the tick are its drill-ins, each one a
// render-to-string function in watch_drill.go.
const (
	watchViewDashboard = "dashboard"
	watchViewFeed      = "feed"
	watchViewTick      = "tick"
)

// The key names parseKeys maps the terminal's bytes to, and the reducer
// reads. The arrows alias j and k — the same movement, whatever a person's
// fingers reach for.
const (
	watchKeyQuit   = "q"
	watchKeyFeed   = "e"
	watchKeyDown   = "j"
	watchKeyUp     = "k"
	watchKeyDown2  = "down"
	watchKeyUp2    = "up"
	watchKeyEnter  = "enter"
	watchKeyEscape = "esc"
	watchKeyCtrlC  = "ctrl-c"
)

// watchUI is the live view's interaction state: which view is up, which
// tick the cursor sits on (the dashboard marks it with "▸"), and how far
// the feed view is scrolled up from its newest line. The zero value is the
// dashboard with no cursor — exactly what a watch opens as.
type watchUI struct {
	view     string
	selected string
	scroll   int
}

// key is the reducer: one named key against the model, the next state out.
// It never blocks, reads nothing and spawns nothing — the same property the
// frame renderer has, and the reason the key handling can be pinned by a
// table test. A selection that is no longer in the plan (a tick absorbed or
// replanned away mid-run) is no selection at all, and a tick view standing
// on one comes down.
func (u watchUI) key(k string, m statusmodel.Model) watchUI {
	order := watchPlanOrder(m)
	if u.selected != "" && !slices.Contains(order, u.selected) {
		u.selected = ""
		if u.view == watchViewTick {
			u.view = watchViewDashboard
		}
	}
	switch {
	case u.view == watchViewFeed:
		switch k {
		case watchKeyDown, watchKeyDown2:
			// Scrolling down reveals newer lines; the bottom of the feed is
			// the end, and the wiring clamps the top against the feed's
			// length, which the reducer does not carry.
			if u.scroll > 0 {
				u.scroll--
			}
		case watchKeyUp, watchKeyUp2:
			u.scroll++
		case watchKeyEscape, watchKeyQuit:
			u.view = watchViewDashboard
		}
	case u.view == watchViewTick:
		switch k {
		case watchKeyEscape, watchKeyQuit:
			u.view = watchViewDashboard
		}
	default:
		// The dashboard — including a zero watchUI, whose view is not set
		// yet: the first key normalizes it.
		u.view = watchViewDashboard
		switch k {
		case watchKeyDown, watchKeyDown2:
			u.selected = watchNeighbourTick(order, u.selected, +1)
		case watchKeyUp, watchKeyUp2:
			u.selected = watchNeighbourTick(order, u.selected, -1)
		case watchKeyEnter:
			if u.selected == "" {
				// Nothing under the cursor: open the tick that is working
				// — the first in-flight one in plan order — else the first
				// tick of the plan.
				u.selected = watchFirstInFlight(m, order)
				if u.selected == "" && len(order) > 0 {
					u.selected = order[0]
				}
			}
			if u.selected != "" {
				u.view = watchViewTick
			}
		case watchKeyFeed:
			u.view = watchViewFeed
			u.scroll = 0
		}
	}
	return u
}

// watchPlanOrder is the epic's ticks in plan order — the waves in order,
// the tracker's order inside each wave — the one sequence the cursor walks.
// Nil when the epic's shape could not be read: nothing to walk.
func watchPlanOrder(m statusmodel.Model) []string {
	if m.Waves == nil {
		return nil
	}
	out := make([]string, 0, 16)
	for _, wave := range *m.Waves {
		for _, tick := range wave.Ticks {
			out = append(out, tick.TickID)
		}
	}
	return out
}

// watchTickOf is the model's own record for one tick, or nil when the plan
// does not name it.
func watchTickOf(m statusmodel.Model, tickID string) *statusmodel.Tick {
	if m.Waves == nil {
		return nil
	}
	for wi := range *m.Waves {
		wave := &(*m.Waves)[wi]
		for ti := range wave.Ticks {
			if wave.Ticks[ti].TickID == tickID {
				return &wave.Ticks[ti]
			}
		}
	}
	return nil
}

// watchNeighbourTick moves the cursor one row in the plan order and clamps
// at both ends — the first and last row are walls, never a wrap and never a
// fall off the table. From no selection, moving down enters at the first
// row; moving up is already at the top end, so it stays no selection.
func watchNeighbourTick(order []string, selected string, step int) string {
	if len(order) == 0 {
		return selected
	}
	if selected == "" {
		if step > 0 {
			return order[0]
		}
		return ""
	}
	at := slices.Index(order, selected)
	if at < 0 {
		return order[0]
	}
	next := at + step
	if next < 0 {
		next = 0
	}
	if next >= len(order) {
		next = len(order) - 1
	}
	return order[next]
}

// watchFirstInFlight is the first tick in plan order whose current attempt
// is standing — the tick a person means by "the one that is working". A
// tick with no try history yet is in flight when the checkpoint says it is
// dispatched; otherwise the last try's own outcome answers.
func watchFirstInFlight(m statusmodel.Model, order []string) string {
	for _, id := range order {
		if tick := watchTickOf(m, id); tick != nil && watchTickInFlight(*tick) {
			return id
		}
	}
	return ""
}

func watchTickInFlight(t statusmodel.Tick) bool {
	if len(t.Tries) > 0 {
		return t.Tries[len(t.Tries)-1].Outcome == statusmodel.TryInFlight
	}
	return t.State == "dispatched"
}

// watchCSIKeys is the escape sequences the reducer answers, on top of the
// single-letter keys: the cursor keys in both the ANSI and the application
// encodings every terminal and pane multiplexer sends.
var watchCSIKeys = map[string]string{
	"\x1b[A": watchKeyUp2,
	"\x1b[B": watchKeyDown2,
	"\x1bOA": watchKeyUp2,
	"\x1bOB": watchKeyDown2,
}

// parseKeys maps the terminal's raw bytes to the key names the reducer
// reads: "\r" is enter, "\x1b" is esc, "\x1b[A"/"\x1bOA" are up and
// "\x1b[B"/"\x1bOB" are down, ctrl-c is "\x03", and the single letters the
// watch answers are e, j, k and q. Everything else — printable text, a
// complete-but-unknown escape sequence — is ignored: a terminal that sends
// sequences this watch does not answer must not have them leak in as
// movement. An escape sequence cut short by the chunk's end reads as the
// escape key itself, though watchReadKeys holds such a tail back so a
// cursor sequence split across two reads stays one key.
func parseKeys(b []byte) []string {
	keys := []string{}
	for i := 0; i < len(b); {
		switch c := b[i]; {
		case c == 0x1b:
			// CSI (\x1b[…) and SS3 (\x1bO…) introductions: find the final
			// byte, 0x40–0x7e, and answer the whole sequence or nothing.
			if i+1 < len(b) && (b[i+1] == '[' || b[i+1] == 'O') {
				j := i + 2
				for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
					j++
				}
				if j < len(b) {
					if name, ok := watchCSIKeys[string(b[i:j+1])]; ok {
						keys = append(keys, name)
					}
					i = j + 1
				} else {
					// The sequence is cut short — the escape key itself.
					keys = append(keys, watchKeyEscape)
					i = len(b)
				}
				continue
			}
			keys = append(keys, watchKeyEscape)
			i++
		case c == '\r':
			keys = append(keys, watchKeyEnter)
			i++
		case c == 0x03:
			keys = append(keys, watchKeyCtrlC)
			i++
		case c == 'e':
			keys = append(keys, watchKeyFeed)
			i++
		case c == 'j':
			keys = append(keys, watchKeyDown)
			i++
		case c == 'k':
			keys = append(keys, watchKeyUp)
			i++
		case c == 'q':
			keys = append(keys, watchKeyQuit)
			i++
		default:
			i++
		}
	}
	return keys
}

// watchAttachKeys is where the live view's keys come from: the terminal's
// keyboard, when stdin is one. Raw mode is what makes j/k/enter/esc reach
// the program instead of the line discipline, and the restore it returns is
// the caller's to defer — watchLive defers it, so every exit path the
// function can take, a return, a context cancellation or a panic, puts the
// terminal back the way it found it. False when the keyboard is not a
// terminal or raw mode could not be entered: the watch then runs keyless,
// exactly as it always did, and it is no worse for it.
var watchAttachKeys = func(ctx context.Context) (<-chan string, func(), bool) {
	in := os.Stdin
	if !term.IsTerminal(int(in.Fd())) {
		return nil, nil, false
	}
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, nil, false
	}
	return watchReadKeys(ctx, in), func() { _ = term.Restore(int(in.Fd()), old) }, true
}

// watchEscapeBound bounds how long the reader will hold a half-written
// escape sequence before giving up on it: a terminal that sent an
// introduction and then went silent has stalled the reader, and the bytes
// after it must not be held hostage behind one.
const watchEscapeBound = 16

// watchReadKeys reads the keyboard on its own goroutine and hands the named
// keys to the loop's channel. The goroutine ends when the context ends or
// the reader errs; until then a key read is one Read of the terminal, and
// the loop redraws on each one.
func watchReadKeys(ctx context.Context, in *os.File) <-chan string {
	keys := make(chan string, 16)
	go func() {
		defer close(keys)
		buf := make([]byte, 256)
		pending := []byte{}
		for {
			n, err := in.Read(buf)
			if n > 0 {
				pending = append(pending, buf[:n]...)
				// Hold back a cut-off escape sequence so a cursor key
				// split across two reads stays one key — and give up on
				// an introduction that never completes.
				cut := len(pending)
				if i := bytes.LastIndexByte(pending, 0x1b); i >= 0 && watchStuckEscape(pending[i:]) {
					cut = i
				}
				for _, k := range parseKeys(pending[:cut]) {
					select {
					case keys <- k:
					case <-ctx.Done():
						return
					}
				}
				pending = append(pending[:0], pending[cut:]...)
			}
			if err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
	return keys
}

// watchStuckEscape says whether a byte slice is an escape introduction
// still waiting for its final byte — the tail the reader holds back. A
// lone "\x1b" is NOT one: the escape key sends exactly that, and a person
// who pressed it must not wait for a second keystroke that may never come.
// A sequence that has grown past the bound is not held either.
func watchStuckEscape(tail []byte) bool {
	if len(tail) < 2 || len(tail) > watchEscapeBound {
		return false
	}
	if tail[1] != '[' && tail[1] != 'O' {
		return false
	}
	for _, c := range tail[2:] {
		if c >= 0x40 && c <= 0x7e {
			return false
		}
	}
	return true
}
