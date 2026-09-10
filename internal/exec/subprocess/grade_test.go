package subprocess

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The source grade, tested as a property of the PROCESS rather than of the
// JobSpec field.
//
// The claim every layer of this repository makes about a read-only grade —
// contracts/job-protocol.json's source_grade, internal/reconcile/doc.go,
// profiles/review-epic.md — is that git write is refused by the ISSUER and not
// by the runner's good manners. A test that reads the grade back off the spec
// says nothing about that. These run a real runner, in a real linked worktree
// of a repository with a real remote, and ask the only question that matters:
// did the ref move.

// readOnly is the same job under a read-only source grant: the grade a
// review-epic job is dispatched with.
func readOnlySpec(f *fixture, jobID, tick string) *JobSpec {
	spec := f.spec(jobID, tick)
	spec.Role = "review-epic"
	spec.Credentials.Source = SourceCredential{Grant: &SourceGrant{Issuer: "host", Grade: gradeReadOnly}}
	return spec
}

// refExists asks the ORIGIN, which is the only place an answer about a push
// can come from.
func refExists(t *testing.T, origin, ref string) bool {
	t.Helper()
	out, err := git(origin, "rev-parse", "--verify", "--quiet", ref)
	return err == nil && strings.TrimSpace(out) != ""
}

func pushLog(t *testing.T, handle *JobHandle) string {
	t.Helper()
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(newStore(local.State).path("push.log"))
	if err != nil {
		return "(the runner wrote no push log: " + err.Error() + ")"
	}
	return string(raw)
}

// The acceptance test for the grade: the same runner, doing the same thing,
// under the two grades. One advances a ref on origin and the other cannot —
// and the one that cannot is not asked not to, it is launched unable to.
func TestAReadOnlyAttemptCannotPushAndAWriteGradeAttemptStillCan(t *testing.T) {
	t.Run("read-only", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{mode: "push", name: "readonly"})
		handle := f.Start(readOnlySpec(f, "run-grade/tick-ro/attempt-1", "ro"))
		f.waitSettled(handle)

		for _, ref := range []string{"refs/heads/pushed-by-runner", "refs/heads/pushed-by-url"} {
			if refExists(t, f.Repo.Origin, ref) {
				t.Errorf("a read-only attempt advanced %s on origin. The grade is the security boundary, "+
					"and it has to be kept where the process is created:\n%s", ref, pushLog(t, handle))
			}
		}
		// The attempt's own branch is not on origin either: the supervisor's
		// timed push is the other half of the same grade.
		if refExists(t, f.Repo.Origin, "refs/heads/tick/ro") {
			t.Error("a read-only attempt's own branch reached origin")
		}
		if log := pushLog(t, handle); !strings.Contains(log, "exit 0") {
			t.Logf("the read-only runner's push attempts:\n%s", log)
		} else {
			t.Errorf("one of the read-only runner's pushes exited 0:\n%s", log)
		}
	})

	t.Run("write", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{mode: "push", name: "writegrade"})
		handle := f.Start(f.spec("run-grade/tick-rw/attempt-1", "rw"))
		f.waitSettled(handle)

		if !refExists(t, f.Repo.Origin, "refs/heads/pushed-by-runner") {
			t.Errorf("a write-grade attempt could not push through its remote's name — the grade that MAY "+
				"write has to still be able to:\n%s", pushLog(t, handle))
		}
		if !refExists(t, f.Repo.Origin, "refs/heads/pushed-by-url") {
			t.Errorf("a write-grade attempt could not push through its remote's url:\n%s", pushLog(t, handle))
		}
	})
}

// A read-only attempt is issued no source credential at all. "The issuer hands
// out no push credential" has to be true of what is on disk, not only of what
// the supervisor declines to spend.
func TestAReadOnlyAttemptIsIssuedNoPushCredential(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report", name: "nocred"})
	handle := f.Start(readOnlySpec(f, "run-grade/tick-nc/attempt-1", "nc"))
	st := f.store(handle)

	if st.credentialLive() {
		t.Error("a read-only attempt holds a credential file: there is nothing for it to spend, " +
			"and a durable record showing one is the opposite of what the issuer did")
	}
	if !mentions(f.inspect(handle).Observations, "no push credential is issued") {
		t.Error("nothing in the observation log says the read-only attempt was issued no push credential")
	}

	// The write grade still gets one — this is a grade, not a removal.
	g := newFixture(t, fixtureOptions{mode: "report", name: "hascred"})
	gHandle := g.Start(g.spec("run-grade/tick-wc/attempt-1", "wc"))
	if !g.store(gHandle).credentialLive() {
		t.Error("a write-grade attempt was issued no credential")
	}
}

