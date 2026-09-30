package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/httpnet"
)

// A rollout that a live run holds open.
//
// cloudflare/wrangler.toml sets rollout_active_grace_period to a day (PR #150):
// during a rollout the platform replaces an instance only once it has been
// connected to its Durable Object for that long, so a rollout never takes a
// container a run is using. Idle instances roll at once; an instance a run
// holds keeps the previous image until the run lets it go. The rollout stays
// `progressing` all that time, and the application record — the `image` that
// `wrangler containers list` reports — only moves to the new image when the
// rollout completes (observed 2026-09-30: the application's updated_at is the
// instant the rollout's last step completed). So under a live run the wait's
// original criterion, "the application reports the new image", cannot be met
// inside any bound a deploy should wait, and the deploy would report a failure
// for a rollout that is landing exactly as designed.
//
// What a deploy owes the operator is narrower and provable: a run started NOW
// boots the new image. That holds when the rollout for this deploy's image is
// progressing without health errors, new instances have come up at its target
// version, and every instance still on an older version is running and bound to
// a Durable Object — a live run's orchestrator or worker. The instances say
// which (`wrangler containers instances <app> --json` names each one's Durable
// Object, run_<id>[-<tick>-<n>], and its application version); the rollout says
// its target version and image (the containers rollouts API — wrangler has no
// command that prints a rollout).
//
// Rollouts do not pile up: creating a rollout marks the one in progress
// `replaced` (the application's rollout history, 2026-09-30: every rollout but
// the newest is `replaced`, including one superseded 250 ms after it was
// created and one replaced at 1 of 7 instances), and an instance still held on
// an older version is replaced by the newest rollout once its run lets it go.
// So a deploy only ever judges the newest rollout — the one whose target image
// is its own.

// runIDPattern is the run a Durable Object belongs to: the orchestrator's
// object is named run_<id>, a worker's run_<id>-<tick>-<n>.
var runIDPattern = regexp.MustCompile(`^run_[0-9a-f]+`)

// defaultHeldCheckEvery spaces the held-rollout check: each one is three
// wrangler calls and an API request, and the wait polls every five seconds.
const defaultHeldCheckEvery = 30 * time.Second

// rolloutAPI is how the wait reaches the containers rollouts API.
type rolloutAPI struct {
	// base replaces https://api.cloudflare.com/client/v4 (tests).
	base   string
	client *http.Client
	// every spaces the held-rollout check; zero means defaultHeldCheckEvery.
	every time.Duration
}

// rolloutRecord is the part of a containers rollout the held check reads.
type rolloutRecord struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	TargetVersion int    `json:"target_version"`
	Target        struct {
		Image string `json:"image"`
	} `json:"target_configuration"`
	Health struct {
		Errors    []json.RawMessage `json:"errors"`
		Instances struct {
			Failed int `json:"failed"`
		} `json:"instances"`
	} `json:"health"`
	Progress struct {
		UpdatedInstances int `json:"updated_instances"`
		TotalInstances   int `json:"total_instances"`
	} `json:"progress"`
}

// containerInstance is one row of `wrangler containers instances --json`.
type containerInstance struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	// Version is the application version the instance runs; null for a
	// Durable Object with no instance.
	Version *int `json:"version"`
}

// heldRollout is a rollout for this deploy's image that is serving new
// instances, with older instances left only where live runs hold them.
type heldRollout struct {
	RolloutID string
	// Runs are the runs holding the older instances, sorted.
	Runs []string
	// Held is how many instances still run an older version.
	Held int
	// Updated/Total are the rollout's own instance counts.
	Updated, Total int
}

// judgeHeldRollout decides whether the rollout serves new instances with only
// run-held instances left, or says why not.
func judgeHeldRollout(r rolloutRecord, digest string, instances []containerInstance) (heldRollout, string) {
	switch {
	case r.Status != "progressing":
		return heldRollout{}, "the rollout is " + r.Status + ", not progressing"
	case digest == "" || digestPattern.FindString(r.Target.Image) != digest:
		return heldRollout{}, "the rollout in progress targets a different image than this deploy's"
	case len(r.Health.Errors) > 0 || r.Health.Instances.Failed > 0:
		return heldRollout{}, "the rollout reports failing instances"
	}
	held := heldRollout{RolloutID: r.ID, Updated: r.Progress.UpdatedInstances, Total: r.Progress.TotalInstances}
	current := 0
	runs := map[string]bool{}
	for _, in := range instances {
		if in.Version == nil {
			continue
		}
		if *in.Version == r.TargetVersion {
			current++
			continue
		}
		if in.State != "running" || in.Name == "" {
			return heldRollout{}, fmt.Sprintf("an instance on version %d (%s) is held by no live run, so the rollout has not reached it yet",
				*in.Version, in.State)
		}
		held.Held++
		run := runIDPattern.FindString(in.Name)
		if run == "" {
			run = in.Name
		}
		runs[run] = true
	}
	if current == 0 && held.Updated == 0 {
		return heldRollout{}, "no instance has come up on the new version yet"
	}
	if held.Held == 0 {
		return heldRollout{}, "no instance is held back; the rollout is finishing"
	}
	for run := range runs {
		held.Runs = append(held.Runs, run)
	}
	sort.Strings(held.Runs)
	return held, ""
}

