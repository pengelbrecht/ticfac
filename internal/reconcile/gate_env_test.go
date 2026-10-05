package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gate's environment is the machine's, not the run's (runenv). The first
// cloud run of epic hn6 ran its integrated gate inside the orchestrator
// container, whose entrypoint exports TICKS_SUBSTRATE=cloud and the run's
// factory endpoint and token; this repository's own tests read them, and
// reconcile.New refused every fixture for declaring no cloud role cells. The
// same gate passed locally and in CI.

// TestAGateDoesNotInheritTheRunsControlPlane sets the variables a cloud
// orchestrator carries in this process and asks the gate's command what it
// sees: none of the run's, and every one a build needs.
//
// serial: t.Setenv, which a parallel test cannot call
// short: one short-lived shell in a tempdir; no repository and no run
func TestAGateDoesNotInheritTheRunsControlPlane(t *testing.T) {
	for name, value := range map[string]string{
		"TICKS_SUBSTRATE":     "cloud",
		"TICKS_RUN_ID":        "run_example",
		"TICKS_FACTORY_URL":   "https://factory.example.invalid",
		"TICKS_FACTORY_TOKEN": "tkr_example",
		"TICFAC_RUNNER":       "pi",
		"AI_GATEWAY_BASE_URL": "https://gateway.example.invalid",
		"AI_GATEWAY_TOKEN":    "gw_example",
		"HERDR_ENV":           "1",
		"TK_ACTOR":            "cloud:orchestrator",
		// Build environment: inherited unchanged.
		"GOFLAGS":     "-mod=mod",
		"HTTPS_PROXY": "http://proxy.example.invalid:3128",
		"LANG":        "C.UTF-8",
		// An operator's explicit opt-in is intent, not a run's mode.
		"TICFAC_LIVE_JEV": "1",
	} {
		t.Setenv(name, value)
	}

	got := gateOnce(t, t.TempDir(), `env`)
	seen := map[string]string{}
	for _, line := range strings.Split(got, "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			seen[name] = value
		}
	}
	for _, name := range []string{
		"TICKS_SUBSTRATE", "TICKS_RUN_ID", "TICKS_FACTORY_URL", "TICKS_FACTORY_TOKEN",
		"TICFAC_RUNNER", "AI_GATEWAY_BASE_URL", "AI_GATEWAY_TOKEN", "HERDR_ENV", "TK_ACTOR",
	} {
		if value, ok := seen[name]; ok {
			t.Errorf("the gate's command inherited the run's %s=%q; a gate must answer the same wherever the run lives", name, value)
		}
	}
	for name, want := range map[string]string{
		"GOFLAGS": "-mod=mod", "HTTPS_PROXY": "http://proxy.example.invalid:3128", "LANG": "C.UTF-8",
		"TICFAC_LIVE_JEV": "1", "TICFAC_GATE": "1", "GIT_TERMINAL_PROMPT": "0",
	} {
		if seen[name] != want {
			t.Errorf("the gate's command saw %s=%q, want %q", name, seen[name], want)
		}
	}
	for _, name := range []string{"PATH", "HOME", "TMPDIR"} {
		if seen[name] == "" {
			t.Errorf("the gate's command lost %s, which every build needs", name)
		}
	}
}

// TestAGateDoesNotSeeTheHostsInstalledTicfac: the operator's Mac has ticfac
// installed beside tk and herdr in ~/.local/bin, and epic hn6's integrated
// gate passed two tests there that GitHub CI — which installs no ticfac —
// failed: both reached the lookup of ticfac-exec-subprocess, and the
// installed copy answered for the tree. The gate's command must resolve none
// of ticfac's own binaries from the host, and keep every other tool the same
// directory holds.
//
// serial: t.Setenv, which a parallel test cannot call
// short: one short-lived shell in a tempdir; no repository and no run
func TestAGateDoesNotSeeTheHostsInstalledTicfac(t *testing.T) {
	installed := t.TempDir()
	for _, name := range []string{"ticfac", "ticfac-exec-subprocess", "ticfac-dash", "tk"} {
		if err := os.WriteFile(filepath.Join(installed, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", installed+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := gateOnce(t, t.TempDir(), `for b in ticfac ticfac-exec-subprocess ticfac-dash tk; do `+
		`if command -v "$b" >/dev/null 2>&1; then echo "$b=found"; else echo "$b=absent"; fi; done`)
	if want := "ticfac=absent\nticfac-exec-subprocess=absent\nticfac-dash=absent\ntk=found\n"; got != want {
		t.Errorf("the gate's command resolved\n%s\nwant\n%s(an installed ticfac must not answer for the tree "+
			"under the gate; the directory's other tools stay)", got, want)
	}
}
