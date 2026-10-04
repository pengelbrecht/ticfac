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

	p := triagePointer("qeu", "epic-qeu")
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
	// Since PR #85 the run disposes of a finding routed to another repository
	// itself — filed into the target's tracker when .tick/runners.toml allows
	// it, else a local backlog tick naming the target — and it never holds the
	// run. The pointer must say that, not send a person to settle it by hand.
	for _, want := range []string{
		"routed to another repository",
		".tick/runners.toml",
		"backlog tick",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the triage pointer does not say how the run disposes of a routed finding (%q): %q", want, p)
		}
	}
	if strings.Contains(p, "keeps its routing") {
		t.Errorf("the triage pointer still tells a person to settle a routed finding by hand: %q", p)
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

// The pointer addresses the run whose drafts it points at (tick q8m): the
// drafts live in the run's own store, and the bare command's default
// (epic-<epic-id>, the local id run-epic derives) is only right when the
// run wrote under it. A cloud run writes under the factory's run_<hex>
// (tick ulw), so its pointer must spell --run-id — a person following the
// pointer must reach the drafts without discovering the flag on their own.
//
// short: reads one pure function's output — no repository is built and no
// process is spawned — and the per-tick gate is exactly where a pointer
// that regresses to the local default (a store a cloud run never wrote)
// must be caught.
func TestTheTriagePointerNamesTheRunWhoseStoreHoldsTheDrafts(t *testing.T) {
	t.Parallel()

	p := triagePointer("qeu", "run_a1b2c3d4e5")
	if !strings.Contains(p, "ticfac triage qeu --run-id run_a1b2c3d4e5") {
		t.Errorf("the triage pointer does not address the run's own store: %q", p)
	}
	// The everyday path itself is unchanged: the short prefixes, the four
	// verdicts, and never the old 64-hex shape.
	for _, want := range []string{"short key prefix", "absorb", "file", "fixed <commit>", "discard"} {
		if !strings.Contains(p, want) {
			t.Errorf("the run-addressed triage pointer does not carry %q: %q", want, p)
		}
	}
	if strings.Contains(p, "ticfac finding qeu") || strings.Contains(p, "--promote-as") {
		t.Errorf("the run-addressed triage pointer teaches the old 64-hex command: %q", p)
	}
}
