package factory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
)

// The GitHub App rung, from `ticfac factory setup`'s side (epic dm6).
//
// The factory hosts the manifest flow and holds the App's key; setup's whole
// job is to put the sealing key, register a one-time state, print ONE link,
// and wait for the operator's two clicks on GitHub. So the far side here is a
// fake FACTORY speaking the Worker's GitHub App routes — the exchange with
// GitHub itself is the Worker's, and is tested in cloudflare/test/
// github-app.test.ts — and the operator is simulated by the clock: each poll
// the walk waits out is a moment in which they may have clicked.

// fakeFactoryApp is the Worker's GitHub App rung, as far as setup sees it.
type fakeFactoryApp struct {
	h *setupHarness

	mu        sync.Mutex
	created   bool
	installed bool
	// allowed is the repositories the installation covers; nil is all.
	allowed map[string]bool
	// states is every one-time state setup registered, and orgs the org each
	// named.
	states []string
	orgs   []string
	// clicks advances the operator one step per poll the walk waits out:
	// "create" then "install". Empty means nobody is clicking.
	clicks []string
}

func (f *fakeFactoryApp) sealing() bool {
	_, err := os.Stat(filepath.Join(f.h.stateDir, "secret-"+SecretGitHubAppSealingKey))
	return err == nil
}

// poll is one wait the walk sat through: the operator's next click, if any.
func (f *fakeFactoryApp) poll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.clicks) == 0 {
		return
	}
	switch f.clicks[0] {
	case "create":
		f.created = true
	case "install":
		f.installed = true
	}
	f.clicks = f.clicks[1:]
}

