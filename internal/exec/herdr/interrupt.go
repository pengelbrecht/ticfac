package herdr

import (
	"strings"

	"github.com/pengelbrecht/ticfac/internal/herd/client"
)

// The interrupt is the HARNESS's, not herdr's.
//
// herdr offers no agent-level interrupt or stop (herdr 0.9.1: `herdr agent`
// is list/get/read/send-keys/prompt/rename/focus/wait/attach/start/explain),
// so a stop is key presses delivered through agent.send_keys — and which key
// stops a running turn is a fact about the agent in the pane, not about
// herdr. The executor used to send ctrl+c to every kind. For pi that is not
// an interrupt at all: pi binds ctrl+c to app.clear ("Clear editor (first) /
// exit (second)") and Escape to app.interrupt ("Cancel / abort"). Re-sent
// once per five-second poll, the ctrl+c never landed as a double press, so a
// working pi agent never stopped, and the wall clock waited out the whole
// grace and closed the pane — epic-6in's 46x attempt 2 (2026-09-28) took 23
// ctrl+c deliveries across 1m54s before the close. dz1 attempt 5 exited within
// five seconds only because TWO polls happened to land in the same second:
// a double ctrl+c, pi's QUIT, by accident.
//
// Each entry names where it was verified, so the next person to add a kind
// checks the same way rather than guessing:
//
//   - pi (@earendil-works/pi-coding-agent 0.85.1): docs/keybindings.md —
//     `app.interrupt` = escape ("Cancel / abort"); `app.clear` = ctrl+c
//     ("Clear editor (first) / exit (second)"). README: "Escape | Cancel/abort".
//   - claude (Claude Code 2.1.283): the default keybinding table in the
//     binary — Chat context `escape: "chat:cancel"`, the cancel of a running
//     turn; Global `"ctrl+c": "app:interrupt"` also interrupts, but it is the
//     double-press exit chord, and Escape is the turn's own cancel.
//   - codex (codex-cli 0.152.1): openai/codex codex-rs/tui/src/keymap.rs —
//     `interrupt_turn: default_bindings![plain(KeyCode::Esc)]`; ctrl+c on a
//     running task opens the "Task is still running" chooser (Cancel task /
//     Run in background / exit), a menu, not a stop.
//
// A kind not listed here keeps herdr's generic chord, ctrl+c, the surface
// `herdr agent send-keys` offers a human: nothing verified says otherwise,
// and changing an unverified harness's stop would be the guess this file
// exists to replace. The key names are herdr's: "esc" is its canonical
// Escape (`herdr agent send-keys --help`).

// defaultInterrupt is herdr's generic interrupt chord, for a kind whose own
// interrupt nobody has verified.
var defaultInterrupt = []string{"ctrl+c"}

// harnessInterrupts is each verified harness's own turn interrupt, as the
// agent.send_keys key sequence that delivers it.
var harnessInterrupts = map[string][]string{
	"pi":     {"esc"},
	"claude": {"esc"},
	"codex":  {"esc"},
}

// interruptKeys is the key sequence that interrupts a running turn of the
// given agent kind. An empty kind is the executor's own default, claude.
func interruptKeys(kind string) []string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "claude"
	}
	keys, ok := harnessInterrupts[kind]
	if !ok {
		keys = defaultInterrupt
	}
	return append([]string(nil), keys...)
}

// honouredInterrupt reports whether herdr's answer about an agent that was
// already interrupted says the interrupt was HONOURED: the agent is no longer
// working — back at its prompt (idle), finished (done), or waiting on a
// person (blocked). None of those is spending, and an interactive agent that
// has returned to its prompt after an interrupt has done everything it is
// ever going to do on its own: it does not exit, and it does not write a
// report. Waiting out the rest of the grace for it buys nothing, so the
// escalation proceeds at once. `working` and `unknown` are not an answer
// that the interrupt landed — the grace still runs for them.
func honouredInterrupt(status client.AgentStatus) bool {
	switch status {
	case client.StatusIdle, client.StatusDone, client.StatusBlocked:
		return true
	default:
		return false
	}
}
