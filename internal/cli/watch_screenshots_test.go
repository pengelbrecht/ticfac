package cli

// The committed screenshots (docs/design/watch-redesign-2026-10): the five
// scenarios the design names, re-captured in colour (tick cl7).
//
// Two tests keep them honest:
//
//   - TestDashboardScreenshotSources writes the five scenarios' frames with
//     the terminal's own style set — the bytes the capture turns into PNGs —
//     behind the -screenshot-dir flag, so the capture script
//     (docs/design/watch-redesign-2026-10/capture.sh) regenerates the
//     screenshots from the same renderer and fixtures the golden frames are
//     pinned against, and the styled bytes it ships are provably the pinned
//     layout with colour on (two SGR spellings the image pipeline misreads
//     are re-spelled, screenshotSource below). The first capture lost the
//     accents by feeding the pipeline plain text; a source that cannot
//     drift is how that stays fixed.
//   - TestCommittedScreenshotsShowTheColourAccents reads the committed PNGs
//     back and asserts, per scenario, the hue families the frame's own
//     accents must show: green done, amber in progress, red failed/held,
//     cyan identities and hints — the acceptance's "the committed
//     screenshots show them", checked by the gate instead of by an eyeball.
//     A capture with window controls would carry its own red, yellow and
//     green, so the captures are taken without window chrome and the
//     assertion reads the whole image.

