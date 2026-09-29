//go:build !windows

package sandboximage

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
)

// The container half of the factory's GitHub token door (epic dm6).
//
// On the GitHub App rung a container boots with an installation token that
// dies an hour later, while an orchestrator lives up to six. The Worker also
// hands it TICKS_GITHUB_TOKEN_URL, and the git credential helper common.sh
// installs must ask THAT door — with the run's own credential — every time git
// needs a password, so the push at hour five carries a live token. These run
// the real install_git_credential_helper and the real `git credential fill`
// against a fake door, and assert what git would send.

// credentialFill sources common.sh, installs the helper, and asks git for a
// github.com password — under the same isolated git environment the git door
// tests use, so no host credential helper (a keychain) can answer instead.
func credentialFill(t *testing.T, extra ...string) string {
	t.Helper()
	password, _ := credentialFillOutput(t, extra...)
	return password
}

// credentialFillOutput is credentialFill, also answering everything git and
// the helper printed — the helper's stderr is part of a failed push's error.
func credentialFillOutput(t *testing.T, extra ...string) (string, string) {
	t.Helper()
	common, err := Path("common.sh")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	script := `ME=credential-test; . "$1" >/dev/null; install_git_credential_helper; ` +
		`printf 'protocol=https\nhost=github.com\n\n' | git credential fill`
	cmd := exec.Command("bash", "-c", script, "bash", common)
	cmd.Env = append(isolatedGitEnv(home), extra...)
	// Outside any repository: a checkout's own config (this one's included)
	// can name a credential helper that would answer before the one under test.
	cmd.Dir = home
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git credential fill: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if password, ok := strings.CutPrefix(line, "password="); ok {
			return password, string(out)
		}
	}
	t.Fatalf("git credential fill answered no password:\n%s", out)
	return "", ""
}

type fakeTokenDoor struct {
	server *httptest.Server
	calls  atomic.Int32
	// failFirst is how many calls answer 502 before the door answers status.
	failFirst int32
}

func newFakeTokenDoor(t *testing.T, status int) *fakeTokenDoor {
	return newFlakyTokenDoor(t, status, 0)
}

// newFlakyTokenDoor is a door that answers 502 to its first failFirst calls
// — a Worker that did not answer once — and status after.
func newFlakyTokenDoor(t *testing.T, status int, failFirst int32) *fakeTokenDoor {
	t.Helper()
	door := &fakeTokenDoor{failFirst: failFirst}
	door.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := door.calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer tkr_placeholder_run" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if call <= door.failFirst {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"github_app_unavailable","detail":"down"}`))
			return
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"token":"ghs_fresh_placeholder","expires_at":"2026-01-01T01:00:00Z"}`))
		} else {
			_, _ = w.Write([]byte(`{"error":"github_app_unavailable","detail":"down"}`))
		}
	}))
	t.Cleanup(door.server.Close)
	return door
}

// short: one bash and one git per case against a local HTTP fake; no network
func TestTheCredentialHelperAsksTheFactoryForTheCurrentToken(t *testing.T) {
	door := newFakeTokenDoor(t, http.StatusOK)
	got := credentialFill(t,
		"GITHUB_TOKEN=ghs_boot_placeholder",
		"TICKS_GITHUB_TOKEN_URL="+door.server.URL+"/api/github/token",
		"TICKS_FACTORY_TOKEN=tkr_placeholder_run",
	)
	if got != "ghs_fresh_placeholder" {
		t.Fatalf("git would send %q, want the door's current token: the boot token dies an hour into a six-hour run", got)
	}
	if door.calls.Load() != 1 {
		t.Fatalf("the door was asked %d times, want 1", door.calls.Load())
	}
}

// short: one bash and one git per case against a local HTTP fake; no network
func TestTheCredentialHelperFallsBackToTheBootTokenWhenTheDoorFails(t *testing.T) {
	door := newFakeTokenDoor(t, http.StatusBadGateway)
	got, out := credentialFillOutput(t,
		"GITHUB_TOKEN=ghs_boot_placeholder",
		"TICKS_GITHUB_TOKEN_URL="+door.server.URL+"/api/github/token",
		"TICKS_FACTORY_TOKEN=tkr_placeholder_run",
	)
	if got != "ghs_boot_placeholder" {
		t.Fatalf("git would send %q, want the boot token when the door cannot answer", got)
	}
	// Epic hn6 (2026-09-29): a silent fallback left a 403 push nobody could
	// attribute to the token git sent. The door is asked a bounded number of
	// times, and the fallback says so where the failed push's error carries it.
	if door.calls.Load() != 3 {
		t.Errorf("the door was asked %d times before the fallback, want 3", door.calls.Load())
	}
	if !strings.Contains(out, "ticks credential helper: the factory token door gave no GitHub token in 3 tries") ||
		!strings.Contains(out, "booted with") {
		t.Errorf("the fallback to the boot token was silent:\n%s", out)
	}
	if strings.Contains(out, "ghs_boot_placeholder") && !strings.Contains(out, "password=ghs_boot_placeholder") {
		t.Errorf("the warning printed the token itself:\n%s", out)
	}
}

// TestTheCredentialHelperRidesOutADoorThatDidNotAnswerOnce: a Worker that
// misses one call must not cost the push its fresh token — the helper asks
// again rather than handing git a boot token that may already be dead.
// short: one bash and one git per case against a local HTTP fake; no network
func TestTheCredentialHelperRidesOutADoorThatDidNotAnswerOnce(t *testing.T) {
	door := newFlakyTokenDoor(t, http.StatusOK, 1)
	got, out := credentialFillOutput(t,
		"GITHUB_TOKEN=ghs_boot_placeholder",
		"TICKS_GITHUB_TOKEN_URL="+door.server.URL+"/api/github/token",
		"TICKS_FACTORY_TOKEN=tkr_placeholder_run",
	)
	if got != "ghs_fresh_placeholder" {
		t.Fatalf("git would send %q, want the door's token on its second answer:\n%s", got, out)
	}
	if door.calls.Load() != 2 {
		t.Errorf("the door was asked %d times, want 2", door.calls.Load())
	}
	if strings.Contains(out, "ticks credential helper:") {
		t.Errorf("a fresh token still printed the fallback warning:\n%s", out)
	}
}

// short: one bash and one git per case against a local HTTP fake; no network
func TestTheCredentialHelperIsStaticWithoutTheDoor(t *testing.T) {
	door := newFakeTokenDoor(t, http.StatusOK)
	got := credentialFill(t, "GITHUB_TOKEN=github_pat_placeholder", "TICKS_FACTORY_TOKEN=tkr_placeholder_run")
	if got != "github_pat_placeholder" {
		t.Fatalf("git would send %q, want the PAT the container booted with", got)
	}
	if door.calls.Load() != 0 {
		t.Fatalf("a container with no TICKS_GITHUB_TOKEN_URL asked a door %d times", door.calls.Load())
	}
}
