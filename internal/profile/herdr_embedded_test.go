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
// virtual directory name that reaches the set and the honest difference
// between it and the local set that is the compiled-in default.

// Every Phase 1 role resolves from the embedded herdr set, and what they
// resolve is the herdr set: the executor is herdr on every role — the one
// field a target repository's routing cannot move — and the provenance
// names the set the binary carries, not a host path.
func TestTheEmbeddedHerdrSetResolvesEveryRole(t *testing.T) {
	for _, role := range Roles {
		resolved, err := Resolve(role, Options{Dir: EmbeddedHerdr})
		if err != nil {
			t.Fatalf("resolve %s from the embedded herdr set: %v", role, err)
		}
		if resolved.Executor != "herdr" {
			t.Errorf("%s resolved executor %q, want herdr — the herdr set's whole point", role, resolved.Executor)
		}
		if resolved.Prompt == "" {
			t.Errorf("%s resolved no prompt: a role dispatched without its prompt is a role in name only", role)
		}
		want := "profiles-herdr/" + role + ".json"
		if resolved.Source != want {
			t.Errorf("%s's provenance names %q, want the embedded set's %q", role, resolved.Source, want)
		}
	}

	// The on-demand roles resolve too: a merge conflict or a failed gate on a
	// herdr run dispatches through the same set, lazily, and a role that
	// resolves at dispatch time must resolve at all times.
	for _, role := range []string{RoleResolveConflict, RoleRepairGate} {
		if _, err := Resolve(role, Options{Dir: EmbeddedHerdr}); err != nil {
			t.Errorf("resolve the on-demand role %s from the embedded herdr set: %v", role, err)
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
	if local.Executor == herdrSet.Executor {
		t.Errorf("both sets name executor %q: the herdr set must name herdr, the local set %q",
			local.Executor, local.Executor)
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
