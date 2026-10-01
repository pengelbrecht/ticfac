package runstate

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// A remote git failure is two different events wearing one exit status, and
// until tick enj nothing here could tell them apart.
//
// Run epic-ncv died outright on this, mid-fetch, with two workers in flight:
//
//	read tick 9fc: fetch epic/ncv from origin: git fetch --quiet ...:
//	exit status 128: Connection reset by <host> port 22
//	fatal: Could not read from remote repository.
//
// That is the remote resetting one ssh connection. The repository was there,
// the credentials were right, and the very next attempt succeeded. Nothing
// was lost — attempts are durable and a resumed run adopts them — so the
// whole cost was that a person had to be watching: the run sat dead until
// somebody noticed and restarted it.
//
// Epic 9pd's retro named exactly this as the blocker for unattended
// operation: "ticfac cannot yet tell a failure it should wait through from
// one it should stop for. It stops for both, correctly, and a person
// decides." Here the distinction is not subtle — a connection reset mid-fetch
// is the textbook retryable error — so the run decides, for that one class,
// and keeps stopping for everything else.

// RemoteClass is what a failed remote git operation turns out to have been.
type RemoteClass int

const (
	// RemoteUnclassified is the default and the safe one: a failure this
	// package has no evidence about is NOT retried. Retrying an unclassified
	// failure is retrying a bug — the whole point of the classification is
	// that waiting is earned by recognising the error, never assumed.
	RemoteUnclassified RemoteClass = iota
	// RemoteTransient is a failure the run should wait through: the network
	// between here and the remote misbehaved, and the same command run again
	// may well succeed.
	RemoteTransient
	// RemoteTerminal is a failure the run should stop for: the remote
	// understood the request and refused it, or answered about something that
	// is not there. Waiting cannot change any of these answers.
	RemoteTerminal
	// RemoteAuthRefused is the remote refusing this machine's credentials:
	// "Permission denied (publickey)", an https "Authentication failed". It is
	// its own class because it is the one refusal that is ALSO seen as a blip
	// (tick jsz): on 2026-09-24, in a burst of DNS failures, one fetch of
	// epic-yoh got "Permission denied (publickey)" and the identical fetch a
	// minute later succeeded with nothing changed — an ssh-agent that did not
	// answer in time, or a remote shedding its auth backend, reads exactly
	// like a key it does not know. So it is retried a SMALL bound
	// (AuthRefusalAttempts), and when it persists it is refused as
	// remote_auth_refused, naming what to check — never as a stop nobody
	// classified, and never retried forever.
	RemoteAuthRefused
)

// AuthRefusalAttempts bounds an auth refusal: the first attempt and two
// retries, inside the transient bound's backoff. A real credential problem
// costs the run a few seconds more than it used to; a blip in front of a
// working key no longer costs it a person.
//
// Four, not three (epic hn6's cloud run, 2026-09-30): the per-run GitHub App
// token was refused twice running at 12:11 and again at 13:23, and both times
// the third attempt went through — one attempt from the bound, with the whole
// boot riding on it. The fourth costs eight seconds more on a key that is
// really wrong.
const AuthRefusalAttempts = 4

// RemoteAuthRefusedClass is the class's name as a run's stop reason and in
// the refusal's own text, so a reader and a switch statement see one word.
const RemoteAuthRefusedClass = "remote_auth_refused"

// authMarkers are the remote refusing WHO is asking. They are checked after
// the terminal markers, so a "repository not found" that arrives beside one
// stays terminal: that is an answer about the repository, not the key.
//
// "permission denied (" is ssh's own spelling — "Permission denied
// (publickey)." or "(publickey,password)." — and is deliberately narrower
// than a bare "permission denied", which is also what a filesystem says and
// stays terminal. "could not read username" and "terminal prompts disabled"
// are an https remote with no credential the helper would hand over, which a
// keychain that did not answer in time produces too.
var authMarkers = []string{
	"permission denied (",
	"authentication failed",
	"invalid username or password",
	"could not read username",
	"terminal prompts disabled",
	// An https remote that KNOWS the credential and refuses it this
	// operation: GitHub's "remote: Permission to <owner>/<repo>.git denied to
	// <who>." above curl's "The requested URL returned error: 403". Epic
	// hn6's cloud run (2026-09-29) halted twice on exactly this, pushing its
	// own integration branch with the per-run GitHub App token, as "a stop
	// this run has no classification for": the words are "permission to …
	// denied", which neither ssh's "permission denied (" nor the bare
	// "permission denied" below ever matched. The same writes went through on
	// the next boot.
	"the requested url returned error: 403",
	"the requested url returned error: 401",
}

