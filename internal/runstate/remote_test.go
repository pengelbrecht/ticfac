package runstate

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestARemoteFailureIsClassifiedByWhatTheRemoteActuallySaid is the boundary
// tick enj turns on: which failures the run waits through and which it stops
// for.
//
// The pairs at the bottom are the ones that matter. A connection reset and a
// rejected public key END WITH THE SAME LINE — "fatal: Could not read from
// remote repository." — and if that line alone decided, an authentication
// failure would be retried until the bound was spent, which is its own bug: a
// bounded wait on a credential that will never be right is just a slower
// refusal with the run's clock spent on it.
func TestARemoteFailureIsClassifiedByWhatTheRemoteActuallySaid(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   RemoteClass
	}{
		{
			// The failure that killed run epic-ncv, verbatim but for the host.
			name: "the reset that filed this tick",
			stderr: "Connection reset by remote.invalid port 22\n" +
				"fatal: Could not read from remote repository.",
			want: RemoteTransient,
		},
		{
			name:   "an ssh that never finished connecting",
			stderr: "ssh: connect to host remote.invalid port 22: Connection timed out",
			want:   RemoteTransient,
		},
		{
			name:   "a name that did not resolve",
			stderr: "ssh: Could not resolve hostname remote.invalid: Name or service not known",
			want:   RemoteTransient,
		},
		{
			name:   "the remote shedding load over http",
			stderr: "error: RPC failed; HTTP 502 curl 22 The requested URL returned error: 502",
			want:   RemoteTransient,
		},
		{
			name:   "the pack protocol seeing the same death from inside",
			stderr: "fatal: the remote end hung up unexpectedly\nfatal: early EOF",
			want:   RemoteTransient,
		},
		{
			// GitHub's own load shed: the daemon answers and then drops.
			name:   "the connection closed before a session started",
			stderr: "kex_exchange_identification: Connection closed by remote host",
			want:   RemoteTransient,
		},
		{
			// The same tail line as the reset above. Everything rests on the
			// terminal markers being read first.
			name: "a key the remote rejected",
			stderr: "git@remote.invalid: Permission denied (publickey).\n" +
				"fatal: Could not read from remote repository.",
			want: RemoteTerminal,
		},
		{
			name:   "credentials the remote refused",
			stderr: "remote: Invalid username or password.\nfatal: Authentication failed for 'https://remote.invalid/x'",
			want:   RemoteTerminal,
		},
		{
			name:   "a repository that is not there",
			stderr: "remote: Repository not found.\nfatal: repository 'https://remote.invalid/x' not found",
			want:   RemoteTerminal,
		},
		{
			name:   "a ref that is not there",
			stderr: "fatal: couldn't find remote ref refs/heads/epic/does-not-exist",
			want:   RemoteTerminal,
		},
		{
			name:   "a host key this machine disagrees with",
			stderr: "Host key verification failed.\nfatal: Could not read from remote repository.",
			want:   RemoteTerminal,
		},
		{
			name:   "a prompt the reconciler refuses to answer",
			stderr: "fatal: could not read Username for 'https://remote.invalid': terminal prompts disabled",
			want:   RemoteTerminal,
		},
		{
			// A lost lease is the compare-and-swap doing its job, and the
			// caller reads it out of stderr itself. It must come back on the
			// first attempt or it is a refusal that raced with whatever moved
			// the ref.
			name:   "a lease the remote refused",
			stderr: "! [rejected] deadbeef -> epic/qeu (stale info)",
			want:   RemoteUnclassified,
		},
		{
			name:   "something nobody here has seen before",
			stderr: "fatal: the sky is the wrong colour",
			want:   RemoteUnclassified,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := fmt.Errorf("git fetch: exit status 128: %s", c.stderr)
			if got := ClassifyRemote(err); got != c.want {
				t.Errorf("ClassifyRemote(%q) = %v, want %v", c.stderr, got, c.want)
			}
		})
	}

	if got := ClassifyRemote(nil); got != RemoteUnclassified {
		t.Errorf("ClassifyRemote(nil) = %v, want %v", got, RemoteUnclassified)
	}
}

// TestOnlyTheSubcommandsThatReachTheNetworkAreRetried.
//
// The retry is wired at the runner rather than at the one call site that was
// observed failing, so the runner has to know which invocations can meet a
// network at all. A local plumbing command that failed is a bug to report,
// and running it four times would only report it four times more slowly.
func TestOnlyTheSubcommandsThatReachTheNetworkAreRetried(t *testing.T) {
	remote := [][]string{
		{"fetch", "--quiet", "--no-write-fetch-head", "--refmap=", "origin", "+refs/heads/x:refs/y"},
		{"push", "--force-with-lease=refs/heads/x:abc", "origin", "def:refs/heads/x"},
		{"ls-remote", "--tags", "origin", "refs/tags/t"},
		{"-c", "protocol.version=2", "fetch", "origin"},
	}
	for _, args := range remote {
		if _, ok := RemoteSubcommand(args); !ok {
			t.Errorf("RemoteSubcommand(%v) says local; it reaches the network", args)
		}
	}
	local := [][]string{
		{"rev-parse", "--git-dir"},
		{"ls-tree", "-r", "HEAD", "--", ".ticfac"},
		{"commit-tree", "abc", "-m", "a message with the word push in it"},
		{"worktree", "add", "--detach", "--quiet", "/tmp/x", "abc"},
	}
	for _, args := range local {
		if sub, ok := RemoteSubcommand(args); ok {
			t.Errorf("RemoteSubcommand(%v) = %q, remote; it touches nothing but this repository", args, sub)
		}
	}
}

