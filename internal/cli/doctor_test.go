package cli

// `ticfac doctor` (tick o0d): what a run needs, each missing thing with its
// fix.
//
// The acceptance is "doctor reports each missing prerequisite with its fix",
// and the test shape follows it literally: one table case per check, each
// answered MISSING by an overridden seam, asserting (a) the check's name is
// on a missing line, (b) its fix command is on the next line, and (c) the
// exit code is 1 — the code a script branches on. The all-ok case is the
// mirror: every probe answered, exit 0.
//
// The seams exist because the probes are the environment's; the one probe a
// test drives for real is herdr's, through the real client against a socket
// that does not exist — the resolution order ($HERDR_SOCKET_PATH, then
// herdr's default) is the run's own, and a missing server is the case the
// operator actually meets.
//
// short: no probes leave the process; herdr's dial is against a socket path
// that is not there.

import (
	"bytes"
	"context"
	"flag"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// doctorSeams is every probe doctor asks, saved for restore.
type doctorSeams struct {
	tk       func(context.Context, string) (string, error)
	herdr    func(context.Context) (string, error)
	github   func() (string, error)
	gitID    func(string) (string, error)
	docker   func(context.Context) (string, error)
	wrangler func() (string, error)
	factory  func(context.Context) (string, error)
}

// saveDoctorSeams overrides every probe with ok (or with the one missing
// probe named), and restores the production probes on cleanup — a test that
// leaked an override is a test that poisoned every later one. keepRealHerdr
// leaves herdr's probe as the production one, so a test can drive the real
// client against a socket that is not there.
func saveDoctorSeams(t *testing.T, missing string, keepRealHerdr bool) {
	t.Helper()
	saved := doctorSeams{
		tk:       doctorTK,
		herdr:    doctorHerdr,
		github:   doctorGitHub,
		gitID:    doctorGitIdentity,
		docker:   doctorDocker,
		wrangler: doctorWrangler,
		factory:  doctorFactory,
	}
	t.Cleanup(func() {
		doctorTK, doctorHerdr, doctorGitHub, doctorGitIdentity = saved.tk, saved.herdr, saved.github, saved.gitID
		doctorDocker, doctorWrangler, doctorFactory = saved.docker, saved.wrangler, saved.factory
	})
	ok := func(name string) func() (string, error) {
		if name == missing {
			return func() (string, error) { return "", errDoctorProbe(name) }
		}
		return func() (string, error) { return name + " is fine", nil }
	}
	doctorTK = func(context.Context, string) (string, error) { return ok("tk")() }
	if !keepRealHerdr {
		doctorHerdr = func(context.Context) (string, error) { return ok("herdr")() }
	}
	doctorGitHub = ok("github")
	doctorGitIdentity = func(string) (string, error) { return ok("git identity")() }
	doctorDocker = func(context.Context) (string, error) { return ok("docker")() }
	doctorWrangler = ok("wrangler")
	doctorFactory = func(context.Context) (string, error) { return ok("factory")() }
}

// errDoctorProbe is the one error shape the table's missing probes report.
func errDoctorProbe(name string) error {
	return &exitError{code: exitGeneric, message: name + " is not there (the probe's controlled answer)"}
}

// runDoctorOn drives the body with the flags parsed the way the cobra
// command parses them.
func runDoctorOn(t *testing.T, repo string, cloud bool) (int, string, string) {
	t.Helper()
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fl := defineDoctorFlags(fs)
	if err := fs.Parse([]string{"--repo", repo}); err != nil {
		t.Fatal(err)
	}
	if cloud {
		_ = fs.Set("cloud", "true")
	}
	var stdout, stderr bytes.Buffer
	code := runDoctor(context.Background(), fl, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// doctorFixture is a repository init already made ready, so the runners
// check reads ok and the case under test is the probe's, not the file's.
func doctorFixture(t *testing.T, cloud bool) string {
	t.Helper()
	repo := initFixture(t, map[string]string{"go.mod": "module example.com/fresh\n"})
	args := []string{"--yes", "--substrate", "local"}
	if cloud {
		args = []string{"--yes", "--substrate", "both"}
	}
	if code, _, stderr := runInitOn(t, repo, "", args...); code != exitSuccess {
		t.Fatalf("the fixture's init exits %d: %s", code, stderr)
	}
	return repo
}

// TestDoctorReportsEachMissingPrerequisiteWithItsFix is the acceptance,
// read one check at a time: each missing prerequisite is a missing line
// carrying its fix, and the exit code says so without parsing the words.
func TestDoctorReportsEachMissingPrerequisiteWithItsFix(t *testing.T) {
	for _, tc := range []struct {
		check, fix string
		cloud      bool
	}{
		{check: "tk", fix: doctorFixTK},
		{check: "herdr", fix: doctorFixHerdr},
		{check: "github", fix: doctorFixGitHub},
		{check: "git identity", fix: doctorFixGit},
		{check: "docker", fix: doctorFixDocker, cloud: true},
		{check: "wrangler", fix: doctorFixWrangler, cloud: true},
		{check: "factory", fix: doctorFixFactory, cloud: true},
	} {
		t.Run(tc.check, func(t *testing.T) {
			saveDoctorSeams(t, tc.check, false)
			repo := doctorFixture(t, tc.cloud)
			code, stdout, _ := runDoctorOn(t, repo, tc.cloud)
			if code != exitGeneric {
				t.Fatalf("doctor with a missing %s exits %d, want %d:\n%s",
					tc.check, code, exitGeneric, stdout)
			}
			if !strings.Contains(stdout, "missing  "+tc.check) {
				t.Errorf("the report has no missing line for %s:\n%s", tc.check, stdout)
			}
			if !strings.Contains(stdout, "fix: "+tc.fix) {
				t.Errorf("the report does not name the fix for %s (%s):\n%s", tc.check, tc.fix, stdout)
			}
		})
	}
}

// TestDoctorWithEverythingPresentExitsZero: every probe answers, the
// repository is ready, and the code a script branches on is success.
func TestDoctorWithEverythingPresentExitsZero(t *testing.T) {
	saveDoctorSeams(t, "", false)
	repo := doctorFixture(t, false)
	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitSuccess {
		t.Fatalf("doctor with everything exits %d, want %d:\n%s", code, exitSuccess, stdout)
	}
	for _, check := range []string{runconfigFileName(), "tk", "herdr", "github", "git identity"} {
		if !strings.Contains(stdout, "ok       "+check) {
			t.Errorf("the report has no ok line for %s:\n%s", check, stdout)
		}
	}
	if strings.Contains(stdout, "docker") {
		t.Errorf("the cloud checks ran for a local answer:\n%s", stdout)
	}
}

// TestDoctorRunsTheCloudChecksWhenTheRepositoryDeclaresTheCloud: --cloud
// forces them, and a repository whose runners.cloud.toml exists gets them
// without the flag.
func TestDoctorRunsTheCloudChecksWhenTheRepositoryDeclaresTheCloud(t *testing.T) {
	saveDoctorSeams(t, "", false)
	repo := doctorFixture(t, true) // a both answer: substrate auto + a cloud file
	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitSuccess {
		t.Fatalf("doctor exits %d, want %d:\n%s", code, exitSuccess, stdout)
	}
	for _, check := range []string{"docker", "wrangler", "factory"} {
		if !strings.Contains(stdout, "ok       "+check) {
			t.Errorf("the cloud check %s did not run for a repository with a cloud override:\n%s", check, stdout)
		}
	}
}

// TestDoctorReportsAMissingRunnersTomlWithInitAsTheFix: the repository's
// half is a check too, because doctor is also the command a person runs
// BEFORE init — and its answer is the command that writes the file.
func TestDoctorReportsAMissingRunnersTomlWithInitAsTheFix(t *testing.T) {
	saveDoctorSeams(t, "", false)
	repo := initFixture(t, nil)
	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitGeneric {
		t.Fatalf("doctor over an uninitiated repo exits %d, want %d:\n%s", code, exitGeneric, stdout)
	}
	if !strings.Contains(stdout, "missing  "+runconfigFileName()) {
		t.Errorf("the report has no missing line for the runners.toml:\n%s", stdout)
	}
	if !strings.Contains(stdout, "fix: ticfac init") {
		t.Errorf("the missing runners.toml does not name init as its fix:\n%s", stdout)
	}
}

// runconfigFileName is the check's display name, isolated so the test reads
// the same constant the report prints.
func runconfigFileName() string { return ".tick/runners.toml" }

// TestDoctorProbesTheRealHerdrSocket: herdr's probe is the real client —
// the same resolution order and handshake a dispatch uses — so a missing
// server is proven through the real path, with the fix on the line and the
// degradation the substrate performs named beside it.
func TestDoctorProbesTheRealHerdrSocket(t *testing.T) {
	saveDoctorSeams(t, "", true) // every other probe answered; herdr stays real
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "no-such.sock"))
	repo := doctorFixture(t, false)
	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitGeneric {
		t.Fatalf("doctor with no herdr exits %d, want %d:\n%s", code, exitGeneric, stdout)
	}
	if !strings.Contains(stdout, "missing  herdr") {
		t.Errorf("herdr is not reported missing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "fix: "+doctorFixHerdr) {
		t.Errorf("the missing herdr does not name its fix:\n%s", stdout)
	}
	if !strings.Contains(stdout, "degrade to a plain harness") {
		t.Errorf("the missing herdr does not say what a run does without it:\n%s", stdout)
	}
}
