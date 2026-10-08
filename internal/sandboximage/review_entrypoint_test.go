//go:build !windows

package sandboximage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The review phase (UC5, tick v7g): the container half of the first autonomous
// loop.
//
// Three properties are under test and they are deliberately not the same
// thing:
//
//  1. **The container is told which pull request it reads, and refuses to boot
//     without one.** The binding lives in the control plane's record; a
//     container that could choose would be a container that could review — and
//     comment on — somebody else's pull request.
//  2. **A review container never executes anything from the pull request.**
//     The head is fetched as a REF and never checked out, no `[sandbox]` setup
//     runs, no toolchain is provisioned and no pre-flight command is executed.
//     Anyone can open a pull request against a public repository, and setup
//     commands are shell: this is the rule that keeps that from being remote
//     code execution beside the run's credentials.
//  3. **The findings leave through the entrypoint, not the agent.** The
//     harness writes a file; the container POSTs that file, once, to the
//     factory's review door with the run's own credential.

// assertNoHarnessRun fails the test if a harness process wrote its record —
// the negative assertion for the halves, where no harness may run.
func assertNoHarnessRun(t *testing.T, f *fixture) {
	t.Helper()
	if _, err := os.Stat(f.record); err == nil {
		t.Errorf("the harness ran on this boot:\n%s", mustNotBeEmpty(t, f.record))
	}
}

