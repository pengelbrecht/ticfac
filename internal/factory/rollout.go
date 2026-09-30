package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// The container rollout is the half of a deploy that `wrangler deploy` does
// not wait for.
//
// Wrangler builds the image, pushes it, calls `modifyApplication` with the new
// configuration and then POSTs a rollout — and returns as soon as the rollout
// has been *created*. The rollout itself is asynchronous, so `wrangler deploy`
// exits while the container application is still serving the previous image.
//
// That gap is not cosmetic. A run started immediately after a green deploy
// boots the PREVIOUS container, which makes a correct fix look like it did not
// work: two consecutive runs produced byte-identical harness output across a
// deploy that had rebuilt and pushed a new image. The natural response to that
// signal is to go looking for a second bug that does not exist, which is the
// most expensive kind of false signal a deploy can emit.
//
// So the deploy waits here, and it waits on the one thing that is actually
// observable through the CLI the operator already has: the image the container
// application reports. `wrangler containers list --json` prints, per
// application, `{id, name, state, instances, image, version, ...}` where
// `image` is the digest-pinned reference the application is configured with
// and `state` is derived from live instance health (`provisioning` while any
// instance is starting or scheduling, `degraded` when one has failed). The
// wait converges when that reference carries the digest this deploy should
// serve.
// A matching digest in `provisioning` is already the new image serving; the
// state can lag the image record by one listing near the rollout boundary.
//
// What it deliberately does NOT do is report success it cannot prove. A
// rollout that has not converged inside the bound, an application the account
// does not list, or an output that neither names a digest nor says the image
// push was skipped — each of those ends the deploy with a message saying
// exactly which one it was. An idempotent deploy whose push was skipped reads
// the existing digest from the application record and confirms that instead.
// `--skip-rollout-wait` is the escape hatch, and it says in the output that
// nothing was confirmed.

// ContainerAppName is the `[[containers]]` application the bundle declares.
// Wrangler creates and looks the application up by exactly this name (it is
// `containerConfig.name`), so it is also the name the rollout wait asks about.
// It must stay in step with cloudflare/wrangler.toml.
const ContainerAppName = "ticks-orchestrator"

const (
	// How long the rollout may take before the deploy stops waiting. A live
	// rollout observed in the field crossed the old five-minute bound and
	// reported the correct digest about 30 seconds later. Ten minutes leaves
	// bounded headroom for that observed tail without turning a stuck rollout
	// into an unbounded deploy.
	defaultRolloutTimeout = 10 * time.Minute
	defaultRolloutPoll    = 5 * time.Second
)

// pushedImagePattern matches a digest-pinned reference to the orchestrator
// image. Wrangler prints the container application's configuration — the whole
// snippet for a new application, a diff for an existing one — and the image
// line inside it is the only place the pushed digest appears at normal
// verbosity (the push itself logs the digest at debug level only).
//
// Anchoring on the application name is what keeps this from matching the
// hundreds of `sha256:` digests a Docker build log contains: the base image,
// every layer, every cache mount. Only the reference wrangler is deploying
// carries `ticks-orchestrator` in its path.
var pushedImagePattern = regexp.MustCompile(
	`[A-Za-z0-9._/-]*` + regexp.QuoteMeta(ContainerAppName) + `[A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}`)

// digestPattern pulls the digest out of an image reference.
var digestPattern = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

// skippedImagePushPattern is the stable wrangler line for an idempotent image
// deploy. In this path wrangler emits no container image block, so the absence
// of a parsed digest is only recoverable when this line explicitly says that
// no push took place. A malformed or unexpectedly quiet deploy must still fail
// closed as it did before the rollout wait existed.
var skippedImagePushPattern = regexp.MustCompile(`(?im)^\s*Image already exists remotely, skipping push\s*$`)

// parsePushedImage returns the digest-pinned orchestrator image reference in
// wrangler's deploy output, and its digest.
//
// The LAST match wins: when wrangler prints a diff for an existing
// application, the previous image is on the `-` side and the new one on the
// `+` side below it. Empty when the output names none. Callers may recover an
// empty digest only when deploySkippedImagePush reports Wrangler's explicit
// idempotent no-push path.
func parsePushedImage(out string) (ref, digest string) {
	matches := pushedImagePattern.FindAllString(out, -1)
	if len(matches) == 0 {
		return "", ""
	}
	ref = matches[len(matches)-1]
	return ref, digestPattern.FindString(ref)
}

func deploySkippedImagePush(out string) bool {
	return skippedImagePushPattern.MatchString(out)
}

