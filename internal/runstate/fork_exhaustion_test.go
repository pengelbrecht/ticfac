package runstate

import (
	"errors"
	"testing"
	"time"
)

// incidentForkError is epic-v5t's halt (2026-10-06, 22:59 UTC), verbatim: a
// `ticfac watch` leaking zombies had spent the per-user process limit, and the
// orchestrator's fetch could not fork git's https helper.
const incidentForkError = "runstate: fetch origin epic/v5t: git fetch --quiet --no-write-fetch-head " +
	"--refmap= origin +refs/heads/epic/v5t:refs/ticfac/fetch/x: exit status 255: " +
	"error: cannot fork() for remote-https: Resource temporarily unavailable"

// Fork exhaustion is the HOST's transient condition, not an answer from the
// remote: it is waited through, never a stop nobody classified.
// short: classification over captured error text, with a stubbed sleep
func TestForkExhaustionIsATransientFailureNotAnUnclassifiedStop(t *testing.T) {
	t.Parallel()
	transient := []string{
		incidentForkError,
		"git fetch origin epic/v5t: fork/exec /opt/homebrew/bin/git: resource temporarily unavailable: ",
		"bash: fork: retry: Resource temporarily unavailable",
		"sh: fork: Resource temporarily unavailable",
	}
	for _, text := range transient {
		if got := ClassifyRemote(errors.New(text)); got != RemoteTransient {
			t.Errorf("fork exhaustion classified %v, want transient: %s", got, text)
		}
	}
	// What the narrow match is narrow FOR: a missing binary and a held
	// non-blocking lock are not fork exhaustion, and waiting fixes neither.
	for _, text := range []string{
		"fork/exec /no/such/git: no such file or directory",
		"flock .tick/lock: resource temporarily unavailable",
	} {
		if got := ClassifyRemote(errors.New(text)); got == RemoteTransient {
			t.Errorf("%q was read as transient fork exhaustion", text)
		}
	}

	// And the in-run bound waits through it: the second attempt, once the
	// host has slots again, is the one that lands.
	tries := 0
	retry := RemoteRetry{Sleep: func(time.Duration) {}}
	err := retry.Do("git fetch", func() error {
		tries++
		if tries == 1 {
			return errors.New(incidentForkError)
		}
		return nil
	})
	if err != nil || tries != 2 {
		t.Fatalf("a fork failure was not waited through: err=%v after %d tries", err, tries)
	}
}
