package factory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
)

// The GitHub App rung, as `ticfac factory setup`, `ticfac factory status` and
// `ticfac doctor` see it (epic dm6).
//
// The App is the operator's OWN, registered through GitHub's App manifest
// flow — and the flow is hosted by the factory Worker, not by this process
// (cloudflare/src/github-app.ts). Setup mints a one-time state, registers it
// with the factory, and prints ONE link. The operator opens it on any device
// signed in to GitHub — a phone works — and clicks twice: "Create GitHub App"
// and "Install". GitHub hands the App's private key to the Worker's callback,
// which seals it into its own storage; the key never reaches this machine,
// and this machine needs no browser and no listener, so setup runs the same
// from a headless box. Setup then polls the factory until the installation
// appears. After that everything is headless: an "All repositories" install
// covers repositories created later, and every run gets its own token.
//
// Rotating the App's private key still takes one visit to the App's settings
// page — GitHub has no API that generates an App key. Re-running the flow
// registers a new App instead.
//
// What this rung does NOT write is ~/.ticfacrc. The file's key vocabulary is
// the pinned credential-ownership contract's, closed on purpose ("an unknown
// factory_ key is a typo"), and it names no App keys yet — adding them is a
// ticks bundle re-cut. So the factory is the record: it holds the App's id,
// slug and installations, and status and doctor ask it, live.

const (
	// SecretGitHubAppSealingKey is the Worker secret the factory seals the
	// App's private key and webhook secret under (AES-GCM, 32 random bytes).
	// Setup puts it once, before the first manifest flow; replacing it
	// orphans the sealed App, so setup never replaces one that exists.
	SecretGitHubAppSealingKey = "GITHUB_APP_SEALING_KEY"

	// AuthApp is the App rung's name wherever a rung is named.
	AuthApp = "app"

	githubAppStatusPath   = "/api/github/app"
	githubAppManifestPath = "/api/github/app/manifest"

	// githubAppPollInterval is how often setup asks whether the App was
	// created and installed. The operator is clicking through GitHub; a few
	// seconds is instant to a person and nothing to the factory.
	githubAppPollInterval = 5 * time.Second
	// DefaultGitHubAppWait is how long setup waits for the two clicks before
	// it stops politely. Nothing is lost when it does: the next setup resumes.
	DefaultGitHubAppWait = 20 * time.Minute
	// sealingKeyPropagation bounds the wait for a freshly put secret to reach
	// the serving Worker version.
	sealingKeyPropagation = 90 * time.Second
)

// errNoAppRung reports a factory that predates the App rung: the route is not
// there, which a bundle this build deployed would never answer.
var errNoAppRung = errors.New("this factory predates the GitHub App rung")

// GitHubAppStatus is the factory's answer to `GET /api/github/app`.
type GitHubAppStatus struct {
	// Rung is "app", "token" (a PAT or device-flow GITHUB_TOKEN) or "none".
	Rung       string          `json:"rung"`
	SealingKey bool            `json:"sealing_key"`
	App        *GitHubAppFacts `json:"app"`
	Check      *GitHubAppCheck `json:"check"`
}

// GitHubAppFacts is the App's public half. Nothing here is a credential.
type GitHubAppFacts struct {
	AppID         string                  `json:"app_id"`
	Slug          string                  `json:"slug"`
	Source        string                  `json:"source"`
	Owner         string                  `json:"owner"`
	InstallURL    string                  `json:"install_url"`
	Installations []GitHubAppInstallation `json:"installations"`
	Error         string                  `json:"error"`
}

// GitHubAppInstallation is one account the App is installed on.
type GitHubAppInstallation struct {
	ID                  int64  `json:"id"`
	Account             string `json:"account"`
	RepositorySelection string `json:"repository_selection"`
}

// GitHubAppCheck is the live check for one repository: a read-only token the
// factory minted and did not return.
type GitHubAppCheck struct {
	Repo           string `json:"repo"`
	OK             bool   `json:"ok"`
	Checked        bool   `json:"checked"`
	InstallationID int64  `json:"installation_id"`
	ExpiresAt      string `json:"expires_at"`
	Error          string `json:"error"`
	Detail         string `json:"detail"`
}