// containerApp is one entry of `wrangler containers list --json`.
//
// The field set is wrangler's own JSON projection of the dashboard
// application record, not the raw API object: id, name, state, instances,
// image, version, timestamps. Only what the wait reads is declared.
type containerApp struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	// Instances is the live instance count, which is 0 for an idle factory:
	// a container exists only while a run does. It is reported, never waited
	// on — "no instances" is the healthy resting state here.
	Instances int    `json:"instances"`
	Image     string `json:"image"`
	Version   int    `json:"version"`
}

// digest returns the image digest this application is configured with, or ""
// when its image reference is not digest-pinned.
func (a containerApp) digest() string { return digestPattern.FindString(a.Image) }

// settled reports whether the application is between transitions. Wrangler
// derives `provisioning` from instances that are starting or scheduling and
// `degraded` from instances that have failed; `active` and `ready` are the two
// resting states (with and without live instances).
func (a containerApp) settled() bool { return a.State == "active" || a.State == "ready" }

// serves reports whether the application is reporting the expected image. A
// matching image in `provisioning` is useful evidence rather than a stale
// rollout: the image has already changed, and the state can lag that record at
// the edge of the rollout. A degraded application is not accepted even when
// its configured image matches.
func (a containerApp) serves(expected string) bool {
	if expected == "" || a.digest() != expected {
		return false
	}
	return a.settled() || a.State == "provisioning"
}

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

// RolloutError reports that the deploy could not prove the container
// application is serving the image it should serve.
//
// It is its own type because the deployment itself succeeded: the bundle is
// uploaded, the secrets landed and ~/.ticfacrc holds the credentials. What
// failed is the confirmation, and the remedy is to wait and look rather than
// to undo anything. Saying that plainly is the whole point — the alternative
// is the green deploy that sends the operator hunting for a bug that is not
// there.
type RolloutError struct {
	// Expected is the digest this deploy should serve, "" when it could not be
	// read. An idempotent deploy gets it from the application record.
	Expected string
	// ExpectedFromApplication records that Expected came from the existing
	// application record because Wrangler skipped the image push.
	ExpectedFromApplication bool
	// Serving is the digest the application last reported, "" when unknown.
	Serving string
	// Reason names which confirmation failed, in one sentence.
	Reason string
	// Waited is how long the wait ran before giving up. Zero when the wait
	// never started.
	Waited time.Duration
	// Err is the underlying failure, if any.
	Err error
}

func (e *RolloutError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "the factory deployed, but the %s container rollout could not be confirmed: %s",
		ContainerAppName, e.Reason)
	if e.Expected != "" {
		label := "image this deploy pushed"
		if e.ExpectedFromApplication {
			label = "image the application record reports"
		}
		fmt.Fprintf(&b, "\n  %s: %s", label, e.Expected)
	}
	if e.Serving != "" {
		fmt.Fprintf(&b, "\n  image the application reports: %s", e.Serving)
	}
	if e.Waited > 0 {
		fmt.Fprintf(&b, "\n  waited: %s", e.Waited.Round(time.Second))
	}
	message := "the image this deploy built rather than the previous one"
	if e.ExpectedFromApplication {
		message = "the image this deploy expected rather than an older one"
	}
	b.WriteString("\n" +
		"Nothing is lost and nothing needs undoing — the bundle, the secrets and your\n" +
		"credentials all landed. What is not proven is that a run started NOW would boot\n" +
		message + ", which is why this is\n" +
		"not being reported as success.\n" +
		"Check `wrangler containers list` until the image matches, then re-run\n" +
		"`ticfac factory deploy` (it is idempotent), or deploy with --skip-rollout-wait to\n" +
		"accept the unconfirmed state deliberately.")
	return b.String()
}

func (e *RolloutError) Unwrap() error { return e.Err }

// rolloutOutcome is what a confirmed (or deliberately skipped) rollout leaves
// for the Result and for the operator.
type rolloutOutcome struct {
	// Ref is the digest-pinned image reference the deploy confirmed.
	Ref string
	// Digest is the image digest the application was confirmed to serve.
	Digest string
	// Confirmed reports whether the application actually answered with it.
	Confirmed bool
}

