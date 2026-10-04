package sandboximage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// piLockfileInstall is the image's pi install, verbatim: the dependency tree
// pinned by image/pi/pnpm-lock.yaml, installed as it was resolved.
const piLockfileInstall = "pnpm install --dir /opt/pi --frozen-lockfile --prod --ignore-scripts"

// pi is installed from a committed lockfile, not from `npm install -g`, and
// the three places its version is written agree.
//
// pi 1.0.1 removed npm-shrinkwrap.json from the published package ("npm
// installations no longer pin transitive dependencies … Use the pi.dev
// installer for pinned installations"), so `npm install -g
// @earendil-works/pi-coding-agent@<exact>` stopped meaning one dependency tree:
// two builds of the same Dockerfile could run different code under the same
// pin. Every other battery in this image is pinned by version AND checksum;
// pi's equivalent is the lockfile, whose integrity hashes pnpm verifies on a
// frozen install. ARG PI_VERSION is what the build asserts `pi --version`
// against, package.json is what the lockfile was resolved from, and the
// lockfile is what is installed — a bump that misses one of them fails here
// rather than as an image that says one version and runs another.
//
// short: reads the image files and asserts on their text; no process runs
func TestDockerfileInstallsPiFromItsLockfile(t *testing.T) {
	df := readDockerfile(t)
	m := regexp.MustCompile(`(?m)^ARG PI_VERSION=(\S+)$`).FindStringSubmatch(df)
	if m == nil {
		t.Fatal("the Dockerfile pins no PI_VERSION")
	}
	version := m[1]
	if !strings.HasPrefix(version, "1.") {
		t.Errorf("PI_VERSION is %s; the workers run pi 1.x (tick jd3)", version)
	}

	if !strings.Contains(df, piLockfileInstall) {
		t.Errorf("the Dockerfile does not install pi with %q", piLockfileInstall)
	}
	if regexp.MustCompile(`npm install[^\n]*pi-coding-agent`).MatchString(df) {
		t.Error("the Dockerfile still installs pi with npm, which since pi 1.0.1 pins no transitive dependency")
	}
	for _, f := range []string{"pi/package.json", "pi/pnpm-lock.yaml", "pi/pnpm-workspace.yaml"} {
		if !strings.Contains(df, f) {
			t.Errorf("the Dockerfile does not COPY %s into the build", f)
		}
	}
	if !strings.Contains(df, `"${PI_VERSION}"`) || !strings.Contains(df, "pi --version") {
		t.Error("the build does not assert the installed `pi --version` against PI_VERSION")
	}

	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := os.ReadFile(filepath.Join(dir, "pi", "package.json"))
	if err != nil {
		t.Fatalf("reading image/pi/package.json: %v", err)
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(pkg, &manifest); err != nil {
		t.Fatalf("image/pi/package.json: %v", err)
	}
	if got := manifest.Dependencies["@earendil-works/pi-coding-agent"]; got != version {
		t.Errorf("image/pi/package.json pins pi %q, the Dockerfile's PI_VERSION is %s: they must be the same exact version", got, version)
	}
	if len(manifest.Dependencies) != 1 {
		t.Errorf("image/pi/package.json carries %d dependencies, want pi alone: %v", len(manifest.Dependencies), manifest.Dependencies)
	}

	lock, err := os.ReadFile(filepath.Join(dir, "pi", "pnpm-lock.yaml"))
	if err != nil {
		t.Fatalf("reading image/pi/pnpm-lock.yaml (regenerate it with `pnpm install --lockfile-only` in image/pi): %v", err)
	}
	importer := regexp.MustCompile(`'@earendil-works/pi-coding-agent':\s*\n\s*specifier: (\S+)\s*\n\s*version: (\S+)`).FindSubmatch(lock)
	if importer == nil {
		t.Fatal("image/pi/pnpm-lock.yaml has no importer entry for @earendil-works/pi-coding-agent")
	}
	// The resolved version carries pnpm's peer-dependency suffix
	// ("1.0.2(ws@8.22.0)…"); the version is what precedes it.
	resolved, _, _ := strings.Cut(string(importer[2]), "(")
	if string(importer[1]) != version || resolved != version {
		t.Errorf("image/pi/pnpm-lock.yaml resolves pi as specifier %s version %s, want %s: regenerate the lockfile after a bump",
			importer[1], importer[2], version)
	}
}
