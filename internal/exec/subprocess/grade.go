package subprocess

import (
	"fmt"
	"strconv"
	"strings"
)

// The SOURCE GRADE, made a property of the launched process rather than of the
// runner's manners.
//
// The grade used to turn off exactly one thing: the supervisor's own timed
// push (attemptRecord.canPush). That is the executor declining to push on the
// job's behalf — it is not the job being unable to push. The runner was
// started with the operator's whole environment, in a LINKED worktree of the
// operator's repository, which shares that repository's remotes, its git
// config and everything a `git push` would authenticate with. A read-only
// review job could advance any ref it liked and nothing would refuse it or
// even notice: the boundary diff (collect.go) reads the attempt's OWN branch,
// so a push somewhere else leaves no trace in it.
//
// So the grade is applied where the process is created. For a read-only grade
// the runner is launched with:
//
//   - every SOURCE credential removed from its environment — the ssh agent,
//     the askpass helpers, the ssh command override and every forge token —
//     while the MODEL credential is left alone, because the model grant is a
//     different grant and the source grade does not own it;
//
//   - git configuration PINNED through GIT_CONFIG_COUNT/GIT_CONFIG_KEY_n/
//     GIT_CONFIG_VALUE_n, which git reads at a higher precedence than the
//     repository's own config: the credential helper list is reset to empty,
//     core.askPass runs `false`, terminal prompting is off, every remote's
//     pushurl points at a transport no helper exists for, and url.<refused>.
//     pushInsteadOf redirects every URL shape a push could name to the same
//     place. pushInsteadOf rewrites PUSHES only, so a read-only job can still
//     fetch and read — which is what a review job is for.
//
// What this is not: a kernel sandbox. A process that can write files can still
// write to a filesystem path it can reach, and a runner that deliberately
// passes `git -c` on its own command line overrides an env-pinned key. The
// claim this file earns is the narrower and checkable one — a read-only
// attempt is issued no source credential and its git resolves no push target
// — and it is why the executor ALSO diffs every attempt against its recorded
// base and reports what it finds (Appendix A #10: compliance is not a property
// of the model).

// The two grades contracts/job-protocol.json allows. Anything that is not
// exactly "write" is treated as read-only, because a grade this executor
// cannot read is not a grant.
const (
	gradeReadOnly = "read-only"
	gradeWrite    = "write"
)

// pushRefusedURL is where a read-only attempt's pushes are sent instead of a
// remote: a scheme no `git-remote-*` helper implements, so a push fails at the
// transport with a message that names the reason rather than hanging on a
// credential prompt nobody will answer.
const pushRefusedURL = "ticfac-read-only-grade://refused-by-issuer"

// pushURLPrefixes is every shape a push target can start with. Each becomes a
// url.<pushRefusedURL>.pushInsteadOf entry, so an explicit `git push <url>`
// that names no configured remote is rewritten too — the remote.<name>.pushurl
// pins below only cover the remotes the worktree already has.
var pushURLPrefixes = []string{
	"https://", "http://", "ssh://", "git://", "ftp://", "ftps://", "file://",
	"git@", "/", "./", "../", "~",
}

// sourceCredentialEnv is the environment a git write authenticates with. Every
// one of these is removed for a read-only grade.
var sourceCredentialEnv = []string{
	"SSH_AUTH_SOCK", "SSH_AGENT_PID", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE",
	"GIT_ASKPASS", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT",
	"GIT_CREDENTIAL_HELPER", "GIT_USERNAME", "GIT_PASSWORD",
	// The pinned config below is passed as these very variables; an inherited
	// set would collide with it and silently win or lose depending on count.
	"GIT_CONFIG_COUNT",
}

// sourceCredentialSubstrings catches the forge tokens nobody can enumerate:
// GH_TOKEN, GITHUB_TOKEN, GITLAB_TOKEN, a CI's injected password, a cloud
// credential file path.
var sourceCredentialSubstrings = []string{"TOKEN", "PASSWORD", "PASSWD", "CREDENTIAL", "SECRET"}

// modelCredentialPrefixes are the variables the substring sweep must NOT take:
// they carry the MODEL grant, which the source grade does not own. Scrubbing
// ANTHROPIC_AUTH_TOKEN because it contains "TOKEN" would be the source grade
// cancelling a credential the job was issued on purpose — and the symptom
// would be a runner that cannot reach a model at all.
var modelCredentialPrefixes = []string{
	"ANTHROPIC_", "CLAUDE_", "OPENAI_", "AZURE_OPENAI_", "OPENROUTER_",
	"XAI_", "GEMINI_", "GOOGLE_GENAI_", "PI_",
}

