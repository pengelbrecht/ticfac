package runstate

import (
	"fmt"
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
)

// terminalMarkers are the things a remote says when the ANSWER is no. A
// credential that is wrong is wrong on the tenth attempt too, and a ref that
// does not exist does not appear because it was asked for again; retrying any
// of these is just a slower refusal, with the run's wall clock spent on it.
//
// "host key verification failed" is here rather than below on purpose: it
// reads like a network error and is a configuration one — the host key this
// machine holds disagrees with the one the remote presented, and no amount of
// waiting reconciles them.
var terminalMarkers = []string{
	"permission denied",
	"authentication failed",
	"invalid username or password",
	"access denied",
	"repository not found",
	"does not appear to be a git repository",
	"couldn't find remote ref",
	"could not read username",
	"terminal prompts disabled",
	"host key verification failed",
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
func ClassifyRemote(err error) RemoteClass {
	if err == nil {
		return RemoteUnclassified
	}
	text := strings.ToLower(err.Error())
	for _, marker := range terminalMarkers {
		if strings.Contains(text, marker) {
			return RemoteTerminal
		}
	}
	for _, marker := range transientMarkers {
		if strings.Contains(text, marker) {
			return RemoteTransient
		}
	}
	return RemoteUnclassified
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
}

// RemoteRetry bounds how long a transient remote failure is waited through.
//
// The BOUND is what makes this safe, and it is not a detail. Retrying forever
// recreates the exact hang the ssh transport bound (tick pul, TransportEnv
// above) exists to prevent: a run alive, holding its workers, emitting
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
// outside. Each attempt is itself bounded by TransportEnv — about a minute of
// silence before ssh concludes a connection is gone — so the worst case is a
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
	for attempt := 1; ; attempt++ {
		if err = op(); err == nil {
			return nil
		}
		if ClassifyRemote(err) != RemoteTransient {
			return err
		}
		if attempt >= rr.Attempts {
			break
		}
		wait := rr.Backoff << (attempt - 1)
		rr.Report(RemoteRetryNotice{What: what, Attempt: attempt, Of: rr.Attempts, Wait: wait, Err: err})
		rr.Sleep(wait)
		waited += wait
	}
	rr.Report(RemoteRetryNotice{What: what, Attempt: rr.Attempts, Of: rr.Attempts, Err: err, GaveUp: true})
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
