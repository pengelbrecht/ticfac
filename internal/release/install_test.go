package release

// The tick's acceptance is "a machine that has never built ticfac installs
// it with one command and runs 'ticfac doctor' successfully" — and this test
// is that machine, built small: install.sh runs against a fake forge that
// reproduces the real release shapes (the /releases/latest redirect, the
// tag page it lands on, the download URL whose name goreleaser's
// name_template dictates), serving an archive packed the way the release
// packs it: both binaries at the root of one tar.gz. The binaries are real —
// built by the same `go build` goreleaser runs for this platform — so what
// the assertions see is what a fresh machine gets.
//
// It is end-to-end and it stays in the gate: a broken install.sh is a broken
// release discovered by the person who tried to install one, which is the
// most expensive and silent place this repo has to discover anything.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The fake release the fake forge serves: an obviously synthetic version, so
// a log line that says 9.9.9-installtest is this test talking and never a
// real download.
const (
	fakeVersion = "9.9.9-installtest"
	fakeTag     = "v" + fakeVersion
)

// installHarness is the fake forge. Everything it serves is derived from the
// distribution files themselves — the repository slug from the config's
// release block, the archive name from the config's name_template — so the
// script and the config must agree or the install this harness watches fails
// exactly the way it would on a real machine.
type installHarness struct {
	t          *testing.T
	repoRoot   string
	script     string
	slug       string
	baseURL    string
	archive    string
	installDir string
}

// newInstallHarness builds the distribution pair for this machine, packs the
// release archive, and serves it from a fake forge. It is the harness
// constructor internal/shorttest's guard knows this package by.
func newInstallHarness(t *testing.T) *installHarness {
	t.Helper()
	shorttest.EndToEnd(t)

	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatalf("find the repository root: %v", err)
	}
	h := &installHarness{
		t:        t,
		repoRoot: root,
		script:   filepath.Join(root, "install.sh"),
		slug:     releaseSlug(t, root),
	}

	// Both binaries, built the way goreleaser builds them for this
	// platform. Ten minutes is a ceiling, not a guess: a warm build cache
	// answers in seconds and a cold one has never been seen anywhere near
	// this.
	binDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, main := range []string{"ticfac", "ticfac-exec-subprocess"} {
		out := filepath.Join(binDir, main)
		build := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/"+main)
		build.Dir = root
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build ./cmd/%s (the same build goreleaser runs): %v\n%s", main, err, out)
		}
	}

	// The archive, packed the way the release packs it: both binaries at the
	// root of one tar.gz whose name is the config's name_template rendered
	// with real values. If the template and install.sh disagree, the
	// download the script asks for does not exist and the install below
	// fails — the drift is not hypothetical, it is what happens.
	h.archive = releaseArchiveName(t, root, fakeVersion, runtime.GOOS, runtime.GOARCH)
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for _, main := range []string{"ticfac", "ticfac-exec-subprocess"} {
		raw, err := os.ReadFile(filepath.Join(binDir, main))
		if err != nil {
			t.Fatalf("read the built %s back: %v", main, err)
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: main, Mode: 0o755, Size: int64(len(raw)), Typeflag: tar.TypeReg,
			ModTime: time.Now(),
		}); err != nil {
			t.Fatalf("write the archive header for %s: %v", main, err)
		}
		if _, err := tw.Write(raw); err != nil {
			t.Fatalf("write %s into the archive: %v", main, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close the archive: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close the archive's gzip: %v", err)
	}
	archive := buf.Bytes()

	// The fake forge: the release shapes install.sh talks to, and nothing
	// else. The /releases/latest redirect is what the version resolution
	// follows (the same redirect ticks' installer resolves against real
	// GitHub); the download URL is where the archive lands.
	mux := http.NewServeMux()
	tagPath := "/" + h.slug + "/releases/tag/" + fakeTag
	mux.HandleFunc("/"+h.slug+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+h.slug+"/releases/tag/"+fakeTag, http.StatusFound)
	})
	mux.HandleFunc(tagPath, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "the tag page curl's HEAD lands on")
	})
	mux.HandleFunc("/"+h.slug+"/releases/download/"+fakeTag+"/"+h.archive, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(archive)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	h.baseURL = server.URL

	// The install directory the parent test installs into: a t.TempDir of
	// the parent's, so it outlives the subtests that inspect it.
	h.installDir = filepath.Join(t.TempDir(), "bin")
	return h
}