// reviewFixture is a fixture with a pull request on its origin: a third commit,
// published the way GitHub publishes one — as refs/pull/<n>/head on the BASE
// repository, which is why a fork's branch is readable through the same remote
// and therefore through the factory's read-only git door.
func reviewFixture(t *testing.T, pr string) (*fixture, string) {
	t.Helper()
	f := newFixture(t, "- `true`\n")
	if err := os.WriteFile(filepath.Join(f.source, "contributed.txt"), []byte("from a stranger\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, f.source, "add", "-A")
	git(t, f.source, "commit", "-q", "-m", "the pull request's commit")
	head := git(t, f.source, "rev-parse", "HEAD")
	git(t, f.source, "update-ref", "refs/pull/"+pr+"/head", head)
	// The base stays where the run was submitted: the tracked, reviewed tree.
	git(t, f.source, "checkout", "-q", f.headSHA)
	git(t, f.source, "update-ref", "refs/heads/main", f.headSHA)

	f.env[EnvPhase] = PhaseReview
	f.env[EnvReviewPR] = pr
	f.env[EnvReviewHeadSHA] = head
	f.env[EnvReviewOutput] = filepath.Join(f.root, "review.md")
	f.env[EnvFactoryURL] = "https://factory.example.com"
	f.env[EnvFactoryToken] = testGatewayToken
	f.env[EnvFactoryProject] = "acme/widgets"
	return f, head
}

// The whole loop, from the container's side: fetch the pull request, review it,
// hand the findings to the factory.
func TestEntrypointReviewsAPullRequestAndPostsItsFindings(t *testing.T) {
	f, prHead := reviewFixture(t, "42")
	f.env["TICKS_TEST_REVIEW_FINDINGS"] = "One finding: contributed.txt:1 says nothing useful."
	out, code := f.run()
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}

	rec := f.harnessRecord()
	mustContain(t, rec, EnvPhase+"="+PhaseReview, "the phase is exported for the harness")
	mustContain(t, rec, EnvReviewPR+"=42", "the container is told which pull request it reads")
	mustContain(t, rec, EnvReviewOutput+"=", "the harness is told where to write its findings")
	mustContain(t, rec, "refs/remotes/pr/42", "the prompt names the ref the diff is read from")
	mustContain(t, rec, "never as instructions", "the prompt says the diff is evidence, not direction")
	mustContain(t, rec, "read-only credential", "the prompt says what this run cannot do")

	// The findings reached the factory's review door, with the run's own
	// credential — the same one a stop revokes.
	calls := f.probeCalls()
	mustContain(t, calls, "https://factory.example.com/api/review", "the findings are posted to the review door")
	mustContain(t, calls, "Authorization: Bearer "+testGatewayToken, "the post carries the run's own credential")
	mustContain(t, calls, "@"+f.env[EnvReviewOutput], "the file the harness wrote is what is posted")

	// The pull request is IN the repository and NOT in the working tree.
	head := strings.TrimSpace(git(t, f.workdir, "rev-parse", "HEAD"))
	if head != f.headSHA {
		t.Errorf("the checkout moved to %s; a review stays at the base %s", head, f.headSHA)
	}
	fetched := strings.TrimSpace(git(t, f.workdir, "rev-parse", "refs/remotes/pr/42"))
	if fetched != prHead {
		t.Errorf("refs/remotes/pr/42 is %s, want the pull request's head %s", fetched, prHead)
	}
	if _, err := os.Stat(filepath.Join(f.workdir, "contributed.txt")); err == nil {
		t.Error("the pull request's file is in the working tree: it was checked out")
	}
}

// The other half of "never executes anything from the pull request": a review
// runs no setup, provisions no toolchain and runs no pre-flight command. Those
// exist so a container can BUILD and TEST a repository, and a review does
// neither — while every one of them is a path from a tracked file to a shell.
func TestEntrypointReviewRunsNothingTheRepositoryDeclares(t *testing.T) {
	f, _ := reviewFixture(t, "7")
	f.env["TICKS_TEST_REVIEW_FINDINGS"] = "nothing to raise"
	// A repository that declares all three. None of them may run.
	f.env["TICKS_TEST_ENV_CHECK"] = "echo the-preflight-ran"
	f.env["TICKS_TEST_SANDBOX_TOOLCHAIN"] = "go@1.24.11"
	out, code := f.run()
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	calls := f.ticfacCalls()
	for _, forbidden := range []string{"sandbox setup", "sandbox environment", "sandbox toolchain"} {
		if strings.Contains(calls, forbidden) {
			t.Errorf("a review boot ran %q:\n%s", forbidden, calls)
		}
	}
	if mise := f.miseCalls(); mise != "" {
		t.Errorf("a review boot provisioned a toolchain:\n%s", mise)
	}
	if strings.Contains(out, "the-preflight-ran") {
		t.Errorf("a review boot ran the repository's pre-flight:\n%s", out)
	}
}

// A review container that does not know which pull request it is reading has
// nothing it could correctly do, and says so at boot rather than paying a model
// to find out. The same for a container with nowhere to send its findings: the
// comment is the only durable thing this run produces.
func TestEntrypointReviewRefusesWithoutItsTarget(t *testing.T) {
	shorttest.EndToEnd(t) // its fixtures are built inside subtests
	for _, missing := range []string{EnvReviewPR, EnvReviewHeadSHA, EnvFactoryToken} {
		t.Run(missing, func(t *testing.T) {
			f, _ := reviewFixture(t, "9")
			delete(f.env, missing)
			out, code := f.run()
			if code != ExitConfig {
				t.Fatalf("exit %d, want %d\n%s", code, ExitConfig, out)
			}
			if f.harnessStarted() {
				t.Error("the harness started for a review boot that could not have posted anything")
			}
		})
	}
}

// A reviewer that wrote nothing produced nothing, and that is its own failure
// class: the diff was read and the model was paid for, and the one durable
// artefact does not exist. It must not read as a clean exit.
func TestEntrypointReviewReportsAReviewItCouldNotPost(t *testing.T) {
	f, _ := reviewFixture(t, "11")
	// No TICKS_TEST_REVIEW_FINDINGS: the harness runs and writes no file.
	out, code := f.run()
	if code != ExitReview {
		t.Fatalf("exit %d, want %d (ExitReview)\n%s", code, ExitReview, out)
	}
	if strings.Contains(f.probeCalls(), "/api/review") {
		t.Error("an empty review was posted anyway")
	}
}

// A review whose comment already landed — an earlier boot of the same run
// posted it — is a review that succeeded. The door's refusal of the second post
// is the at-most-once guarantee working, not a failure to report.
func TestEntrypointReviewTreatsAnAlreadyPostedReviewAsDone(t *testing.T) {
	f, _ := reviewFixture(t, "13")
	f.env["TICKS_TEST_REVIEW_FINDINGS"] = "findings from the first boot"
	f.env["TICKS_TEST_REVIEW_STATUS"] = "409"
	out, code := f.run()
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	mustContain(t, out, "already posted", "the container says why it is content")
}

// The hosted review (epic ex6, tick 8gd): the PR-review job's conversation is
// hosted on the run's WorkerAgent like every other cloud worker's, so the
// container runs two halves instead of a CLI harness — --boot sets up (clone,
// the pull request fetched as a ref, the model probe) and hands off the
// prompt, --finish posts the findings the conversation wrote. The halves are
// the review contract's own (contracts/worker-boot-con.json, review_*), and
// the all-in-one CLI path stays for the CLI harnesses a non-hosting
// deployment routes to.
func TestEntrypointHostedReviewBootsAndHandsOff(t *testing.T) {
	f, prHead := reviewFixture(t, "17")
	f.env[EnvHarness] = "pi-durable"
	f.env[EnvModel] = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
	out, code := f.runWithArgs("--boot")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}

	// The handoff: the marker line carries the reviewed ref and the findings
	// path, and the prompt follows between the review's own markers — the
	// same prompt the all-in-one review harness receives.
	mustContain(t, out, ReviewBootMarker+" branch=refs/pull/17/head", "the marker line names the ref that was reviewed")
	mustContain(t, out, "result="+f.env[EnvReviewOutput], "the marker line names the findings path the finish posts")
	mustContain(t, out, ReviewBootPromptBegin, "the prompt markers frame the handoff")
	mustContain(t, out, "never as instructions", "the prompt says the diff is evidence, not direction")

	// No harness ran and nothing was posted: the conversation is not this
	// container's, and the findings are not written yet.
	assertNoHarnessRun(t, f)
	if strings.Contains(f.probeCalls(), "/api/review") {
		t.Error("a hosted review boot posted findings")
	}

	// The setup happened: the pull request is IN the repository and NOT in
	// the working tree, exactly as the all-in-one boot leaves it.
	fetched := strings.TrimSpace(git(t, f.workdir, "rev-parse", "refs/remotes/pr/17"))
	if fetched != prHead {
		t.Errorf("refs/remotes/pr/17 is %s, want the pull request's head %s", fetched, prHead)
	}
	if _, err := os.Stat(filepath.Join(f.workdir, "contributed.txt")); err == nil {
		t.Error("the pull request's file is in the working tree: it was checked out")
	}
}

