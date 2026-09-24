package cli

// The command tests for `ticfac factory status` and `ticfac factory
// dashboard`, ported from ticks' cmd/tk/cmd/factory_test.go (its status
// command only — deploy and setup test with Factory move B, ticks tick b3a)
// and cmd/tk/cmd/factory_dashboard_test.go. ExecuteArgs-with-captured-output
// becomes Run(args, stdout, stderr) with the exit code asserted.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
)

// fakeCredentialEndpoints stands up a fake GitHub API and a fake AI Gateway,
// so a live `factory status` run probes endpoints the test controls. Ported
// verbatim from ticks' cmd/tk/cmd/factory_test.go.
func fakeCredentialEndpoints(t *testing.T, pat, repo, gatewayKey string) (githubBase, gatewayBase string) {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+pat {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Bad credentials"})
			return
		}
		switch r.URL.Path {
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "octo-user"})
		case "/repos/" + repo:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"full_name":   repo,
				"permissions": map[string]any{"push": true},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		}
	}))
	t.Cleanup(gh.Close)

	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/compat/models") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if gatewayKey != "" && r.Header.Get("Authorization") != "Bearer "+gatewayKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "model-alpha"}}})
	}))
	t.Cleanup(gw.Close)

	return gh.URL, gw.URL + "/v1/00000000000000000000000000000000/ticks"
}

func TestFactoryStatusWithNothingConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	code, out, stderr := runCloudArgs(t, []string{"factory", "status"})
	if code != exitSuccess {
		t.Fatalf("ticfac factory status: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "tk factory setup") {
		t.Errorf("status does not name the command that configures a factory:\n%s", out.String())
	}
}

// --offline reports configuration without touching the network; --check turns
// a rejected credential into a nonzero exit, and its absence does not.
func TestFactoryStatusOfflineAndCheck(t *testing.T) {
	const repo = "octo-org/octo-repo"
	home := t.TempDir()
	t.Setenv("HOME", home)
	githubBase, _ := fakeCredentialEndpoints(t, "github_pat_right", repo, "")
	rc := strings.Join([]string{
		"factory_github_token=github_pat_revoked",
		"factory_github_login=octo-user",
		"factory_github_repo=" + repo,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(home, credentials.FileName), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := runCloudArgs(t, []string{"factory", "status", "--offline"})
	if code != exitSuccess {
		t.Fatalf("ticfac factory status --offline: %s\n%s", stderr.String(), out.String())
	}
	output := out.String()
	if !strings.Contains(output, repo) || !strings.Contains(output, "--offline") {
		t.Errorf("offline status does not report the configuration it read:\n%s", output)
	}
	if strings.Contains(output, "github_pat_revoked") {
		t.Errorf("status printed the stored token:\n%s", output)
	}

	// Live, without --check: reported but exit 0.
	code, out, stderr = runCloudArgs(t, []string{"factory", "status", "--github-api-base", githubBase})
	if code != exitSuccess {
		t.Fatalf("ticfac factory status: %s\n%s", stderr.String(), out.String())
	}
	if !strings.Contains(out.String(), "rejected") {
		t.Errorf("a revoked token was not reported as rejected:\n%s", out.String())
	}

	// Live, with --check: nonzero.
	code, _, _ = runCloudArgs(t, []string{"factory", "status", "--check", "--github-api-base", githubBase})
	if code == exitSuccess {
		t.Fatal("ticfac factory status --check returned 0 for a revoked token")
	}
}

// A factory's deployment is pinned to the tk version that deployed it (D16,
// "upgrades ride the repo"), so status — not upgrade — is where a build
// running ahead of its deployed factory shows up.
func TestFactoryStatusFlagsStaleDeployment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rc := "factory_url=https://factory.example\nfactory_version=1.2.3\n"
	if err := os.WriteFile(filepath.Join(home, credentials.FileName), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}

	previous := Version
	Version = "1.3.0"
	t.Cleanup(func() { Version = previous })

	code, out, stderr := runCloudArgs(t, []string{"factory", "status", "--offline"})
	if code != exitSuccess {
		t.Fatalf("ticfac factory status --offline: %s\n%s", stderr.String(), out.String())
	}
	if output := out.String(); !strings.Contains(output, "version behind") || !strings.Contains(output, "tk factory deploy") {
		t.Errorf("status does not flag the stale factory deployment:\n%s", output)
	}
}

func TestFactoryGroupHelpMentionsItsCommands(t *testing.T) {
	code, out, stderr := runCloudArgs(t, []string{"factory", "--help"})
	if code != exitSuccess {
		t.Fatalf("ticfac factory --help: %s\n%s", stderr.String(), out.String())
	}
	for _, want := range []string{"setup", "status", "deploy", "dashboard"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the factory help does not mention %q:\n%s", want, out.String())
		}
	}
}