// The scrub keeps the SOURCE credentials out and leaves the MODEL grant alone.
// They are different grants, and a source grade that cancelled the model's
// would be a read-only review that cannot reach a model at all.
func TestTheReadOnlySandboxScrubsSourceCredentialsAndKeepsTheModelGrant(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"HOME=/home/worker",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"GIT_ASKPASS=/usr/bin/askpass",
		"SSH_ASKPASS=/usr/bin/askpass",
		"GIT_SSH_COMMAND=ssh -i /home/worker/.ssh/id_ed25519",
		"GITHUB_TOKEN=ghp_notreal",
		"GH_TOKEN=ghp_notreal",
		"CI_JOB_PASSWORD=notreal",
		"GOOGLE_APPLICATION_CREDENTIALS=/home/worker/creds.json",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=store",
		"ANTHROPIC_API_KEY=sk-notreal",
		"ANTHROPIC_AUTH_TOKEN=notreal",
		"OPENAI_API_KEY=sk-notreal",
	}
	readOnly := &attemptRecord{SourceGrade: gradeReadOnly}
	remotes := remoteSet{Names: []string{"origin", "upstream"}, URLs: []string{"git@example.com:org/repo.git"}}

	box := sandboxFor(base, readOnly, remotes)
	env := envMap(box.Env)

	for _, gone := range []string{
		"SSH_AUTH_SOCK", "GIT_ASKPASS", "SSH_ASKPASS", "GIT_SSH_COMMAND",
		"GITHUB_TOKEN", "GH_TOKEN", "CI_JOB_PASSWORD", "GOOGLE_APPLICATION_CREDENTIALS",
	} {
		if _, ok := env[gone]; ok {
			t.Errorf("%s survived into a read-only runner's environment", gone)
		}
	}
	for _, kept := range []string{"PATH", "HOME", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY"} {
		if _, ok := env[kept]; !ok {
			t.Errorf("%s was scrubbed; the source grade does not own the model grant", kept)
		}
	}

	// The operator's own GIT_CONFIG_* is replaced wholesale rather than
	// appended to, or its credential.helper would be read alongside the pins.
	pins := pinMap(box.Env)
	if got := pins["credential.helper"]; len(got) != 1 || got[0] != "" {
		t.Errorf("credential.helper pinned to %q; an empty value is what RESETS the helper list", got)
	}
	if got := pins["core.askPass"]; len(got) != 1 || got[0] != "false" {
		t.Errorf("core.askPass pinned to %q", got)
	}
	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Error("a read-only runner can still be asked for a password at the terminal")
	}
	for _, name := range remotes.Names {
		if got := pins["remote."+name+".pushurl"]; len(got) != 1 || got[0] != pushRefusedURL {
			t.Errorf("remote.%s.pushurl pinned to %q, want %q", name, got, pushRefusedURL)
		}
	}
	rewrites := pins["url."+pushRefusedURL+".pushInsteadOf"]
	redirects := strings.Join(rewrites, " ")
	for _, prefix := range []string{"https://", "ssh://", "git@", "/", "git@example.com:org/repo.git"} {
		if !strings.Contains(redirects, prefix) {
			t.Errorf("a push to a url starting %q is not redirected; the pushurl pins only cover remote NAMES", prefix)
		}
	}
	// The empty prefix, which is what makes the set total: a bare relative
	// path starts with none of the named shapes. Joining the values above
	// would hide it, so it is asked for by itself.
	if !slices.Contains(rewrites, pushCatchAll) {
		t.Errorf("there is no catch-all pushInsteadOf entry among %q: a push to a BARE relative path "+
			"(`link`, `sub/origin.git`) starts with no listed prefix and is not rewritten at all", rewrites)
	}

	// A write grade is launched exactly as this process was.
	write := sandboxFor(base, &attemptRecord{SourceGrade: gradeWrite}, remotes)
	if len(write.Env) != len(base) || len(write.Scrubbed) != 0 || len(write.Pins) != 0 {
		t.Errorf("a write grade's environment was modified: %d entries, %d scrubbed, %d pins",
			len(write.Env), len(write.Scrubbed), len(write.Pins))
	}

	// A grade this build cannot read is not a grant.
	unknown := sandboxFor(base, &attemptRecord{SourceGrade: "something-new"}, remotes)
	if len(unknown.Pins) == 0 {
		t.Error("an unrecognised source grade was launched as a write grade; the grade has to fail closed")
	}
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}