// usable reports an App that exists and whose key the factory can use.
func (s *GitHubAppStatus) usable() bool {
	return s != nil && s.Rung == AuthApp && s.App != nil && s.App.AppID != "" && s.App.Error == ""
}

// ready reports the rung complete: an App with an installation, and — when
// there is a repository to check — a token minted for it.
func (s *GitHubAppStatus) ready(repo string) bool {
	if !s.usable() || len(s.App.Installations) == 0 {
		return false
	}
	return repo == "" || (s.Check != nil && s.Check.OK)
}

// FetchGitHubAppStatus asks the factory which GitHub rung is live and, with a
// repository, checks the App rung for it live.
func FetchGitHubAppStatus(ctx context.Context, client *http.Client, factoryURL, token, repo string) (*GitHubAppStatus, error) {
	endpoint := strings.TrimSuffix(factoryURL, "/") + githubAppStatusPath
	if repo != "" {
		endpoint += "?repo=" + url.QueryEscape(repo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNoAppRung
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GET %s answered %s: %s", githubAppStatusPath, resp.Status, firstLine(body))
	}
	var status GitHubAppStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("GET %s answered something that is not its status: %w", githubAppStatusPath, err)
	}
	return &status, nil
}

// startManifestFlow registers a one-time state with the factory and returns
// the link the operator opens.
func startManifestFlow(ctx context.Context, client *http.Client, factoryURL, token, state, org string) (string, string, error) {
	payload := map[string]string{"state": state}
	if org != "" {
		payload["org"] = org
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(factoryURL, "/")+githubAppManifestPath, bytes.NewReader(raw))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusCreated {
		var refusal struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(body, &refusal)
		if refusal.Detail == "" {
			refusal.Detail = firstLine(body)
		}
		return "", "", fmt.Errorf("the factory would not start the GitHub App flow (%s): %s", resp.Status, refusal.Detail)
	}
	var started struct {
		StartURL  string `json:"start_url"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &started); err != nil || started.StartURL == "" {
		return "", "", errors.New("the factory started the GitHub App flow but named no link")
	}
	return started.StartURL, started.ExpiresAt, nil
}

// randomToken is n random bytes, URL-safe base64.
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// DescribeGitHubApp is the App rung's one-line summary, shared by setup and
// status so the two name it the same way.
func DescribeGitHubApp(app *GitHubAppFacts) string {
	if app == nil {
		return "GitHub App"
	}
	parts := []string{fmt.Sprintf("GitHub App %s (app %s)", app.Slug, app.AppID)}
	if app.Owner != "" {
		parts = append(parts, "owned by @"+app.Owner)
	}
	switch n := len(app.Installations); n {
	case 0:
		parts = append(parts, "not installed anywhere yet")
	case 1:
		parts = append(parts, fmt.Sprintf("installed on %s (%s repositories)", app.Installations[0].Account, app.Installations[0].RepositorySelection))
	default:
		parts = append(parts, fmt.Sprintf("%d installations", n))
	}
	return strings.Join(parts, ", ") + " — per-run installation tokens"
}

// setupGitHubApp settles the GitHub rung through the factory's own App. It
// reports handled=false when the walk should fall through to the token rungs
// below it: the operator declined, a token was supplied by hand, or the
// deployed factory predates the App rung.
func setupGitHubApp(
	ctx context.Context,
	w *wrangler,
	in *bufio.Reader,
	out io.Writer,
	client *http.Client,
	cfg *credentials.File,
	opts SetupOptions,
	repo string,
	result *SetupResult,
) (bool, error) {
	mode := strings.ToLower(strings.TrimSpace(opts.GitHubApp))
	switch mode {
	case "", "auto", "yes", "no":
	default:
		return false, fmt.Errorf("--github-app %q: want auto, yes or no", opts.GitHubApp)
	}
	if mode == "no" || (strings.TrimSpace(opts.GitHubToken) != "" && mode != "yes") {
		return false, nil
	}
	factoryURL := strings.TrimSuffix(result.URL, "/")
	factoryToken := cfg.Get(credentials.KeyToken)
	if factoryURL == "" || factoryToken == "" {
		return false, nil
	}

	status, err := FetchGitHubAppStatus(ctx, client, factoryURL, factoryToken, repo)
	if errors.Is(err, errNoAppRung) {
		if mode == "yes" {
			return false, errors.New("the deployed factory predates the GitHub App rung — run `ticfac factory deploy` from this build, then setup again")
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("asking the factory about its GitHub App: %w", err)
	}

	fmt.Fprintf(out, "\nGitHub\n")
	if status.Rung == AuthApp && status.App != nil && status.App.Error != "" {
		return false, fmt.Errorf("the factory's GitHub App is registered but unusable: %s\n"+
			"If the %s secret was replaced, restore it — or delete the App at https://github.com/settings/apps and run setup again to register a new one",
			status.App.Error, SecretGitHubAppSealingKey)
	}

	if !status.usable() {
		if mode == "" || mode == "auto" {
			fmt.Fprintf(out, "The factory can register its OWN GitHub App: no token to create or paste,\n")
			fmt.Fprintf(out, "and every run gets its own token for one repository, dead within the hour.\n")
			fmt.Fprintf(out, "It takes two clicks in a browser — on any device, a phone works.\n")
			answer, err := promptLine(in, out, "Register the factory's GitHub App now? [Y/n] ")
			if err != nil {
				return false, err
			}
			if !isYesDefaultYes(answer) {
				return false, nil
			}
		}
		if err := ensureSealingKey(ctx, w, out, client, opts, factoryURL, factoryToken, status); err != nil {
			return false, err
		}
		state, err := randomToken(32)
		if err != nil {
			return false, err
		}
		link, expires, err := startManifestFlow(ctx, client, factoryURL, factoryToken, state, strings.TrimSpace(opts.GitHubOrg))
		if err != nil {
			return false, err
		}
		fmt.Fprintf(out, "\nOpen this link on any device signed in to GitHub:\n\n  %s\n\n", link)
		fmt.Fprintf(out, "  1. GitHub shows the App, pre-filled: click \"Create GitHub App\".\n")
		fmt.Fprintf(out, "  2. Pick the repositories (\"All repositories\" covers ones you create later)\n")
		fmt.Fprintf(out, "     and click \"Install\".\n")
		fmt.Fprintf(out, "\nThe App's private key goes straight to your factory; it never touches this machine.\n")
		if expires != "" {
			fmt.Fprintf(out, "The link works once, until %s.\n", expires)
		}
	} else if len(status.App.Installations) == 0 || !status.ready(repo) {
		fmt.Fprintf(out, "%s\n", DescribeGitHubApp(status.App))
		fmt.Fprintf(out, "Install it%s:\n\n  %s\n", onRepo(repo), status.App.InstallURL)
		if status.Check != nil && !status.Check.OK && status.Check.Detail != "" {
			fmt.Fprintf(out, "\n(%s)\n", status.Check.Detail)
		}
	}

	final, err := waitForGitHubApp(ctx, out, client, opts, factoryURL, factoryToken, repo, status)
	if err != nil {
		return false, err
	}

	fmt.Fprintf(out, "%s\n", DescribeGitHubApp(final.App))
	if repo != "" {
		fmt.Fprintf(out, "Minted a read-only token for %s through installation %d — the rung is live\n",
			repo, final.Check.InstallationID)
		result.GitHubRepoChecked = true
	} else {
		fmt.Fprintf(out, "note: no git remote to check a repository against — pass --repo owner/name to verify one\n")
	}
	if repo != "" {
		// The one fact this rung records locally, under a key the contract
		// already has: the repository the rung was verified against, so status
		// and doctor re-check the same one.
		cfg.Set(credentials.KeyGitHubRepo, repo)
		if err := cfg.Save(); err != nil {
			return false, err
		}
	}
	result.GitHubAuth = AuthApp
	result.GitHubRepo = repo
	result.GitHubLogin = final.App.Slug + "[bot]"
	result.GitHubAppID = final.App.AppID
	result.GitHubAppSlug = final.App.Slug
	return true, nil
}

func onRepo(repo string) string {
	if repo == "" {
		return ""
	}
	return " on " + repo
}

// ensureSealingKey puts the sealing key when the factory has none, and waits
// for the serving Worker to see it. It never replaces one: an App sealed
// under the old key would be orphaned.
func ensureSealingKey(
	ctx context.Context,
	w *wrangler,
	out io.Writer,
	client *http.Client,
	opts SetupOptions,
	factoryURL, factoryToken string,
	status *GitHubAppStatus,
) error {
	if status.SealingKey {
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if err := putSecret(ctx, w, opts, SecretGitHubAppSealingKey, base64.StdEncoding.EncodeToString(key)); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s stored as a Worker secret (the key the App's credentials are sealed under)\n", SecretGitHubAppSealingKey)
	flow := opts.deviceFlow(io.Discard)
	deadline := flow.clock().Add(sealingKeyPropagation)
	for {
		current, err := FetchGitHubAppStatus(ctx, client, factoryURL, factoryToken, "")
		if err == nil && current.SealingKey {
			return nil
		}
		if !flow.clock().Before(deadline) {
			return fmt.Errorf("the factory has not picked up the %s secret after %s — run setup again in a minute", SecretGitHubAppSealingKey, sealingKeyPropagation)
		}
		if err := flow.wait(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// waitForGitHubApp polls the factory until the App exists, is installed and —
// when there is a repository — mints a token for it, saying each step out
// loud once. It stops politely at the deadline: the next setup resumes.
func waitForGitHubApp(
	ctx context.Context,
	out io.Writer,
	client *http.Client,
	opts SetupOptions,
	factoryURL, factoryToken, repo string,
	status *GitHubAppStatus,
) (*GitHubAppStatus, error) {
	if status.ready(repo) {
		return status, nil
	}
	wait := opts.GitHubAppWait
	if wait <= 0 {
		wait = DefaultGitHubAppWait
	}
	flow := opts.deviceFlow(io.Discard)
	deadline := flow.clock().Add(wait)
	announcedApp := status.usable()
	announcedInstall := announcedApp && len(status.App.Installations) > 0
	fmt.Fprintf(out, "\nWaiting for GitHub (up to %s; Ctrl-C is safe — setup resumes where it left off)…\n", wait)
	for {
		if err := flow.wait(ctx, githubAppPollInterval); err != nil {
			return nil, err
		}
		current, err := FetchGitHubAppStatus(ctx, client, factoryURL, factoryToken, repo)
		if err == nil {
			if !announcedApp && current.usable() {
				fmt.Fprintf(out, "App %s created (app %s) — now install it\n", current.App.Slug, current.App.AppID)
				announcedApp = true
			}
			if !announcedInstall && current.usable() && len(current.App.Installations) > 0 {
				fmt.Fprintf(out, "Installed on %s\n", current.App.Installations[0].Account)
				announcedInstall = true
			}
			if current.ready(repo) {
				return current, nil
			}
			status = current
		}
		if !flow.clock().Before(deadline) {
			return nil, gitHubAppTimeout(status, repo, wait)
		}
	}
}

// gitHubAppTimeout says where the walk stopped and what finishes it.
func gitHubAppTimeout(status *GitHubAppStatus, repo string, wait time.Duration) error {
	switch {
	case !status.usable():
		return fmt.Errorf("no GitHub App was created within %s. Nothing is half-done: run `ticfac factory setup` again for a fresh link", wait)
	case len(status.App.Installations) == 0:
		return fmt.Errorf("the GitHub App %s exists but is not installed yet (waited %s). Install it at %s, then run `ticfac factory setup` again — it picks up from here",
			status.App.Slug, wait, status.App.InstallURL)
	default:
		detail := ""
		if status.Check != nil && status.Check.Detail != "" {
			detail = " (" + status.Check.Detail + ")"
		}
		return fmt.Errorf("the GitHub App %s is installed, but not on %s%s. Add the repository to the installation at %s, then run `ticfac factory setup` again",
			status.App.Slug, repo, detail, status.App.InstallURL)
	}
}
