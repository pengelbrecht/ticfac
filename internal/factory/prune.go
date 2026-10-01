package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/httpnet"
)

// Old orchestrator images are pruned from the managed registry by the deploy.
//
// Every deploy pushes a new ticks-orchestrator image under a new tag (wrangler
// tags it with the Worker version), and nothing ever removed one. CI builds
// the image on a fresh runner with no layer cache, so about 0.8 GB of each
// ~1.2 GB image is layers no earlier image shares: on 2026-09-30 the registry
// held 59 tags, 45 GB of unique orchestrator layers (66 GB counted per image),
// against Cloudflare's 50 GB per-account image storage limit — one or two
// deploys from a limit that rejects pushes or pulls. (It was the first suspect
// when rollouts failed with ImagePullError that day; it was not the cause —
// rollout.go has what was — but nothing bounded it either.)
//
// So a deploy keeps the registry bounded itself:
//
//   - BEFORE `wrangler deploy`, so a registry that is already full makes room
//     for the image this deploy is about to push (the recovery path: a deploy
//     that failed because storage ran out heals on its retry), protecting the
//     image the application serves right now;
//   - AFTER a confirmed rollout, protecting the new image and the one it
//     replaced (the rollback target).
//
// Both keep the newest imageKeepNewest tags besides. The selection is a pure
// function (selectImagesToPrune) because deleting the wrong image is the one
// mistake here that cannot be undone: a container application whose image is
// gone cannot start an instance.
//
// Pruning never fails a deploy. A registry that cannot be read or an image that
// cannot be deleted is a warning naming what happened; the deploy's own verdict
// is the rollout's.

const (
	// imageKeepNewest is how many of the most recent orchestrator images a
	// prune keeps, on top of the protected ones. Five covers the last few
	// Worker versions for a rollback while bounding storage to a few GB.
	imageKeepNewest = 5
	// imageKeepFloor is the least a caller can ask to keep.
	imageKeepFloor = 2

	// imageStorageLimitBytes is Cloudflare's total image storage per account
	// (developers.cloudflare.com/containers/platform-details/limits).
	imageStorageLimitBytes int64 = 50_000_000_000
	// imageStorageWarnBytes is where a deploy starts saying the orchestrator
	// images alone are close to it. The limit is account-wide, so other
	// repositories share the remainder.
	imageStorageWarnBytes int64 = 35_000_000_000

	registryDomain = "registry.cloudflare.com"
)

// registryImage is one tag of the orchestrator repository.
type registryImage struct {
	Tag    string
	Digest string
	// Created is the image config's creation time; zero when it could not be
	// read, which makes the image unprunable.
	Created time.Time
	// Layers are the compressed layer blobs, for the storage estimate.
	Layers []registryBlob
}

type registryBlob struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// selectImagesToPrune returns the images a prune deletes, oldest first.
//
// Kept, whatever their age:
//   - the newest `keep` images by creation time (floored at imageKeepFloor);
//   - every image whose digest is in protected (the served image, the image
//     just pushed, the one it replaced, anything a rollout names);
//   - every tag that shares a digest with a kept image — deleting a tag can
//     delete the manifest under it, and with it the kept image;
//   - every image whose digest or creation time is unknown: an image that
//     cannot be dated or identified is never deleted blind.
func selectImagesToPrune(images []registryImage, keep int, protected []string) []registryImage {
	if keep < imageKeepFloor {
		keep = imageKeepFloor
	}
	keptDigests := map[string]bool{}
	for _, d := range protected {
		if d != "" {
			keptDigests[d] = true
		}
	}

	dated := make([]registryImage, 0, len(images))
	for _, img := range images {
		if img.Digest == "" || img.Created.IsZero() {
			if img.Digest != "" {
				keptDigests[img.Digest] = true
			}
			continue
		}
		dated = append(dated, img)
	}
	sort.SliceStable(dated, func(i, j int) bool {
		if !dated[i].Created.Equal(dated[j].Created) {
			return dated[i].Created.After(dated[j].Created)
		}
		return dated[i].Tag > dated[j].Tag
	})
	for i, img := range dated {
		if i < keep {
			keptDigests[img.Digest] = true
		}
	}

	var prune []registryImage
	for _, img := range dated {
		if keptDigests[img.Digest] {
			continue
		}
		prune = append(prune, img)
	}
	// Oldest first: a prune interrupted halfway has removed the least useful.
	for i, j := 0, len(prune)-1; i < j; i, j = i+1, j-1 {
		prune[i], prune[j] = prune[j], prune[i]
	}
	return prune
}

// uniqueLayerBytes is the compressed size of the distinct layers the images
// hold — what the registry stores for them, since layers are shared by digest.
func uniqueLayerBytes(images []registryImage) int64 {
	seen := map[string]bool{}
	var total int64
	for _, img := range images {
		for _, l := range img.Layers {
			if seen[l.Digest] {
				continue
			}
			seen[l.Digest] = true
			total += l.Size
		}
	}
	return total
}