// TestTheBoundIsSpentAndThenTheRunStops.
//
// Retrying forever recreates the hang the ssh transport bound exists to
// prevent (tick pul): a run alive, holding its workers, emitting nothing. The
// cap is what makes waiting safe, and the error it produces has to NAME THE
// ATTEMPTS — "the remote reset us three times" and "the remote reset us" are
// the same sentence about two very different remotes, and only the first
// tells an operator to go look at their network.
func TestTheBoundIsSpentAndThenTheRunStops(t *testing.T) {
	reset := errors.New("git fetch: exit status 128: Connection reset by remote.invalid port 22")
	var slept []time.Duration
	var notices []RemoteRetryNotice
	retry := RemoteRetry{
		Attempts: 3,
		Backoff:  2 * time.Millisecond,
		Sleep:    func(d time.Duration) { slept = append(slept, d) },
		Report:   func(n RemoteRetryNotice) { notices = append(notices, n) },
	}

	tries := 0
	err := retry.Do("git fetch", func() error { tries++; return reset })
	if err == nil {
		t.Fatal("a remote that never came back returned success")
	}
	if tries != 3 {
		t.Errorf("the operation ran %d times, want the bound of 3", tries)
	}
	if !errors.Is(err, reset) {
		t.Errorf("the bound's error dropped the last failure: %v", err)
	}
	if !strings.Contains(err.Error(), "3 times") {
		t.Errorf("the error does not name the attempts, so a reader cannot tell one reset from three: %v", err)
	}
	// Backoff doubles: a retry that hammers a remote that is already
	// struggling is a retry that makes the outage worse.
	if len(slept) != 2 || slept[0] != 2*time.Millisecond || slept[1] != 4*time.Millisecond {
		t.Errorf("waits were %v, want a doubling 2ms then 4ms", slept)
	}
	if len(notices) != 3 {
		t.Fatalf("%d notices, want two retries and one giving up: %+v", len(notices), notices)
	}
	if notices[0].GaveUp || notices[1].GaveUp {
		t.Error("a retry was reported as giving up")
	}
	if last := notices[2]; !last.GaveUp || last.Of != 3 {
		t.Errorf("the last notice is %+v, want the bound spent at 3 attempts", last)
	}
}

// TestOneResetIsWaitedThroughAndTheWorkGoesOn is the other direction: the
// failure that actually happened, with the remote back on the next attempt.
func TestOneResetIsWaitedThroughAndTheWorkGoesOn(t *testing.T) {
	var notices []RemoteRetryNotice
	retry := RemoteRetry{
		Attempts: 4,
		Backoff:  time.Microsecond,
		Sleep:    func(time.Duration) {},
		Report:   func(n RemoteRetryNotice) { notices = append(notices, n) },
	}

	tries := 0
	err := retry.Do("git fetch", func() error {
		if tries++; tries == 1 {
			return errors.New("git fetch: exit status 128: Connection reset by remote.invalid port 22")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("a reset the remote recovered from still killed the caller: %v", err)
	}
	if tries != 2 {
		t.Errorf("the operation ran %d times, want one failure and one success", tries)
	}
	// A silent retry is how a real outage looks healthy.
	if len(notices) != 1 || notices[0].Attempt != 1 || notices[0].GaveUp {
		t.Errorf("notices were %+v, want exactly one retry reported", notices)
	}
}

// TestAnAuthenticationFailureIsNotRetried.
//
// Ten attempts at a credential that will never be right is its own bug, and
// it is the one a naive "just retry remote errors" fix ships with.
func TestAnAuthenticationFailureIsNotRetried(t *testing.T) {
	for _, stderr := range []string{
		"git@remote.invalid: Permission denied (publickey).\nfatal: Could not read from remote repository.",
		"fatal: couldn't find remote ref refs/heads/epic/nope",
		"! [rejected] abc -> epic/qeu (stale info)",
	} {
		tries := 0
		retry := RemoteRetry{
			Attempts: 5,
			Backoff:  time.Microsecond,
			Sleep:    func(time.Duration) { t.Error("the bound waited on a failure waiting cannot fix") },
			Report:   func(RemoteRetryNotice) { t.Error("a failure that was never retried was reported as retried") },
		}
		err := retry.Do("git push", func() error {
			tries++
			return fmt.Errorf("git push: exit status 128: %s", stderr)
		})
		if err == nil {
			t.Fatal("a refused remote operation returned success")
		}
		if tries != 1 {
			t.Errorf("%q ran %d times, want exactly one: waiting cannot change this answer", stderr, tries)
		}
		// Untouched: the caller's own handling is written against this text.
		if !strings.Contains(err.Error(), stderr) || strings.Contains(err.Error(), "the bound is spent") {
			t.Errorf("the error was rewritten by the bound: %v", err)
		}
	}
}