// b3a landed the deploy path in the same wave as this read path, so deploy and
// setup are WIRED now — the placeholder that named the move they were waiting
// for is gone. What is still worth asserting is that they reach their real
// handlers rather than the dispatcher's unknown-subcommand arm: an unknown name
// exits 2 with usage, and these two do not.
//
// The handlers are REAL, so the test isolates them exactly as its siblings in
// factory_test.go do: an empty PATH, a temp TK_HOME and an empty working
// directory leave no wrangler anywhere the resolver looks, and each command
// stops at the wrangler prerequisite (exit 1). Without that, on a machine
// where wrangler is reachable, `factory deploy` staged into the operator's
// real ~/.tick/factory and ran a live `wrangler deploy` — remote D1
// migrations and a container image push — from inside `go test -short`.
func TestFactoryDeployAndSetupAreWired(t *testing.T) {
	t.Setenv("TK_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Chdir(t.TempDir())
	for _, name := range []string{"deploy", "setup"} {
		code, _, stderr := runCloudArgs(t, []string{"factory", name})
		if code == exitUsage {
			t.Errorf("factory %s fell through to the unknown-subcommand arm: %s", name, stderr.String())
		}
		if strings.Contains(stderr.String(), "unknown subcommand") {
			t.Errorf("factory %s is not wired: %s", name, stderr.String())
		}
	}
}

// The board is one of the factory's command families, and its help is where
// its read-only posture is stated. A dashboard whose help does not say it
// cannot steer a run invites someone to look for the key that does.
func TestFactoryDashboardHelpStatesItsPosture(t *testing.T) {
	code, out, stderr := runCloudArgs(t, []string{"factory", "dashboard", "--help"})
	if code != exitSuccess {
		t.Fatalf("ticfac factory dashboard --help: %s\n%s", stderr.String(), out.String())
	}
	for _, want := range []string{"Read-only", "observation", "j / k"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the help does not mention %q:\n%s", want, out.String())
		}
	}
}

func TestFactoryDashboardRefusesNonsenseIntervals(t *testing.T) {
	for _, args := range [][]string{
		{"factory", "dashboard", "--interval", "0"},
		{"factory", "dashboard", "--cost-interval", "-1"},
		{"factory", "dashboard", "--tail-bytes", "0"},
	} {
		code, _, stderr := runCloudArgs(t, args)
		if code == exitSuccess {
			t.Fatalf("%v: a non-positive bound must be refused", args)
		}
		if code != exitUsage {
			t.Fatalf("%v: exit %d, want %d (%s)", args, code, exitUsage, stderr.String())
		}
	}
}

// "No factory is configured" must name the command that configures one, and
// must not be reported as a dashboard failure.
func TestFactoryDashboardWithoutAFactoryNamesTheSetupCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	code, _, stderr := runCloudArgs(t, []string{"factory", "dashboard"})
	if code == exitSuccess {
		t.Fatal("a dashboard with no factory configured must not start")
	}
	if !strings.Contains(stderr.String(), "tk factory setup") {
		t.Fatalf("the refusal must name the fix, got %s", stderr.String())
	}
}