// runScript runs install.sh once, with the environment the script's contract
// names: INSTALL_DIR and TICFAC_RELEASE_BASE_URL over the machine's own.
func (h *installHarness) runScript(t *testing.T, installDir, baseURL string, overrides ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("sh", h.script)
	cmd.Env = childEnv(append([]string{
		"INSTALL_DIR=" + installDir,
		"TICFAC_RELEASE_BASE_URL=" + baseURL,
	}, overrides...))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run install.sh: %v", err)
	}
	return code, out.String()
}

// runInstalled runs an installed binary with arguments and answers its exit
// code and combined output.
func runInstalled(t *testing.T, bin string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = childEnv(nil)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s %s: %v", bin, strings.Join(args, " "), err)
	}
	return code, out.String()
}

// childEnv builds a child environment from this process's, with every
// override winning over any same-named entry (a duplicate would be answered
// by the first, silently ignoring the override — the exact trap a PATH
// override for a uname shim falls into).
func childEnv(overrides []string) []string {
	seen := make(map[string]string)
	for _, kv := range append(os.Environ(), overrides...) {
		key, value, _ := strings.Cut(kv, "=")
		seen[key] = value
	}
	env := make([]string, 0, len(seen))
	for key, value := range seen {
		env = append(env, key+"="+value)
	}
	return env
}

// releaseSlug reads owner/name out of the config's release block — the
// repository the fake forge serves, so a moved repository moves the test
// with it.
func releaseSlug(t *testing.T, root string) string {
	t.Helper()
	src := readDistFile(t, ".goreleaser.yaml")
	owner, ok := yamlScalar(src, "owner")
	if !ok {
		t.Fatal(".goreleaser.yaml's release block does not carry `owner:`")
	}
	name, ok := yamlScalar(src, "name")
	if !ok {
		t.Fatal(".goreleaser.yaml's release block does not carry `name:`")
	}
	return owner + "/" + name
}

// releaseArchiveName renders the config's archive name_template with real
// values, goreleaser's fields one for one, and appends the archive format's
// extension — the exact name the release publishes and install.sh requests.
//
// The platforms come from runtime (what this machine is), which is what
// uname reports on any host these tests run on; install.sh detects the same
// values through uname.
func releaseArchiveName(t *testing.T, root, version, goos, goarch string) string {
	t.Helper()
	src := readDistFile(t, ".goreleaser.yaml")
	archives := yamlBlocks(src, "archives")
	if len(archives) != 1 {
		t.Fatal("`archives:` has no block to read the name_template from")
	}
	template, ok := scalarIn(archives[0], "name_template")
	if !ok {
		t.Fatal("the archive block carries no name_template")
	}
	project, _ := yamlScalar(src, "project_name")
	rendered := strings.NewReplacer(
		"{{ .ProjectName }}", project,
		"{{ .Version }}", version,
		"{{ .Os }}", goos,
		"{{ .Arch }}", goarch,
	).Replace(template)
	if rendered == template || strings.Contains(rendered, "{{") {
		t.Fatalf("the name_template %q does not render with the four fields — the fake forge cannot name the archive", template)
	}
	return rendered + ".tar.gz"
}

