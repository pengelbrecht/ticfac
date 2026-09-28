package factory

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
	"github.com/pengelbrecht/ticfac/internal/httpnet"
	"github.com/pengelbrecht/ticfac/internal/jev"
)

// `tk factory status` answers two questions the ladder leaves open: what is
// configured, and does it still work. It follows `tk channel status` exactly —
// live checks by default, --offline to skip them, and a rejected credential
// reported either way, because a status call has to be safe to make
// unconditionally (in a script, on a plane, before a run).
//
// Nothing here prints a credential. It reports the public half — the account a
// token authenticates as, the repository it can reach, the provider behind the
// gateway — and whether the live check passed.

// StatusOptions configures a status report.
type StatusOptions struct {
	// ConfigPath overrides the ~/.ticfacrc location (tests).
	ConfigPath string

	// Offline skips every live check.
	Offline bool

	// HTTPClient makes the live checks. Nil means a client with a short
	// timeout.
	HTTPClient *http.Client

	// GitHubAPIBase overrides https://api.github.com (tests, GHES).
	GitHubAPIBase string

	// CloudflareAPIBase overrides https://api.cloudflare.com/client/v4
	// (tests).
	CloudflareAPIBase string

	// CurrentVersion is the tk build running this status check. Compared
	// against the deployed factory's recorded version (KeyFactoryVersion) to
	// flag a factory left behind by an upgrade. Empty skips the comparison.
	CurrentVersion string
}

// CredentialState is one rung's line in the report.
type CredentialState struct {
	// Name is the rung's stable identifier, and what Failures() reports.
	Name string
	// Configured reports whether anything is stored for this rung.
	Configured bool
	// Summary is the public description of what is configured.
	Summary string
	// Checked reports whether a live check ran; OK is its verdict.
	Checked bool
	OK      bool
	// Detail carries the live check's outcome, or why it did not run.
	Detail string
}

// StatusReport is the whole ladder's state.
type StatusReport struct {
	ConfigPath string
	Deployment CredentialState
	GitHub     CredentialState
	Gateway    CredentialState
	// Telemetry is the credential a run's cost is read with (D17). It is its
	// own rung because "model traffic routes" and "spend is visible" fail
	// separately, and a factory whose budget has nothing to act on must not
	// look like a healthy one.
	Telemetry CredentialState
	// Billing is which pot the gateway's Workers AI traffic bills to. It is
	// not a credential at all — it is a per-gateway setting one dashboard
	// click changes, moving every run from the Cloudflare invoice an account
	// credit pays onto a separately purchased prepaid wallet, with the
	// identical cost reported either way. Status is where an operator can see
	// that before submitting a run.
	Billing CredentialState
	// Classifier is whether Jev — typesafe/jev on Workers AI, the model a
	// run classifies its ticks with (tick tum) — answers on the Cloudflare
	// credential above. It stores nothing of its own: the cost-telemetry
	// token and the gateway's account are the whole credential, so it is
	// configured exactly when both are, and its live check is one tiny
	// classification.
	Classifier CredentialState
}

// rungs returns the report's states in the order the ladder is walked.
func (r *StatusReport) rungs() []CredentialState {
	return []CredentialState{r.Deployment, r.GitHub, r.Gateway, r.Telemetry, r.Billing, r.Classifier}
}

// Configured reports whether any rung has been walked at all.
func (r *StatusReport) Configured() bool {
	for _, state := range r.rungs() {
		if state.Configured {
			return true
		}
	}
	return false
}

// Failures names the rungs that are configured and were checked and rejected.
// A rung that is not configured, or was not checked, is not a failure — that
// is what keeps `tk factory status --offline` and a half-walked ladder from
// reporting problems that do not exist.
func (r *StatusReport) Failures() []string {
	var failed []string
	for _, state := range r.rungs() {
		if state.Configured && state.Checked && !state.OK {
			failed = append(failed, state.Name)
		}
	}
	return failed
}

