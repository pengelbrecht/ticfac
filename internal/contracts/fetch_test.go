package contracts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarball builds a GitHub-shaped archive: everything under one root directory
// named <repo>-<sha>.
func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		header := &tar.Header{
			Name:     "ticks-5d14bcb/" + name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBundleTakesOnlyTheBundleDirectory(t *testing.T) {
	archive := tarball(t, map[string]string{
		"contracts/bundle.json":          `{"version":"3.0.0"}`,
		"contracts/tracker-layout.json":  `{}`,
		"contracts/CHANGELOG.md":         "## 3.0.0\n",
		"internal/contracts/bundle.go":   "package contracts\n",
		"internal/contracts/bundle_test": "not a contract",
		"contracts/nested/deeper.json":   `{}`,
		"README.md":                      "# ticks\n",
	})

	files, err := ExtractBundle(bytes.NewReader(archive), "contracts")
	if err != nil {
		t.Fatalf("%v", err)
	}
	want := []string{"CHANGELOG.md", "bundle.json", "tracker-layout.json"}
	if len(files) != len(want) {
		t.Fatalf("extracted %d files, want %d: %v", len(files), len(want), keysOf(files))
	}
	for _, name := range want {
		if _, ok := files[name]; !ok {
			t.Errorf("%s was not extracted", name)
		}
	}
	// The point of anchoring on the archive root: a Go package that happens to
	// be called internal/contracts is not the bundle.
	if _, ok := files["bundle.go"]; ok {
		t.Error("internal/contracts/ was mistaken for the bundle directory")
	}
}

func TestExtractBundleRefusesARefWithNoBundle(t *testing.T) {
	archive := tarball(t, map[string]string{"README.md": "# ticks\n"})
	_, err := ExtractBundle(bytes.NewReader(archive), "contracts")
	if err == nil || !strings.Contains(err.Error(), "no contracts/ directory") {
		t.Errorf("a ref carrying no bundle must be refused, got %v", err)
	}
}

func TestExtractBundleRefusesSomethingThatIsNotAnArchive(t *testing.T) {
	_, err := ExtractBundle(strings.NewReader("<html>404</html>"), "contracts")
	if err == nil {
		t.Error("a non-gzip body was accepted as an archive")
	}
}

// Diff is what the CI contracts job asserts: the vendored bytes ARE what ticks
// published at the pinned ref, under the ticks bundle version the pin names.
// The upstream fixture here is ticks' 7.0.0-shaped fetch: ticks' own manifest
// plus the two files it lists. Here Diff is shown to be able to say no.
func upstreamAtPin(t *testing.T, root string) map[string][]byte {
	t.Helper()
	pin, err := LoadPin(root)
	if err != nil {
		t.Fatal(err)
	}
	upstream := map[string][]byte{
		BundleFile: []byte(`{"version":"` + pin.BundleVersion + `","files":["tk-json-manifest.json","tracker-layout.json"],"digests":{"tk-json-manifest.json":"` + pin.Digests["tk-json-manifest.json"] + `","tracker-layout.json":"` + pin.Digests["tracker-layout.json"] + `"}}`),
	}
	for _, name := range pin.Files {
		raw, err := os.ReadFile(filepath.Join(root, DirName, name))
		if err != nil {
			t.Fatal(err)
		}
		upstream[name] = raw
	}
	return upstream
}

func TestDiffSeesAVendoredEdit(t *testing.T) {
	root := throwaway(t)
	upstream := upstreamAtPin(t, root)

	problems, err := Diff(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("an untouched copy differed from its own upstream: %v", problems)
	}

	// An edit to a ticks-owned file is the one thing both halves of the
	// mechanism must catch: the pin's digest offline, Diff online.
	path := filepath.Join(root, DirName, "tk-json-manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, ' '), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, err = Diff(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "tk-json-manifest.json") {
		t.Errorf("Diff did not name the edited ticks-owned file: %v", problems)
	}

	// An edit to a TICFAC-owned contract is none of Diff's business: ticks no
	// longer ships it, and this repository is its authority now.
	local := filepath.Join(root, DirName, "message-context.json")
	raw, err = os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, append(raw, ' '), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, err = Diff(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 { // still only the ticks-owned edit from before
		t.Errorf("Diff reported %d problems over a ticfac-owned edit it must not compare upstream: %v", len(problems), problems)
	}
}

// The pin's digests are a recorded claim about ticks' bundle; ticks' manifest
// at the ref is the authority. A pin whose digests are not what ticks
// published for the version it names is refused — this is the check that
// binds `bundleVersion` to bytes, the one the offline gate cannot make.
func TestDiffBindsThePinsDigestsToTicksManifest(t *testing.T) {
	root := throwaway(t)
	upstream := upstreamAtPin(t, root)

	// A digest disagreement between the pin and ticks' manifest.
	var manifest map[string]any
	if err := json.Unmarshal(upstream[BundleFile], &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["digests"].(map[string]any)["tk-json-manifest.json"] = strings.Repeat("0", 64)
	rebuilt, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	upstream[BundleFile] = rebuilt
	problems, err := Diff(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "disagree") {
		t.Errorf("Diff did not refuse a pin whose digests are not what ticks published: %v", problems)
	}

	// A version disagreement between the pin and ticks' manifest.
	upstream = upstreamAtPin(t, root)
	upstream[BundleFile] = bytes.Replace(upstream[BundleFile], []byte(pinVersion(t, root)), []byte("9.9.9"), 1)
	problems, err = Diff(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, " "), "pins ") {
		t.Errorf("Diff did not refuse a pin whose version the ref does not publish: %v", problems)
	}

	// A fetch without ticks' manifest cannot bind the version at all.
	upstream = upstreamAtPin(t, root)
	delete(upstream, BundleFile)
	if _, err := Diff(root, upstream); err == nil {
		t.Error("a fetch without ticks' bundle.json was accepted; the version would bind to nothing")
	}
}

func pinVersion(t *testing.T, root string) string {
	t.Helper()
	pin, err := LoadPin(root)
	if err != nil {
		t.Fatal(err)
	}
	return pin.BundleVersion
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