// httpsTokenMarkers say an auth refusal was an HTTPS TOKEN's, not an ssh
// key's. The remedy is a different sentence — the App's permissions, the
// token's scope or life, the credential helper — and a person sent to
// ssh-add over a GitHub App token is looking in the wrong place.
var httpsTokenMarkers = []string{
	"the requested url returned error: 403",
	"the requested url returned error: 401",
	"invalid username or password",
	"authentication failed for 'http",
}

// IsHTTPSTokenRefusal answers whether an auth refusal came from an https
// remote refusing a token rather than an ssh remote refusing a key.
func IsHTTPSTokenRefusal(err error) bool {
	return err != nil && containsAny(strings.ToLower(err.Error()), httpsTokenMarkers)
}

// AuthRefusalRemedy is what to check about a refused credential, worded for
// the credential that was refused. The feed line and the halt both quote it,
// so the two never send a person to different places.
func AuthRefusalRemedy(err error) string {
	if IsHTTPSTokenRefusal(err) {
		return "the https remote refused this run's token. On the factory's GitHub App rung the likely " +
			"causes are a permission the installation token lacks (contents: write for any push; " +
			"workflows: write for a push whose commits touch .github/workflows) or a token that expired " +
			"because the container's git credential helper could not get a fresh one from the factory's " +
			"token door and fell back to the token it booted with (its warning is in the stderr below). " +
			"Check `ticfac factory status` (the App's live mint) and accept any permissions the App " +
			"requests on its installation's settings page; for a PAT, check it is unexpired and can push " +
			"(with the workflow scope)"
	}
	return "check that ssh-agent is running and holds the key (ssh-add -l), that the key is one the remote " +
		"knows (ssh -T git@github.com), that the key or deploy key has access to this repository, and for an " +
		"https remote that `gh auth status` is logged in and the credential helper answers"
}

// RemoteAuthRefusedError is an auth refusal that outlived its bound. Its text
// starts with the class and the remedy, on one line, because a supervisor's
// halt line quotes a stop's first line and that line must say what to do.
type RemoteAuthRefusedError struct {
	What     string
	Attempts int
	Err      error
}

func (e *RemoteAuthRefusedError) Error() string {
	return fmt.Sprintf("%s: %s was refused authentication %d times running, so this is not a blip; %s: %v",
		RemoteAuthRefusedClass, e.What, e.Attempts, AuthRefusalRemedy(e.Err), e.Err)
}

func (e *RemoteAuthRefusedError) Unwrap() error { return e.Err }

// terminalMarkers are the things a remote says when the ANSWER is no. A ref
// that does not exist does not appear because it was asked for again;
// retrying any of these is just a slower refusal, with the run's wall clock
// spent on it. (A refused credential is the exception that earned its own
// class and a small bound: see RemoteAuthRefused.)
//
// "host key verification failed" is here rather than below on purpose: it
// reads like a network error and is a configuration one — the host key this
// machine holds disagrees with the one the remote presented, and no amount of
// waiting reconciles them.
//
// They are read in two passes around authMarkers (see ClassifyRemote). The
// answers about the REPOSITORY come first and win over an auth refusal in the
// same text; the bare "permission denied" and "access denied" come after, so
// that ssh's "Permission denied (publickey)" is read as the auth refusal it
// is while any other "permission denied" stays terminal.
var terminalMarkers = []string{
	"repository not found",
	"does not appear to be a git repository",
	"couldn't find remote ref",
	"host key verification failed",
}

var deniedMarkers = []string{
	"permission denied",
	"access denied",
}

