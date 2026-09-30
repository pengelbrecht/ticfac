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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/forge"
)

// doctorSeams is every probe doctor asks, saved for restore.
type doctorSeams struct {
	tk          func(context.Context, string) (string, error)
	herdr       func(context.Context) (string, error)
	github      func() (string, error)
	forgeRemote func(string) (string, error)
	gitID       func(string) (string, error)
	docker      func(context.Context) (string, error)
	wrangler    func() (string, error)
	factory     func(context.Context) (string, error)
	classifier  func(context.Context) (string, error)
}

// saveDoctorSeams overrides every probe with ok (or with the one missing
// probe named), and restores the production probes on cleanup — a test that
// leaked an override is a test that poisoned every later one. keepRealHerdr
// leaves herdr's probe as the production one, so a test can drive the real
// client against a socket that is not there.
func saveDoctorSeams(t *testing.T, missing string, keepRealHerdr bool) {
	t.Helper()
	saved := doctorSeams{
		tk:          doctorTK,
		herdr:       doctorHerdr,
		github:      doctorGitHub,
		forgeRemote: doctorForgeRemote,
		gitID:       doctorGitIdentity,
		docker:      doctorDocker,
		wrangler:    doctorWrangler,
		factory:     doctorFactory,
		classifier:  doctorClassifier,
	}
	t.Cleanup(func() {
		doctorTK, doctorHerdr, doctorGitHub, doctorForgeRemote, doctorGitIdentity =
			saved.tk, saved.herdr, saved.github, saved.forgeRemote, saved.gitID
		doctorDocker, doctorWrangler, doctorFactory = saved.docker, saved.wrangler, saved.factory
		doctorClassifier = saved.classifier
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
	doctorForgeRemote = func(string) (string, error) {
		if missing == "forge remote" {
			return "", errDoctorProbe("forge remote")
		}
		return "example/example", nil
	}
	doctorGitIdentity = func(string) (string, error) { return ok("git identity")() }
	doctorDocker = func(context.Context) (string, error) { return ok("docker")() }
	doctorWrangler = ok("wrangler")
	doctorFactory = func(context.Context) (string, error) { return ok("factory")() }
	doctorClassifier = func(context.Context) (string, error) { return ok("classifier")() }
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
	// A GitHub origin (tick 6vp): init guesses the close-out rule from it,
	// so the fixture declares the rule and doctor's forge check — the
	// remote and the credential, both halves — runs for it.
	mustGit(t, repo, "remote", "add", "origin", "git@github.com:example/example.git")
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
		{check: "classifier", fix: doctorFixClassifier},
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
	for _, check := range []string{runconfigFileName(), "routing", "tk", "herdr", "github", "git identity", "classifier"} {
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

// TestDoctorReportsAJobThatCannotRoute (epic hn6, 2026-09-30): a cloud
// ceiling the review cell declares no tier for leaves the resolve-conflict
// job unroutable, which a run met only at its first merge conflict. doctor
// resolves every job a run can dispatch and names the one that fails.
func TestDoctorReportsAJobThatCannotRoute(t *testing.T) {
	saveDoctorSeams(t, "", false)
	repo := doctorFixture(t, true)
	cloudFile := filepath.Join(repo, ".tick", "runners.cloud.toml")
	raw, err := os.ReadFile(cloudFile)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(`
[roles.implement.tiers.strong]
effort = "high"

[tier_policy]
default = "strong"
ceiling = "strong"
`)...)
	if err := os.WriteFile(cloudFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitGeneric {
		t.Fatalf("doctor over an unroutable resolve-conflict job exits %d, want %d:\n%s", code, exitGeneric, stdout)
	}
	for _, want := range []string{"missing  routing", "resolve-conflict", `"strong"`, "fix: " + doctorFixRouting} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
	}
}

// runconfigFileName is the check's display name, isolated so the test reads
// the same constant the report prints.
func runconfigFileName() string { return ".tick/runners.toml" }

// TestDoctorChecksTheForgeRemoteTheRuleNeeds (tick 6vp): a repository that
// declares the rule needs BOTH halves of the forge — a GitHub remote to open
// the epic PR on and the credential to speak with — and before this tick
// doctor checked only the credential, so a declared rule over a non-GitHub
// origin was an ok the run then refused to start on. The check names the
// half it is missing, and each half carries its own fix.
func TestDoctorChecksTheForgeRemoteTheRuleNeeds(t *testing.T) {
	saveDoctorSeams(t, "forge remote", false)
	repo := doctorFixture(t, false) // declares the rule: init guessed pr from origin
	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitGeneric {
		t.Fatalf("doctor with a rule and no GitHub remote exits %d, want %d:\n%s", code, exitGeneric, stdout)
	}
	if !strings.Contains(stdout, "missing  github") {
		t.Errorf("the report has no missing github line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "fix: "+doctorFixForgeRemote) {
		t.Errorf("the missing remote does not name its fix (%s):\n%s", doctorFixForgeRemote, stdout)
	}

	// The ok mirror names the remote the epic PR opens on: an ok without
	// the repository it addresses is the half-answer the missing half hid.
	saveDoctorSeams(t, "", false)
	code, stdout, _ = runDoctorOn(t, repo, false)
	if code != exitSuccess {
		t.Fatalf("doctor with both halves exits %d, want 0:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "example/example") {
		t.Errorf("the ok line does not name the remote the epic PR opens on:\n%s", stdout)
	}
}

// TestDoctorSkipsTheForgeCheckWhenNoRuleIsDeclared: the run resolves a
// forge when and only when the rule is declared (tick hio), so a repository
// init left without the rule — one with no GitHub origin — is not told its
// GitHub credential is missing: a fix for a problem no run would have is
// the friction this tick exists to remove, pointed at doctor.
func TestDoctorSkipsTheForgeCheckWhenNoRuleIsDeclared(t *testing.T) {
	saveDoctorSeams(t, "", false)
	repo := initFixture(t, map[string]string{"go.mod": "module example.com/fresh\n"})
	// init guesses no rule: the fixture has no origin.
	if code, _, stderr := runInitOn(t, repo, "", "--yes"); code != exitSuccess {
		t.Fatalf("the fixture's init exits %d: %s", code, stderr)
	}
	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitSuccess {
		t.Fatalf("doctor over a repository that needs no forge exits %d, want 0:\n%s", code, stdout)
	}
	if strings.Contains(stdout, "github") {
		t.Errorf("the forge check ran for a repository that declares no rule:\n%s", stdout)
	}
}

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

// TestDoctorGitHubNamesTheRungItFound (tick vo4): the github check resolves
// its credential through the SAME ladder the run's own surface resolves
// from, and the detail names the rung that answered. Both halves matter: a
// doctor that accepted gh while the run read the environment only was an ok
// the run refused at startup, and an ok without its source is the
// half-answer that hid which credential a run would actually speak with.
func TestDoctorGitHubNamesTheRungItFound(t *testing.T) {
	saved := doctorGitHub
	t.Cleanup(func() { doctorGitHub = saved })

	for _, tc := range []struct {
		name   string
		ladder func() (string, forge.TokenSource, error)
		want   string
	}{
		{name: "env", ladder: func() (string, forge.TokenSource, error) {
			return "a-token", forge.TokenSourceEnv, nil
		}, want: forge.TokenEnv + " is set"},
		{name: "gh", ladder: func() (string, forge.TokenSource, error) {
			return "a-token", forge.TokenSourceGH, nil
		}, want: "gh auth token answers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			savedLadder := resolveForgeToken
			resolveForgeToken = tc.ladder
			t.Cleanup(func() { resolveForgeToken = savedLadder })

			detail, err := doctorGitHub()
			if err != nil {
				t.Fatalf("the probe failed where its rung answered: %v", err)
			}
			if detail != tc.want {
				t.Errorf("the probe's detail is %q, want it to name the rung: %q", detail, tc.want)
			}
		})
	}

	// A ladder that answers with nothing is a missing line, not an ok one.
	t.Run("neither rung", func(t *testing.T) {
		savedLadder := resolveForgeToken
		resolveForgeToken = func() (string, forge.TokenSource, error) {
			return "", "", fmt.Errorf("no %s is set, and gh auth token did not answer", forge.TokenEnv)
		}
		t.Cleanup(func() { resolveForgeToken = savedLadder })

		detail, err := doctorGitHub()
		if err == nil {
			t.Fatalf("the probe answered ok with no credential: %q", detail)
		}
		if !strings.Contains(err.Error(), forge.TokenEnv) {
			t.Errorf("the missing credential does not name the rung it read: %v", err)
		}
	})
}

// TestDoctorForgeRemoteRefusesAnotherForge (tick 4zo): the remote check
// resolves through the SAME reader the run's own surface resolves through,
// so its refusal must be the host check's, not the old slug-only
// resolution: a GitLab origin resolved to owner/name, doctor read it as ok,
// and the operator learned the truth only at close-out time, as 404s
// against api.github.com. The production probe runs here, against a real
// checkout — a seam test could not prove the host check reached the probe
// doctor actually runs.
func TestDoctorForgeRemoteRefusesAnotherForge(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet", "-b", "main")
	mustGit(t, dir, "remote", "add", "origin", "git@gitlab.com:example/example.git")
	if _, err := doctorForgeRemote(dir); err == nil {
		t.Fatal("the forge remote check accepted a GitLab origin")
	} else if !strings.Contains(err.Error(), "gitlab.com") || !strings.Contains(err.Error(), "github.com") {
		t.Errorf("the refusal does not name the host against github.com: %v", err)
	}

	// The same checkout over a GitHub origin resolves, so the refusal is
	// the host's, not the probe's.
	mustGit(t, dir, "remote", "set-url", "origin", "git@github.com:example/example.git")
	if slug, err := doctorForgeRemote(dir); err != nil {
		t.Fatalf("the forge remote check refuses a GitHub origin: %v", err)
	} else if slug != "example/example" {
		t.Errorf("the forge remote check resolves to %q, want example/example", slug)
	}
}
