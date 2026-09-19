package sandboxpin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ticfac "github.com/pengelbrecht/ticfac"
	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The negative control, the way internal/contracts/bundle_test.go runs one for
// the contract bundle: a check nothing has ever seen fail is not known to be a
// check. Every test below breaks the vendored image context in a THROWAWAY
// copy and requires the verification to refuse it, naming what broke.
//
// One of these is the acceptance criterion the tick spells out: a test proves
// the guard by changing one copy — TestAnEditedScriptIsRefused changes exactly
// one file of exactly one copy and requires the check to fail.

// realRoot is the repository this test is running in.
func realRoot(t *testing.T) string {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatalf("locate the repository root: %v", err)
	}
	return root
}

// throwaway copies the vendored image context and sandbox.pin.json into a temp
// directory, so
// a test can break the tree without touching the tree it is running in.
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
		mode := 0o644
		if info, err := e.Info(); err == nil && info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(dst, DirName, e.Name()), raw, fs.FileMode(mode)); err != nil {
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

// The real, vendored tree: the positive control the negatives are measured
// against. This test is the offline gate — it runs in `go test ./...`, so it
// runs on every CI run and in the ticks gate, not only in a step someone has
// to remember to add.
func TestTheVendoredSandboxTreeVerifies(t *testing.T) {
	root := realRoot(t)
	if err := VerifyPin(root); err != nil {
		t.Fatalf("%v", err)
	}
}

// The tree this repository ships is the one embedded.go embeds, so the pin
// must cover exactly the bytes that leave in the binary. A file added to
// the tree without being embedded ships nowhere while still passing every
// on-disk check — this is the seam that catches it.
func TestThePinnedTreeIsWhatTheBinaryShips(t *testing.T) {
	root := realRoot(t)
	pin, err := LoadPin(root)
	if err != nil {
		t.Fatal(err)
	}

	embedded := map[string]bool{}
	err = fs.WalkDir(ticfac.SandboxFS(), DirName, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		embedded[strings.TrimPrefix(p, DirName+"/")] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded image context: %v", err)
	}

	for _, name := range pin.Files {
		if !embedded[name] {
			t.Errorf("%s is pinned but not embedded — embedded.go's sandboxFS list does not carry it, so it ships in no binary", name)
		}
	}
	for name := range embedded {
		if _, ok := pin.Digests[name]; !ok {
			t.Errorf("%s is embedded but not pinned — the shipped bytes are not the verified bytes", name)
		}
	}
}

// THE acceptance criterion: change one copy, and the check must fail.
func TestAnEditedScriptIsRefused(t *testing.T) {
	root := throwaway(t)
	path := filepath.Join(root, DirName, "worker.sh")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A change small enough to be any edit anywhere in the file: drift that
	// matters starts exactly this small.
	if err := os.WriteFile(path, append(raw, '\n'), 0o755); err != nil {
		t.Fatal(err)
	}
	refuses(t, root, "one vendored script was edited")
}

func TestAMissingScriptIsRefused(t *testing.T) {
	root := throwaway(t)
	if err := os.Remove(filepath.Join(root, DirName, "preflight.sh")); err != nil {
		t.Fatal(err)
	}
	refuses(t, root, "a pinned script is missing")
}

func TestAnUnpinnedFileIsRefused(t *testing.T) {
	root := throwaway(t)
	if err := os.WriteFile(filepath.Join(root, DirName, "stray.txt"), []byte("nothing pins this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refuses(t, root, "an unpinned file is sitting in the image context/")
}

// git carries the exec bit as part of the tree, and the Docker build context
// is materialized from modes as well as bytes — so a flipped bit is drift too.
func TestAFlippedExecBitIsRefused(t *testing.T) {
	root := throwaway(t)
	path := filepath.Join(root, DirName, "Dockerfile")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	refuses(t, root, "a pinned file's exec bit was flipped")
}

func TestAShortRefIsRefused(t *testing.T) {
	root := throwaway(t)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)
	pin["ref"] = "e1146e6d"
	writeJSON(t, filepath.Join(root, PinFile), pin)
	refuses(t, root, "the pin names a short sha")
}

func TestAnUnsortedFileListIsRefused(t *testing.T) {
	root := throwaway(t)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)
	files := pin["files"].([]any)
	if len(files) < 3 {
		t.Fatal("the pin's file list is too short for this test to shuffle")
	}
	shuffled := append([]any{files[1], files[0]}, files[2:]...)
	pin["files"] = shuffled
	writeJSON(t, filepath.Join(root, PinFile), pin)
	refuses(t, root, "the pin's file list is not sorted")
}

func TestATamperedDigestIsRefused(t *testing.T) {
	root := throwaway(t)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)
	digests := pin["digests"].(map[string]any)
	digests["worker.sh"] = strings.Repeat("0", 64)
	writeJSON(t, filepath.Join(root, PinFile), pin)
	refuses(t, root, "a pin digest was replaced")
}

