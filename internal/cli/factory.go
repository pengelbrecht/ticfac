package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/factory"
)

// The factory commands: `ticfac factory deploy` and `ticfac factory setup`.
//
// They are an installer and a first-run walk for the ticks cloud factory
// deployed into the operator's OWN Cloudflare account (D16 in the ticks SPEC:
// the factory is a deployable, not a service). Everything they do lives in
// internal/factory, which moved from ticks (tick v3i, split ek7/b3a/0e1); what
// this file owns is the flag surface and one rule every subcommand shares:
// a failure is a stop with the remedy in it, printed to stderr, exit 1 — never
// a usage error (which would bury the remedy under a flag list) and never a
// silent partial configuration.
//
// The read half — `ticfac factory status` and `ticfac factory dashboard`, and
// the `ticfac cloud …` slice — moves with ticks tick 0e1 ("Factory move C").

// factoryCommand dispatches the factory subcommands.
// factoryDeploy installs (or upgrades) the factory in the operator's own
// Cloudflare account, from the bundle embedded in this build.
// newFactoryDeployCommand builds `factory deploy`'s cobra command.
func newFactoryDeployCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "put the ticks cloud factory in your own Cloudflare account",
		Long: "Install (or upgrade) the factory in the operator's OWN Cloudflare account,\nfrom the bundle embedded in this build. A failure is a stop with the remedy\nin it, never a half-configured account left behind.\n\n" +
			"CI is the normal path: .github/workflows/deploy-factory.yml runs this same\n" +
			"command for every commit on main that CI passed and that changes what the\n" +
			"factory ships (and for v* tags, and on workflow_dispatch — which is also how\n" +
			"a failed CI deploy is retried). Run it locally only as the fallback: for the\n" +
			"first install (after `ticfac factory setup`), to rotate the token, or when\n" +
			"CI cannot deploy.",
	}
	fs := flag.NewFlagSet("factory deploy", flag.ContinueOnError)
	var (
		bundleDir   = fs.String("bundle-dir", "", "stage the embedded bundle here")
		rotateToken = fs.Bool("rotate-token", false, "mint a new factory token instead of reusing the stored one")
		url         = fs.String("url", "", "the factory's base endpoint, when wrangler's output does not name it")
		skipRollout = fs.Bool("skip-rollout-wait", false, "accept an unconfirmed container rollout")
		skipPrune   = fs.Bool("skip-image-prune", false, "leave old ticks-orchestrator images in the managed registry (by default all but the newest few and the served one are deleted)")
		keepImages  = fs.Int("keep-images", 0, "how many of the newest ticks-orchestrator images a prune keeps besides the served one (default 5)")
		asJSON      = fs.Bool("json", false, "print one versioned document (ticfac.factory-deploy.v1) with the deployment's facts; the token is never in it")
	)
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(factoryDeploy(args, bundleDir, rotateToken, url, skipRollout, skipPrune, keepImages, asJSON, stdout, stderr))
	}
	return cmd
}