import (
	"flag"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// screenshotDir is where TestDashboardScreenshotSources writes the styled
// frame sources; empty (the default, and what the gate runs) writes nothing.
var screenshotDir = flag.String("screenshot-dir", "", "write the five scenarios' styled frame sources (ANSI text) here for the screenshot capture")

// screenshotScenarios is the five scenarios the design names, in the order
// the capture script walks them, each with the hue families its frame must
// show: the accents the palette draws for the states that scenario carries.
func screenshotScenarios() []struct {
	name  string
	model func() statusmodel.Model
	want  []string
} {
	return []struct {
		name  string
		model func() statusmodel.Model
		want  []string
	}{
		{"fresh", scenarioFresh, []string{"green", "cyan"}},
		{"busy", scenarioBusy, []string{"amber", "green", "cyan"}},
		{"held", scenarioHeld, []string{"red", "amber", "green", "cyan"}},
		{"landed", scenarioLanded, []string{"green", "cyan"}},
		{"failed", scenarioFailed, []string{"red", "green", "cyan"}},
	}
}

// The capture source is the renderer's own output with two SGR sequences
// re-spelled — same words, same widths, same meaning to a terminal that
// accumulates attributes, spelled the one way every consumer reads:
//
//   - the faint accent ("2") becomes the palette's dark grey ("90"): the
//     dim accent IS a dark grey on a dark terminal, and the image pipeline
//     draws no faintness of its own;
//   - a bare bold ("1") that arrives after a colour — the needs-you
//     announcement's red+bold, adjacent or with the box's border between —
//     is re-stated as one combined sequence ("1;31"): a terminal
//     accumulates the two, but a tokenizer that styles per span drops the
//     fill it was carrying when bold arrives after it.
var (
	screenshotFaint     = regexp.MustCompile("\x1b\\[2m")
	screenshotBoldAfter = regexp.MustCompile("(\x1b\\[)((?:3[0-9]|9[0-7]))m([^\x1b]*)\x1b\\[1m")
)

// screenshotSource renders one styled frame into the bytes the capture
// turns into a PNG, padded to the terminal's 40 rows.
func screenshotSource(frame []string) string {
	lines := append([]string{}, frame...)
	for len(lines) < 40 {
		lines = append(lines, "")
	}
	text := strings.Join(lines, "\n") + "\n"
	text = screenshotBoldAfter.ReplaceAllString(text, "${1}${2}m${3}\x1b[1;${2}m")
	text = screenshotFaint.ReplaceAllString(text, "\x1b[90m")
	return text
}

// TestDashboardScreenshotSources: the styled bytes the screenshots are
// captured from. Skipped unless -screenshot-dir names a directory — the gate
// pays for assertions, not for artifacts. Each file is the scenario's frame
// rendered with ansiWatchStyles at 120x40, asserted to be the pinned plain
// frame with the colour on, so the capture can never ship a layout the
// goldens do not pin. The file carries the terminal's full 40 rows — the
// frame's own lines, then the blank rows a 120x40 terminal shows under it —
// so the capture keeps the proportions the file name states.
func TestDashboardScreenshotSources(t *testing.T) {
	if *screenshotDir == "" {
		t.Skip("no -screenshot-dir: writing screenshot sources is a capture step, not a gate step")
	}
	if err := os.MkdirAll(*screenshotDir, 0o755); err != nil {
		t.Fatalf("create the screenshot source directory: %v", err)
	}
	for _, tc := range screenshotScenarios() {
		t.Run(tc.name, func(t *testing.T) {
			styled := renderWatchFrame(tc.model(), ansiWatchStyles(), 120, 40, "")
			plain := renderWatchFrame(tc.model(), plainStyles(), 120, 40, "")
			if len(styled) != len(plain) {
				t.Fatalf("the styled frame holds %d lines, the pinned plain frame %d", len(styled), len(plain))
			}
			for i := range styled {
				if stripSGR(styled[i]) != plain[i] {
					t.Fatalf("line %d is not the pinned layout with colour on:\n got: %q\nwant: %q", i, stripSGR(styled[i]), plain[i])
				}
			}
			if !strings.Contains(strings.Join(styled, "\n"), "\x1b[") {
				t.Fatalf("scenario %s rendered with no SGR sequence at all — the capture would be monochrome again", tc.name)
			}
			source := screenshotSource(styled)
			lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
			if len(lines) != 40 {
				t.Fatalf("the source carries %d rows, want the terminal's 40", len(lines))
			}
			for i := range plain {
				if stripSGR(lines[i]) != plain[i] {
					t.Fatalf("source line %d is not the pinned layout with colour on:\n got: %q\nwant: %q", i, stripSGR(lines[i]), plain[i])
				}
			}
			for i := len(plain); i < len(lines); i++ {
				if lines[i] != "" {
					t.Fatalf("padding row %d is not blank: %q", i, lines[i])
				}
			}
			if screenshotBoldAfter.MatchString(source) || strings.Contains(source, "\x1b[2m") {
				t.Fatalf("the capture source still carries a sequence the image pipeline misreads:\n%s", source)
			}
			path := filepath.Join(*screenshotDir, "watch-"+tc.name+".ansi")
			if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		})
	}
}

// TestScreenshotSourceRespellsForTheCapture: the two re-spellings are exact
// and nothing else is touched — every other sequence, and every word and
// space, rides through unchanged.
func TestScreenshotSourceRespellsForTheCapture(t *testing.T) {
	t.Parallel()
	frame := []string{
		"\x1b[31m\x1b[1mNeeds you: hold\x1b[0m\x1b[0m",     // adjacent: red, then bold
		"\x1b[31m│ \x1b[1mNeeds you: hold\x1b[0m │\x1b[0m", // the box's row: red border, then bold
		"\x1b[2mNeeds you: nothing\x1b[0m",                 // the quiet answer: faint
		"\x1b[32m● healthy\x1b[0m · \x1b[1mNOW\x1b[0m",     // green, and a bold header
		"plain text with no styling",
	}
	want := []string{
		"\x1b[31m\x1b[1;31mNeeds you: hold\x1b[0m\x1b[0m",
		"\x1b[31m│ \x1b[1;31mNeeds you: hold\x1b[0m │\x1b[0m",
		"\x1b[90mNeeds you: nothing\x1b[0m",
		"\x1b[32m● healthy\x1b[0m · \x1b[1mNOW\x1b[0m",
		"plain text with no styling",
	}
	got := strings.Split(strings.TrimSuffix(screenshotSource(frame), "\n"), "\n")
	for len(want) < 40 { // the padding rows are empty and carry no styling
		want = append(want, "")
	}
	if len(got) != len(want) {
		t.Fatalf("the source holds %d lines, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got: %q\nwant: %q", i, got[i], want[i])
		}
	}
}