// registryClient reads the orchestrator repository through the registry API,
// with short-lived pull credentials wrangler mints.
type registryClient struct {
	base    string // https://registry.cloudflare.com
	account string
	auth    string // "user:password"
	http    *http.Client
}

type registryCredentials struct {
	AccountID    string `json:"account_id"`
	RegistryHost string `json:"registry_host"`
	Username     string `json:"username"`
	Password     string `json:"password"`
}

// newRegistryClient mints pull credentials. The password is never printed:
// the wrangler output that carries it is parsed, not echoed.
func newRegistryClient(ctx context.Context, w *wrangler, baseOverride string, client *http.Client) (*registryClient, error) {
	out, err := w.run(ctx, "", "containers", "registries", "credentials", registryDomain,
		"--pull", "--json", "--expiration-minutes", "15")
	if err != nil {
		return nil, errors.New("wrangler could not mint registry pull credentials " +
			"(`wrangler containers registries credentials " + registryDomain + " --pull`)")
	}
	raw, ok := jsonObject(out)
	if !ok {
		return nil, errors.New("wrangler's registry credentials were not JSON")
	}
	var creds registryCredentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil || creds.Password == "" || creds.AccountID == "" {
		return nil, errors.New("wrangler's registry credentials named no account or password")
	}
	host := creds.RegistryHost
	if host == "" {
		host = registryDomain
	}
	base := "https://" + host
	if baseOverride != "" {
		base = strings.TrimSuffix(baseOverride, "/")
	}
	user := creds.Username
	if user == "" {
		user = "v1"
	}
	if client == nil {
		client = httpnet.Client(60 * time.Second)
	}
	return &registryClient{base: base, account: creds.AccountID, auth: user + ":" + creds.Password, http: client}, nil
}