// Status reads the local mirror and, unless offline, re-checks each credential
// against the service that issued it.
func Status(ctx context.Context, opts StatusOptions) (*StatusReport, error) {
	cfg, err := loadConfig(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = httpnet.Client(15 * time.Second)
	}
	apiBase := strings.TrimSuffix(strings.TrimSpace(opts.GitHubAPIBase), "/")
	if apiBase == "" {
		apiBase = defaultGitHubAPIBase
	}

	report := &StatusReport{ConfigPath: cfg.Path()}

	// Deployment.
	url := strings.TrimSuffix(cfg.Get(credentials.KeyURL), "/")
	report.Deployment = CredentialState{Name: "deployment"}
	if url != "" {
		report.Deployment.Configured = true
		report.Deployment.Summary = url
		version := cfg.Get(credentials.KeyVersion)
		if version != "" {
			report.Deployment.Summary += " (tk " + version + ")"
		}
		switch {
		case opts.Offline:
			report.Deployment.Detail = "not checked (--offline)"
		default:
			report.Deployment.Checked = true
			if err := verifyOnce(ctx, client, url, cfg.Get(credentials.KeyToken)); err != nil {
				report.Deployment.Detail = "rejected: " + err.Error()
			} else {
				report.Deployment.OK = true
				report.Deployment.Detail = "live, and it accepts your token"
			}
		}
		// The factory bundle is pinned to the tk version that deployed it
		// (D16, "upgrades ride the repo"), so an upgrade leaves a deployed
		// factory a version behind until the operator redeploys. This is the
		// one place that says so — status is the pre-flight an operator
		// already runs to see what's configured.
		if opts.CurrentVersion != "" && version != "" && version != opts.CurrentVersion {
			report.Deployment.Detail += fmt.Sprintf("; a version behind (you have ticfac %s) — run `ticfac factory deploy` to redeploy it from this build", opts.CurrentVersion)
		}
	}

	// GitHub: whichever rung is live, checked live unless offline.
	report.GitHub = githubRungState(ctx, cfg, client, apiBase, url, opts.Offline)

	// Gateway and the provider behind it.
	gateway := strings.TrimSuffix(cfg.Get(credentials.KeyGatewayURL), "/")
	providerID := cfg.Get(credentials.KeyGatewayProvider)
	key := cfg.Get(credentials.KeyGatewayKey)
	report.Gateway = CredentialState{Name: "gateway"}
	if gateway != "" {
		report.Gateway.Configured = true
		report.Gateway.Summary = describeGateway(gateway, providerID, key != "")
		switch {
		case opts.Offline:
			report.Gateway.Detail = "not checked (--offline)"
		default:
			report.Gateway.Checked = true
			probe, err := probeGateway(ctx, client, gateway, key)
			provider, known := LookupProvider(providerID)
			switch {
			case err != nil:
				report.Gateway.Detail = "rejected: " + err.Error()
			case probe.AuthRejected && known && !provider.NeedsKey():
				// Workers AI is called from the Worker with the account's own
				// credentials, so an anonymous model list is not expected to
				// succeed: reachability is the whole local claim.
				report.Gateway.OK = true
				report.Gateway.Detail = "reachable (Workers AI needs no key from here)"
			case probe.AuthRejected:
				report.Gateway.Detail = "rejected: the gateway refused the stored provider key"
			default:
				report.Gateway.OK = true
				report.Gateway.Detail = fmt.Sprintf("live, %d models (%s)", len(probe.Models), summarizeModels(probe.Models))
			}
		}
	}

	// Cost telemetry: the credential the Run Workflow reads gateway spend with.
	telemetry := cfg.Get(credentials.KeyCloudflareAPIToken)
	report.Telemetry = CredentialState{Name: "cost telemetry"}
	if telemetry != "" {
		report.Telemetry.Configured = true
		report.Telemetry.Summary = "Cloudflare API token — run cost comes from gateway logs"
		account, gatewayID, ok := gatewayIDs(gateway)
		switch {
		case opts.Offline:
			report.Telemetry.Detail = "not checked (--offline)"
		case !ok:
			report.Telemetry.Checked = true
			report.Telemetry.Detail = "rejected: the gateway URL names no Cloudflare account and gateway to read logs from"
		default:
			report.Telemetry.Checked = true
			if err := probeGatewayLogs(ctx, client, opts.CloudflareAPIBase, account, gatewayID, telemetry); err != nil {
				report.Telemetry.Detail = "rejected: " + err.Error()
			} else {
				report.Telemetry.OK = true
				report.Telemetry.Detail = "live, gateway logs readable"
			}
		}
	}

	// Workers AI billing mode: which wallet the spend above comes out of.
	report.Billing = CredentialState{Name: "workers ai billing"}
	if gateway != "" {
		report.Billing.Configured = true
		stored := cfg.Get(credentials.KeyWorkersAIBillingMode)
		expected, expectedErr := ExpectedBillingMode(stored)
		_, gatewayID, ok := gatewayIDs(gateway)
		switch {
		case expectedErr != nil:
			report.Billing.Summary = fmt.Sprintf("%s (recorded in %s)", stored, cfg.Path())
			report.Billing.Checked = true
			report.Billing.Detail = "rejected: " + expectedErr.Error()
		case opts.Offline:
			report.Billing.Summary = describeBillingExpectation(expected, stored)
			report.Billing.Detail = "not checked (--offline)"
		case telemetry == "":
			report.Billing.Summary = describeBillingExpectation(expected, stored)
			report.Billing.Detail = "not checked — reading the gateway's billing mode needs the same " +
				"Cloudflare API token as cost telemetry"
		case !ok:
			report.Billing.Summary = describeBillingExpectation(expected, stored)
			report.Billing.Checked = true
			report.Billing.Detail = "rejected: the gateway URL names no Cloudflare account and gateway to read the billing mode from"
		default:
			report.Billing.Summary = describeBillingExpectation(expected, stored)
			report.Billing.Checked = true
			mode, err := CheckWorkersAIBilling(ctx, BillingOptions{
				HTTPClient:         client,
				CloudflareAPIBase:  opts.CloudflareAPIBase,
				GatewayURL:         gateway,
				CloudflareAPIToken: telemetry,
				Expected:           expected,
			})
			if err != nil {
				report.Billing.Detail = "rejected: " + err.Error()
			} else {
				report.Billing.OK = true
				report.Billing.Detail = fmt.Sprintf("live, gateway %s reports %s billing", gatewayID, mode)
			}
		}
	}

	// The classifier: Jev on Workers AI, on the same token and account.
	report.Classifier = CredentialState{Name: "classifier"}
	if account, _, ok := gatewayIDs(gateway); ok && telemetry != "" {
		report.Classifier.Configured = true
		report.Classifier.Summary = "Jev (" + jev.Model + ") on Workers AI — the Cloudflare API token above, on the gateway's account"
		switch {
		case opts.Offline:
			report.Classifier.Detail = "not checked (--offline)"
		default:
			report.Classifier.Checked = true
			classifier := jev.New(jev.Config{
				APIBase: strings.TrimSuffix(strings.TrimSpace(opts.CloudflareAPIBase), "/"), AccountID: account, APIKey: telemetry,
			}, client)
			if detail, err := classifier.Probe(ctx); err != nil {
				report.Classifier.Detail = "rejected: " + err.Error()
			} else {
				report.Classifier.OK = true
				report.Classifier.Detail = "live, " + detail
			}
		}
	}

	return report, nil
}

