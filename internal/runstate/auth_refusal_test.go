package runstate

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tick jsz. epic-yoh, 2026-09-24 14:34 UTC: in a burst of network trouble,
// one fetch of an attempt branch got "git@github.com: Permission denied
// (publickey)" and the same fetch succeeded a minute later with nothing
// changed. The refusal was classified terminal, the run stopped over "a stop
// this run has no classification for", and a person had to restart it.

const publickeyRefusal = "git@github.com: Permission denied (publickey).\n" +
	"fatal: Could not read from remote repository."

// githubAppPushRefusal is what GitHub said to epic hn6's cloud orchestrator
// (2026-09-29) pushing its own integration branch over https with the run's
// GitHub App installation token — twice, each time ending the boot as "a stop
// this run has no classification for". Verbatim but for the shas.
const githubAppPushRefusal = "remote: Permission to pengelbrecht/ticfac.git denied to ticfac[bot].\n" +
	"fatal: unable to access 'https://github.com/pengelbrecht/ticfac.git/': The requested URL returned error: 403"

// TestAnHTTPSTokenRefusalIsRetriedABoundAndThenNamedForTheToken: the hn6
// refusal is an auth refusal — waited through a small bound, because each
// attempt runs the credential helper again and so asks the factory for a
// fresh token — and when it persists the refusal names a TOKEN's causes (the
// App's permissions, workflows: write, an expired token behind a helper that
// fell back), not an ssh key's.
// short: remote failure classification over captured stderr, with a stubbed sleep
func TestAnHTTPSTokenRefusalIsRetriedABoundAndThenNamedForTheToken(t *testing.T) {
	refusal := errors.New("git push --force-with-lease=refs/heads/epic/e1:" + strings.Repeat("a", 40) +
		" origin " + strings.Repeat("b", 40) + ":refs/heads/epic/e1: exit status 128: " + githubAppPushRefusal)

	t.Run("a one-off refusal is waited through", func(t *testing.T) {
		retry := RemoteRetry{Attempts: 4, Backoff: time.Microsecond, Sleep: func(time.Duration) {}}
		tries := 0
		err := retry.Do("git push", func() error {
			if tries++; tries == 1 {
				return refusal
			}
			return nil
		})
		if err != nil || tries != 2 {
			t.Fatalf("a one-off token refusal stopped the caller after %d tries: %v", tries, err)
		}
	})

	t.Run("a persistent refusal is refused by name, with the token's remedy", func(t *testing.T) {
		retry := RemoteRetry{Attempts: 10, Backoff: time.Microsecond, Sleep: func(time.Duration) {}}
		tries := 0
		err := retry.Do("git push", func() error { tries++; return refusal })
		if tries != AuthRefusalAttempts {
			t.Errorf("a persistent token refusal ran %d times, want the auth bound of %d", tries, AuthRefusalAttempts)
		}
		var named *RemoteAuthRefusedError
		if !errors.As(err, &named) {
			t.Fatalf("a persistent token refusal is not a RemoteAuthRefusedError: %v", err)
		}
		first, _, _ := strings.Cut(err.Error(), "\n")
		for _, want := range []string{RemoteAuthRefusedClass, fmt.Sprintf("%d times", AuthRefusalAttempts), "https remote refused this run's token",
			"contents: write", "workflows: write", "credential helper", "ticfac factory status",
			"denied to ticfac[bot]"} {
			if !strings.Contains(first, want) {
				t.Errorf("the refusal's first line does not say %q: %s", want, first)
			}
		}
		if strings.Contains(first, "ssh-add") {
			t.Errorf("a refused https token sends the reader to ssh-add: %s", first)
		}
	})
}

