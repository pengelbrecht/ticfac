package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

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
func factoryDeploy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("factory deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		bundleDir   = fs.String("bundle-dir", "", "stage the embedded bundle here")
		rotateToken = fs.Bool("rotate-token", false, "mint a new factory token instead of reusing the stored one")
		url         = fs.String("url", "", "the factory's base endpoint, when wrangler's output does not name it")
		skipRollout = fs.Bool("skip-rollout-wait", false, "accept an unconfirmed container rollout")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
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
	})
	if err != nil {
		// Every failure is a stop with an explanation the operator can act
		// on — a missing prerequisite included. Nothing falls back to a
		// default deployment.
		fmt.Fprintf(stderr, "ticfac factory deploy: %v\n", err)
		return 1
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
func factorySetup(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("factory setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		bundleDir    = fs.String("bundle-dir", "", "stage the embedded bundle here")
		repo         = fs.String("repo", "", "the repository the GitHub credential must reach")
		githubToken  = fs.String("github-token", "", "supply the GitHub credential by hand")
		githubAPI    = fs.String("github-api", "", "GitHub's REST root (tests, GHES)")
		githubClient = fs.String("github-client", "", "the GitHub App client id for the device flow")
		githubOAuth  = fs.String("github-oauth", "", "the host the device flow runs on")
		gatewayURL   = fs.String("gateway-url", "", "the AI Gateway base URL")
		provider     = fs.String("provider", "", "the provider behind the gateway")
		providerKey  = fs.String("provider-key", "", "the BYOK provider's key")
		cfAPIToken   = fs.String("cloudflare-api-token", "", "add cost telemetry: read what the gateway billed")
		billingMode  = fs.String("workers-ai-billing-mode", "", "the wallet the gateway bills: postpaid | unified")
		cfAPIBase    = fs.String("cloudflare-api-base", "", "Cloudflare's REST root (tests)")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "ticfac factory setup: takes no positional arguments\n")
		return 2
	}

	_, err := factory.Setup(context.Background(), factory.SetupOptions{
		Version:              Version,
		BundleDir:            *bundleDir,
		In:                   os.Stdin,
		Out:                  stdout,
		GitHubAPIBase:        *githubAPI,
		Repo:                 *repo,
		GitHubToken:          *githubToken,
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
	return 0
}
