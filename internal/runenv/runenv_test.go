package runenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestTheControlPlaneIsTheRunsAndNotTheMachines(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"TICKS_SUBSTRATE", "TICKS_RUN_ID", "TICKS_FACTORY_URL", "TICKS_FACTORY_TOKEN",
		"TICKS_PHASE", "TICKS_MODEL", "TICFAC_RUNNER", "TICFAC_EXEC_STATE_DIR",
		"TICFAC_JEV_API_TOKEN", "TICFAC_GATE", isolatedEnv,
		"HERDR_ENV", "HERDR_SOCKET_PATH", "AI_GATEWAY_BASE_URL", "AI_GATEWAY_TOKEN", "TK_ACTOR",
	} {
		if !IsControlPlane(name) {
			t.Errorf("%s is a run's control plane, but IsControlPlane says it is not", name)
		}
	}
	for _, name := range []string{
		"PATH", "HOME", "TMPDIR", "GOPATH", "GOCACHE", "GOMODCACHE", "GOFLAGS",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "LANG", "LC_ALL",
		"GIT_SSH_COMMAND", "ANTHROPIC_API_KEY", "TK_HOME", "TK_HERD_LIVE_TEST",
		"TICFAC_LIVE_JEV", "TICFAC_LIVE_RUNNER", "TICFAC_GIT",
		// A name only LIKE a control-plane one.
		"TICKS", "MY_TICKS_SUBSTRATE",
	} {
		if IsControlPlane(name) {
			t.Errorf("%s belongs to the machine, but IsControlPlane takes it", name)
		}
	}
}

func TestScrubRemovesOnlyTheControlPlane(t *testing.T) {
	t.Parallel()
	env := []string{"PATH=/bin", "TICKS_SUBSTRATE=cloud", "GOFLAGS=-mod=mod", "TK_ACTOR=cloud:orchestrator", "odd"}
	kept, removed := Scrub(env)
	if got, want := strings.Join(kept, " "), "PATH=/bin GOFLAGS=-mod=mod odd"; got != want {
		t.Errorf("kept %q, want %q", got, want)
	}
	if got, want := strings.Join(removed, " "), "TICKS_SUBSTRATE TK_ACTOR"; got != want {
		t.Errorf("removed %q, want %q", got, want)
	}
	if env[1] != "TICKS_SUBSTRATE=cloud" {
		t.Error("Scrub modified its input")
	}
}

// childEnv marks this binary re-exec'd by the isolation tests below. It is
// not a control-plane name, so it survives the child's own init.
const childEnv = "RUNENV_TEST_CHILD"

// TestATestBinaryShedsALiveRunsControlPlane starts this test binary the way a
// gate inside a cloud orchestrator does — with the run's variables set and no
// isolation marker — and asks it what its tests see.
func TestATestBinaryShedsALiveRunsControlPlane(t *testing.T) {
	t.Parallel()
	got := runChild(t, false)
	if want := "TICKS_SUBSTRATE= TICKS_FACTORY_TOKEN= TK_ACTOR= TICFAC_LIVE_JEV=1 PATH=set"; got != want {
		t.Errorf("a test binary started with a live run's environment saw %q, want %q", got, want)
	}
}

// TestAReExecdTestBinaryKeepsWhatItsParentSetForIt: a child this binary
// starts inherits the marker, and with it what the parent's test set on
// purpose (the run-epic and evacuation children depend on it).
func TestAReExecdTestBinaryKeepsWhatItsParentSetForIt(t *testing.T) {
	t.Parallel()
	got := runChild(t, true)
	if want := "TICKS_SUBSTRATE=cloud TICKS_FACTORY_TOKEN=tkr_example TK_ACTOR=cloud:orchestrator TICFAC_LIVE_JEV=1 PATH=set"; got != want {
		t.Errorf("a re-exec'd test binary saw %q, want %q", got, want)
	}
}