const manifestAccept = "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// get fetches a repository path. Errors deliberately never carry the URL: it
// contains the account id, and a deploy's output lands in a public CI log.
func (c *registryClient) get(ctx context.Context, path string, into any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/v2/"+c.account+"/"+path, nil)
	if err != nil {
		return nil, errors.New("building a registry request failed")
	}
	user, pass, _ := strings.Cut(c.auth, ":")
	req.SetBasicAuth(user, pass)
	req.Header.Set("Accept", manifestAccept)
	resp, err := c.http.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, fmt.Errorf("registry request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("registry answered %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(into); err != nil {
		return nil, errors.New("registry response was not the JSON expected")
	}
	return resp.Header, nil
}

// listImages reads every tag of the orchestrator repository with its digest,
// creation time and layers. A tag whose manifest or config cannot be read is
// returned without them, which selectImagesToPrune keeps.
func (c *registryClient) listImages(ctx context.Context, repo string) ([]registryImage, error) {
	var tags struct {
		Tags []string `json:"tags"`
	}
	if _, err := c.get(ctx, repo+"/tags/list?n=10000", &tags); err != nil {
		return nil, fmt.Errorf("listing %s tags: %w", repo, err)
	}
	var images []registryImage
	for _, tag := range tags.Tags {
		// The managed registry lists each manifest's digest among the tags
		// too; those are not tags anyone pushed.
		if strings.HasPrefix(tag, "sha256:") {
			continue
		}
		img := registryImage{Tag: tag}
		var manifest struct {
			Config registryBlob   `json:"config"`
			Layers []registryBlob `json:"layers"`
		}
		header, err := c.get(ctx, repo+"/manifests/"+url.PathEscape(tag), &manifest)
		if err == nil {
			img.Digest = header.Get("Docker-Content-Digest")
			img.Layers = manifest.Layers
			var config struct {
				Created time.Time `json:"created"`
			}
			if manifest.Config.Digest != "" {
				if _, err := c.get(ctx, repo+"/blobs/"+manifest.Config.Digest, &config); err == nil {
					img.Created = config.Created
				}
			}
		}
		images = append(images, img)
	}
	return images, nil
}

// pruneOrchestratorImages deletes the orchestrator images selectImagesToPrune
// picks and reports the repository's storage. It never returns an error: every
// failure is a warning in the deploy's output (see the file comment).
func pruneOrchestratorImages(ctx context.Context, w *wrangler, out io.Writer, opts Options, protected ...string) {
	if opts.SkipImagePrune {
		return
	}
	var live []string
	for _, d := range protected {
		if d != "" {
			live = append(live, d)
		}
	}
	if len(live) == 0 {
		// Never prune without knowing what the application serves.
		fmt.Fprintf(out, "WARNING: not pruning %s images: the image the application serves is unknown\n",
			ContainerAppName)
		return
	}
	keep := opts.ImageKeep
	if keep == 0 {
		keep = imageKeepNewest
	}
	pruneRepositoryImages(ctx, w, out, opts, ContainerAppName, keep, live, "the served image")
}

// FactorySandboxImageRepo is the registry repository of FactorySandbox's
// named image `factory` (epic umq): wrangler names it
// <worker>-<class, lowercased>-<image name>. A durable_object-policy
// container keeps running the image it STARTED on, and a run's containers
// all start on the image the run pinned at submit (tick v1d) — so a digest is
// in use for as long as any live run pins it, whatever has been deployed
// since. Deleting it would leave that run's next container unable to start.
const FactorySandboxImageRepo = "ticks-factory-factorysandbox-factory"

// factorySandboxKeepNewest is how many of the newest FactorySandbox images a
// prune keeps besides every pinned one: the deployment's current image and
// one or two to roll back to. Lean on purpose — the account's 50 GB image
// storage is shared with the 0.x repository until it is deleted (dax).
const factorySandboxKeepNewest = 3

// livePinsSQL selects the image every live run pinned (migration 0023, the
// run states ACTIVE_RUN_STATES names in cloudflare/src/runs.ts).
const livePinsSQL = `SELECT s.image AS image FROM run_substrate s JOIN runs r ON r.run_id = s.run_id ` +
	`WHERE s.image IS NOT NULL AND r.state IN ('starting','running','stopping')`

// pinnedDigests reads `wrangler d1 execute --json` output for livePinsSQL
// and returns the digests the pinned references name. ok is false when the
// output is not the shape expected — which must stop the prune, never let it
// run as if nothing were pinned.
func pinnedDigests(out string) (digests []string, ok bool) {
	start := strings.Index(out, "[")
	end := strings.LastIndex(out, "]")
	if start < 0 || end < start {
		return nil, false
	}
	var results []struct {
		Results []struct {
			Image string `json:"image"`
		} `json:"results"`
		Success *bool `json:"success"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &results); err != nil || len(results) == 0 {
		return nil, false
	}
	for _, r := range results {
		if r.Success != nil && !*r.Success {
			return nil, false
		}
		for _, row := range r.Results {
			if d := digestPattern.FindString(row.Image); d != "" {
				digests = append(digests, d)
			}
		}
	}
	return digests, true
}

// pruneFactorySandboxImages keeps FactorySandbox's repository bounded: the
// newest factorySandboxKeepNewest images and every digest a live run pins.
// It prunes nothing when the pins cannot be read.
func pruneFactorySandboxImages(ctx context.Context, w *wrangler, out io.Writer, opts Options, database string) {
	if opts.SkipImagePrune {
		return
	}
	raw, err := w.run(ctx, "", "d1", "execute", database, "--remote", "--json", "--command", livePinsSQL)
	if err != nil {
		fmt.Fprintf(out, "WARNING: not pruning %s images: could not read the images live runs pin\n",
			FactorySandboxImageRepo)
		return
	}
	pins, ok := pinnedDigests(raw)
	if !ok {
		fmt.Fprintf(out, "WARNING: not pruning %s images: the live runs' pins were unreadable\n",
			FactorySandboxImageRepo)
		return
	}
	pruneRepositoryImages(ctx, w, out, opts, FactorySandboxImageRepo, factorySandboxKeepNewest, pins,
		fmt.Sprintf("%d image(s) live runs pin", len(pins)))
}

// pruneRepositoryImages deletes what selectImagesToPrune picks from one
// repository and reports its storage. Never an error (see the file comment).
func pruneRepositoryImages(ctx context.Context, w *wrangler, out io.Writer, opts Options,
	repo string, keep int, live []string, protectedLabel string) {
	client, err := newRegistryClient(ctx, w, opts.registryBaseURL, opts.HTTPClient)
	if err != nil {
		fmt.Fprintf(out, "WARNING: not pruning %s images: %v\n", repo, err)
		return
	}
	images, err := client.listImages(ctx, repo)
	if err != nil {
		fmt.Fprintf(out, "WARNING: not pruning %s images: %v\n", repo, err)
		return
	}
	before := uniqueLayerBytes(images)
	prune := selectImagesToPrune(images, keep, live)

	deleted := map[string]bool{}
	var failed []string
	for _, img := range prune {
		if _, err := w.run(ctx, "", "containers", "images", "delete", repo+":"+img.Tag, "--skip-confirmation"); err != nil {
			failed = append(failed, img.Tag)
			continue
		}
		deleted[img.Tag] = true
	}
	var remaining []registryImage
	for _, img := range images {
		if !deleted[img.Tag] {
			remaining = append(remaining, img)
		}
	}
	after := uniqueLayerBytes(remaining)

	fmt.Fprintf(out, "%s images: %d tags, %s of layers; pruned %d (keeping the newest %d and %s), %s remain\n",
		repo, len(images), gigabytes(before), len(deleted), keep, protectedLabel, gigabytes(after))
	if len(failed) > 0 {
		fmt.Fprintf(out, "WARNING: could not delete %d %s image(s): %s\n",
			len(failed), repo, strings.Join(failed, ", "))
	}
	if after >= imageStorageWarnBytes {
		fmt.Fprintf(out, "WARNING: %s images still hold %s of the account's %s image storage limit; "+
			"a push past the limit fails the rollout with ImagePullError\n",
			repo, gigabytes(after), gigabytes(imageStorageLimitBytes))
	}
}

func gigabytes(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }

// jsonObject returns the first top-level JSON object in wrangler's output,
// which may carry banner lines around it.
func jsonObject(out string) (string, bool) {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return "", false
	}
	return out[start : end+1], true
}