// confirmContainerRollout waits for the container application to report the
// image this deploy should serve, and reports honestly when it cannot.
func confirmContainerRollout(
	ctx context.Context,
	w *wrangler,
	out io.Writer,
	deployOut string,
	timeout, poll, extension time.Duration,
	skip bool,
) (rolloutOutcome, error) {
	ref, digest := parsePushedImage(deployOut)
	noPush := deploySkippedImagePush(deployOut)
	outcome := rolloutOutcome{Ref: ref, Digest: digest}

	if skip {
		fmt.Fprintf(out, "container rollout NOT confirmed (--skip-rollout-wait): "+
			"a run started now may still boot the previous image\n")
		if ref != "" {
			fmt.Fprintf(out, "  image this deploy pushed: %s\n", ref)
		}
		return outcome, nil
	}

	if digest == "" && !noPush {
		// Not a parse bug to shrug at: without the digest there is nothing to
		// compare the application against, so the deploy cannot tell a rolled
		// out image from a stale one — the exact condition this wait exists
		// to remove.
		return outcome, &RolloutError{
			Reason: "wrangler's deploy output named no digest-pinned " + ContainerAppName +
				" image, so there is nothing to compare the application against",
		}
	}

	if timeout <= 0 {
		timeout = defaultRolloutTimeout
	}
	if poll <= 0 {
		poll = defaultRolloutPoll
	}
	if extension <= 0 {
		extension = rolloutExtension
	}

	if digest == "" {
		fmt.Fprintf(out, "wrangler skipped the image push; resolving the existing %s image from the container application\n",
			ContainerAppName)
	} else {
		fmt.Fprintf(out, "waiting for the %s container application to serve %s\n",
			ContainerAppName, shortDigest(digest))
	}

	started := time.Now()
	deadline := started.Add(timeout)
	var lastServing string
	var lastState string
	var lastAppID string
	extended := false
	var lastErr error
	for attempt := 1; ; attempt++ {
		apps, err := w.listContainerApps(ctx)
		switch {
		case err != nil:
			lastErr = err
		default:
			lastErr = nil
			app, ok := findContainerApp(apps, ContainerAppName)
			switch {
			case !ok:
				lastErr = fmt.Errorf("the account lists no container application named %q", ContainerAppName)
			default:
				lastServing = app.digest()
				lastState = app.State
				lastAppID = app.ID
				if digest == "" && lastServing != "" {
					// Wrangler intentionally omitted the image block because it did
					// not push. The application record is the authoritative image
					// identity for this idempotent deploy.
					ref = app.Image
					digest = lastServing
					outcome.Ref = ref
					outcome.Digest = digest
					fmt.Fprintf(out, "  existing container image is %s; confirming it\n", shortDigest(digest))
				}
				if app.serves(digest) {
					fmt.Fprintf(out, "container application %s is serving %s (%s)\n",
						ContainerAppName, shortDigest(digest), app.State)
					outcome.Confirmed = true
					return outcome, nil
				}
			}
		}

		remaining := time.Until(deadline)
		if remaining <= 0 && !extended && lastErr == nil && lastAppID != "" {
			// The base bound is what a rollout takes when the new image pulls
			// promptly (2-3 minutes through 2026-09-29). It is not what a
			// rollout that is still progressing is entitled to: on 2026-09-30
			// the managed registry served each freshly pushed ~200 MB layer at
			// 0.07-0.16 MB/s for its first half hour or so, the runtime gave up
			// each pull at exactly 10 minutes (ImagePullError) and retried, and
			// the rollouts completed after 23-48 minutes. So while the platform
			// still reports the rollout in progress, the wait extends once
			// rather than calling a rollout that is going to land a failure.
			extended = true
			if health := w.containerHealth(ctx, lastAppID); health.RolloutActive {
				fmt.Fprintf(out, "  the rollout is still in progress after %s; waiting up to %s more\n",
					time.Since(started).Round(time.Second), extension.Round(time.Minute))
				for _, e := range health.Errors {
					fmt.Fprintf(out, "  the application reports: %s\n", e)
				}
				deadline = deadline.Add(extension)
				remaining = time.Until(deadline)
			}
		}
		if remaining <= 0 {
			break
		}
		wait := poll
		if remaining < wait {
			wait = remaining
		}
		if attempt == 1 {
			// One line, not one per poll: a ten-minute wait must not bury
			// the deploy's own output.
			fmt.Fprintf(out, "  rollout in progress; polling every %s for up to %s\n",
				poll.Round(time.Second), timeout.Round(time.Second))
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return outcome, ctx.Err()
		case <-timer.C:
		}
	}

	failure := &RolloutError{
		Expected:                ref,
		ExpectedFromApplication: noPush,
		Serving:                 lastServing,
		Waited:                  time.Since(started),
		Err:                     lastErr,
	}
	switch {
	case lastErr != nil:
		failure.Reason = "the container application could not be read: " + lastErr.Error()
	case lastServing == "":
		if noPush {
			failure.Reason = "wrangler skipped the image push, but the container application reports no digest-pinned image"
		} else {
			failure.Reason = "the container application reports no digest-pinned image"
		}
	case lastServing == digest && lastState == "degraded":
		failure.Reason = "the container application reports the expected image but is degraded"
	case lastServing == digest:
		failure.Reason = "the container application is still reporting the expected image in state " + lastState
	default:
		failure.Reason = "the container application is still serving a different image"
	}
	// Why, when the application says: a new instance that cannot pull its
	// image reports ImagePullError there and nowhere else the deploy looks
	// (2026-09-30: rollouts timed out on it with nothing in the deploy's output
	// to say so).
	if lastErr == nil && lastAppID != "" {
		health := w.containerHealth(ctx, lastAppID)
		if len(health.Errors) > 0 {
			failure.Reason += "; the application reports: " + strings.Join(health.Errors, "; ")
		}
		if health.RolloutActive {
			failure.Reason += "; the rollout is still in progress and may yet land"
		}
	}
	return outcome, failure
}

