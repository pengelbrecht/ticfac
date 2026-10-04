package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// The harness package is the third payload (epic 43y, tick xd3): the factory
// bundle links it for its WorkerAgent, so a deploy that staged the bundle
// without it would fail to bundle — or worse, bundle a harness from some
// other disk. These tests hold the payload, its staging and its link.

func requireHarnessPayload(t *testing.T) {
	t.Helper()
	requireEmbeddedPayload(t)
	if harnessFS == nil {
		t.Skip("the embedded harness package is not wired into this test binary")
	}
}

func TestHarnessPayloadShipsWhatTheInstallAndTheBundlerRead(t *testing.T) {
	requireHarnessPayload(t)
	have := map[string]bool{}
	for _, p := range HarnessPaths() {
		have[p] = true
		if strings.HasPrefix(p, "node_modules/") || strings.HasPrefix(p, "test/") {
			t.Errorf("the harness payload ships %s, which is not what a deploy installs or bundles", p)
		}
	}
	for _, want := range []string{
		"package.json",
		"pnpm-lock.yaml",
		"pnpm-workspace.yaml",
		"src/index.ts",
		"src/host/worker-attempt.ts",
		"src/host/cloud.ts",
	} {
		if !have[want] {
			t.Errorf("the embedded harness package is missing %s", want)
		}
	}
}

// The bundle's dependency on the harness is a link to the staged position:
// the committed package.json and the staging constant say the same thing.
func TestTheBundleLinksTheHarnessWhereTheDeployStagesIt(t *testing.T) {
	requireHarnessPayload(t)
	data, err := ReadBundleFile("package.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("cloudflare/package.json: %v", err)
	}
	if got, want := manifest.Dependencies["ticfac-harness"], "link:"+harnessRelativeToBundle; got != want {
		t.Errorf("cloudflare/package.json depends on ticfac-harness as %q, but the deploy stages the harness at %q (want %q)",
			got, harnessRelativeToBundle, want)
	}
	bundle := filepath.Join(t.TempDir(), "cloudflare")
	if got, want := HarnessDir(bundle), filepath.Join(filepath.Dir(bundle), "harness"); got != want {
		t.Errorf("HarnessDir = %s, want %s", got, want)
	}
}

func TestMaterializeHarnessStagesAndPrunes(t *testing.T) {
	fake := fstest.MapFS{
		"harness/package.json":   &fstest.MapFile{Data: []byte(`{"name":"ticfac-harness"}`)},
		"harness/src/index.ts":   &fstest.MapFile{Data: []byte("export {};")},
		"harness/src/host/a.ts":  &fstest.MapFile{Data: []byte("export const a = 1;")},
		"harness/pnpm-lock.yaml": &fstest.MapFile{Data: []byte("lockfileVersion: '9.0'\n")},
	}
	previous := harnessFS
	harnessFS = fake
	resetPayloadCaches()
	t.Cleanup(func() {
		harnessFS = previous
		wireEmbeddedPayload()
	})

	dir := filepath.Join(t.TempDir(), "harness")
	if err := os.MkdirAll(filepath.Join(dir, "src", "stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "stale", "old.ts"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "x", "index.js"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeHarness(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"package.json", "src/index.ts", "src/host/a.ts", "pnpm-lock.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s was not staged: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "stale", "old.ts")); !os.IsNotExist(err) {
		t.Errorf("a file the payload no longer ships survived the staging: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "x", "index.js")); err != nil {
		t.Errorf("the install's node_modules was pruned: %v", err)
	}
}

// The deployed Worker bundles the harness, so a harness change is a change of
// what is deployed: the bundle SHA must move with it.
func TestBundleSHACoversTheHarness(t *testing.T) {
	stageFakePayload(t)
	one := fstest.MapFS{"harness/src/index.ts": &fstest.MapFile{Data: []byte("export const v = 1;")}}
	two := fstest.MapFS{"harness/src/index.ts": &fstest.MapFile{Data: []byte("export const v = 2;")}}
	previous := harnessFS
	t.Cleanup(func() {
		harnessFS = previous
		wireEmbeddedPayload()
	})
	harnessFS = one
	resetPayloadCaches()
	first := BundleSHA()
	harnessFS = two
	resetPayloadCaches()
	if second := BundleSHA(); second == first {
		t.Error("the bundle SHA did not change when the harness package did")
	}
}
