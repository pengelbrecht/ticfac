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

// factoryUsage is printed for `ticfac factory` with no (or an unknown)
// subcommand.
const factoryUsage = `usage:
  ticfac factory deploy   put the factory in your own Cloudflare account
  ticfac factory setup    walk the credential ladder, one verified rung at a time

deploy flags:
  --bundle-dir <dir>     stage the embedded bundle here (default: ~/.tick/factory/bundle)
  --rotate-token         mint a new factory token instead of reusing the stored one
  --url <url>            the factory's base endpoint, when wrangler's output does not name it
  --skip-rollout-wait    accept an unconfirmed container rollout, deliberately

setup flags:
  --repo <owner/name>      the repository the GitHub credential must reach
                           (default: the checkout's origin remote)
  --github-token <token>   supply the GitHub credential by hand (bypasses the device flow)
  --github-api <url>       GitHub's REST root (tests, GHES)
  --github-client <id>     the GitHub App client id for the device flow
  --github-oauth <url>     the host the device flow runs on (default: github.com)
  --gateway-url <url>      the AI Gateway base URL
  --provider <id>          workers-ai | anthropic | openai | openrouter
  --provider-key <key>     the BYOK provider's key (Workers AI needs none)
  --cloudflare-api-token <token>   add cost telemetry: read what the gateway billed
  --workers-ai-billing-mode <mode>  postpaid | unified — the wallet the gateway bills
  --cloudflare-api-base <url>      Cloudflare's REST root (tests)
  --bundle-dir <dir>       stage the embedded bundle here

Every answer setup asks for can also be supplied as a flag, which is what makes
the walk scriptable. Nothing either command stores ever lands in a repository.
`

// factoryCommand dispatches the factory subcommands.
func factoryCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "ticfac factory: a subcommand is required\n\n%s", factoryUsage)
		return 2
	}
	switch args[0] {
	case "deploy":
		return factoryDeploy(args[1:], stdout, stderr)
	case "setup":
		return factorySetup(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ticfac factory: unknown subcommand %q\n\n%s", args[0], factoryUsage)
		return 2
	}
}

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
