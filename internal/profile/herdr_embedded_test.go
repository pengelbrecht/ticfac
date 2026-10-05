package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// short: resolving the embedded herdr set reads the bytes compiled into the
// binary and touches no repository and no subprocess — the one thing it
// checks that disk would not is that the virtual name never consults a
// directory an operator happens to have.

// The herdr profile set travels inside the executable (tick 9sz): `ticfac
// run` dispatches into herdr panes with the set EMBEDDED in the binary, so
// an operator names no filesystem path and a profile read off some other
// disk cannot disagree with the binary that resolved it. These pin the
// virtual directory name that reaches the set and the SPLIT tick 2q5
// designed (docs/herdr-pi-durable-hosting.md) between the two hosts a
// local run has: the durable host for implementation work, herdr panes for
// the frontier rung.

// Every Phase 1 role resolves from the embedded herdr set, and what they
// resolve is the split, not a uniform substrate: implementation work runs
// on the pi-durable harness through the local subprocess executor —
// headless, one Node process per attempt on its own SQLite storage — while
// the frontier rung (the claude CLI, the operator's local-only exception to
// "one harness") rides herdr panes. The set is what makes A1 true on a
// herdr host: routing an implement worker onto the herdr executor would
// launch a herdr agent of kind pi, which is the pi CLI — the worker path
// epic 43y deletes — because a herdr agent template is static and cannot
// carry the per-attempt state (worker.json, storage, steer socket) the
// durable host needs.
func TestTheEmbeddedHerdrSetResolvesEveryRole(t *testing.T) {
	const glm53 = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
	for _, role := range Roles {
		resolved, err := Resolve(role, Options{Dir: EmbeddedHerdr})
		if err != nil {
			t.Fatalf("resolve %s from the embedded herdr set: %v", role, err)
		}
		if resolved.Prompt == "" {
			t.Errorf("%s resolved no prompt: a role dispatched without its prompt is a role in name only", role)
		}
		want := "profiles-herdr/" + role + ".json"
		if resolved.Source != want {
			t.Errorf("%s's provenance names %q, want the embedded set's %q", role, resolved.Source, want)
		}
		switch role {
		case "implement-tick":
			// The durable host carries implementation: the local subprocess
			// executor's runner pi IS the pi-durable Node harness since tick
			// hpk, so this pairing is A1's "every implement worker runs on
			// pi-durable" holding on a herdr host too.
			if resolved.Executor != "local-subprocess" {
				t.Errorf("implement-tick resolved executor %q, want local-subprocess — on the herdr "+
					"executor a pi-kind worker is the pi CLI, the path epic 43y deletes", resolved.Executor)
			}
			if resolved.Runner != "pi" || resolved.Model != glm53 {
				t.Errorf("implement-tick pairs %s/%s, want pi/%s on the durable host",
					resolved.Runner, resolved.Model, glm53)
			}
		default:
			// The frontier rung keeps the panes: review and closeout are the
			// claude CLI jobs an operator attends, the local-only exception
			// the operator's 2026-10-04 decision keeps.
			if resolved.Executor != "herdr" {
				t.Errorf("%s resolved executor %q, want herdr — the frontier rung is the one "+
					"part of a local run that rides panes", role, resolved.Executor)
			}
			if resolved.Runner != "claude" || resolved.Model != "opus" {
				t.Errorf("%s pairs %s/%s, want claude/opus — the frontier rung is the claude CLI",
					role, resolved.Runner, resolved.Model)
			}
		}
	}

	// The on-demand roles resolve too: a merge conflict or a failed gate on a
	// herdr run dispatches through the same set, lazily, and a role that
	// resolves at dispatch time must resolve at all times. Both are
	// judgement jobs the roles table routes at the ceiling — claude, the
	// frontier rung — and the shipped profile must not name a worker path
	// jhp deletes.
	for _, role := range []string{RoleResolveConflict, RoleRepairGate} {
		resolved, err := Resolve(role, Options{Dir: EmbeddedHerdr})
		if err != nil {
			t.Errorf("resolve the on-demand role %s from the embedded herdr set: %v", role, err)
			continue
		}
		if resolved.Executor != "herdr" || resolved.Runner != "claude" {
			t.Errorf("the on-demand role %s pairs %s on %s, want the frontier rung: claude on herdr",
				role, resolved.Runner, resolved.Executor)
		}
	}

	// And the set is a different set from the local one that is the
	// compiled-in default: the digests disagree, so an attempt record's
	// provenance can always say which dispatch substrate a run used.
	local, err := Resolve("implement-tick", Options{})
	if err != nil {
		t.Fatal(err)
	}
	herdrSet, err := Resolve("implement-tick", Options{Dir: EmbeddedHerdr})
	if err != nil {
		t.Fatal(err)
	}
	if local.Digest == herdrSet.Digest {
		t.Error("the local set and the herdr set digest the same: an attempt record could not tell a pane dispatch from a local one")
	}
}