func factoryDeploy(args []string, bundleDir *string, rotateToken *bool, url *string, skipRollout, skipPrune *bool, keepImages *int, asJSON *bool, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "ticfac factory deploy: takes no positional arguments\n")
		return 2
	}

	result, err := factory.Deploy(context.Background(), factory.Options{
		Version:         Version,
		BundleDir:       *bundleDir,
		RotateToken:     *rotateToken,
		URL:             *url,
		Out:             stdout,
		SkipRolloutWait: *skipRollout,
		SkipImagePrune:  *skipPrune,
		ImageKeep:       *keepImages,
	})
	if err != nil {
		// Every failure is a stop with an explanation the operator can act
		// on — a missing prerequisite included. Nothing falls back to a
		// default deployment.
		fmt.Fprintf(stderr, "ticfac factory deploy: %v\n", err)
		return 1
	}

	if *asJSON {
		// The deployment's facts, and never the token: a credential that
		// travelled inside a document would be one a log, a paste or an agent's
		// transcript carried for free.
		doc := struct {
			agentDoc
			URL              string `json:"url"`
			Version          string `json:"version"`
			SourceRef        string `json:"source_ref"`
			BundleSHA        string `json:"bundle_sha"`
			ImageRef         string `json:"image_ref"`
			ImageDigest      string `json:"image_digest"`
			WorkerVersionID  string `json:"worker_version_id"`
			RolloutConfirmed bool   `json:"rollout_confirmed"`
			Rotated          bool   `json:"token_rotated"`
			ConfigPath       string `json:"credentials_path"`
		}{
			agentDoc:         agentDoc{Schema: agentSchemaID("factory-deploy"), State: agentStateDone},
			URL:              result.URL,
			Version:          result.Version,
			SourceRef:        result.SourceRef,
			BundleSHA:        result.BundleSHA,
			ImageRef:         result.ImageRef,
			ImageDigest:      result.ImageDigest,
			WorkerVersionID:  result.WorkerVersionID,
			RolloutConfirmed: result.RolloutConfirmed,
			Rotated:          result.Rotated,
			ConfigPath:       result.ConfigPath,
		}
		if err := emitAgentJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "ticfac factory deploy: %v\n", err)
			return 1
		}
		return 0
	}

	// "Ready" is a claim about what a run started now would boot, so it is
	// only made when the container rollout was actually confirmed.
	if result.RolloutConfirmed {
		fmt.Fprintf(stdout, "\nFactory ready at %s\n", result.URL)
	} else {
		fmt.Fprintf(stdout, "\nFactory deployed at %s — container rollout NOT confirmed\n", result.URL)
	}
	fmt.Fprintf(stdout, "  ticfac:      %s\n", result.Version)
	fmt.Fprintf(stdout, "  image tk:    built from %s\n", result.SourceRef)
	if result.WorkerVersionID != "" {
		fmt.Fprintf(stdout, "  worker:      %s\n", result.WorkerVersionID)
	}
	if result.ImageDigest != "" {
		fmt.Fprintf(stdout, "  image:       %s\n", result.ImageDigest)
	}
	if !result.RolloutConfirmed {
		fmt.Fprintf(stdout, "  rollout:     unconfirmed — a run started now may still boot the previous image\n")
	}
	fmt.Fprintf(stdout, "  credentials: %s\n", result.ConfigPath)
	if result.Rotated {
		fmt.Fprintf(stdout, "  token:       rotated — anything holding the previous token must be updated\n")
	}
	return 0
}

// factorySetup walks the factory's credential ladder: a deployment, a GitHub
// credential (the device flow by default), and model access through the
// operator's own AI Gateway. It prompts for anything a flag did not supply.
// newFactorySetupCommand builds `factory setup`'s cobra command.
func newFactorySetupCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "walk the factory's credential ladder, one verified rung at a time",
		Long:  "The first-run walk: a deployment, a GitHub credential (the factory's own\nGitHub App by default — one link, two clicks on any device; then the device\nflow or a PAT), and model access through the operator's own AI Gateway — every\nrung verified against the live service before it is stored. It prompts for\nanything a flag did not supply.",
	}
	fs := flag.NewFlagSet("factory setup", flag.ContinueOnError)
	var (
		bundleDir    = fs.String("bundle-dir", "", "stage the embedded bundle here")
		repo         = fs.String("repo", "", "the repository the GitHub credential must reach")
		githubToken  = fs.String("github-token", "", "supply the GitHub credential by hand")
		githubApp    = fs.String("github-app", "auto", "the factory's own GitHub App: auto (offer it), yes, or no (use a token rung)")
		githubOrg    = fs.String("github-org", "", "register the GitHub App under this organization instead of your user")
		githubAPI    = fs.String("github-api", "", "GitHub's REST root (tests, GHES)")
		githubClient = fs.String("github-client", "", "the GitHub App client id for the device flow")
		githubOAuth  = fs.String("github-oauth", "", "the host the device flow runs on")
		gatewayURL   = fs.String("gateway-url", "", "the AI Gateway base URL")
		provider     = fs.String("provider", "", "the provider behind the gateway")
		providerKey  = fs.String("provider-key", "", "the BYOK provider's key")
		cfAPIToken   = fs.String("cloudflare-api-token", "", "add cost telemetry: read what the gateway billed")
		billingMode  = fs.String("workers-ai-billing-mode", "", "the wallet the gateway bills: postpaid | unified")
		cfAPIBase    = fs.String("cloudflare-api-base", "", "Cloudflare's REST root (tests)")
		asJSON       = fs.Bool("json", false, "print one versioned document (ticfac.factory-setup.v1) with the walked ladder's facts; no credential value is ever in it")
	)
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(factorySetup(args, bundleDir, repo, githubToken, githubApp, githubOrg, githubAPI, githubClient, githubOAuth,
			gatewayURL, provider, providerKey, cfAPIToken, billingMode, cfAPIBase, asJSON, stdout, stderr))
	}
	return cmd
}

