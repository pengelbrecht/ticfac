package cli

// The herdr half of the factory's gate path (tick 53k, absorbed from the
// epic's final review): `run` never forwards --gate, so executorFactory is
// built with "" — a path the reconciler itself defaults to the repository's
// own .tick/runners.toml, but the factory captured BEFORE that defaulting
// happened. An empty path read as "routes nothing" is a herdr dispatch that
// drops the roles table's effort and args and dials the DEFAULT socket even
// when orchestration.socket is where the run detected herdr — which breaks
// A1's "runs an epic ... with no other flag" story the moment the repo
// routes its panes through runners.toml.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/herdr"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// herdrGateDispatch is the dispatch a herdr profile routes: the same shape
// cloudDispatch carries, with the executor the honoured set names herdr and
// a repository the factory can read routing from.
func herdrGateDispatch(t *testing.T, repo string) reconcile.Dispatch {
	t.Helper()
	return reconcile.Dispatch{
		RunID:    "r1",
		EpicID:   "2jn",
		TickID:   "53k",
		Attempt:  1,
		JobID:    "run-r1/tick-53k/attempt-1",
		Role:     "implement-tick",
		Repo:     repo,
		Remote:   "origin",
		WriteRef: "refs/heads/ticfac/run-r1/tick-53k/attempt-1",
		BaseSHA:  "0123456789abcdef0123456789abcdef01234567",
		BaseRef:  "epic/2jn",
		Title:    "Route the herdr gate path from the repository",
		Profile: &profile.Profile{
			Role: "implement-tick", Executor: herdr.ExecutorName,
			Runner: "claude", Model: "opus",
			Prompt: "# implement-tick\n\nYou are implementing ONE unit of work.\n",
		},
	}
}

// writeHerdrRunners writes the routing a herdr pane is dispatched under:
// an effort and an args entry the spawn must carry, and a pinned socket the
// dispatch must dial — the three things an empty gate path dropped.
func writeHerdrRunners(t *testing.T, repo, socket string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repo, ".tick"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "version = 2\n\n[orchestration]\nsocket = " + quote(socket) + "\n\n" +
		"[roles.implement]\nkind = \"claude\"\nmodel = \"opus\"\neffort = \"high\"\n" +
		"args = [\"--strict-mcp-config\"]\n"
	if err := os.WriteFile(filepath.Join(repo, ".tick", "runners.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// quote renders a TOML basic string the way the fixtures need it.
func quote(s string) string { return "\"" + s + "\"" }

// An UNNAMED gate is the dispatch's own repository's .tick/runners.toml —
// the same default the reconciler applies to GateConfig — so a herdr pane
// launched without --gate carries the effort and the args the roles table
// declares and dials the socket its orchestration pins. Read through
// spawnArgv, the seam the executor compiles from, because the assertion is
// about what the spawn is compiled FROM, not about dialing a real herdr.
//
// short: one tempdir, one file, one compile; no herdr is dialled.
func TestAnUnnamedGateIsTheRepositorysOwnRunnersToml(t *testing.T) {
	repo := t.TempDir()
	pinned := filepath.Join(t.TempDir(), "herdr.sock")
	writeHerdrRunners(t, repo, pinned)
	// The default the UNGATED resolve falls to must differ from the pinned
	// one, or the socket assertion proves nothing.
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "default.sock"))

	d := herdrGateDispatch(t, repo)
	cfg, argv, err := spawnArgv("", d)
	if err != nil {
		t.Fatalf("the spawn refused a dispatch the repository routes: %v", err)
	}
	if cfg == nil {
		t.Fatal("an unnamed gate read no configuration: the empty path must resolve to the dispatch's own " +
			"repository's runners.toml — the same default the reconciler applies to GateConfig — not be treated " +
			"as a repository that routes nothing")
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--effort high") {
		t.Errorf("the spawn argv %q carries no effort: the roles table's effort is the whole point of routing "+
			"the spawn, and a pane without it runs at the model's default while its record says otherwise", joined)
	}
	if !strings.Contains(joined, "--strict-mcp-config") {
		t.Errorf("the spawn argv %q carries none of the roles table's args: --approve, --strict-mcp-config and "+
			"every other operator escape hatch lives there, and a pane without them is not the pane the "+
			"repository asked for", joined)
	}
	socket, err := runconfig.ResolveSocket(cfg)
	if err != nil {
		t.Fatalf("the routed configuration names no socket the dispatch could dial: %v", err)
	}
	if socket != pinned {
		t.Errorf("the dispatch would dial %q, want the pinned %q: a run that detected herdr at "+
			"orchestration.socket must dispatch at the same socket it probed, or a live herdr is reported dead "+
			"by the very panes meant to use it", socket, pinned)
	}
}

// The named gate still wins, and a repository that carries NO runners.toml
// at all still routes nothing — the fix must not make a missing file an
// error, only make a missing NAME mean the repository's own.
//
// short: two tempdirs, two compiles; nothing is dialled.
func TestAGateNamedStillWinsAndAnUnroutedRepoStillRoutesNothing(t *testing.T) {
	repo := t.TempDir()
	pinned := filepath.Join(t.TempDir(), "herdr.sock")
	writeHerdrRunners(t, repo, pinned)
	other := t.TempDir()
	writeHerdrRunners(t, other, filepath.Join(t.TempDir(), "other.sock"))

	d := herdrGateDispatch(t, repo)
	cfg, _, err := spawnArgv(filepath.Join(other, runconfig.FileName), d)
	if err != nil {
		t.Fatalf("the spawn refused a dispatch with a named gate: %v", err)
	}
	if cfg == nil {
		t.Fatal("a named gate read no configuration")
	}
	socket, err := runconfig.ResolveSocket(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if socket == pinned {
		t.Errorf("a gate named on the command line resolved the dispatch's own repository's socket %q: the "+
			"named path is an expert override and must win over the repository's own", pinned)
	}

	// A repository with no runners.toml keeps the old behaviour: no routing,
	// no refusal — the profile ships as written.
	empty := t.TempDir()
	cfg, _, err = spawnArgv("", herdrGateDispatch(t, empty))
	if err != nil {
		t.Fatalf("a repository that carries no runners.toml refused the dispatch: %v", err)
	}
	if cfg != nil {
		t.Error("a repository with no runners.toml routed a configuration from nowhere")
	}
}