// pinMap reads the GIT_CONFIG_COUNT/KEY_n/VALUE_n triple back out of an
// environment the way git itself would, so the test asserts on what git will
// see rather than on the slice that produced it.
func pinMap(env []string) map[string][]string {
	values := envMap(env)
	out := map[string][]string{}
	count, err := strconv.Atoi(values["GIT_CONFIG_COUNT"])
	if err != nil {
		return out
	}
	for i := 0; i < count; i++ {
		key := values[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)]
		out[key] = append(out[key], values[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)])
	}
	return out
}

// A supervisor that is TERM'd settles its attempt. Any stop that is not a
// cancel — a system shutdown, a container stop, an operator's kill — used to
// leave an attempt with no settlement and no live pid, which inspect reads as
// `lost`: not terminal, so every later restart re-adopts it and refuses it
// again, forever.
func TestASignalledSupervisorSettlesTheAttemptItStops(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang", name: "termed"})
	handle := f.Start(f.spec("run-grade/tick-tm/attempt-1", "tm"))
	st := f.store(handle)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return st.runnerPID() > 0 })
	runner := st.runnerPID()

	if err := signalGroup(st.supervisorPID(), sigTerm()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the stopped supervisor to settle the attempt", 20*time.Second, st.settled)
	waitFor(t, "the runner to stop with its supervisor", 20*time.Second, func() bool { return !processAlive(runner) })

	if code, ok := st.exitCode(); !ok || code != 128+int(sigTerm()) {
		t.Errorf("the stopped attempt settled with exit %d (read %v), want %d", code, ok, 128+int(sigTerm()))
	}
	status := f.inspect(handle)
	if status.State != StateFailed || !status.Terminal {
		t.Fatalf("a TERM'd attempt inspects as %s (terminal %v); an unsettled attempt is one nobody can ever settle",
			status.State, status.Terminal)
	}
	if !mentions(status.Observations, "a stop is not a completion") {
		t.Error("the observation log does not say the attempt was settled by a stop rather than by an exit")
	}
}

// Disposal leaves the operator's exclude file as it found it. "No run-created
// worktree and no run-created branch" has to include the one line this
// executor appends to a file it does not own.
//
// The attempt is read-only and commits nothing (a review's deliverable is its
// report), so its branch is still at the base and disposal may delete it: the
// branch-safety refusal is about commits no remote has, and there are none.
func TestDisposalRemovesTheExcludeLineItAdded(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "nocommit", name: "exclude"})
	spec := readOnlySpec(f, "run-grade/tick-ex/attempt-1", "ex")
	handle := f.Start(spec)
	f.waitSettled(handle)

	excludePath := runGit(t, f.Repo.Dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	line := "/" + strings.Trim(spec.ArtifactPrefix, "/")

	if !excludeHas(t, excludePath, line) {
		t.Fatalf("%s does not carry %q, so this test is not looking at the file the executor wrote", excludePath, line)
	}
	if err := f.Executor.Dispose(handle, DisposeOptions{Reason: "the test disposes of what it created"}); err != nil {
		t.Fatal(err)
	}
	if excludeHas(t, excludePath, line) {
		t.Errorf("%q is still in %s after disposal: every attempt this host ever ran would leave one",
			line, excludePath)
	}
}

func excludeHas(t *testing.T, path, line string) bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, have := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(have) == line {
			return true
		}
	}
	return false
}

// -------------------------------------- every shape a push target can take ---
//
// The pins are PREFIX rewrites, so "does `git push origin` fail" is not the
// question. The question is whether any other spelling of the same origin gets
// through — and one did: a BARE relative path (`link`, `sub/origin.git`)
// starts with none of the shapes pushURLPrefixes lists, so nothing rewrote it
// and the ref landed on the remote with no `git -c` trickery at all. These
// three tests are the review's bypass probe, kept.

// sandboxEnvFor is the environment ONE worktree's runner is launched with,
// built by the same sandboxFor the supervisor calls. HOME is a directory of
// the test's own, so the host's global git config cannot decide the answer.
func sandboxEnvFor(t *testing.T, worktree, grade string) []string {
	t.Helper()
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	return sandboxFor(base, &attemptRecord{SourceGrade: grade}, readRemotes(worktree)).Env
}