// transientMarkers are the things the transport says when nothing got an
// answer at all: the connection died, the name did not resolve, or the remote
// shed load. Every one of these is a sentence about the PIPE, not about the
// request.
//
// "could not read from remote repository" is the interesting one, and it is
// why the two lists are consulted in the order they are — see ClassifyRemote.
// "the remote end hung up unexpectedly", "early eof" and the sideband
// disconnects are the same event seen from inside the pack protocol instead
// of from the socket. The http 5xx spellings cover the smart-http transport,
// where a reset shows up as a status code rather than as a dead socket.
var transientMarkers = []string{
	"connection reset",
	"connection timed out",
	"connection closed by remote host",
	"operation timed out",
	"timed out",
	"broken pipe",
	"network is unreachable",
	"no route to host",
	"could not resolve host",
	"name or service not known",
	"temporary failure in name resolution",
	"ssh_exchange_identification",
	"kex_exchange_identification",
	"early eof",
	"the remote end hung up unexpectedly",
	"unexpected disconnect while reading sideband packet",
	"rpc failed",
	"the requested url returned error: 5",
	"could not read from remote repository",
	// The HTTPS transport's words for a TCP connect that never completed
	// (curl, under git's https remote): seen on 2026-09-24 when this host's
	// IPv4 flapped, "Failed to connect to github.com port 443 after 62 ms:
	// Couldn't connect to server". Nothing was asked, so nothing was refused.
	"failed to connect to",
	"couldn't connect to server",
	// curl giving up on a connection that went silent (curl 28,
	// CURLE_OPERATION_TIMEDOUT): gitbin.TransportEnv's low-speed bound firing
	// on an https remote that accepted the connection and stopped answering —
	// epic-6in's thirty-minute checkpoint push, 2026-09-28, once bounded.
	// Waiting on the first request of a push or fetch, git says only
	// "fatal: unable to access '<url>': Operation too slow. Less than 1000
	// bytes/sec transferred the last 60 seconds", with no "RPC failed" and no
	// "timed out" to recognise it by; mid-pack it is "error: RPC failed; curl
	// 28 Operation too slow ...". Nothing was answered, so nothing was refused.
	"operation too slow",
	// The remote's own storage failing mid-push: the request reached it and
	// was never answered with a no — the server could not write the pack it
	// had just received. Seen on 2026-09-27, when epic-2jn's checkpoint push
	// halted the run over GitHub saying "remote: error: unable to write file
	// .../objects/.../pack-<sha>.pack: No such file or directory", "remote:
	// fatal error in commit_refs", "! [remote rejected] <sha> -> epic/2jn
	// (failure)". The ref did not move, and the same push minutes later went
	// through. Each marker carries its "remote: " prefix, so this machine's
	// own disk failing ("error: unable to write file" with no prefix) is not
	// read as the remote's.
	"remote: fatal error in commit_refs",
	"remote: error: unable to write file",
	"remote: fatal: unable to rename temporary",
	"remote: internal server error",
}

// ClassifyRemote reads a failed git command's error — which carries the
// command's stderr, because every runner in this repository wraps it in
// (git %s: %w: %s) precisely so a refusal can be classified — and says
// whether the run should wait through it or stop for it.
//
// TERMINAL IS CHECKED FIRST, and that ordering is the whole trick. Both a
// connection reset and a rejected public key end with the same line:
//
//	fatal: Could not read from remote repository.
//
// What tells them apart is what git said ABOVE it — "Connection reset by
// <host> port 22" against "Permission denied (publickey)". Reading the
// terminal markers first is what lets "could not read from remote
// repository" be a transient marker at all: it is the tail of a handshake
// that succeeded and then died, unless something in the same text already
// said the remote answered and the answer was no.
//
// A rejected key is its own class, RemoteAuthRefused, read in that same first
// pass (tick jsz): it is still never mistaken for a reset, but it is no longer
// a flat terminal either, because the observed rejected key was a blip.
func ClassifyRemote(err error) RemoteClass {
	if err == nil {
		return RemoteUnclassified
	}
	text := strings.ToLower(err.Error())
	switch {
	case containsAny(text, terminalMarkers):
		return RemoteTerminal
	case containsAny(text, authMarkers):
		return RemoteAuthRefused
	case containsAny(text, deniedMarkers):
		return RemoteTerminal
	case containsAny(text, transientMarkers), remoteRefFailed.MatchString(text):
		return RemoteTransient
	}
	return RemoteUnclassified
}