// TestCommittedScreenshotsShowTheColourAccents: the five PNGs under
// docs/design/watch-redesign-2026-10 each show the hue families the
// dashboard's own accents draw for their scenario — the committed evidence
// of the colour accents, read back the way a person sees it. This is the
// guard the first capture failed: those PNGs were monochrome (the capture
// ran without colour), and no assertion noticed.
func TestCommittedScreenshotsShowTheColourAccents(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatalf("locate the repository root: %v", err)
	}
	dir := filepath.Join(root, "docs", "design", "watch-redesign-2026-10")
	for _, tc := range screenshotScenarios() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, "watch-"+tc.name+"-120x40.png")
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open the committed screenshot: %v", err)
			}
			defer f.Close()
			img, err := png.Decode(f)
			if err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			bounds := img.Bounds()
			families := map[string]int{}
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					if family := hueFamilyOf(img.At(x, y)); family != "" {
						families[family]++
					}
				}
			}
			for _, want := range tc.want {
				if families[want] == 0 {
					t.Errorf("%s carries no %s accent anywhere — the screenshot does not show the dashboard's colour (families found: %v)",
						filepath.Base(path), want, families)
				}
			}
		})
	}
}

// hueFamilyOf names the hue family one pixel belongs to, "" for none: the
// palette's four accents read by hue, not by one rendering's exact RGB —
// red, amber, green, cyan; greys, the background and dim text (which carries
// no hue of its own) all answer "". A pixel counts only when it is clearly
// saturated and clearly bright: the anti-aliased edge of a glyph blends into
// the dark background and must not tip a family in or out.
func hueFamilyOf(c color.Color) string {
	r8, g8, b8, _ := c.RGBA()
	r, g, b := int(r8>>8), int(g8>>8), int(b8>>8)
	mx, mn := max(r, g, b), min(r, g, b)
	if mx-mn < 40 || mx < 70 {
		return ""
	}
	var h float64
	switch mx {
	case r:
		h = 60 * float64(g-b) / float64(mx-mn)
	case g:
		h = 60 * (2 + float64(b-r)/float64(mx-mn))
	default:
		h = 60 * (4 + float64(r-g)/float64(mx-mn))
	}
	if h < 0 {
		h += 360
	}
	switch {
	case h < 20 || h >= 330:
		return "red"
	case h < 75:
		return "amber"
	case h < 170:
		return "green"
	case h < 265:
		return "cyan"
	}
	return "" // blue and magenta: no accent in the palette carries them
}

// TestHueFamilyOfRefusesMonochrome: the committed-screenshot assertion is
// itself held to its own oracle — it must refuse everything a colourless
// capture produces (the dark background, the plain text, the washed greys of
// the old captures) and accept the palette's accents. The washed reds and
// yellows are the old captures' window controls: below the saturation floor
// they refuse, which is also why the re-capture takes no window chrome — a
// chrome yellow saturated enough to survive the floor would read as amber
// and make the assertion vacuous.
func TestHueFamilyOfRefusesMonochrome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		col    color.Color
		family string
	}{
		{color.RGBA{R: 30, G: 30, B: 46, A: 255}, ""},    // the dark background
		{color.RGBA{R: 196, G: 196, B: 196, A: 255}, ""}, // plain text grey
		{color.RGBA{R: 159, G: 128, B: 130, A: 255}, ""}, // the old captures' washed window red
		{color.RGBA{R: 146, G: 133, B: 137, A: 255}, ""}, // ...its other wash
		{color.RGBA{R: 202, G: 197, B: 176, A: 255}, ""}, // ...and washed yellow
		{color.RGBA{R: 255, G: 90, B: 84, A: 255}, "red"},
		{color.RGBA{R: 230, G: 191, B: 41, A: 255}, "amber"},
		{color.RGBA{R: 82, G: 193, B: 43, A: 255}, "green"},
		{color.RGBA{R: 59, G: 142, B: 165, A: 255}, "cyan"},
	} {
		if got := hueFamilyOf(tc.col); got != tc.family {
			t.Errorf("hueFamilyOf(%v) = %q, want %q", tc.col, got, tc.family)
		}
	}
}