func runChild(t *testing.T, marked bool) string {
	t.Helper()
	env, _ := Scrub(os.Environ())
	env = append(env, childEnv+"=1",
		"TICKS_SUBSTRATE=cloud", "TICKS_FACTORY_TOKEN=tkr_example", "TK_ACTOR=cloud:orchestrator", "TICFAC_LIVE_JEV=1")
	if marked {
		env = append(env, isolatedEnv+"=1")
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestIsolationChild$", "-test.count=1", "-test.v")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child failed: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if _, seen, ok := strings.Cut(line, "seen: "); ok {
			return strings.TrimSpace(seen)
		}
	}
	t.Fatalf("the child reported nothing:\n%s", out)
	return ""
}

// TestIsolationChild is the child's half; it does nothing in a normal run.
func TestIsolationChild(t *testing.T) {
	t.Parallel()
	if os.Getenv(childEnv) == "" {
		t.Skip("run only as the isolation tests' child")
	}
	var parts []string
	for _, name := range []string{"TICKS_SUBSTRATE", "TICKS_FACTORY_TOKEN", "TK_ACTOR", "TICFAC_LIVE_JEV"} {
		parts = append(parts, name+"="+os.Getenv(name))
	}
	path := ""
	if os.Getenv("PATH") != "" {
		path = "set"
	}
	parts = append(parts, "PATH="+path)
	t.Logf("seen: %s", strings.Join(parts, " "))
}

// buildEnv are the names the run's boot exports that are NOT its control
// plane: the machine's toolchain, caches, git transport, and the model
// provider variables the image wires to the gateway. A gate inherits them.
var buildEnv = map[string]bool{
	"PATH": true, "GOCACHE": true, "GOMODCACHE": true,
	"npm_config_store_dir": true, "npm_config_cache": true, "XDG_CACHE_HOME": true,
	"UV_CACHE_DIR": true, "BUN_INSTALL_CACHE_DIR": true,
	"MISE_DATA_DIR": true, "MISE_CACHE_DIR": true, "MISE_GLOBAL_CONFIG_FILE": true,
	"GIT_SSH_COMMAND": true, "GIT_HTTP_LOW_SPEED_LIMIT": true, "GIT_HTTP_LOW_SPEED_TIME": true,
	"ANTHROPIC_BASE_URL": true, "ANTHROPIC_AUTH_TOKEN": true, "ANTHROPIC_API_KEY": true,
	"OPENAI_BASE_URL": true, "OPENAI_API_KEY": true,
	"OPENROUTER_BASE_URL": true, "OPENROUTER_API_KEY": true,
	"WORKERS_AI_BASE_URL": true, "CLOUDFLARE_ACCOUNT_ID": true,
	"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS": true,
}

var exportRE = regexp.MustCompile(`(?m)\bexport\s+([A-Za-z_][A-Za-z0-9_]*)(?:=|[ \t]*;|[ \t]*$)`)

// TestEveryVariableARunsBootExportsIsClassified reads what the cloud boot
// puts in the orchestrator's and worker's environment and fails on a name
// that is neither control plane nor listed above as build environment, so a
// new export is decided on rather than inherited by every gate by default.
func TestEveryVariableARunsBootExportsIsClassified(t *testing.T) {
	t.Parallel()
	sources, err := filepath.Glob(filepath.Join("..", "..", "image", "*.sh"))
	if err != nil || len(sources) == 0 {
		t.Fatalf("find the image's shell: %v (%d files)", err, len(sources))
	}
	sources = append(sources, filepath.Join("..", "factory", "ticfacentrypoint.go"))
	seen := map[string]bool{}
	for _, src := range sources {
		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range exportRE.FindAllStringSubmatch(string(body), -1) {
			seen[m[1]] = true
		}
	}
	for _, want := range []string{"TICKS_SUBSTRATE", "TICKS_FACTORY_TOKEN", "TK_ACTOR", "AI_GATEWAY_TOKEN"} {
		if !seen[want] {
			t.Errorf("the boot no longer exports %s: this guard's reading of the image is broken", want)
		}
	}
	var unclassified []string
	for name := range seen {
		if !IsControlPlane(name) && !buildEnv[name] {
			unclassified = append(unclassified, name)
		}
	}
	sort.Strings(unclassified)
	for _, name := range unclassified {
		t.Errorf("the run's boot exports %s, which is neither a control-plane variable (runenv.IsControlPlane) "+
			"nor listed in buildEnv: decide whether a gate and a test may inherit it", name)
	}
}