func TestAMissingModeIsRefused(t *testing.T) {
	root := throwaway(t)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)
	modes := pin["modes"].(map[string]any)
	delete(modes, "worker.sh")
	writeJSON(t, filepath.Join(root, PinFile), pin)
	refuses(t, root, "a pinned file has no recorded mode")
}

func TestAnUnknownModeIsRefused(t *testing.T) {
	root := throwaway(t)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)
	pin["modes"].(map[string]any)["worker.sh"] = "0777"
	writeJSON(t, filepath.Join(root, PinFile), pin)
	refuses(t, root, "a pin mode git cannot record")
}

func TestANonPinnedModeIsRefused(t *testing.T) {
	root := throwaway(t)
	var pin map[string]any
	readJSON(t, filepath.Join(root, PinFile), &pin)
	pin["mode"] = "workspace"
	writeJSON(t, filepath.Join(root, PinFile), pin)
	refuses(t, root, "a mode this repository does not have")
}

// ---- Extract: the upstream fetch, shown to take the right directory ----

// tarFile is one entry in a fake GitHub tarball.
type tarFile struct {
	body string
	exec bool
}

// tarball builds a GitHub-shaped archive: everything under one root directory
// named <repo>-<sha>.
func tarball(t *testing.T, files map[string]tarFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, f := range files {
		mode := int64(0o644)
		if f.exec {
			mode = 0o755
		}
		header := &tar.Header{
			Name:     "ticks-e1146e6d/" + name,
			Mode:     mode,
			Size:     int64(len(f.body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
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

func TestExtractTakesOnlyTheSandboxDirectory(t *testing.T) {
	archive := tarball(t, map[string]tarFile{
		"cloud/sandbox/worker.sh":        {body: "#!/bin/sh\n", exec: true},
		"cloud/sandbox/Dockerfile":       {body: "FROM scratch\n"},
		"cloud/factory/src/index.ts":     {body: "export {}\n"},
		"README.md":                      {body: "# ticks\n"},
		"internal/sandbox/worker.sh":     {body: "a decoy one level down\n", exec: true},
		"cloud/sandbox/nested/deeper.sh": {body: "not part of the flat build context\n"},
	})

	files, err := Extract(bytes.NewReader(archive), "cloud/sandbox")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(files) != 2 {
		t.Fatalf("extracted %d files, want 2: %v", len(files), namesOf(files))
	}
	w, ok := files["worker.sh"]
	if !ok || string(w.Body) != "#!/bin/sh\n" {
		t.Errorf("worker.sh was not extracted byte-for-byte: %+v", w)
	}
	if ok && !w.Executable {
		t.Error("worker.sh lost its exec bit, which git carries as part of the tree")
	}
	d, ok := files["Dockerfile"]
	if !ok || d.Executable {
		t.Errorf("Dockerfile was extracted wrong: %+v", d)
	}
}

func TestExtractRefusesARefWithNoSandboxDirectory(t *testing.T) {
	archive := tarball(t, map[string]tarFile{"README.md": {body: "# ticks\n"}})
	_, err := Extract(bytes.NewReader(archive), "cloud/sandbox")
	if err == nil || !strings.Contains(err.Error(), "no cloud/sandbox") {
		t.Errorf("a ref carrying no image context must be refused, got %v", err)
	}
}

func TestExtractRefusesSomethingThatIsNotAnArchive(t *testing.T) {
	_, err := Extract(strings.NewReader("<html>404</html>"), "cloud/sandbox")
	if err == nil {
		t.Error("a non-gzip body was accepted as an archive")
	}
}

func namesOf(files map[string]UpstreamFile) []string {
	var names []string
	for name := range files {
		names = append(names, name)
	}
	return names
}

func infoOf(t *testing.T, e os.DirEntry) os.FileInfo {
	t.Helper()
	info, err := e.Info()
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// ---- Diff: what CI's verify-upstream asserts, shown to be able to say no ----

// upstreamOf re-reads a throwaway's vendored tree as if it were the upstream
// fetch, so a test can compare an edited copy against its own origin.
func upstreamOf(t *testing.T, root string) map[string]UpstreamFile {
	t.Helper()
	upstream := map[string]UpstreamFile{}
	entries, err := os.ReadDir(filepath.Join(root, DirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, DirName, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		upstream[e.Name()] = UpstreamFile{
			Body:       raw,
			Executable: infoOf(t, e).Mode()&0o111 != 0,
		}
	}
	return upstream
}

func TestDiffSeesAnUntouchedCopyAsIdentical(t *testing.T) {
	root := throwaway(t)
	problems, err := Diff(root, upstreamOf(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("an untouched copy differed from itself: %v", problems)
	}
}

func TestDiffSeesAVendoredEdit(t *testing.T) {
	root := throwaway(t)
	upstream := upstreamOf(t, root)

	path := filepath.Join(root, DirName, "worker.sh")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, ' '), 0o755); err != nil {
		t.Fatal(err)
	}

	problems, err := Diff(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "worker.sh") {
		t.Fatalf("a vendored edit must produce exactly one named problem, got %v", problems)
	}
}

func TestDiffSeesAModeChange(t *testing.T) {
	root := throwaway(t)
	upstream := upstreamOf(t, root)

	if err := os.Chmod(filepath.Join(root, DirName, "Dockerfile"), 0o755); err != nil {
		t.Fatal(err)
	}

	problems, err := Diff(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "Dockerfile") {
		t.Fatalf("a mode change must produce exactly one named problem, got %v", problems)
	}
}

func TestDiffSeesAMissingAndAnExtraFile(t *testing.T) {
	root := throwaway(t)
	upstream := upstreamOf(t, root)
	delete(upstream, "preflight.sh")
	upstream["invented.sh"] = UpstreamFile{Body: []byte("#!/bin/sh\n"), Executable: true}

	problems, err := Diff(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 {
		t.Fatalf("a dropped and an added file must produce two problems, got %v", problems)
	}
	var sawMissing, sawExtra bool
	for _, p := range problems {
		sawMissing = sawMissing || strings.Contains(p, "absent upstream")
		sawExtra = sawExtra || strings.Contains(p, "not vendored here")
	}
	if !sawMissing || !sawExtra {
		t.Fatalf("the two problems must be named in both directions, got %v", problems)
	}
}

// ---- Write: what sync does, shown to adopt bytes and modes together ----

func TestWriteAdoptsUpstreamBytesAndModes(t *testing.T) {
	root := throwaway(t)
	upstream := upstreamOf(t, root)

	// A changed script, a flipped bit, and a dropped file: one adoption, and
	// the pin must move with all three or the next check fails.
	edited := append(append([]byte(nil), upstream["worker.sh"].Body...), '\n')
	upstream["worker.sh"] = UpstreamFile{Body: edited, Executable: true}
	dockerfile := upstream["Dockerfile"]
	upstream["Dockerfile"] = UpstreamFile{Body: dockerfile.Body, Executable: !dockerfile.Executable}
	delete(upstream, "preflight.sh")

	if err := Write(root, upstream); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// The pin was rewritten to describe the new bytes, so the check passes —
	// that is what makes sync a person's act rather than a hole: the diff of
	// the pin is what a reviewer reads.
	if err := VerifyPin(root); err != nil {
		t.Fatalf("the pin does not describe the adopted bytes: %v", err)
	}
	have, err := os.ReadFile(filepath.Join(root, DirName, "worker.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(have) != string(edited) {
		t.Error("the adopted bytes were not written")
	}
	if _, err := os.Stat(filepath.Join(root, DirName, "preflight.sh")); !os.IsNotExist(err) {
		t.Error("a file upstream dropped was left vendored")
	}
	info, err := os.Stat(filepath.Join(root, DirName, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("an adopted mode change was not applied")
	}
}

// Write must never touch anything but the vendored tree and the pin: a bug
// that wrote outside them would be a guard that damages what it guards.
func TestWriteRefusesARootWithNoPin(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(root, map[string]UpstreamFile{}); err == nil {
		t.Error("a sync into a root with no pin must refuse, not invent one")
	}
}