// remoteRefFailed is a remote that took a push and then failed to move the
// ref, saying no more than that: "! [remote rejected] <sha> -> <ref> (failed)"
// (or "(failure)"), with no hook, no protection rule and no lease named.
// Epic hn6's cloud run (2026-09-30) lost its first orchestrator boot to
// exactly this on a claim push — "a stop this run has no classification for"
// — and the next boot's identical push went through. The ref did not move,
// the push is lease-protected, and a hook that declined or a lease that was
// stale says so in the parentheses, which is why only these two bare words
// match.
var remoteRefFailed = regexp.MustCompile(`\[remote rejected\][^\n]*\((failed|failure)\)`)

func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// RemoteRetryNotice is one retry, handed to whoever is in a position to say
// so. A silent retry is how a real outage looks healthy: a run that spent two
// minutes waiting on a remote that was down must LOOK like a run that spent
// two minutes waiting on a remote that was down, in the feed, while it is
// happening — not like a run that was briefly quiet.
type RemoteRetryNotice struct {
	// What is the operation, as a person would name it: "git fetch".
	What string
	// Attempt is the 1-based attempt that just failed, and Of is the bound.
	Attempt int
	Of      int
	// Wait is how long before the next attempt, and is zero when GaveUp.
	Wait time.Duration
	// Err is the failure this attempt produced.
	Err error
	// GaveUp is set on the last notice, the one that says the bound is spent.
	GaveUp bool
	// Class is what the failure was: RemoteTransient, or RemoteAuthRefused —
	// whose Attempt and Of count against the auth bound, not the transient
	// one. A feed line saying "failed transiently" about a refused key sends
	// the person reading it to look at their network instead of their key.
	Class RemoteClass
}

// RemoteRetry bounds how long a transient remote failure is waited through.
//
// The BOUND is what makes this safe, and it is not a detail. Retrying forever
// recreates the exact hang the transport bound (tick pul; gitbin.TransportEnv)
// exists to prevent: a run alive, holding its workers, emitting
// nothing, indistinguishable from a run that is working. This repository's
// standing rule is that a hang is worse than a refusal, and an unbounded
// retry is a hang assembled out of refusals.
type RemoteRetry struct {
	// Attempts is the total number of tries, the first one included. One
	// means no retry at all.
	Attempts int
	// Backoff is the wait after the first failure; each later wait doubles it.
	Backoff time.Duration
	// Sleep is how the wait is spent. A test replaces it, so that a backoff
	// measured in seconds is testable in microseconds without the backoff
	// itself becoming a test-only number.
	Sleep func(time.Duration)
	// Report is told about every retry and about giving up. Nil is valid and
	// means nobody is listening — a store opened outside a run has no feed to
	// write to — but inside a run it is always set, because a retry nobody
	// can see is the failure mode this whole file is guarding against.
	Report func(RemoteRetryNotice)
}

// DefaultRemoteAttempts and DefaultRemoteBackoff are the bound: four tries,
// waiting 2s, 4s and 8s between them, so fourteen seconds of waiting at the
// outside. Each attempt is itself bounded by gitbin.TransportEnv — about a minute of
// silence before ssh or curl concludes a connection is gone — so the worst case is a
// little over four minutes before the run stops and says so. That is long
// enough to ride out a reset and a short blip, and short enough that a person
// reading the feed sees a run that is stuck on the network rather than a run
// that has quietly become a process.
const (
	DefaultRemoteAttempts = 4
	DefaultRemoteBackoff  = 2 * time.Second
)

// normalized fills the zero value in, so that a caller that supplied nothing
// still gets the bound rather than a struct that retries zero times.
func (rr RemoteRetry) normalized() RemoteRetry {
	if rr.Attempts <= 0 {
		rr.Attempts = DefaultRemoteAttempts
	}
	if rr.Backoff <= 0 {
		rr.Backoff = DefaultRemoteBackoff
	}
	if rr.Sleep == nil {
		rr.Sleep = time.Sleep
	}
	if rr.Report == nil {
		rr.Report = func(RemoteRetryNotice) {}
	}
	return rr
}

