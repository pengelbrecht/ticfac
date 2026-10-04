package sandboximage

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac"
)

// The tree the tests here run is the tree the binary ships: embedded.go lists
// image/'s files one by one (the build context is exactly the Dockerfile and
// what it copies), so a file added to image/ without being added there passes
// every test in this package and ships in no binary. This is the seam that
// catches it — carried over from internal/sandboxpin's
// TestThePinnedTreeIsWhatTheBinaryShips when ticfac took ownership of image/
// (tick r6w) and the pin it compared against went away.
//
// The walk is recursive: image/pi/ holds the lockfile the image installs pi
// from (tick jd3), and a file under a subdirectory ships only if embedded.go
// names it too. node_modules is skipped — a local `pnpm install` there is a
// developer's scratch, not part of the build context.
//
// short: walks the embedded FS and one directory tree; no process runs
func TestTheImageTreeIsWhatTheBinaryShips(t *testing.T) {
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string][]byte{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		onDisk[filepath.ToSlash(rel)] = body
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	embedded := map[string][]byte{}
	err = fs.WalkDir(ticfac.SandboxFS(), "image", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(ticfac.SandboxFS(), p)
		if err != nil {
			return err
		}
		embedded[strings.TrimPrefix(p, "image/")] = body
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded image context: %v", err)
	}

	var names []string
	for name := range onDisk {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body, ok := embedded[name]
		if !ok {
			t.Errorf("image/%s is in the tree but not embedded — add it to embedded.go's sandboxFS list or it ships in no binary", name)
			continue
		}
		if string(body) != string(onDisk[name]) {
			t.Errorf("image/%s differs between the tree and the embedded copy", name)
		}
	}
	for name := range embedded {
		if _, ok := onDisk[name]; !ok {
			t.Errorf("image/%s is embedded but not in the tree", name)
		}
	}
	if len(embedded) == 0 {
		t.Fatal("the embedded image context is empty: a guard with nothing to compare guards nothing")
	}
}

// The scripts the Dockerfile installs as commands must be executable in git,
// because this package's tests run them as commands and a checkout is what
// they run. (The image itself chmods them in the Dockerfile; this is about the
// tree being runnable where it is tested.)
//
// short: stats four files; no process runs
func TestTheImageScriptsAreExecutable(t *testing.T) {
	for _, name := range []string{EntrypointScript, WorkerScript, PreflightScript, "build.sh"} {
		p, err := Path(name)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("image/%s is not executable (mode %v)", name, info.Mode().Perm())
		}
	}
}