// The virtual name is the BINARY'S, never the disk's: a directory on the
// machine that happens to be called "herdr" is not consulted, exactly as no
// directory is consulted for the empty name. The herdr set a run resolves
// is the bytes compiled into the executable or nothing at all — the whole
// reason `ticfac run` needs no profile path.
func TestTheEmbeddedHerdrNameIsNotReadOffDisk(t *testing.T) {
	dir := t.TempDir()
	// A disk directory with the virtual name holding a profile that would
	// refuse loudly if it were ever read: filed for another role, so a pass
	// here cannot be a pass by accident.
	if err := os.MkdirAll(filepath.Join(dir, EmbeddedHerdr), 0o755); err != nil {
		t.Fatal(err)
	}
	decoy := map[string]any{
		"schema_version": SchemaVersion,
		"role":           "review-epic", // not implement-tick: a refusal if read
		"version":        "0.0.0-decoy",
		"executor":       "herdr",
		"runner":         "pi",
		"model":          "decoy",
		"prompt":         "implement-tick.md",
	}
	raw, err := json.Marshal(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, EmbeddedHerdr, "implement-tick.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, err := Resolve("implement-tick", Options{Dir: EmbeddedHerdr})
	if err != nil {
		t.Fatalf("the virtual name consulted the disk: %v", err)
	}
	if resolved.Version == "0.0.0-decoy" {
		t.Fatal("the profile resolved from the disk decoy, not from the binary")
	}
	if resolved.Source != "profiles-herdr/implement-tick.json" {
		t.Errorf("provenance names %q, not the embedded set", resolved.Source)
	}
}

// short: resolving every embedded set reads only the bytes compiled into
// the binary — no repository, no subprocess.

// No profile in any embedded set a herdr run can select pairs the herdr
// executor with runner pi (tick 2q5's guard for epic 43y's A1). On the
// herdr executor a runner names a herdr AGENT KIND, and kind pi is the
// interactive pi CLI — the worker path jhp deletes, the one A1 says no
// worker may run on. The same runner name on the local-subprocess executor
// is a different thing (the pi-durable Node harness, tick hpk), so the guard
// refuses the PAIRING, not the name: a durable-host worker is
// local-subprocess + pi, a pane worker is herdr + an interactive CLI the
// operator's frontier rung names.
//
// This is the test that fails at the base of tick 2q5: every profile the
// herdr set shipped named herdr + pi, so a local epic run on a herdr host —
// substrate auto, the set `ticfac run` selects when herdr answers — dispatched
// its implement workers onto the pi CLI while the epic claimed pi-durable
// was the only worker harness.
func TestNoEmbeddedProfilePairsTheHerdrExecutorWithThePiCLI(t *testing.T) {
	for _, set := range EmbeddedSets {
		for _, role := range EveryRole() {
			resolved, err := Resolve(role, Options{Dir: set})
			if err != nil {
				t.Fatalf("resolve %s from the embedded set %q: %v", role, set, err)
			}
			if resolved.Executor == "herdr" && resolved.Runner == "pi" {
				t.Errorf("%s in the set %q pairs executor herdr with runner pi: a herdr agent of kind pi is the "+
					"interactive pi CLI, the worker path epic 43y deletes — name the durable host "+
					"(local-subprocess + pi) or an interactive CLI the frontier rung runs", role, set)
			}
		}
	}
}
