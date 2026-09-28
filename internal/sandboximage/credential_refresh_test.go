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
			return password
		}
	}
	t.Fatalf("git credential fill answered no password:\n%s", out)
	return ""
}

type fakeTokenDoor struct {
	server *httptest.Server
	calls  atomic.Int32
}

func newFakeTokenDoor(t *testing.T, status int) *fakeTokenDoor {
	t.Helper()
	door := &fakeTokenDoor{}
	door.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		door.calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer tkr_placeholder_run" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
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
	got := credentialFill(t,
		"GITHUB_TOKEN=ghs_boot_placeholder",
		"TICKS_GITHUB_TOKEN_URL="+door.server.URL+"/api/github/token",
		"TICKS_FACTORY_TOKEN=tkr_placeholder_run",
	)
	if got != "ghs_boot_placeholder" {
		t.Fatalf("git would send %q, want the boot token when the door cannot answer", got)
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