// TestAnAuthRefusalIsRetriedABoundAndThenNamed is the rule over RemoteRetry
// itself: a small bound of retries, and a named refusal with the remedy when
// the refusal persists.
// short: remote failure classification over captured stderr, with a stubbed sleep
func TestAnAuthRefusalIsRetriedABoundAndThenNamed(t *testing.T) {
	refusal := errors.New("git fetch: exit status 128: " + publickeyRefusal)

	t.Run("a one-off refusal is waited through", func(t *testing.T) {
		var notices []RemoteRetryNotice
		retry := RemoteRetry{Attempts: 4, Backoff: time.Microsecond, Sleep: func(time.Duration) {},
			Report: func(n RemoteRetryNotice) { notices = append(notices, n) }}
		tries := 0
		err := retry.Do("git fetch", func() error {
			if tries++; tries == 1 {
				return refusal
			}
			return nil
		})
		if err != nil {
			t.Fatalf("a one-off key refusal still stopped the caller: %v", err)
		}
		if tries != 2 || len(notices) != 1 || notices[0].GaveUp {
			t.Errorf("tries=%d notices=%+v, want one retry, said out loud", tries, notices)
		}
	})

	t.Run("a persistent refusal is refused by name", func(t *testing.T) {
		var slept []time.Duration
		retry := RemoteRetry{Attempts: 10, Backoff: time.Millisecond,
			Sleep: func(d time.Duration) { slept = append(slept, d) }}
		tries := 0
		err := retry.Do("git fetch", func() error { tries++; return refusal })
		if tries != AuthRefusalAttempts {
			t.Errorf("a persistent refusal ran %d times, want its own bound of %d, not the transient one",
				tries, AuthRefusalAttempts)
		}
		if len(slept) != AuthRefusalAttempts-1 || slept[1] != 2*slept[0] {
			t.Errorf("waits were %v, want a doubling backoff between the attempts", slept)
		}
		assertNamedAuthRefusal(t, err, refusal.Error())
	})

	t.Run("the auth bound never outlasts the transient one", func(t *testing.T) {
		retry := RemoteRetry{Attempts: 2, Backoff: time.Microsecond, Sleep: func(time.Duration) {}}
		tries := 0
		err := retry.Do("git fetch", func() error { tries++; return refusal })
		var named *RemoteAuthRefusedError
		if tries != 2 || !errors.As(err, &named) {
			t.Errorf("tries=%d err=%v, want 2 tries and the named refusal", tries, err)
		}
	})
}

// assertNamedAuthRefusal checks the refusal a person reads: the class, and
// what to check, on the FIRST line — which is the line a supervisor's halt
// quotes — and the remote's own words still inside it.
func assertNamedAuthRefusal(t *testing.T, err error, underlying string) {
	t.Helper()
	var named *RemoteAuthRefusedError
	if !errors.As(err, &named) {
		t.Fatalf("a persistent refusal is not a RemoteAuthRefusedError: %v", err)
	}
	if ClassifyRemote(err) != RemoteAuthRefused {
		t.Errorf("the named refusal no longer classifies as one: %v", err)
	}
	if !strings.Contains(err.Error(), "Permission denied (publickey)") {
		t.Errorf("the named refusal lost what the remote said (%q): %v", underlying, err)
	}
	first, _, _ := strings.Cut(err.Error(), "\n")
	for _, want := range []string{RemoteAuthRefusedClass, fmt.Sprintf("%d times", AuthRefusalAttempts), "ssh-agent", "ssh-add -l",
		"deploy key", "gh auth status"} {
		if !strings.Contains(first, want) {
			t.Errorf("the refusal's first line does not say %q: %s", want, first)
		}
	}
}

// TestAOneOffKeyRefusalIsWaitedThroughAndTheRunContinues is the incident end
// to end, over a real ssh transport that refuses the key once and then works.
func TestAOneOffKeyRefusalIsWaitedThroughAndTheRunContinues(t *testing.T) {
	o := newOrigin(t)
	transport := newFlap(t, 1, publickeyRefusal)
	var notices []RemoteRetryNotice
	s := o.overSSH(t, RemoteRetry{
		Attempts: 4,
		Backoff:  time.Millisecond,
		Sleep:    func(time.Duration) {},
		Report:   func(n RemoteRetryNotice) { notices = append(notices, n) },
	})

	if _, err := s.Fetch(); err != nil {
		t.Fatalf("one refused key during a flap stopped the run: %v", err)
	}
	if got := transport.invocations(); got != 2 {
		t.Errorf("the transport was reached for %d times, want the refusal and the retry", got)
	}
	if len(notices) != 1 || notices[0].GaveUp {
		t.Errorf("notices were %+v, want one retry said out loud", notices)
	}
	if got, err := s.CreateIfAbsent(CheckpointPath("r-flap"), []byte(`{"sequence":1}`)); err != nil || got != Created {
		t.Fatalf("the write after the recovered fetch: %v %v", got, err)
	}
}

// TestAPersistentKeyRefusalStopsTheRunByNameWithTheRemedy: a key the remote
// really does not accept is refused after the small bound — not retried for
// the whole transient budget, and not as a stop nobody classified.
func TestAPersistentKeyRefusalStopsTheRunByNameWithTheRemedy(t *testing.T) {
	o := newOrigin(t)
	transport := newFlap(t, 1000, publickeyRefusal)
	s := o.overSSH(t, RemoteRetry{
		Attempts: 5,
		Backoff:  time.Millisecond,
		Sleep:    func(time.Duration) {},
	})

	_, err := s.Fetch()
	if err == nil {
		t.Fatal("a rejected key produced a fetch that succeeded")
	}
	if got := transport.invocations(); got != AuthRefusalAttempts {
		t.Errorf("the transport was reached for %d times, want the auth bound of %d", got, AuthRefusalAttempts)
	}
	assertNamedAuthRefusal(t, err, publickeyRefusal)
}