// rolloutExtension is how much longer the wait runs, once, when the base bound
// is spent and the platform still reports the rollout in progress. Fifty
// minutes on top of the base ten covers the slowest rollout observed (48
// minutes, 2026-09-30) and stays inside the deploy workflow's job timeout.
const rolloutExtension = 50 * time.Minute

// containerHealth is what `wrangler containers info` says about the
// application beyond its image.
type containerHealth struct {
	// Errors are the health errors — ImagePullError and the like — one line
	// each.
	Errors []string
	// RolloutActive reports that a rollout is still in progress.
	RolloutActive bool
}

// containerHealth reads the application's health, or the zero value when it
// cannot. It is diagnosis around a wait, so it never fails anything itself.
func (w *wrangler) containerHealth(ctx context.Context, id string) containerHealth {
	out, err := w.run(ctx, "", "containers", "info", id)
	if err != nil {
		return containerHealth{}
	}
	raw, ok := jsonObject(out)
	if !ok {
		return containerHealth{}
	}
	var info struct {
		ActiveRolloutID *string `json:"active_rollout_id"`
		Health          struct {
			Errors []json.RawMessage `json:"errors"`
		} `json:"health"`
	}
	if json.Unmarshal([]byte(raw), &info) != nil {
		return containerHealth{}
	}
	return containerHealth{
		Errors:        renderHealthErrors(info.Health.Errors),
		RolloutActive: info.ActiveRolloutID != nil && *info.ActiveRolloutID != "",
	}
}

// registryAccountPattern is the account id inside a managed-registry image
// reference. A health error carries the full reference, and the deploy's
// output lands in a public CI log.
var registryAccountPattern = regexp.MustCompile(`(registry\.cloudflare\.com/)[0-9a-f]{32}`)

// renderHealthErrors turns the application's health errors into one line each.
// The observed shape is {instance_id, event: {type, name, message, details:
// {duration, image, ...}}}; a string is taken as is, and anything else as
// compact JSON with the account id redacted.
func renderHealthErrors(errs []json.RawMessage) []string {
	var lines []string
	for _, e := range errs {
		var s string
		if json.Unmarshal(e, &s) == nil {
			lines = append(lines, redactAccount(s))
			continue
		}
		var obj struct {
			Event struct {
				Name    string `json:"name"`
				Message string `json:"message"`
				Details struct {
					Duration string `json:"duration"`
				} `json:"details"`
			} `json:"event"`
			Name    string `json:"name"`
			Message string `json:"message"`
		}
		if json.Unmarshal(e, &obj) == nil {
			name, message := obj.Event.Name, obj.Event.Message
			if name == "" && message == "" {
				name, message = obj.Name, obj.Message
			}
			if name != "" || message != "" {
				line := strings.TrimPrefix(name+": "+message, ": ")
				line = strings.TrimSuffix(line, ": ")
				if d := obj.Event.Details.Duration; d != "" {
					line += " (after " + d + ")"
				}
				lines = append(lines, redactAccount(line))
				continue
			}
		}
		lines = append(lines, redactAccount(strings.TrimSpace(string(e))))
	}
	return lines
}

func redactAccount(s string) string {
	return registryAccountPattern.ReplaceAllString(s, "${1}<account>")
}

func shortDigest(digest string) string {
	hex := strings.TrimPrefix(digest, "sha256:")
	if len(hex) > 12 {
		return "sha256:" + hex[:12]
	}
	return digest
}