// Do runs op, waiting through transient remote failures up to the bound.
//
// A failure that is not transient — terminal, or unrecognised — comes back
// immediately and untouched, because the caller's own error handling is
// written against it: the run-state CAS reads a push's refusal out of stderr,
// and a refusal that arrived four attempts late is a refusal that raced with
// whatever moved the ref.
//
// When the bound is spent the error NAMES THE ATTEMPTS. A run that retried
// four times and gave up must report that it did, not just hand back the last
// error as though it had happened once: those are the same sentence about two
// very different remotes, and the difference is the only thing that tells an
// operator whether to look at their network or at their credentials.
func (rr RemoteRetry) Do(what string, op func() error) error {
	rr = rr.normalized()
	var err error
	var waited time.Duration
	refused := 0
	authBound := min(AuthRefusalAttempts, rr.Attempts)
	for attempt := 1; ; attempt++ {
		if err = op(); err == nil {
			return nil
		}
		notice := RemoteRetryNotice{What: what, Attempt: attempt, Of: rr.Attempts, Err: err, Class: RemoteTransient}
		switch ClassifyRemote(err) {
		case RemoteTransient:
		case RemoteAuthRefused:
			// Waited through a SMALL bound of its own, inside the transient
			// one: a blip gets its retries, and a key that is really wrong is
			// refused seconds later under its own name with what to check.
			refused++
			notice.Class, notice.Attempt, notice.Of = RemoteAuthRefused, refused, authBound
			if refused >= authBound || attempt >= rr.Attempts {
				notice.GaveUp = true
				rr.Report(notice)
				return &RemoteAuthRefusedError{What: what, Attempts: refused, Err: err}
			}
		default:
			return err
		}
		if attempt >= rr.Attempts {
			break
		}
		notice.Wait = rr.Backoff << (attempt - 1)
		rr.Report(notice)
		rr.Sleep(notice.Wait)
		waited += notice.Wait
	}
	rr.Report(RemoteRetryNotice{What: what, Attempt: rr.Attempts, Of: rr.Attempts, Err: err, GaveUp: true,
		Class: RemoteTransient})
	return fmt.Errorf("%s failed %d times over %s, every one a transient remote failure, and the bound is spent: %w",
		what, rr.Attempts, waited, err)
}

// remoteSubcommands are the git subcommands that talk to a remote, which is
// to say the ones a reset can land on. The tick was filed against a fetch;
// the same reset lands on a push, and on the ls-remote the tracker reads
// origin's head with, so the retry is wired at the runner rather than at the
// one call site that happened to be observed failing.
//
// The names are one string rather than quoted map keys because tick wdb's
// guard (TestEveryProductionFetchStaysOffSharedRefs) reads every production
// line carrying the fetch subcommand as a quoted literal and requires
// --no-write-fetch-head and --refmap= beside it. That guard is right about
// every line which ISSUES a fetch. This one issues nothing — it is a
// vocabulary — and spelling it like a command line would be claiming to be
// one.
var remoteSubcommands = func() map[string]bool {
	set := map[string]bool{}
	for _, name := range strings.Fields("fetch push ls-remote pull clone") {
		set[name] = true
	}
	return set
}()

// RemoteSubcommand answers whether an argv reaches the network, and names the
// subcommand if it does. Leading global flags are skipped so that a runner
// that prepends its own `-c key=value` is still understood.
func RemoteSubcommand(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			return arg, remoteSubcommands[arg]
		}
		// These global flags take their value as the NEXT argument, so it must
		// be skipped too or a path would be read as the subcommand. The list
		// is closed on purpose: treating every unrecognised `--flag` as
		// value-taking would swallow the subcommand itself.
		switch arg {
		case "-c", "-C", "--git-dir", "--work-tree", "--namespace", "--exec-path":
			i++
		}
	}
	return "", false
}