func (f *fakeFactoryApp) route(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == githubAppStatusPath && r.Method == http.MethodGet:
		body := map[string]any{"rung": "none", "sealing_key": f.sealing()}
		if f.created {
			body["rung"] = "app"
			installations := []any{}
			if f.installed {
				installations = append(installations, map[string]any{
					"id": 42, "account": "octo-org", "repository_selection": "selected",
				})
			}
			body["app"] = map[string]any{
				"app_id": "777", "slug": "ticfac-test", "source": "manifest", "owner": "octo-org",
				"install_url":   "https://github.example.invalid/apps/ticfac-test/installations/new",
				"installations": installations,
			}
			if repo := r.URL.Query().Get("repo"); repo != "" {
				if f.installed && (f.allowed == nil || f.allowed[repo]) {
					body["check"] = map[string]any{"repo": repo, "ok": true, "checked": true, "installation_id": 42}
				} else {
					body["check"] = map[string]any{"repo": repo, "ok": false, "checked": true,
						"error": "github_app_not_installed", "detail": "the factory's GitHub App is not installed on " + repo}
				}
			}
		}
		_ = json.NewEncoder(w).Encode(body)
		return true
	case r.URL.Path == githubAppManifestPath && r.Method == http.MethodPost:
		if !f.sealing() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "github_app_sealing_key_missing", "detail": "no sealing key"})
			return true
		}
		var body struct {
			State string `json:"state"`
			Org   string `json:"org"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.states = append(f.states, body.State)
		f.orgs = append(f.orgs, body.Org)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"start_url":  f.h.server.URL + "/github/app/start?state=" + body.State,
			"expires_at": "2026-01-01T00:30:00Z",
		})
		return true
	}
	return false
}

func newFakeFactoryApp(h *setupHarness) *fakeFactoryApp {
	f := &fakeFactoryApp{h: h}
	route := f.route
	h.routes.Store(&route)
	return f
}

// appOptions is the harness's options with the App rung switched on and the
// operator's clicks driven by the walk's own waits.
func (h *setupHarness) appOptions(f *fakeFactoryApp, stdin string) SetupOptions {
	opts := h.options(stdin)
	opts.GitHubApp = ""
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	opts.deviceFlowNow = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	opts.deviceFlowSleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
		f.poll()
		return ctx.Err()
	}
	return opts
}

// The headline: a fresh factory reaches a live GitHub rung with no token
// created, pasted or stored — one link, two clicks, and the key never here.
func TestSetupRegistersTheFactorysOwnGitHubAppFromOneLink(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	app := newFakeFactoryApp(h)
	app.clicks = []string{"create", "install"}

	// deploy? / register the App? (default yes) / gateway URL / provider / key
	stdin := strings.Join([]string{"y", "", h.gateway.base(), "anthropic", "sk-provider-key", ""}, "\n")
	result, err := Setup(context.Background(), h.appOptions(app, stdin))
	if err != nil {
		t.Fatalf("Setup: %v\n%s", err, h.out.String())
	}

	if result.GitHubAuth != AuthApp || result.GitHubAppID != "777" || result.GitHubAppSlug != "ticfac-test" {
		t.Fatalf("result = auth %q app %q slug %q; want the factory's App", result.GitHubAuth, result.GitHubAppID, result.GitHubAppSlug)
	}
	if !result.GitHubRepoChecked {
		t.Error("the walk did not check the App against the repository")
	}

	// One link, carrying a one-time state long enough to be a capability.
	if len(app.states) != 1 || len(app.states[0]) < 43 {
		t.Fatalf("registered states = %q; want exactly one fresh state of >= 32 random bytes", app.states)
	}
	out := h.out.String()
	link := h.server.URL + "/github/app/start?state=" + app.states[0]
	for _, want := range []string{link, "Create GitHub App", "Install", "never touches this machine"} {
		if !strings.Contains(out, want) {
			t.Errorf("the walk never said %q:\n%s", want, out)
		}
	}

	// The sealing key was put — 32 random bytes — and NO GitHub credential
	// was: not the App's key (the factory holds it), not a token.
	raw, err := os.ReadFile(filepath.Join(h.stateDir, "secret-"+SecretGitHubAppSealingKey))
	if err != nil {
		t.Fatalf("the sealing key was not put as a Worker secret: %v", err)
	}
	if key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw))); err != nil || len(key) != 32 {
		t.Fatalf("the sealing key is not 32 base64 bytes: %q", raw)
	}
	for _, name := range []string{SecretGitHubToken, "GITHUB_APP_PRIVATE_KEY", "GITHUB_APP_ID"} {
		if _, err := os.Stat(filepath.Join(h.stateDir, "secret-"+name)); err == nil {
			t.Errorf("setup put a %s Worker secret; the App rung needs none from this machine", name)
		}
	}
	rc := h.rc(t)
	if rc.Get(credentials.KeyGitHubToken) != "" {
		t.Error("a GitHub token was stored in ~/.ticfacrc on the App rung")
	}
	if rc.Get(credentials.KeyGitHubRepo) != testRepo {
		t.Errorf("factory_github_repo = %q, want the repository the rung was verified against", rc.Get(credentials.KeyGitHubRepo))
	}
	if h.github.calls.Load() != 0 {
		t.Errorf("the walk probed GitHub with a token %d times; the App rung has no token here", h.github.calls.Load())
	}
}

// An App created but not installed is picked up where it stopped: no new
// flow, the install link printed, and the wait finishes on the install.
func TestSetupResumesAnAppThatIsNotInstalledYet(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	app := newFakeFactoryApp(h)
	app.created = true
	app.clicks = []string{"install"}
	putSealingKey(t, h)

	stdin := strings.Join([]string{"y", h.gateway.base(), "anthropic", "sk-provider-key", ""}, "\n")
	result, err := Setup(context.Background(), h.appOptions(app, stdin))
	if err != nil {
		t.Fatalf("Setup: %v\n%s", err, h.out.String())
	}
	if result.GitHubAuth != AuthApp {
		t.Fatalf("GitHubAuth = %q, want app", result.GitHubAuth)
	}
	if len(app.states) != 0 {
		t.Errorf("a second App flow was started (%d); the existing App only needed installing", len(app.states))
	}
	if !strings.Contains(h.out.String(), "https://github.example.invalid/apps/ticfac-test/installations/new") {
		t.Errorf("the walk did not print the install link:\n%s", h.out.String())
	}
}

// Nobody clicks: the walk stops at its bound, politely, saying how to resume.
func TestSetupStopsPolitelyWhenTheAppIsNeverInstalled(t *testing.T) {
	h := newSetupHarness(t, "")
	app := newFakeFactoryApp(h)
	app.clicks = []string{"create"}

	opts := h.appOptions(app, "y\n\n")
	opts.GitHubAppWait = time.Minute
	_, err := Setup(context.Background(), opts)
	if err == nil {
		t.Fatal("Setup finished with an App nobody installed")
	}
	for _, want := range []string{"not installed yet", "installations/new", "ticfac factory setup` again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout does not say %q: %v", want, err)
		}
	}
}

