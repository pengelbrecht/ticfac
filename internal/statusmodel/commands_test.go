package statusmodel

import (
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// Tick mwt: a clearing command printed for a person is one they can run, and
// a command whose operand is empty — "ticfac run-epic " with nothing after
// the verb, "ticfac settle  yjq 12 --run-id …" with the epic's space left
// in — is one they cannot. These builders are the ONE spelling of every
// command the model names, so the refusal belongs here: an epic the model
// cannot state names no command at all, never a command with a hole in it.
// The row and the header then answer with their reason alone, which already
// says what is wanted.

func heldLine(tickID string, attempt int) runfeed.Event {
	n := attempt
	return runfeed.NewEvent(time.Now(), "run_x", tickID, &n, reconcile.StageRunHeld,
		"attempt_struck_out: the report names no status")
}

func TestNoClearingCommandCarriesAnEmptyOperand(t *testing.T) {
	if got := ResumeCommand(HostLocal, ""); got != "" {
		t.Errorf("a local resume with no epic is %q, want no command at all", got)
	}
	if got := ResumeCommand(HostCloud, ""); got != "" {
		t.Errorf("a cloud resume with no epic is %q, want no command at all", got)
	}
	if got := TriageCommand(""); got != "" {
		t.Errorf("a triage with no epic is %q, want no command at all", got)
	}
	if got := TriageCommandForRun("", "run_x"); got != "" {
		t.Errorf("a run-addressed triage with no epic is %q, want no command at all", got)
	}
	if got := TriageCommandForRun("mwx", ""); got != "" {
		t.Errorf("a run-addressed triage with no run to address is %q, want no command at all", got)
	}
	if got := TriageCommandForCurrentRun("", ""); got != "" {
		t.Errorf("a current-run triage with no epic is %q, want no command at all", got)
	}
	if got := SettleCommandForCurrentRun("", "yjq", 12, "run_x"); got != "" {
		t.Errorf("a settle with no epic is %q, want no command at all", got)
	}
	if got := SettleCommandForCurrentRun("mwx", "", 12, "run_x"); got != "" {
		t.Errorf("a settle with no tick is %q, want no command at all", got)
	}
	if got := HoldClearingCommand("", HostLocal, "", "run_x", heldLine("yjq", 12)); got != nil {
		t.Errorf("a hold a model with no epic cannot address names the command %q, want none", *got)
	}

	// The same builders with their operands stated answer as they always
	// did — the refusal costs nothing but the hole.
	if got := ResumeCommand(HostLocal, "mwx"); got != "ticfac run-epic mwx" {
		t.Errorf("the local resume of an epic it states is %q", got)
	}
	if got := ResumeCommand(HostCloud, "mwx"); got != "ticfac run mwx --cloud" {
		t.Errorf("the cloud resume of an epic it states is %q", got)
	}
	if got := HoldClearingCommand("mwx", HostLocal, "run_x", "run_x", heldLine("yjq", 12)); got == nil ||
		*got != "ticfac settle mwx yjq 12 --run-id run_x --release \"<who>\"" {
		t.Errorf("the settle of a hold with its operands stated is %v", got)
	}
}
