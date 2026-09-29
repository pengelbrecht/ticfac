// Package runenv names the environment a live run carries that describes the
// RUN rather than the machine, and keeps it out of the two places that must
// not inherit it: a gate's command, and a test binary.
//
// # Why
//
// A run's orchestrator is booted with its own control plane in the
// environment. The cloud orchestrator's entrypoint (image/entrypoint.sh, and
// the staged copy internal/factory/ticfacentrypoint.go writes) exports
// TICKS_SUBSTRATE=cloud, TICKS_RUN_ID, TICKS_FACTORY_URL and
// TICKS_FACTORY_TOKEN, TK_ACTOR and the rest; common.sh adds the model
// gateway (AI_GATEWAY_BASE_URL, AI_GATEWAY_TOKEN) and TICKS_MODEL*; a local
// run started inside a herdr pane carries HERDR_ENV=1 and the pane's socket.
// Every one of those switches ticfac's own code into a mode: TICKS_SUBSTRATE
// picks the substrate a reconciler resolves roles against, the factory pair
// is a live endpoint and a run-scoped credential, AI_GATEWAY_* IS the Jev
// credential.
//
// The first cloud run of epic hn6 showed what happens when a gate inherits
// them. Its integrated gate ran this repository's own short suite inside the
// orchestrator container, and the tests saw TICKS_SUBSTRATE=cloud:
// reconcile.New refused every fixture repository for declaring no cloud role
// cells, and `ticfac cloud branch`'s "no credential" test POSTed to the live
// factory with the run's token. The same tests pass locally and in GitHub CI.
// A gate that answers differently depending on where the run lives is not a
// gate.
//
// # The list
//
// [IsControlPlane] is the whole list, and this package is the one place it is
// written down:
//
//   - every TICKS_* variable: the cloud boot's inputs and outputs (phase, run
//     id, run branch, pass, factory endpoint and token, substrate, model, the
//     harness and its probes, cache and setup knobs);
//   - every TICFAC_* variable: ticfac's own knobs (runner, status push,
//     registry and exec-state dirs, Jev credential, the runner-side
//     TICFAC_ROLE/TICK/WORKTREE/RESULT_PATH, the gate's own TICFAC_GATE*)
//     — except the ones in [keep];
//   - every HERDR_* variable: the pane a local run was started in;
//   - every AI_GATEWAY_* variable: the run's model gateway and its token;
//   - TK_ACTOR: who a tk mutation is attributed to (cloud:orchestrator in a
//     container), which a test's own fixture tracker must not take on.
//
// Kept on purpose ([keep]): TICFAC_LIVE_* (an operator's explicit opt-in to
// the live tests, which is intent, not a run's mode) and TICFAC_GIT (which git
// binary this machine uses — a fact about the host, like PATH).
//
// NOT in the list, and inherited by a gate unchanged: everything a build
// needs — PATH, HOME, USER, SHELL, TMPDIR (the gate sets its own, #76),
// GOPATH/GOCACHE/GOMODCACHE/GOFLAGS/GOTOOLCHAIN, the package-manager caches
// the image points at (npm_config_*, XDG_CACHE_HOME, UV_CACHE_DIR, MISE_*,
// BUN_INSTALL_CACHE_DIR), the proxies (HTTP(S)_PROXY, NO_PROXY), locale
// (LANG, LC_*), git's transport settings (GIT_SSH_COMMAND, GIT_HTTP_*), and
// the model provider variables the image wires to the gateway (ANTHROPIC_*,
// OPENAI_*, OPENROUTER_*, WORKERS_AI_*): those name no ticfac mode, and a
// repository's gate may legitimately need them. A deny list rather than an
// allow list, for that reason: a gate runs ANY repository's commands, and an
// allow list would break every toolchain it did not foresee.
//
// runenv_test.go reads every `export NAME=` in the image's shell and the
// staged entrypoint and fails on a name that is neither in this list nor
// named there as build environment, so a new export has to be classified.
//
// # The two layers
//
//   - The gate (internal/reconcile/gate.go) starts its command with
//     [Scrub] applied to its own environment.
//   - A test binary sheds the list in this package's init, before any test
//     or TestMain runs: every package that reads one of these variables
//     links this one (runconfig, reconcile, cli, forge, runsignal). The
//     HERDR_* half is left to internal/herd/client's own init, which must
//     record the operator's inherited socket before anything clears it and
//     then points HERDR_SOCKET_PATH at a socket nothing listens on.
package runenv

import (
	"os"
	"strings"
	"testing"
)

// prefixes are the namespaces that belong to a run's control plane.
var prefixes = []string{"TICKS_", "TICFAC_", "HERDR_", "AI_GATEWAY_"}

// names are the control-plane variables outside those namespaces.
var names = []string{"TK_ACTOR"}

// keep are the names or namespaces inside [prefixes] that are NOT a run's
// mode; see the package documentation for why each one stays.
var keep = []string{"TICFAC_LIVE_", "TICFAC_GIT"}

// IsControlPlane reports whether an environment variable belongs to a run's
// control plane rather than to the machine.
func IsControlPlane(name string) bool {
	for _, k := range keep {
		if name == k || (strings.HasSuffix(k, "_") && strings.HasPrefix(name, k)) {
			return false
		}
	}
	for _, n := range names {
		if name == n {
			return true
		}
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Scrub returns env without its control-plane entries, and the names it
// removed. env is not modified. An entry with no '=' is kept as it is.
func Scrub(env []string) (kept, removed []string) {
	kept = make([]string, 0, len(env))
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if ok && IsControlPlane(name) {
			removed = append(removed, name)
			continue
		}
		kept = append(kept, entry)
	}
	return kept, removed
}

// isolatedEnv marks a test process whose environment has already been
// scrubbed. A test binary re-execs itself (the detached run-epic child, the
// SIGTERM evacuation child, the supervisor children), and the child inherits
// both the parent's scrubbed environment and the TICFAC_* variables the
// parent's test set FOR it on purpose; the child must not strip those again.
// It is itself a TICFAC_* name, so a gate — which scrubs — never passes it
// on, and a test binary a gate runs scrubs afresh.
const isolatedEnv = "TICFAC_TEST_ENV_ISOLATED"

// A test binary never inherits a live run's mode.
func init() {
	if !testing.Testing() || os.Getenv(isolatedEnv) != "" {
		return
	}
	isolate()
}

// isolate unsets every control-plane variable in this process except the
// HERDR_* half (see the package documentation), and marks the process.
func isolate() {
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || strings.HasPrefix(name, "HERDR_") || !IsControlPlane(name) {
			continue
		}
		_ = os.Unsetenv(name)
	}
	_ = os.Setenv(isolatedEnv, "1")
}