// A sealing key that exists is never replaced: the App sealed under it would
// be orphaned.
func TestSetupNeverReplacesAnExistingSealingKey(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	app := newFakeFactoryApp(h)
	app.clicks = []string{"create", "install"}
	putSealingKey(t, h)
	before, _ := os.ReadFile(filepath.Join(h.stateDir, "secret-"+SecretGitHubAppSealingKey))

	opts := h.appOptions(app, strings.Join([]string{"y", "", h.gateway.base(), "anthropic", "sk-provider-key", ""}, "\n"))
	opts.GitHubOrg = "octo-org"
	if _, err := Setup(context.Background(), opts); err != nil {
		t.Fatalf("Setup: %v\n%s", err, h.out.String())
	}
	after, _ := os.ReadFile(filepath.Join(h.stateDir, "secret-"+SecretGitHubAppSealingKey))
	if string(before) != string(after) {
		t.Fatal("setup replaced an existing sealing key")
	}
	if len(app.orgs) != 1 || app.orgs[0] != "octo-org" {
		t.Errorf("the flow was registered for org %q, want octo-org", app.orgs)
	}
}

// --github-app no, or a factory that predates the rung, is the token ladder
// exactly as before — the headline PAT walk proves the second; this the first.
func TestSetupSkipsTheAppRungWhenAskedTo(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	app := newFakeFactoryApp(h)
	opts := h.appOptions(app, strings.Join([]string{"y", testPAT, h.gateway.base(), "anthropic", "sk-provider-key", ""}, "\n"))
	opts.GitHubApp = "no"
	result, err := Setup(context.Background(), opts)
	if err != nil {
		t.Fatalf("Setup: %v\n%s", err, h.out.String())
	}
	if result.GitHubAuth != AuthPAT || len(app.states) != 0 {
		t.Fatalf("auth %q with %d App flows; want the PAT rung and no flow", result.GitHubAuth, len(app.states))
	}
}

// Status and doctor name the App rung and check it live: the factory mints a
// read-only token for the recorded repository, or says why it cannot.
func TestStatusNamesTheAppRungAndChecksItLive(t *testing.T) {
	h := newSetupHarness(t, "")
	if _, err := Deploy(context.Background(), h.harness.options()); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	app := newFakeFactoryApp(h)
	app.created, app.installed = true, true
	rc := h.rc(t)
	rc.Set(credentials.KeyGitHubRepo, testRepo)
	if err := rc.Save(); err != nil {
		t.Fatal(err)
	}

	state, err := GitHubRung(context.Background(), StatusOptions{ConfigPath: h.ticfacrc})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state.Summary, "rung: app") || !state.OK ||
		!strings.Contains(state.Detail, "minted a read-only token for "+testRepo) {
		t.Fatalf("github = %+v; want the App rung, live for %s", state, testRepo)
	}

	app.allowed = map[string]bool{"octo-org/other-repo": true}
	report, err := Status(context.Background(), StatusOptions{ConfigPath: h.ticfacrc, GitHubAPIBase: h.github.base()})
	if err != nil {
		t.Fatal(err)
	}
	if report.GitHub.OK || !strings.Contains(report.GitHub.Detail, "not installed on "+testRepo) {
		t.Fatalf("github = %+v; want a rejection naming the repository", report.GitHub)
	}
	if failed := report.Failures(); !contains(failed, "github") {
		t.Errorf("Failures() = %v, want github", failed)
	}
}

func putSealingKey(t *testing.T, h *setupHarness) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.stateDir, "secret-"+SecretGitHubAppSealingKey),
		[]byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