// githubRungState is the github line: the factory is asked first which rung is
// live — only it knows whether it holds its own GitHub App (epic dm6), and only
// it can mint a token with one — and a factory that predates the App rung, or
// could not be asked, is reported from the local mirror as before.
func githubRungState(ctx context.Context, cfg *credentials.File, client *http.Client, apiBase, url string, offline bool) CredentialState {
	stored := storedGitHubCredential(cfg)
	repo := cfg.Get(credentials.KeyGitHubRepo)
	state := CredentialState{Name: "github"}
	if url != "" && !offline {
		checkRepo := repo
		if checkRepo == "" {
			if detected, err := detectProject(); err == nil {
				checkRepo = detected
			}
		}
		if status, err := FetchGitHubAppStatus(ctx, client, url, cfg.Get(credentials.KeyToken), checkRepo); err == nil && status.Rung == AuthApp {
			return GitHubAppState(status, checkRepo)
		}
	}
	if stored.Token != "" {
		now := time.Now()
		lifetime := DescribeGitHubLifetime(stored.ExpiresAt, now)
		state.Configured = true
		state.Summary = describeGitHub(stored.Auth, cfg.Get(credentials.KeyGitHubLogin), repo)
		switch {
		case offline:
			state.Detail = "not checked (--offline) — " + lifetime
		case !stored.ExpiresAt.IsZero() && !stored.ExpiresAt.After(now):
			// A passed deadline is a LOCAL fact, and a rejection on its own.
			// Waiting for the probe to agree would report "live" for a
			// credential the operator has to renew before the next run, which
			// is the state this rung exists to surface.
			state.Checked = true
			state.Detail = fmt.Sprintf("rejected: %s — run `ticfac factory setup` to renew it%s",
				lifetime, renewalCost(stored))
		default:
			state.Checked = true
			login, err := probeGitHubUser(ctx, client, apiBase, stored.Token)
			if err != nil {
				state.Detail = "rejected: " + err.Error()
				break
			}
			if repo == "" {
				state.OK = true
				state.Detail = fmt.Sprintf("live (@%s), %s; no repository recorded to check the scope against", login, lifetime)
				break
			}
			push, err := probeGitHubRepo(ctx, client, apiBase, stored.Token, repo)
			switch {
			case err != nil:
				state.Detail = fmt.Sprintf("rejected for %s: %v", repo, err)
			case !push:
				state.Detail = fmt.Sprintf("rejected: read-only on %s — the factory could not push", repo)
			default:
				state.OK = true
				state.Detail = fmt.Sprintf("live (@%s), can write to %s, %s", login, repo, lifetime)
			}
		}
	}

	return state
}