// checkHeldRollout reads the rollout in progress and the instances, and judges
// them. It never fails the deploy itself: an unreadable rollout is a reason to
// keep waiting, returned as the second value.
func (w *wrangler) checkHeldRollout(ctx context.Context, api rolloutAPI, appID, digest string) (heldRollout, string) {
	health := w.containerHealth(ctx, appID)
	if !health.RolloutActive {
		return heldRollout{}, "the application reports no rollout in progress"
	}
	if health.AccountID == "" {
		return heldRollout{}, "the application record names no account"
	}
	instances, err := w.containerInstances(ctx, appID)
	if err != nil {
		return heldRollout{}, err.Error()
	}
	token, err := w.apiToken(ctx)
	if err != nil {
		return heldRollout{}, err.Error()
	}
	rollout, err := fetchRollout(ctx, api, token, health.AccountID, appID, health.RolloutID)
	if err != nil {
		return heldRollout{}, err.Error()
	}
	return judgeHeldRollout(rollout, digest, instances)
}

// apiToken is the credential wrangler itself uses — the CI deploy's
// CLOUDFLARE_API_TOKEN, or the operator's `wrangler login` OAuth token. It is
// never printed.
func (w *wrangler) apiToken(ctx context.Context) (string, error) {
	out, err := w.run(ctx, "", "auth", "token", "--json")
	if err != nil {
		return "", errors.New("wrangler could not hand over its API token (`wrangler auth token --json`)")
	}
	raw, ok := jsonObject(out)
	var tok struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if !ok || json.Unmarshal([]byte(raw), &tok) != nil || tok.Token == "" {
		return "", errors.New("`wrangler auth token --json` named no token (a global API key cannot read the rollouts API)")
	}
	return tok.Token, nil
}

// fetchRollout reads one rollout. Its errors never carry the URL: it names
// the account.
func fetchRollout(ctx context.Context, api rolloutAPI, token, account, appID, rolloutID string) (rolloutRecord, error) {
	base := api.base
	if base == "" {
		base = defaultCloudflareAPIBase
	}
	client := api.client
	if client == nil {
		client = httpnet.Client(30 * time.Second)
	}
	endpoint := fmt.Sprintf("%s/accounts/%s/containers/applications/%s/rollouts/%s",
		strings.TrimRight(base, "/"), url.PathEscape(account), url.PathEscape(appID), url.PathEscape(rolloutID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return rolloutRecord{}, errors.New("could not build the rollout request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return rolloutRecord{}, fmt.Errorf("the containers rollouts API could not be reached: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return rolloutRecord{}, fmt.Errorf("the containers rollouts API answered %s", resp.Status)
	}
	var envelope struct {
		Result *rolloutRecord `json:"result"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Result == nil {
		return rolloutRecord{}, errors.New("the containers rollouts API answered with no rollout")
	}
	return *envelope.Result, nil
}

// containerInstances lists the application's instances. Wrangler prints a
// bare array, or an object carrying one under "instances" when paginating.
func (w *wrangler) containerInstances(ctx context.Context, appID string) ([]containerInstance, error) {
	out, err := w.run(ctx, "", "containers", "instances", appID, "--json")
	if err != nil {
		return nil, errors.New("wrangler could not list the application's instances (`wrangler containers instances --json`)")
	}
	var instances []containerInstance
	trimmed := strings.TrimSpace(out)
	if obj, ok := jsonObject(trimmed); ok && strings.HasPrefix(trimmed, "{") {
		var page struct {
			Instances []containerInstance `json:"instances"`
		}
		if json.Unmarshal([]byte(obj), &page) == nil {
			return page.Instances, nil
		}
	}
	raw, ok := jsonArray(out)
	if !ok || json.Unmarshal([]byte(raw), &instances) != nil {
		return nil, errors.New("could not parse `wrangler containers instances --json`")
	}
	return instances, nil
}
