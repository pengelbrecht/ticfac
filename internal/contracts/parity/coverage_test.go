package parity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// readerKind says what a reader is able to assert. The distinction is written
// down rather than left to prose because a structural reader counted as a
// parity reader is a fixture that looks two-sided and is not.
type readerKind string

const (
	// executable: the fixture's cases are run against ticfac code.
	executable readerKind = "executable"

	// structural: the fixture pins a rule over a format ticfac does not parse
	// yet. The shape, the vocabularies and the cross-file claims are asserted;
	// the cases wait for the work named in `turnedExecutableBy`.
	structural readerKind = "structural"
)

type reader struct {
	kind readerKind
	// file is this package's reader for the contract.
	file string
	// turnedExecutableBy names what will run the fixture's cases, for a
	// structural reader. Empty for an executable one.
	turnedExecutableBy string
}

// readers is the map this package is accountable to: every file in the bundle
// has one here, and every entry here is in the bundle.
var readers = map[string]reader{
	"job-protocol.json":             {kind: executable, file: "job_protocol_test.go"},
	"ticfac-run-state.json":         {kind: executable, file: "run_state_test.go"},
	"lifecycle-invariants.json":     {kind: executable, file: "lifecycle_invariants_test.go"},
	"credential-ownership.json":     {kind: executable, file: "credential_ownership_test.go"},
	"tk-json-manifest.json":         {kind: executable, file: "tk_json_manifest_test.go"},
	"collect-vocabulary.json":       {kind: executable, file: "collect_vocabulary_test.go"},
	"message-context.json":          {kind: executable, file: "message_context_test.go"},
	"runners-config-contract.json":  {kind: executable, file: "runners_config_test.go"},
	"tracker-layout.json":           {kind: executable, file: "tracker_layout_test.go"},
	"worker-boot-contract.json":     {kind: structural, file: "worker_boot_test.go", turnedExecutableBy: "the Herdr executor (SPEC §12 Phase 2)"},
	"sandbox-image-cases.json":      {kind: structural, file: "toml_cases_test.go", turnedExecutableBy: "a .tick/runners.toml reader (SPEC §12 Phase 2)"},
	"signal-source-cases.json":      {kind: structural, file: "toml_cases_test.go", turnedExecutableBy: "a .tick/runners.toml reader (SPEC §12 Phase 2)"},
	"sweep-policy-cases.json":       {kind: structural, file: "toml_cases_test.go", turnedExecutableBy: "a .tick/runners.toml reader (SPEC §12 Phase 2)"},
	"sweep-selection-contract.json": {kind: structural, file: "sweep_selection_test.go", turnedExecutableBy: "the reconciler's wave selection (SPEC §12 Phase 1 step 2)"},
}

// Every file in the pinned bundle has a reader in this package, and every
// reader names a file in the bundle. A contract added upstream with no reader
// here is a rule ticfac has not been held to; a reader for a file the bundle
// no longer carries points at nothing.
func TestEveryBundleFileHasAReader(t *testing.T) {
	dir := bundleDir(t)
	bundle, err := contracts.Load(dir)
	if err != nil {
		t.Fatalf("%v", err)
	}

	for _, name := range bundle.Files {
		r, ok := readers[name]
		if !ok {
			t.Errorf("%s is in bundle %s and has no reader in this package — ticfac is not held to it",
				name, bundle.Version)
			continue
		}
		if r.file == "" {
			t.Errorf("%s: the reader entry names no file", name)
		} else if _, err := os.Stat(r.file); err != nil {
			t.Errorf("%s names reader %s, which is not in this package: %v", name, r.file, err)
		}
		if r.kind == structural && r.turnedExecutableBy == "" {
			t.Errorf("%s: a structural reader must name what will make it executable, or it is a permanent half-check", name)
		}
		if r.kind == executable && r.turnedExecutableBy != "" {
			t.Errorf("%s: an executable reader has nothing left to be turned into", name)
		}
	}

	listed := map[string]bool{}
	for _, name := range bundle.Files {
		listed[name] = true
	}
	for name := range readers {
		if !listed[name] {
			t.Errorf("this package reads %s, which bundle %s does not carry", name, bundle.Version)
		}
	}

	if len(bundle.Files) != 14 {
		t.Errorf("bundle %s carries %d contracts; 3.0.0 carries 14 — if that is intended, "+
			"move the pin deliberately and add the reader in the same commit",
			bundle.Version, len(bundle.Files))
	}
}

// The readers read the vendored copy, and the vendored copy is verified. Said
// here as well as in internal/contracts because a green reader over unverified
// bytes proves nothing about the contract it claims to follow.
func TestTheBundleTheReadersReadIsVerified(t *testing.T) {
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	if err := contracts.VerifyPin(root); err != nil {
		t.Fatalf("%v", err)
	}
	if err := contracts.VerifySchemaIDs(filepath.Join(root, contracts.DirName)); err != nil {
		t.Fatalf("%v", err)
	}
}