// GitHubRung is the github line on its own, live: what `ticfac doctor` asks
// for, so it names the rung the factory actually uses and checks it the way
// status does — for the App rung, a read-only token minted for the repository.
func GitHubRung(ctx context.Context, opts StatusOptions) (CredentialState, error) {
	cfg, err := loadConfig(opts.ConfigPath)
	if err != nil {
		return CredentialState{}, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = httpnet.Client(15 * time.Second)
	}
	apiBase := strings.TrimSuffix(strings.TrimSpace(opts.GitHubAPIBase), "/")
	if apiBase == "" {
		apiBase = defaultGitHubAPIBase
	}
	url := strings.TrimSuffix(cfg.Get(credentials.KeyURL), "/")
	return githubRungState(ctx, cfg, client, apiBase, url, opts.Offline), nil
}

// describeBillingExpectation says which mode is asserted and whether that was
// chosen or defaulted, because "postpaid because nobody said otherwise" and
// "postpaid because the operator settled on it" read the same in a report and
// are not the same claim.
func describeBillingExpectation(expected, stored string) string {
	source := "the default"
	if strings.TrimSpace(stored) != "" {
		source = "recorded"
	}
	return fmt.Sprintf("expects %s (%s) — %s", expected, source, DescribeBillingMode(expected))
}