// gate: 4s — measured 4.0s/2.6s/2.3s warm over three runs after the tick-5o5
// temp-dir subtest (the builds answer from the gate's cache); the tick's
// acceptance names a machine that never built ticfac installing it and
// running doctor, this is the only proof of that path, and its failure mode
// ships silently broken releases
func TestInstallScriptInstallsBothBinariesAndRunsDoctor(t *testing.T) {
	shorttest.LoadBearing(t)
	h := newInstallHarness(t)

	code, out := h.runScript(t, h.installDir, h.baseURL)
	if code != 0 {
		t.Fatalf("install.sh exited %d against the fake forge:\n%s", code, out)
	}
	for _, want := range []string{
		"Installing ticfac v" + fakeVersion,
		"ticfac-exec-subprocess",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("install.sh's output does not mention %q:\n%s", want, out)
		}
	}

	// The install must place ticfac-exec-subprocess beside ticfac: a run
	// resolves its supervisor there first (internal/reconcile's
	// supervisorArgv), and an install without the pair is a ticfac that
	// refuses to start a run.
	t.Run("both binaries installed side by side", func(t *testing.T) {
		for _, name := range []string{"ticfac", "ticfac-exec-subprocess"} {
			path := filepath.Join(h.installDir, name)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("%s was not installed beside ticfac: %v", name, err)
			}
			if info.IsDir() || info.Mode()&0o111 == 0 {
				t.Errorf("%s is installed at %s without an execute bit (%s)", name, path, info.Mode())
			}
		}
	})

	t.Run("the installed ticfac answers", func(t *testing.T) {
		code, out := runInstalled(t, filepath.Join(h.installDir, "ticfac"), "version")
		if code != 0 {
			t.Fatalf("the installed ticfac version exited %d:\n%s", code, out)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("the installed ticfac version printed nothing")
		}
	})

	// The acceptance's doctor: the machine-dependent checks may pass or miss
	// (tk, herdr, a credential — a doctor that names them missing is a
	// doctor doing its job), so the assertion is the contract: exit 0 or 1,
	// the documented codes, and the versioned document on stdout.
	t.Run("the installed ticfac doctor answers", func(t *testing.T) {
		repo := t.TempDir() // a machine with no checkout: doctor's --repo
		code, out := runInstalled(t, filepath.Join(h.installDir, "ticfac"), "doctor", "--json", "--repo", repo)
		if code != 0 && code != 1 {
			t.Fatalf("the installed ticfac doctor exited %d — not one of the documented codes (0 done, 1 missing):\n%s", code, out)
		}
		if !strings.Contains(out, "ticfac.doctor.v1") {
			t.Errorf("the installed ticfac doctor --json did not emit its versioned document:\n%s", out)
		}
	})

	t.Run("the installed executor answers", func(t *testing.T) {
		code, out := runInstalled(t, filepath.Join(h.installDir, "ticfac-exec-subprocess"))
		if code != 2 {
			t.Fatalf("the installed ticfac-exec-subprocess with no operation exited %d, want the usage code 2:\n%s", code, out)
		}
		for _, operation := range []string{"start", "inspect", "cancel", "collect"} {
			if !strings.Contains(out, operation) {
				t.Errorf("the installed executor's usage does not name the %s operation:\n%s", operation, out)
			}
		}
	})

	// The negative controls: the script's refusals, driven through a PATH
	// shim that lies about the machine, the way the script actually detects
	// it — uname — rather than a seam the script does not have.
	t.Run("refuses platforms no release serves", func(t *testing.T) {
		for _, tc := range []struct {
			uname string
			want  string
		}{
			{uname: "#!/bin/sh\ncase \"$1\" in -s) echo SunOS;; -m) echo riscv64;; esac\n", want: "Unsupported OS"},
			{uname: "#!/bin/sh\ncase \"$1\" in -s) echo Darwin;; -m) echo riscv64;; esac\n", want: "Unsupported architecture"},
		} {
			shim := t.TempDir()
			if err := os.WriteFile(filepath.Join(shim, "uname"), []byte(tc.uname), 0o755); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			code, out := h.runScript(t, dir, h.baseURL, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
			if code != 1 {
				t.Errorf("install.sh exited %d on %q, want the refusal's 1:\n%s", code, tc.want, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("install.sh's output does not refuse with %q:\n%s", tc.want, out)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("install.sh left %d files in the install dir while refusing:\n%s", len(entries), out)
			}
		}
	})

	// The temp dir the download lands in is removed by the script's exit
	// trap — QUOTED, because mktemp's answer can carry a space (GNU mktemp
	// honors TMPDIR, and a TMPDIR with a space in it is a legal one), and the
	// trap is the last line of the script to see that path. An unquoted trap
	// does not fail the install — it fails only to clean up, so nothing but a
	// path that makes the word-split matter can see it. The mktemp shim
	// reproduces GNU mktemp -d against a spaced TMPDIR the way the script
	// actually gets its temp dir — by calling mktemp — rather than a seam
	// the script does not have.
	t.Run("cleans its temp dir when the path carries a space", func(t *testing.T) {
		shim := t.TempDir()
		spaced := filepath.Join(shim, "sp ace")
		if err := os.Mkdir(spaced, 0o700); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\nd='" + spaced + "/tmp.down load.XXXX'\nmkdir \"$d\" && echo \"$d\"\n"
		if err := os.WriteFile(filepath.Join(shim, "mktemp"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		code, out := h.runScript(t, dir, h.baseURL, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
		if code != 0 {
			t.Fatalf("install.sh exited %d with a spaced temp dir:\n%s", code, out)
		}
		entries, err := os.ReadDir(spaced)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("install.sh left %d entries in its temp dir — the exit trap must remove the QUOTED temp path, and a path with a space is the one that exposes an unquoted expansion:\n%s", len(entries), out)
		}
	})

	t.Run("fails closed when the release lookup answers nothing", func(t *testing.T) {
		empty := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(empty.Close)
		dir := t.TempDir()
		code, out := h.runScript(t, dir, empty.URL)
		if code == 0 {
			t.Fatalf("install.sh succeeded against a forge with no releases:\n%s", out)
		}
		if !strings.Contains(out, "Failed to resolve the latest release") {
			t.Errorf("install.sh's output does not name the failed release lookup:\n%s", out)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("install.sh left %d files in the install dir after failing:\n%s", len(entries), out)
		}
	})
}
