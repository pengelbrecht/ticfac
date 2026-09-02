package contracts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
// published at the pinned ref. Here it is shown to be able to say no.
func TestDiffSeesAVendoredEdit(t *testing.T) {
	root := throwaway(t)

	upstream := map[string][]byte{}
	entries, err := os.ReadDir(filepath.Join(root, DirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(root, DirName, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		upstream[e.Name()] = raw
	}

	problems, err := Diff(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("an untouched copy differed from itself: %v", problems)
	}

	path := filepath.Join(root, DirName, "message-context.json")
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
	if len(problems) != 1 || !strings.Contains(problems[0], "message-context.json") {
		t.Errorf("Diff did not name the edited file: %v", problems)
	}

	delete(upstream, "message-context.json")
	problems, err = Diff(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "absent upstream") {
		t.Errorf("Diff did not report a file vendored here and absent upstream: %v", problems)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
