package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// The container applications a deploy manages.
//
// There is one application now: `ticks-factory-sandbox`, the FactorySandbox
// class on the `durable_object` scheduling policy (epic umq). A container on
// it keeps the image it started on through every later deploy, and each start
// picks its own image and instance size — so there is no application-wide
// rollout for a deploy to wait for, and no grace period to keep one off a
// live run's containers. What a deploy still does after `wrangler deploy`
// succeeds is resolve the image it pushed (so it can be recorded, which is
// what a run's `run_image` stamp and `factory status` read), and delete the
// 0.x application `ticks-orchestrator` from the account once no run is still
// on it (legacyapp.go).
//
// The image is read from the two places it appears. `wrangler deploy` prints
// the application's configuration — the whole snippet for a new application,
// a diff for an existing one — and the image line inside it is the only place
// the pushed digest appears at normal verbosity (the push itself logs the
// digest at debug level only). When the push was an idempotent skip
// ("Image already exists remotely, skipping push"), wrangler emits no image
// block at all, and the image is read back from the application record
// instead: one `wrangler containers list --json`, no waiting.

// FactorySandboxAppName is the `[[containers]]` application the bundle
// declares for the FactorySandbox class. It must stay in step with
// cloudflare/wrangler.toml.
const FactorySandboxAppName = "ticks-factory-sandbox"

// LegacyContainerAppName is the 0.x application the bundle no longer declares.
// It was the `Sandbox` class's application until tick dax deleted it; the
// name is what the deploy looks the leftover application up by, and what its
// image repository is called, so the leftover application and its images can
// be deleted.
const LegacyContainerAppName = "ticks-orchestrator"

// FactorySandboxImagePattern matches a digest-pinned reference to the
// FactorySandbox application's image. Anchoring on the image repository —
// `<worker>-<class, lowercased>-<image name>`, the same name
// FactorySandboxImageRepo (prune.go) deletes against — is what keeps this
// from matching the hundreds of `sha256:` digests a Docker build log
// contains: the base image, every layer, every cache mount. Only the
// reference wrangler is deploying carries the repository in its path.
var FactorySandboxImagePattern = regexp.MustCompile(
	`[A-Za-z0-9._/-]*` + regexp.QuoteMeta(FactorySandboxImageRepo) + `[A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}`)

// digestPattern pulls the digest out of an image reference.
var digestPattern = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

// parseDeployedImage returns the digest-pinned FactorySandbox image reference
// in wrangler's deploy output, and its digest.
//
// The LAST match wins: when wrangler prints a diff for the application, the
// previous image is on the `-` side and the new one on the `+` side below it.
// Empty when the output names none — which is the idempotent no-push path's
// normal shape, and the caller reads the image back from the application
// record rather than failing the deploy for it.
func parseDeployedImage(out string) (ref, digest string) {
	matches := FactorySandboxImagePattern.FindAllString(out, -1)
	if len(matches) == 0 {
		return "", ""
	}
	ref = matches[len(matches)-1]
	return ref, digestPattern.FindString(ref)
}

// containerApp is one entry of `wrangler containers list --json`.
//
// The field set is wrangler's own JSON projection of the dashboard
// application record, not the raw API object: id, name, state, instances,
// image, version, timestamps. Only what is read is declared — the id is what
// `wrangler containers delete` takes, the image what the deploy's image
// resolution falls back to.
type containerApp struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	// Instances is the live instance count, which is 0 for an idle factory:
	// a container exists only while a run does.
	Instances int    `json:"instances"`
	Image     string `json:"image"`
	Version   int    `json:"version"`
}

// digest returns the image digest this application is configured with, or ""
// when its image reference is not digest-pinned.
func (a containerApp) digest() string { return digestPattern.FindString(a.Image) }

// listContainerApps returns the account's container applications.
func (w *wrangler) listContainerApps(ctx context.Context) ([]containerApp, error) {
	out, err := w.run(ctx, "", "containers", "list", "--json")
	if err != nil {
		return nil, err
	}
	raw, ok := jsonArray(out)
	if !ok {
		return nil, fmt.Errorf("could not read the container application list from "+
			"`wrangler containers list --json`:\n%s", strings.TrimSpace(out))
	}
	var apps []containerApp
	if err := json.Unmarshal([]byte(raw), &apps); err != nil {
		return nil, fmt.Errorf("could not parse `wrangler containers list --json`: %w", err)
	}
	return apps, nil
}

// findContainerApp returns the named application, or false when the listing
// does not have one.
func findContainerApp(apps []containerApp, name string) (containerApp, bool) {
	for _, app := range apps {
		if app.Name == name {
			return app, true
		}
	}
	return containerApp{}, false
}

// resolveDeploymentImage identifies the image this deployment serves: the
// digest-pinned FactorySandbox reference wrangler's deploy output names, or —
// when the push was an idempotent skip and wrangler printed no image block —
// the one the application record reports. It never fails the deploy: an image
// that cannot be determined is a warning and no record, not a rollback,
// because a durable_object application has no rollout whose success the
// digest was evidence of.
func resolveDeploymentImage(ctx context.Context, w *wrangler, out io.Writer, deployOut string) (ref, digest string) {
	ref, digest = parseDeployedImage(deployOut)
	if digest != "" {
		fmt.Fprintf(out, "the %s application serves %s\n", FactorySandboxAppName, shortDigest(digest))
		return ref, digest
	}
	fmt.Fprintf(out, "wrangler's deploy output named no %s image (an unchanged image is not "+
		"pushed again); reading the application record\n", FactorySandboxImageRepo)
	apps, err := w.listContainerApps(ctx)
	if err != nil {
		fmt.Fprintf(out, "WARNING: could not determine the image this deployment serves "+
			"(the application list could not be read: %v); not recording one\n", err)
		return "", ""
	}
	app, ok := findContainerApp(apps, FactorySandboxAppName)
	if !ok {
		fmt.Fprintf(out, "WARNING: could not determine the image this deployment serves "+
			"(the account lists no application named %s); not recording one\n", FactorySandboxAppName)
		return "", ""
	}
	if digest := app.digest(); digest != "" {
		fmt.Fprintf(out, "  existing application image is %s\n", shortDigest(digest))
		return app.Image, digest
	}
	fmt.Fprintf(out, "WARNING: the %s application record names no digest-pinned image; "+
		"not recording one\n", FactorySandboxAppName)
	return "", ""
}

// shortDigest renders a digest for a line of deploy output: enough hex to
// tell one image from another, not the whole 71-character reference.
func shortDigest(digest string) string {
	hex := strings.TrimPrefix(digest, "sha256:")
	if len(hex) > 12 {
		return "sha256:" + hex[:12]
	}
	return digest
}
