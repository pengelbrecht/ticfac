package subprocess

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The CI wiring for the drain suite (tick 3wq).
//
// TestPiDrainsTheGatewayStreamThroughTheMeteringOverride — the only
// end-to-end check that pi resolves the generated !command and that the
// account token displaces the key pi stored on its own (m4t) — skips
// wherever pi is not on PATH, and no CI runner had one: CI checked the
// join only through a string match on the generated extension
// (pimeter_test.go), and the displacement and the drain, the two claims a
// fake gateway proves against real pi, were claims only this host made.
// The fix is wiring: the go-test job that carries this package installs a
// pinned pi, the same version the sandbox image ships. These guards keep
// that wiring from rotting back to a skip nobody reads — the same shape as
// reconcile's paths-ignore guard, which keeps ciIgnoredPrefixes and the
// workflow one list so the close-out's borrow stays sound.

// piPackage is the package whose tests need a real pi on PATH.
const piPackage = "./internal/exec/subprocess"

// ciYAMLJob returns the named job's block of ci.yml: its `  <name>:` line
// through the next top-level key, comments included.
func ciYAMLJob(t *testing.T, job string) []string {
	t.Helper()
	root := testRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	var block []string
	in := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !in {
			if strings.HasPrefix(line, "  "+job+":") {
				in = true
			}
			continue
		}
		// The next job starts at the first two-space key that is not a
		// comment; jobs indent their own keys one level deeper.
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") &&
			!strings.HasPrefix(line, "  #") && strings.TrimSpace(line) != "" {
			break
		}
		block = append(block, line)
	}
	if !in {
		t.Fatalf("ci.yml has no %q job", job)
	}
	return block
}

// testRepoRoot is the repository root, for reading the files CI runs.
// (The package's own repoRoot answers for a checkout under test; this one
// answers for the tree the test itself runs in.)
func testRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test: cannot locate the repository root")
		}
		dir = parent
	}
}