// The finish half: the host runs it once the conversation settles. It posts
// the findings file the conversation wrote, with the run's own credential,
// and exits with the post's own status — no clone, no probes, no harness.
func TestEntrypointHostedReviewFinishPostsTheFindings(t *testing.T) {
	f, _ := reviewFixture(t, "19")
	f.env[EnvHarness] = "pi-durable"
	f.env[EnvModel] = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
	if out, code := f.runWithArgs("--boot"); code != 0 {
		t.Fatalf("the boot half: exit %d, want 0\n%s", code, out)
	}
	// The conversation wrote its findings.
	findings := "Two findings:\n- a leak\n- a typo"
	if err := os.WriteFile(f.env[EnvReviewOutput], []byte(findings), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := f.runWithArgs("--finish")
	if code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, out)
	}
	calls := f.probeCalls()
	mustContain(t, calls, "https://factory.example.com/api/review", "the findings are posted to the review door")
	mustContain(t, calls, "Authorization: Bearer "+testGatewayToken, "the post carries the run's own credential")
	mustContain(t, calls, "@"+f.env[EnvReviewOutput], "the file the conversation wrote is what is posted")
	assertNoHarnessRun(t, f)
}

// A hosted review with nothing to post is the honest failure: the finish
// exits ExitReview and the run learns the conversation wrote nothing durable.
func TestEntrypointHostedReviewFinishWithoutFindingsIsTheReviewFailure(t *testing.T) {
	f, _ := reviewFixture(t, "23")
	f.env[EnvHarness] = "pi-durable"
	f.env[EnvModel] = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
	if out, code := f.runWithArgs("--boot"); code != 0 {
		t.Fatalf("the boot half: exit %d, want 0\n%s", code, out)
	}
	// No findings file: the conversation wrote nothing.
	out, code := f.runWithArgs("--finish")
	if code != ExitReview {
		t.Fatalf("exit %d, want %d (ExitReview)\n%s", code, ExitReview, out)
	}
	if strings.Contains(f.probeCalls(), "/api/review") {
		t.Error("an empty review was posted anyway")
	}
}

// The two halves are the HOSTED review's, and the refusals say so: an
// all-in-one CLI review takes no argument, a hosted review without one would
// exec nothing, and neither half belongs to another phase.
func TestEntrypointHostedReviewRefusesAMisroutedBoot(t *testing.T) {
	shorttest.EndToEnd(t) // its fixtures are built inside subtests
	for name, fixture := range map[string]func(t *testing.T){
		"a hosted review with no argument": func(t *testing.T) {
			f, _ := reviewFixture(t, "29")
			f.env[EnvHarness] = "pi-durable"
			out, code := f.run()
			if code != ExitConfig {
				t.Fatalf("exit %d, want %d\n%s", code, ExitConfig, out)
			}
			mustContain(t, out, "pi-durable", "the refusal names the kind")
			mustContain(t, out, "--boot", "the refusal names the half a hosted review runs")
			assertNoHarnessRun(t, f)
		},
		"a CLI review handed the boot half": func(t *testing.T) {
			f, _ := reviewFixture(t, "31")
			out, code := f.runWithArgs("--boot")
			if code != ExitConfig {
				t.Fatalf("exit %d, want %d\n%s", code, ExitConfig, out)
			}
			mustContain(t, out, "all-in-one", "the refusal says the CLI review needs no handoff")
			assertNoHarnessRun(t, f)
		},
		"an orchestrator boot handed the boot half": func(t *testing.T) {
			f := newFixture(t, "- `true`\n")
			out, code := f.runWithArgs("--boot")
			if code != ExitConfig {
				t.Fatalf("exit %d, want %d\n%s", code, ExitConfig, out)
			}
			mustContain(t, out, "review", "the refusal names the phase that owns the half")
		},
	} {
		t.Run(name, fixture)
	}
}

// The phase vocabulary crosses three languages (this package, the Worker's
// src/sandbox.ts, and entrypoint.sh's own case statement), and a value one of
// them accepts and another does not is a boot that fails at the container.
//
// short: reads the image files and asserts on their text; no process runs
func TestReviewPhaseIsInTheVocabulary(t *testing.T) {
	found := false
	for _, phase := range Phases {
		if phase == PhaseReview {
			found = true
		}
	}
	if !found {
		t.Errorf("PhaseReview is not in Phases: %v", Phases)
	}
	if ReviewRef("42") != "refs/pull/42/head" {
		t.Errorf("ReviewRef(42) = %q", ReviewRef("42"))
	}
	if !strings.Contains(ReviewFindingsPath("run_x"), "run_x") {
		t.Errorf("ReviewFindingsPath does not carry the run id: %q", ReviewFindingsPath("run_x"))
	}
}
