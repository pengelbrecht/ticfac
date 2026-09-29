package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
	"github.com/pengelbrecht/ticfac/internal/httpnet"
)

// What the factory actually runs.
//
// Since CI became the normal deploy path (.github/workflows/deploy-factory.yml)
// most deploys happen on a runner, and the laptop's ~/.ticfacrc still holds the
// factory_version of the last deploy IT made. The factory is the only party
// that knows what it runs, so status and doctor ask it: GET /api/deployment
// answers the deploy's own D1 record (the ticfac version that deployed, which
// CI stamps with `git describe` so it names the commit), the image the
// container rollout was confirmed serving, and the Worker version serving the
// request.

// deployedPath is the factory route that answers what it runs.
const deployedPath = "/api/deployment"

// errNoDeployedRoute is a factory that predates GET /api/deployment.
var errNoDeployedRoute = errors.New("this factory predates GET /api/deployment — the next deploy adds it")

// DeployedFacts is the factory's answer. Every field may be empty: a factory
// no deploy has recorded, a rollout nobody confirmed, a runtime without the
// version-metadata binding.
type DeployedFacts struct {
	Version                string `json:"version"`
	BundleSHA              string `json:"bundle_sha256"`
	DeployedAt             string `json:"deployed_at"`
	ImageRef               string `json:"image_ref"`
	ImageDigest            string `json:"image_digest"`
	WorkerVersionID        string `json:"worker_version_id"`
	WorkerVersionTimestamp string `json:"worker_version_timestamp"`
}

// deployedCommitPattern reads the commit out of a `git describe` version:
// v1.2.3-4-g<sha>, or the bare sha while no release tag exists. It is the same
// rule the deploy workflow's path check uses.
var deployedCommitPattern = regexp.MustCompile(`(?:^|-g)([0-9a-f]{12,40})$`)

// Commit is the commit the deployed version names, or "" when it names none
// (a release version, a "dev" build).
func (d *DeployedFacts) Commit() string {
	if d == nil {
		return ""
	}
	if m := deployedCommitPattern.FindStringSubmatch(d.Version); m != nil {
		return m[1]
	}
	return ""
}

// LocalDisagreement says how ~/.ticfacrc's recorded version differs from what
// the factory runs, or "" when it does not (or either side is unknown).
func (d *DeployedFacts) LocalDisagreement(localVersion string) string {
	if d == nil || d.Version == "" || localVersion == "" || localVersion == d.Version {
		return ""
	}
	return fmt.Sprintf("%s records %s from this machine's last deploy, but the factory runs %s (deployed since, most likely by CI)",
		credentials.FileName, localVersion, d.Version)
}

// FetchDeployed asks the factory what it runs.
func FetchDeployed(ctx context.Context, client *http.Client, factoryURL, token string) (*DeployedFacts, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(factoryURL, "/")+deployedPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", deployedPath, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNoDeployedRoute
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GET %s answered %s: %s", deployedPath, resp.Status, firstLine(body))
	}
	var facts DeployedFacts
	if err := json.Unmarshal(body, &facts); err != nil {
		return nil, fmt.Errorf("GET %s answered something that is not the deployment: %w", deployedPath, err)
	}
	return &facts, nil
}

// ReadDeployed asks the factory configured in ~/.ticfacrc (configPath "" is
// the real one) what it runs, and returns the version this machine recorded
// beside it so the caller can say when the two disagree.
func ReadDeployed(ctx context.Context, configPath string, client *http.Client) (facts *DeployedFacts, localVersion string, err error) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, "", err
	}
	url := strings.TrimSuffix(cfg.Get(credentials.KeyURL), "/")
	token := cfg.Get(credentials.KeyToken)
	localVersion = cfg.Get(credentials.KeyVersion)
	if url == "" || token == "" {
		return nil, localVersion, errors.New("no factory is configured")
	}
	if client == nil {
		client = httpnet.Client(15 * time.Second)
	}
	facts, err = FetchDeployed(ctx, client, url, token)
	return facts, localVersion, err
}