// Write renders the report the way `tk channel status` renders channels: what
// is configured first, then whether it works.
func (r *StatusReport) Write(w io.Writer) {
	if !r.Configured() {
		fmt.Fprintln(w, "No factory is configured.")
		fmt.Fprintln(w, "Run 'ticfac factory setup' to deploy one and walk the credential ladder.")
		return
	}

	fmt.Fprintf(w, "Factory config: %s\n", r.ConfigPath)
	for _, state := range r.rungs() {
		fmt.Fprintf(w, "\n%s\n", state.Name)
		if !state.Configured {
			hint := "run 'ticfac factory setup'"
			if state.Name == "cost telemetry" {
				// The one rung a factory runs without. Say what it costs, and
				// say the flag: a budget with nothing to act on is a fact the
				// operator should choose, not discover after a run.
				hint = "run cost is unknown and the cost budget cannot act — add one with 'ticfac factory setup --cloudflare-api-token <token>'"
			}
			if state.Name == "classifier" {
				// It rides the telemetry token and the gateway's account, so
				// the remedy is theirs — and the cost of going without is a
				// run that classifies nothing, not a run that stops.
				hint = "Jev runs on Workers AI with the cost-telemetry token and the gateway's account — without both, runs classify nothing and every dispatch starts at [tier_policy.start]"
			}
			fmt.Fprintf(w, "  state         not configured — %s\n", hint)
			continue
		}
		fmt.Fprintf(w, "  configured    %s\n", state.Summary)
		fmt.Fprintf(w, "  check         %s\n", orUnchecked(state.Detail))
	}
}

func orUnchecked(detail string) string {
	if detail == "" {
		return "not checked"
	}
	return detail
}

// renewalCost says what renewing will actually take, because "run setup again"
// means two different things: a silent refresh for a device-flow credential
// with a live refresh token, and a browser for anything else.
func renewalCost(stored githubCredential) string {
	switch {
	case stored.Auth != AuthDeviceFlow:
		return " with a new token"
	case stored.RefreshToken != "" && (stored.RefreshExpiresAt.IsZero() || stored.RefreshExpiresAt.After(time.Now())):
		return " — no browser needed, its refresh token is still live"
	default:
		return " — one approval at github.com/login/device"
	}
}

// GitHubAppState is the github line for a factory on the App rung, from the
// factory's own live answer: a read-only token minted for the repository, or
// the reason none could be.
func GitHubAppState(status *GitHubAppStatus, repo string) CredentialState {
	state := CredentialState{Name: "github", Configured: true, Checked: true}
	state.Summary = "rung: app — " + DescribeGitHubApp(status.App)
	switch {
	case status.App == nil:
		state.Detail = "rejected: the factory names the App rung but no App"
	case status.App.Error != "":
		state.Detail = "rejected: " + status.App.Error
	case repo == "" && len(status.App.Installations) > 0:
		state.OK = true
		state.Detail = "live, installed; no repository recorded to mint a token for — pass one with `ticfac factory setup --repo owner/name`"
	case repo == "":
		state.Detail = "rejected: the App is not installed anywhere — install it at " + status.App.InstallURL
	case status.Check != nil && status.Check.OK:
		state.OK = true
		state.Detail = fmt.Sprintf("live: the factory minted a read-only token for %s (installation %d)", repo, status.Check.InstallationID)
	case status.Check != nil && status.Check.Detail != "":
		state.Detail = fmt.Sprintf("rejected for %s: %s", repo, status.Check.Detail)
	default:
		state.Detail = fmt.Sprintf("rejected for %s: the factory did not check it", repo)
	}
	return state
}

func describeGitHub(auth, login, repo string) string {
	kind := "rung: pat — fine-grained PAT"
	if auth == AuthDeviceFlow {
		kind = "rung: device-flow — device flow (user-to-server token)"
	}
	parts := []string{kind}
	if login != "" {
		parts = append(parts, "@"+login)
	}
	if repo != "" {
		parts = append(parts, "for "+repo)
	}
	return strings.Join(parts, " ")
}

func describeGateway(url, provider string, hasKey bool) string {
	if provider == "" {
		provider = "(no provider recorded)"
	}
	suffix := "no key needed"
	if hasKey {
		suffix = "key stored as a Worker secret"
	}
	return fmt.Sprintf("%s — %s, %s", url, provider, suffix)
}
