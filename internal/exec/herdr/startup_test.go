package herdr

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// trustDialogScreen is what Claude Code 2.1.296 shows at its folder-trust
// dialog, as read off a pty in a scratch HOME.
const trustDialogScreen = `Accessing workspace:

/Users/someone/.herdr/worktrees/repo/ticfac-run-epic-e-tick-t-attempt-1

Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known
open source project, or work from your team). If not, take a moment to review what's in this folder first.

Claude Code'll be able to read, edit, and execute files here.

❯ 1. No, exit
  2. Yes, I trust this folder

Enter to confirm · Esc to cancel`

// stuckPaneHerdr makes the harness's herdr behave like a pane whose claude sits at
// a dialog: agent.start answers a launch that is neither ready nor pending,
// agent.get answers the agent blocked and never ready, and pane.read shows
// screen — until the dialog is dismissed (an "esc" through agent.send_keys),
// when clears is true; then every later call answers as the harness always
// does. It returns a func reporting whether the dismissal arrived.
func stuckPaneHerdr(h *harness, screen string, clears bool) func() bool {
	var mu sync.Mutex
	dismissed := false
	isStuck := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return !(clears && dismissed)
	}
	stuckAgent := func(name, pane string) map[string]any {
		return map[string]any{
			"pane_id": pane, "agent_status": "blocked", "name": name,
			"interactive_ready": false, "launch_pending": false, "agent_session": nil,
		}
	}
	h.server.Route(herdtest.MethodAgentStart, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		if !isStuck() {
			return h.server.Builtin(herdtest.MethodAgentStart)(t, req, w)
		}
		var p struct {
			Name   string `json:"name"`
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(req.Params, &p)
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "agent_started", "agent": stuckAgent(p.Name, p.PaneID),
		})
	})
	h.server.Route(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		if !isStuck() {
			return h.server.Builtin(herdtest.MethodAgentGet)(t, req, w)
		}
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(req.Params, &p)
		h.mu.Lock()
		closed := h.paneClosed
		h.mu.Unlock()
		if closed {
			return herdtest.RespondErr(w, req.ID, herdtest.CodePaneNotFound, "pane closed")
		}
		return herdtest.RespondJSON(w, req.ID, map[string]any{"type": "agent_info", "agent": stuckAgent(p.Target, "w1:p1")})
	})
	h.server.Route(herdtest.MethodPaneRead, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		text := screen
		if !isStuck() {
			text = "❯ "
		}
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "pane_read",
			"read": map[string]any{"pane_id": "w1:p1", "source": "visible", "format": "text", "text": text},
		})
	})
	h.server.Route(herdtest.MethodAgentSendKeys, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Keys []string `json:"keys"`
		}
		_ = json.Unmarshal(req.Params, &p)
		for _, k := range p.Keys {
			if k == "esc" {
				mu.Lock()
				dismissed = true
				mu.Unlock()
			}
		}
		return h.server.Builtin(herdtest.MethodAgentSendKeys)(t, req, w)
	})
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		return dismissed
	}
}

func observationsOf(t *testing.T, h *harness, tick string) string {
	t.Helper()
	spec := h.spec("run-harness/tick-"+tick+"/attempt-1", tick)
	st := h.ex.storeAt(h.ex.stateDirFor(spec.JobID, h.ex.opts.Attempt))
	observations, _ := st.observationsFrom("")
	var b strings.Builder
	for _, obs := range observations {
		b.WriteString(obs.Detail)
		b.WriteString("\n")
	}
	return b.String()
}

func recordOf(t *testing.T, h *harness, tick string) *attemptRecord {
	t.Helper()
	spec := h.spec("run-harness/tick-"+tick+"/attempt-1", tick)
	record, err := h.ex.storeAt(h.ex.stateDirFor(spec.JobID, h.ex.opts.Attempt)).readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// Before claude is launched in the worktree the executor created, that exact
// worktree is trusted in Claude Code's config — already at the moment
// agent.start is sent, which is when it matters (tick o35).
func TestStartTrustsTheWorktreeBeforeLaunchingClaude(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	config := h.ex.opts.ClaudeConfigPath
	var trustedAtLaunch bool
	h.server.Route(herdtest.MethodAgentStart, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(req.Params, &p)
		h.mu.Lock()
		for id, ws := range h.workspaces {
			if strings.HasPrefix(p.PaneID, id+":") {
				raw, _ := os.ReadFile(config)
				var c map[string]any
				_ = json.Unmarshal(raw, &c)
				trustedAtLaunch = trusted(c, ws.path)
			}
		}
		h.mu.Unlock()
		return h.server.Builtin(herdtest.MethodAgentStart)(t, req, w)
	})
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}
	if !trustedAtLaunch {
		t.Fatal("agent.start was sent before the worktree was trusted: claude would stop at its folder-trust dialog")
	}
	if home, _ := os.UserHomeDir(); config == filepath.Join(home, ".claude.json") {
		t.Fatalf("the harness registered trust in the operator's real config %s", config)
	}
}