func factorySetup(args []string, bundleDir, repo, githubToken, githubApp, githubOrg, githubAPI, githubClient, githubOAuth,
	gatewayURL, provider, providerKey, cfAPIToken, billingMode, cfAPIBase *string, asJSON *bool, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "ticfac factory setup: takes no positional arguments\n")
		return 2
	}

	result, err := factory.Setup(context.Background(), factory.SetupOptions{
		Version:   Version,
		BundleDir: *bundleDir,
		In:        os.Stdin,
		// The walk's prose is a person's: under --json it goes to stderr so
		// stdout stays the one document's.
		Out:                  setupProseOut(asJSON, stdout, stderr),
		GitHubAPIBase:        *githubAPI,
		Repo:                 *repo,
		GitHubToken:          *githubToken,
		GitHubApp:            *githubApp,
		GitHubOrg:            *githubOrg,
		GitHubClientID:       *githubClient,
		GitHubOAuthBase:      *githubOAuth,
		GatewayURL:           *gatewayURL,
		Provider:             *provider,
		ProviderKey:          *providerKey,
		CloudflareAPIToken:   *cfAPIToken,
		CloudflareAPIBase:    *cfAPIBase,
		WorkersAIBillingMode: *billingMode,
	})
	if err != nil {
		// A rung that did not verify is a stop with an explanation, never a
		// partially configured factory left behind quietly.
		fmt.Fprintf(stderr, "ticfac factory setup: %v\n", err)
		return 1
	}
	if *asJSON {
		// The walked ladder's facts — which rungs were verified, what the
		// gateway bills, the models it listed — and never a credential VALUE:
		// the document is the map of what is stored, not the store.
		doc := struct {
			agentDoc
			URL             string   `json:"url"`
			Version         string   `json:"version"`
			ConfigPath      string   `json:"config_path"`
			Deployed        bool     `json:"deployed"`
			GitHubLogin     string   `json:"github_login"`
			GitHubAuth      string   `json:"github_auth"`
			GitHubRefreshed bool     `json:"github_refreshed"`
			GitHubAppID     string   `json:"github_app_id,omitempty"`
			GitHubAppSlug   string   `json:"github_app_slug,omitempty"`
			GatewayURL      string   `json:"gateway_url"`
			Provider        string   `json:"provider"`
			CostTelemetry   bool     `json:"cost_telemetry"`
			BillingMode     string   `json:"workers_ai_billing_mode"`
			Models          []string `json:"models"`
		}{
			agentDoc:        agentDoc{Schema: agentSchemaID("factory-setup"), State: agentStateDone},
			URL:             result.URL,
			Version:         result.Version,
			ConfigPath:      result.ConfigPath,
			Deployed:        result.Deployed,
			GitHubLogin:     result.GitHubLogin,
			GitHubAuth:      result.GitHubAuth,
			GitHubRefreshed: result.GitHubRefreshed,
			GitHubAppID:     result.GitHubAppID,
			GitHubAppSlug:   result.GitHubAppSlug,
			GatewayURL:      result.GatewayURL,
			Provider:        result.Provider,
			CostTelemetry:   result.CostTelemetry,
			BillingMode:     result.WorkersAIBillingMode,
			Models:          result.Models,
		}
		if result.Models == nil {
			doc.Models = []string{}
		}
		if err := emitAgentJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "ticfac factory setup: %v\n", err)
			return 1
		}
		return 0
	}
	return 0
}

// setupProseOut picks where the setup walk's own prose goes: stdout for a
// person, stderr under --json where the document owns stdout.
func setupProseOut(asJSON *bool, stdout, stderr io.Writer) io.Writer {
	if *asJSON {
		return stderr
	}
	return stdout
}
