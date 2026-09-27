package reconcile

import (
	"strings"
	"testing"
)

// The triage pointer (tick 8yn) is what every surface that TELLS somebody how
// to triage embeds: the note the discovering tick's record carries, the
// close-out's hold, the absorption-depth stop. It must teach the everyday path
// — `ticfac triage <epic>`, each finding addressed by a short key prefix —
// and never the old `ticfac finding <epic> <64-hex> --promote-as ... --by
// ...` shape: a person following the pointer must not be sent to type the
// very friction the triage surface (tick sg5) exists to remove. The old
// command is named bare, and only for the one thing it alone still does:
// promoting a tick that ALREADY exists, into the repository a finding is
// routed to.
//
// short: reads one pure function's output — no repository is built and no
// process is spawned — and the per-tick gate is exactly where a pointer
// that regresses to the old 64-hex triage command must be caught.
func TestTheTriagePointerTeachesTheEverydayPathNotThe64HexCommand(t *testing.T) {
	t.Parallel()

	p := triagePointer("qeu")
	for _, want := range []string{
		"ticfac triage qeu",
		"short key prefix",
		"absorb",
		"file",
		"fixed <commit>",
		"discard",
		"ticfac finding", // bare, for the routed promotion alone
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the triage pointer does not carry %q — a person following it must reach the "+
				"everyday path: %q", want, p)
		}
	}
	for _, old := range []string{
		"ticfac finding qeu", // the old shape: the command with the key in tow
		"--promote-as",
		"--discard",
		"--fixed-as",
		"--by",
	} {
		if strings.Contains(p, old) {
			t.Errorf("the triage pointer still teaches the old 64-hex command %q: %q", old, p)
		}
	}
}