// A kind other than claude has no folder-trust dialog to pre-empt: nothing is
// written to Claude Code's config for it.
func TestStartLeavesClaudesConfigAloneForOtherKinds(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{kind: "codex"})
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}
	if pathExists(h.ex.opts.ClaudeConfigPath) {
		t.Error("a codex launch wrote Claude Code's config")
	}
}

// A pane whose claude sits at its folder-trust dialog is named precisely, and
// re-dispatched once in the same pane: the dialog is dismissed and the
// launch repeated, which here starts — so the Start succeeds with no person.
func TestAPaneStuckAtTheTrustDialogIsRedispatchedOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.ex.opts.StartupTimeout = time.Second
	dismissed := stuckPaneHerdr(h, trustDialogScreen, true)

	if _, err := h.start("t1"); err != nil {
		t.Fatalf("a pane the re-dispatch cleared still failed the start: %v", err)
	}
	if !dismissed() {
		t.Error("the stuck pane was never sent the Escape that dismisses the dialog")
	}
	if n := h.server.CountMethod(herdtest.MethodAgentStart); n != 2 {
		t.Errorf("agent.start was called %d times: want the launch and exactly one re-dispatch", n)
	}
	record := recordOf(t, h, "t1")
	if !record.LaunchConfirmed {
		t.Error("the re-dispatched launch is not recorded as confirmed")
	}
	observed := observationsOf(t, h, "t1")
	want := "claude is waiting at its folder-trust dialog in " + record.Worktree
	if !strings.Contains(observed, want) || !strings.Contains(observed, "re-dispatched once") {
		t.Errorf("the observations do not say what stopped the pane and that it was re-dispatched (want %q):\n%s",
			want, observed)
	}
}

// A pane still stuck after the one re-dispatch fails the start with a
// *PaneStartError that names the dialog and the worktree — not a generic
// startup timeout — and leaves the never-prompted attempt for the caller to
// run elsewhere: a later Start (a resumed run) reports it the same way, and
// AbandonUnstartedLaunch frees its branch.
func TestAPaneStuckTwiceIsAPaneStartErrorTheCallerCanAbandon(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.ex.opts.StartupTimeout = time.Second
	stuckPaneHerdr(h, trustDialogScreen, false)

	_, err := h.start("t1")
	pse, ok := AsPaneStartError(err)
	if !ok {
		t.Fatalf("a pane stuck at the trust dialog failed as %T (%v), want a *PaneStartError", err, err)
	}
	record := recordOf(t, h, "t1")
	if pse.Dialog != DialogFolderTrust || !pse.Redispatched || pse.Worktree != record.Worktree {
		t.Errorf("the error carries dialog %q, redispatched %v, worktree %q", pse.Dialog, pse.Redispatched, pse.Worktree)
	}
	if want := "claude is waiting at its folder-trust dialog in " + record.Worktree; !strings.Contains(err.Error(), want) {
		t.Errorf("the start failure does not say %q:\n%v", want, err)
	}
	if _, refused := subprocess.AsRefusal(err); refused {
		t.Errorf("a stuck pane surfaced as a refusal: %v", err)
	}
	if record.LaunchConfirmed {
		t.Fatal("a launch that never became ready is recorded confirmed")
	}

	// A resumed run finds the same never-prompted, stuck launch: it is the
	// pane-start failure again, never an adoption held for a person.
	_, err = h.start("t1")
	if again, ok := AsPaneStartError(err); !ok || !again.Resumed {
		t.Fatalf("the resume answered %v, want the stuck launch reported as a resumed *PaneStartError", err)
	}

	spec := h.spec("run-harness/tick-t1/attempt-1", "t1")
	if err := h.ex.AbandonUnstartedLaunch(spec, "falling back to the headless runner"); err != nil {
		t.Fatalf("the never-prompted launch could not be abandoned: %v", err)
	}
	if closes := h.paneCloses(); len(closes) != 1 || closes[0] != record.PaneID {
		t.Errorf("pane.close calls %v, want the stuck pane %s closed", closes, record.PaneID)
	}
	if branchExists(h.repo.Dir, record.Branch) {
		t.Errorf("branch %s survived the abandonment: the fallback runner cannot take the attempt's branch", record.Branch)
	}
	if _, err := os.Stat(record.Worktree); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the abandoned worktree %s is still on disk: %v", record.Worktree, err)
	}
}

