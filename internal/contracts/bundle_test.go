package contracts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The negative control. contracts/README.md's rule is that "a copied JSON file
// without an executable check is not a contract" — and a check nothing has
// ever seen fail is not known to be a check. Every test below breaks the
// vendored bundle in a THROWAWAY copy and requires the verification to refuse
// it, naming what broke.
//
// This is also the acceptance criterion the tick spells out: a deliberately
// broken vendored fixture fails the build. It fails here, in `go test -short
// ./...`, so it fails on every CI run rather than in a step someone has to
// remember to add.

// realRoot is the repository this test is running in.
func realRoot(t *testing.T) string {
	t.Helper()
	root, err := RepoRoot()
	if err != nil {
		t.Fatalf("locate the repository root: %v", err)
	}
	return root
}

// throwaway copies contracts/ and contracts.pin.json into a temp directory, so
// a test can break the bundle without touching the tree it is running in.
func throwaway(t *testing.T) string {
	t.Helper()
	src := realRoot(t)
	dst := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dst, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(src, DirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(src, DirName, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, DirName, e.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(src, PinFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, PinFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := VerifyPin(dst); err != nil {
		t.Fatalf("the throwaway copy does not verify before it is broken: %v", err)
	}
	return dst
}

func refuses(t *testing.T, root, because string) {
	t.Helper()
	err := VerifyPin(root)
	if err == nil {
		t.Fatalf("the check passed with %s — it is not a check", because)
	}
	t.Logf("refused (%s): %s", because, firstLine(err.Error()))
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The real, vendored bundle: the positive control the negatives are measured
// against.
func TestTheVendoredBundleVerifies(t *testing.T) {
	root := realRoot(t)
	if err := VerifyPin(root); err != nil {
		t.Fatalf("%v", err)
	}
	if err := VerifySchemaIDs(filepath.Join(root, DirName)); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestAnEditedFixtureIsRefused(t *testing.T) {
	root := throwaway(t)
	path := filepath.Join(root, DirName, "worker-boot-contract.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), `"branch_prefix"`, `"branch_prefix_renamed"`, 1)
	if edited == string(raw) {
		t.Fatal("the edit changed nothing; the fixture no longer has the key this test edits")
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	refuses(t, root, "a vendored fixture was edited")
}

func TestAnUnlistedFixtureIsRefused(t *testing.T) {
	root := throwaway(t)
	path := filepath.Join(root, DirName, "invented-contract.json")
	if err := os.WriteFile(path, []byte(`{"why": "nothing versions this"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	refuses(t, root, "an unlisted fixture is sitting in contracts/")
}

func TestAMissingFixtureIsRefused(t *testing.T) {
	root := throwaway(t)
	if err := os.Remove(filepath.Join(root, DirName, "message-context.json")); err != nil {
		t.Fatal(err)
	}
	refuses(t, root, "a listed fixture is missing")
}

// An edit to a ticks-owned contract is caught TWICE offline: by ticfac's
// bundle digest and by the pin's own digest. This is the replacement for the
// stale-bundle-version refusal of the vendored era: `bundleVersion` now names
// ticks' bundle, and binding it to bytes is verify-upstream's job online (see
// fetch_test.go) — but the BYTES the pin records are checked here, offline,
// every test run.
func TestAnEditedTicksOwnedFixtureIsRefused(t *testing.T) {
	root := throwaway(t)
	path := filepath.Join(root, DirName, "tk-json-manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, []byte("\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	refuses(t, root, "a ticks-owned contract was edited here")
}

func TestAnAbsentBundleVersionIsRefused(t *testing.T) {
	root := throwaway(t)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)
	delete(pin, "bundleVersion")
	writeJSON(t, filepath.Join(root, PinFile), pin)
	refuses(t, root, "the pin names no bundle version")
}

func TestAVersionWithNoChangelogEntryIsRefused(t *testing.T) {
	root := throwaway(t)
	path := filepath.Join(root, DirName, ChangelogFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The CURRENT version's entry, read from the manifest, so a re-cut does
	// not leave this test removing a heading the check no longer looks for.
	var manifest struct {
		Version string `json:"version"`
	}
	readJSON(t, filepath.Join(root, DirName, BundleFile), &manifest)
	heading := "\n## " + manifest.Version + "\n"
	stripped := strings.Replace(string(raw), heading, "\n## "+manifest.Version+"-renamed\n", 1)
	if stripped == string(raw) {
		t.Fatalf("the changelog no longer carries a `## %s` heading for this test to remove", manifest.Version)
	}
	if err := os.WriteFile(path, []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}
	// ticfac authors this changelog, so an ordinary edit is legitimate
	// authoring and must NOT be a refusal — the one edit that is a refusal is
	// the one that makes the current version unsayable: removing its entry.
	refuses(t, root, "the bundle version has no changelog entry")
}

func TestADisagreeingFileListIsRefused(t *testing.T) {
	root := throwaway(t)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)
	files := pin["files"].([]any)
	pin["files"] = files[:len(files)-1]
	writeJSON(t, filepath.Join(root, PinFile), pin)
	refuses(t, root, "a bundled contract is not pinned")
}

// The dishonest re-cut: edit a fixture, regenerate the manifest's per-file
// digests, and leave `version` alone. Every per-file digest agrees again, so
// nothing but the append-only ledger can see it — and a consumer pinned to the
// version by exact value is exactly who cannot see it.
func TestADishonestReCutIsRefused(t *testing.T) {
	root := throwaway(t)
	dir := filepath.Join(root, DirName)

	path := filepath.Join(dir, "tracker-layout.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), `"epic_type": "epic"`, `"epic_type": "EPIC"`, 1)
	if edited == string(raw) {
		t.Fatal("tracker-layout.json no longer carries the key this test edits")
	}
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	recutManifest(t, dir)
	recutPin(t, root)

	// Every per-file digest now agrees, and the pin agrees with the files.
	// Only version_digests still remembers what 3.0.0 was cut with.
	if err := Verify(dir); err == nil {
		t.Fatal("a re-cut at an unchanged version passed Verify — the ledger is not being checked")
	}
	refuses(t, root, "the manifest was re-cut without bumping the version")
}

func TestADeletedLedgerEntryIsRefused(t *testing.T) {
	root := throwaway(t)
	dir := filepath.Join(root, DirName)

	var manifest map[string]any
	readJSON(t, filepath.Join(dir, BundleFile), &manifest)
	// The entry that must exist is the CURRENT version's — deleting an older
	// version's entry is history-tampering, but the check on the pin path is
	// that the version on disk still has its own ledger record.
	ledger := manifest["version_digests"].(map[string]any)
	delete(ledger, fmt.Sprint(manifest["version"]))
	writeJSON(t, filepath.Join(dir, BundleFile), manifest)
	recutPin(t, root)

	refuses(t, root, "the version's ledger entry was deleted")
}

func TestADeletedPinIsRefused(t *testing.T) {
	root := throwaway(t)
	if err := os.Remove(filepath.Join(root, PinFile)); err != nil {
		t.Fatal(err)
	}
	refuses(t, root, "contracts.pin.json was deleted")
}

func TestAnEmptyContractsDirectoryIsRefused(t *testing.T) {
	root := throwaway(t)
	dir := filepath.Join(root, DirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	refuses(t, root, "contracts/ is empty")
}

func TestAMovingRefIsRefused(t *testing.T) {
	root := throwaway(t)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)
	pin["ref"] = "main"
	writeJSON(t, filepath.Join(root, PinFile), pin)
	refuses(t, root, "the pin names a branch rather than an immutable sha")
}

func TestWorkspaceModeIsRefusedInThisRepository(t *testing.T) {
	root := throwaway(t)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)
	pin["mode"] = "workspace"
	writeJSON(t, filepath.Join(root, PinFile), pin)
	refuses(t, root, "there is no ticks checkout here for workspace mode to mean")
}

// Two contracts describing one record, with neither defining it, is the drift
// bundle 2.0.0 was cut for. The rule is asserted against the real bundle in
// TestTheVendoredBundleVerifies; here it is shown to be able to fail.
func TestTwoDefinitionsOfOneSchemaIDAreRefused(t *testing.T) {
	root := throwaway(t)
	dir := filepath.Join(root, DirName)

	path := filepath.Join(dir, "ticfac-run-state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	// Give the run-state contract its own definition of the evidence record —
	// the exact shape 1.2.0 shipped, under the id the bundle publishes today
	// (v2 since 4.0.0: provenance gained tier and the closed record moved).
	document["evidence_envelope"] = map[string]any{
		"schema_id": "ticfac.evidence.v3",
		"schema":    map[string]any{"type": "object"},
	}
	writeJSON(t, path, document)

	if err := VerifySchemaIDs(dir); err == nil {
		t.Fatal("two definitions of ticfac.evidence.v3 passed VerifySchemaIDs")
	} else {
		t.Logf("refused: %s", firstLine(err.Error()))
	}
}

// recutManifest regenerates bundle.json's per-file digests WITHOUT touching
// `version` or `version_digests` — the dishonest half of a bundle bump.
func recutManifest(t *testing.T, dir string) {
	t.Helper()
	var manifest map[string]any
	readJSON(t, filepath.Join(dir, BundleFile), &manifest)

	names, err := FilesOnDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	digests := map[string]any{}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		digests[name] = FileDigest(raw)
	}
	sort.Strings(names)
	files := make([]any, 0, len(names))
	for _, name := range names {
		files = append(files, name)
	}
	manifest["files"] = files
	manifest["digests"] = digests
	writeJSON(t, filepath.Join(dir, BundleFile), manifest)
}

// recutPin regenerates the pin's digests for the pinned ticks files from
// whatever is on disk, so that a test exercising ticfac's manifest ledger is
// not merely failing the pin. The pin's files never change: ticfac's bundle
// grew around them, and the pin's list is the ownership statement itself.
func recutPin(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, DirName)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)

	digests := map[string]any{}
	for _, name := range filesOf(pin) {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		digests[name] = FileDigest(raw)
	}
	pin["digests"] = digests
	writeJSON(t, filepath.Join(root, PinFile), pin)
}

func filesOf(pin map[string]any) []string {
	list, _ := pin["files"].([]any)
	out := make([]string, 0, len(list))
	for _, name := range list {
		out = append(out, name.(string))
	}
	return out
}