// pushUnder runs one git command with exactly the environment a grade
// produces. Its output is for the failure message; what the test acts on is
// whether the ref appeared on ORIGIN.
func pushUnder(dir string, env []string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// Probe case 4. Each shape runs TWICE: once under a write grade, where it must
// land the ref — a case that cannot reach origin at all proves nothing — and
// once under the read-only grade, where it must not.
func TestAReadOnlyGradeRefusesEveryShapeAPushTargetCanBeWrittenAs(t *testing.T) {
	repo := newRepo(t, "pushshapes")
	origin, err := filepath.EvalSymlinks(repo.Origin)
	if err != nil {
		t.Fatal(err)
	}

	// Two bare relative paths — no scheme, no leading `/`, `./`, `../` or `~`.
	// These are the spellings that used to reach origin unrewritten.
	if err := os.Symlink(origin, filepath.Join(repo.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo.Dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(origin, filepath.Join(repo.Dir, "sub", "origin.git")); err != nil {
		t.Fatal(err)
	}

	shapes := []struct{ name, target string }{
		{"the remote's name", "origin"},
		{"an absolute path", origin},
		{"a file:// url", "file://" + origin},
		{"a ../ relative path", "../" + filepath.Base(repo.Origin)},
		{"an explicitly relative path", "./link"},
		{"a BARE relative path", "link"},
		{"a BARE relative path one directory down", "sub/origin.git"},
	}

	write := sandboxEnvFor(t, repo.Dir, gradeWrite)
	readOnly := sandboxEnvFor(t, repo.Dir, gradeReadOnly)

	for i, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			// Numbered rather than named: a ref name is not allowed to carry
			// the punctuation these targets are made of.
			control := fmt.Sprintf("refs/heads/control-%d", i)
			refused := fmt.Sprintf("refs/heads/refused-%d", i)

			out := pushUnder(repo.Dir, write, "push", shape.target, "HEAD:"+control)
			if !refExists(t, repo.Origin, control) {
				t.Fatalf("a WRITE grade could not push to %q, so this case says nothing about the read-only "+
					"grade — the target is unreachable, not refused:\n%s", shape.target, out)
			}

			out = pushUnder(repo.Dir, readOnly, "push", shape.target, "HEAD:"+refused)
			if refExists(t, repo.Origin, refused) {
				t.Errorf("a read-only attempt advanced %s on origin by pushing to %q. The grade is the security "+
					"boundary and it is kept at launch: every shape a push target can be written as has to be "+
					"rewritten, including one that starts with no prefix at all:\n%s", refused, shape.target, out)
			}
		})
	}
}

// Probe cases 3 and 5: the residual, pinned so that the sentence four files
// state about it stays true. Both need a DELIBERATE `git -c` on the runner's
// own command line, which overrides an env-pinned key by git's own precedence
// and which no launch configuration can take back.
//
// If either of these ever starts failing, the executor got STRONGER: fix the
// expectation here and then fix the residual sentence in grade.go,
// internal/reconcile/doc.go and profiles/review-epic.md, which all say this
// party is not stopped.
func TestTheReadOnlyGradesResidualIsADeliberateOverrideOnTheCommandLine(t *testing.T) {
	repo := newRepo(t, "residual")
	origin, err := filepath.EvalSymlinks(repo.Origin)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := sandboxEnvFor(t, repo.Dir, gradeReadOnly)

	t.Run("a longer url.<prefix>.pushInsteadOf wins over the pin", func(t *testing.T) {
		url := "file://" + origin + "/"
		out := pushUnder(repo.Dir, readOnly,
			"-c", "url."+url+".pushInsteadOf="+url,
			"push", url, "HEAD:refs/heads/residual-longer-prefix")
		if !refExists(t, repo.Origin, "refs/heads/residual-longer-prefix") {
			t.Errorf("git no longer takes the LONGEST matching pushInsteadOf prefix from the command line, so "+
				"the residual three files describe is narrower than they say:\n%s", out)
		}
	})

	t.Run("a -c remote.<x>.url with its own pushInsteadOf", func(t *testing.T) {
		url := "file://" + origin
		out := pushUnder(repo.Dir, readOnly,
			"-c", "remote.evil.url="+url,
			"-c", "url."+url+".pushInsteadOf="+url,
			"push", "evil", "HEAD:refs/heads/residual-c-remote")
		if !refExists(t, repo.Origin, "refs/heads/residual-c-remote") {
			t.Errorf("a remote invented on the command line no longer reaches origin, so the residual three "+
				"files describe is narrower than they say:\n%s", out)
		}
	})

	// And the half that IS kept whatever the command line rewrites: the
	// sandbox resolves no credential. A local path needs none, which is why
	// the two cases above land; a remote that does need one gets nothing from
	// this process — no helper, no askpass, no terminal.
	t.Run("and it still resolves no credential", func(t *testing.T) {
		cmd := exec.Command("git", "-c", "url.https://forge.example.com/.pushInsteadOf=https://forge.example.com/",
			"credential", "fill")
		cmd.Dir = repo.Dir
		cmd.Env = readOnly
		cmd.Stdin = strings.NewReader("protocol=https\nhost=forge.example.com\n\n")
		out, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(out), "password=") {
			t.Errorf("the read-only sandbox resolved a credential for a host that needs one: %v\n%s", err, out)
		}
	})
}