// A blocked pane that shows no dialog this build recognises is still
// reported as blocked, with what the pane shows, not as a startup timeout.
func TestAPaneBlockedAtAnUnknownPromptSaysSo(t *testing.T) {
	t.Parallel()
	h := newHarness(t, harnessOptions{})
	h.ex.opts.StartupTimeout = time.Second
	stuckPaneHerdr(h, "Choose your favourite colour\n> red\n  blue", false)
	_, err := h.start("t1")
	pse, ok := AsPaneStartError(err)
	if !ok {
		t.Fatalf("got %v, want a *PaneStartError", err)
	}
	if pse.Dialog != DialogUnrecognised || !strings.Contains(err.Error(), "is blocked at a prompt in") ||
		!strings.Contains(err.Error(), "Choose your favourite colour") {
		t.Errorf("an unknown blocking prompt was reported as %q: %v", pse.Dialog, err)
	}
}

// The startup budget is the operator's: TICFAC_HERDR_STARTUP_TIMEOUT sets it
// for every executor that is not handed one, the default stays 60s, and a
// value that is not a positive duration fails the build of the executor
// rather than silently running on the default.
//
// serial: it sets TICFAC_HERDR_STARTUP_TIMEOUT with t.Setenv, which a parallel test may not.
func TestTheStartupBudgetComesFromTheEnvironment(t *testing.T) {
	repo := newRepo(t, "repo")
	s := herdtest.New(t, herdtest.Config{})
	build := func() (*Executor, error) {
		return New(Options{Repo: repo.Dir, StateDir: filepath.Join(t.TempDir(), "state"), SocketPath: s.Path(),
			ClaudeConfigPath: "-"})
	}

	t.Setenv(StartupTimeoutEnv, "")
	ex, err := build()
	if err != nil {
		t.Fatal(err)
	}
	if ex.opts.StartupTimeout != DefaultStartupTimeout {
		t.Errorf("unset: the budget is %s, want the unchanged default %s", ex.opts.StartupTimeout, DefaultStartupTimeout)
	}
	if ex.opts.ClaudeConfigPath != "" {
		t.Errorf("\"-\" did not turn trust registration off: %q", ex.opts.ClaudeConfigPath)
	}

	t.Setenv(StartupTimeoutEnv, "3m")
	if ex, err = build(); err != nil || ex.opts.StartupTimeout != 3*time.Minute {
		t.Errorf("3m: budget %v, err %v", ex.opts.StartupTimeout, err)
	}

	t.Setenv(StartupTimeoutEnv, "soon")
	if _, err := build(); err == nil || !strings.Contains(err.Error(), StartupTimeoutEnv) {
		t.Errorf("an unparseable budget built an executor (err %v): a typo must not silently mean the default", err)
	}
}

// short: pure parsing.
// serial: it sets TICFAC_HERDR_STARTUP_TIMEOUT with t.Setenv, which a parallel test may not.
func TestStartupTimeoutFromEnvParses(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"": 0, "90": 90 * time.Second, "2m30s": 150 * time.Second, " 45s ": 45 * time.Second,
	} {
		t.Setenv(StartupTimeoutEnv, raw)
		got, err := StartupTimeoutFromEnv()
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"0", "-5", "-1m", "never"} {
		t.Setenv(StartupTimeoutEnv, raw)
		if _, err := StartupTimeoutFromEnv(); err == nil {
			t.Errorf("%q was accepted as a startup budget", raw)
		}
	}
}

// short: pure string matching.
func TestClassifyScreenNamesClaudesDialogs(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		trustDialogScreen: DialogFolderTrust,
		"Allow external CLAUDE.md file imports?\nThis project's CLAUDE.md imports files outside": DialogExternalImports,
		"Detected a custom API key in your environment\nDo you want to use this API key?":        DialogAPIKey,
		"WARNING: Claude Code running in Bypass Permissions mode":                                DialogBypassWarning,
		"> working on it": "",
	}
	for screen, want := range cases {
		if got, _ := classifyScreen(screen); got != want {
			t.Errorf("%q classified as %q, want %q", firstLineOf(screen), got, want)
		}
	}
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