// short: reads ci.yml and image/Dockerfile as text; no repository, no run
//
// The pin, both halves: the go-test job must install a pinned pi behind
// the plan's `pi` flag — an unconditional install would pay for it in the
// reconcile shards that run no test of this package — and the pin must be
// the sandbox image's own, because the image is the pi a real run executes
// and CI is the only place that can catch the two drifting apart before a
// container answers a dispatch with a different pi than CI verified.
func TestTheGoTestJobInstallsTheImagePinnedPiBehindThePlanFlag(t *testing.T) {
	t.Parallel()

	block := ciYAMLJob(t, "go-test")
	install := regexp.MustCompile(
		`npm install -g --ignore-scripts "@earendil-works/pi-coding-agent@([0-9][0-9A-Za-z.\-+]*)"`)
	var version string
	for i, l := range block {
		if m := install.FindStringSubmatch(l); m != nil {
			version = m[1]
			// The step's own condition, within the step: back to its
			// opening `- ` line, so an `if:` in a neighbouring step
			// cannot answer for this one.
			step := i
			for ; step > 0 && !strings.Contains(block[step], "- name:") && !strings.Contains(block[step], "- uses:"); step-- {
			}
			if !slicesContains(block[step:i], "if: matrix.pi") {
				t.Errorf("the pi install step is not behind `if: matrix.pi`: the plan sets the flag only on the job that runs %s,\nand an unconditional install would pay npm in every shard\n%s", piPackage, strings.Join(block[step:i+1], "\n"))
			}
			break
		}
	}
	if version == "" {
		t.Fatalf("the go-test job installs no pinned pi: %s's drain suite needs pi on PATH or it skips, which is what\nmade CI's word on the m4t displacement and the 648 drain a string match on the generated extension.\njob:\n%s", piPackage, strings.Join(block, "\n"))
	}

	root := testRepoRoot(t)
	dockerfile, err := os.ReadFile(filepath.Join(root, "image", "Dockerfile"))
	if err != nil {
		t.Fatalf("read image/Dockerfile: %v", err)
	}
	arg := regexp.MustCompile(`ARG PI_VERSION=([0-9][0-9A-Za-z.\-+]*)`).FindSubmatch(dockerfile)
	if arg == nil {
		t.Fatal("image/Dockerfile carries no ARG PI_VERSION")
	}
	if version != string(arg[1]) {
		t.Errorf("CI installs pi %s, the image ships pi %s: a real run executes the image's pi, so the drain suite\nCI runs must be run against the pi a dispatched worker actually launches", version, arg[1])
	}
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

// short: runs the plan script itself, both modes; no repository, no run
//
// The plan, executed: `matrix.pi` must land on exactly the job whose
// packages carry this one, in the full run and in a pull request's
// affected-only plan. The script is parsed by running it — the real CLI,
// the way release's tests run install.sh — because a text match on the
// script could keep passing while the flag stopped reaching the matrix.
func TestTheCIPlanPutsPiOnTheJobThatCarriesTheDrainSuite(t *testing.T) {
	t.Parallel()

	bash := bash4(t)
	if bash == "" {
		t.Skip("no bash with mapfile on this host (macOS ships 3.2): the plan script runs on CI's ubuntu, bash 5")
	}
	root := testRepoRoot(t)

	for _, tc := range []struct {
		name  string
		event string
		files string
	}{
		// A push runs the full suite: the packages job carries every
		// package but reconcile's, this one among them.
		{name: "full", event: "push"},
		// A pull request that touches this package runs its affected
		// set, this one among them.
		{name: "affected", event: "pull_request", files: "internal/exec/subprocess/pimeter.go\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bash, filepath.Join(root, ".github", "scripts", "ci-plan.sh"))
			cmd.Dir = root
			cmd.Stdin = strings.NewReader(tc.files)
			out := filepath.Join(t.TempDir(), "plan")
			cmd.Env = append(os.Environ(), "EVENT="+tc.event, "GITHUB_OUTPUT="+out)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("ci-plan.sh (%s): %v\n%s", tc.event, err, stderr.String())
			}
			raw, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			var matrix string
			for _, kv := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(kv, "matrix=") {
					matrix = strings.TrimPrefix(kv, "matrix=")
				}
			}
			if matrix == "" {
				t.Fatalf("the plan wrote no matrix:\n%s", raw)
			}
			var parsed struct {
				Include []struct {
					Name string `json:"name"`
					Pkgs string `json:"pkgs"`
					Pi   *bool `json:"pi"`
				} `json:"include"`
			}
			if err := json.Unmarshal([]byte(matrix), &parsed); err != nil {
				t.Fatalf("parse the plan's matrix %s: %v", matrix, err)
			}
			var carrying, reconcile int
			for _, entry := range parsed.Include {
				if strings.Contains(" "+entry.Pkgs+" ", " "+piPackage+" ") {
					carrying++
					if entry.Pi == nil || !*entry.Pi {
						t.Errorf("the %s plan puts %s on a job without pi: the drain suite skips there,\nexactly as it did on every CI runner before tick 3wq (entry %q, pi=%v)", tc.name, piPackage, entry.Name, entry.Pi)
					}
				} else {
					if strings.Contains(entry.Pkgs, "internal/reconcile") {
						reconcile++
						if entry.Pi != nil && *entry.Pi {
							t.Errorf("the %s plan puts pi on the reconcile shard %q: that job runs no test of %s, so the npm install would be spent on nothing", tc.name, entry.Name, piPackage)
						}
					}
				}
			}
			if carrying != 1 {
				t.Errorf("the %s plan has %d jobs carrying %s, want one: the drain suite is one package", tc.name, carrying, piPackage)
			}
			if tc.event == "push" && reconcile == 0 {
				t.Errorf("the full plan carried no reconcile shards: this guard reads the shape it knows, and the shape changed")
			}
		})
	}
}

// bash4 returns a bash new enough for the plan script's mapfile and
// associative arrays — CI's ubuntu /bin/bash is; a dev Mac's /bin/bash is
// 3.2, so the well-known Homebrew paths answer there. Empty when none.
func bash4(t *testing.T) string {
	t.Helper()
	probe := "declare -A a; mapfile -t l < <(printf 'x\\n'); [ \"${l[0]}\" = x ]"
	for _, candidate := range []string{"bash", "/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/usr/bin/bash"} {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		if err := exec.Command(path, "-c", probe).Run(); err == nil {
			return path
		}
	}
	return ""
}