// readOnly reports whether this attempt may advance no ref. It fails CLOSED: a
// grade this build does not recognise is not a write grant.
func (r *attemptRecord) readOnly() bool { return r.SourceGrade != gradeWrite }

// sandbox is the environment one attempt's runner is launched with, and the
// two lists that say what the grade did — recorded as an observation, because
// "which credentials did this attempt hold" is a question a durable record has
// to be able to answer after the process is gone.
type sandbox struct {
	Env      []string
	Scrubbed []string
	Pins     [][2]string
}

// remoteSet is what a worktree's remotes resolve to: the names, so each one's
// pushurl can be pinned, and the URLs, so an explicit push to one of them is
// redirected too.
type remoteSet struct {
	Names []string
	URLs  []string
}

// sandboxFor builds the launch environment for one attempt. A write grade is
// launched exactly as this process was — the grade that may push is the grade
// nothing is taken from.
func sandboxFor(base []string, record *attemptRecord, remotes remoteSet) sandbox {
	if !record.readOnly() {
		return sandbox{Env: append([]string{}, base...)}
	}

	pins := readOnlyPins(remotes)
	env := make([]string, 0, len(base)+2*len(pins)+2)
	var scrubbed []string
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			env = append(env, entry)
			continue
		}
		if isSourceCredential(name) {
			scrubbed = append(scrubbed, name)
			continue
		}
		env = append(env, entry)
	}
	// A prompt is the other way a credential arrives. GIT_TERMINAL_PROMPT=0
	// and core.askPass=false together mean a push that needs one fails rather
	// than blocking a headless runner forever.
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	env = append(env, gitConfigEnv(pins)...)
	return sandbox{Env: env, Scrubbed: scrubbed, Pins: pins}
}

// isSourceCredential is the scrub rule: a named variable, or one whose name
// carries a secret by convention and does not belong to the model grant.
func isSourceCredential(name string) bool {
	upper := strings.ToUpper(name)
	for _, known := range sourceCredentialEnv {
		if upper == known {
			return true
		}
	}
	// GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n, which the pins replace wholesale.
	if strings.HasPrefix(upper, "GIT_CONFIG_KEY_") || strings.HasPrefix(upper, "GIT_CONFIG_VALUE_") {
		return true
	}
	for _, prefix := range modelCredentialPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return false
		}
	}
	for _, needle := range sourceCredentialSubstrings {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}

// readOnlyPins is the git configuration a read-only attempt runs under. The
// order matters only for readability; git applies them all.
func readOnlyPins(remotes remoteSet) [][2]string {
	pins := [][2]string{
		// An empty value RESETS the helper list, so a helper configured in the
		// system, global or repository config is discarded rather than added
		// to — env config is read after all three.
		{"credential.helper", ""},
		{"credential.interactive", "never"},
		{"core.askPass", "false"},
	}
	for _, name := range remotes.Names {
		pins = append(pins, [2]string{"remote." + name + ".pushurl", pushRefusedURL})
	}
	// url.<base>.pushInsteadOf is multi-valued, so every prefix and every
	// known remote URL is its own entry under the same key.
	seen := map[string]bool{}
	add := func(prefix string) {
		if prefix == "" || seen[prefix] {
			return
		}
		seen[prefix] = true
		pins = append(pins, [2]string{"url." + pushRefusedURL + ".pushInsteadOf", prefix})
	}
	for _, prefix := range pushURLPrefixes {
		add(prefix)
	}
	for _, url := range remotes.URLs {
		add(url)
	}
	return pins
}

// gitConfigEnv spells pinned configuration the way git reads it from an
// environment: GIT_CONFIG_COUNT and one KEY/VALUE pair per entry. It is the
// only way to hand git configuration that a repository's own config cannot
// override, and it needs no file this executor would then have to clean up.
func gitConfigEnv(pins [][2]string) []string {
	out := make([]string, 0, 1+2*len(pins))
	out = append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(len(pins)))
	for i, pin := range pins {
		out = append(out,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, pin[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, pin[1]))
	}
	return out
}

// note is the one sentence the observation log carries about the grade, so a
// collected attempt can still say what its runner was and was not holding.
func (s sandbox) note(record *attemptRecord) string {
	if !record.readOnly() {
		return fmt.Sprintf("source grade %s, remote %q: the attempt holds a push credential",
			record.SourceGrade, record.Remote)
	}
	return fmt.Sprintf(
		"source grade %s: the model credential only. No push credential file was issued, "+
			"%d source credential variable(s) were removed from the runner's environment and %d git config key(s) "+
			"are pinned so no push resolves to a remote",
		record.SourceGrade, len(s.Scrubbed), len(s.Pins))
}
