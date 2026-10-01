package factory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The docker wrangler runs during a deploy.
//
// Wrangler builds and pushes the orchestrator image with whatever docker
// WRANGLER_DOCKER_BIN names, and it logs that docker in to the managed
// registry once, after the build, with a credential that lasts 15 minutes. A
// push that outlives it fails `unauthorized` (deploy-factory run 36735949343).
// The bundle ships the shim that fixes that — cloudflare/scripts/
// wrangler-docker.sh, which mints a 120-minute credential before each push
// attempt and retries a failed push — and the deploy points wrangler at the
// staged copy, so a laptop deploy and a CI deploy run the same docker. CI's
// build cache lives in the same script and switches itself on only where the
// Actions cache is.

// dockerShimPath is the shim inside the staged bundle.
const dockerShimPath = "scripts/wrangler-docker.sh"

// dockerShimEnv makes the staged shim executable (//go:embed drops the bit)
// and returns the environment that routes wrangler's docker through it: the
// shim as WRANGLER_DOCKER_BIN, the docker the operator had chosen as the one it
// wraps, and the wrangler it mints registry credentials with.
func dockerShimEnv(bundleDir string, w *wrangler) ([]string, error) {
	shim := filepath.Join(bundleDir, filepath.FromSlash(dockerShimPath))
	if err := os.Chmod(shim, 0o755); err != nil {
		return nil, fmt.Errorf("preparing the docker shim %s: %w", shim, err)
	}
	wrapped := dockerBinary()
	if same, err := sameFile(wrapped, shim); err == nil && same {
		wrapped = "docker"
	}
	return []string{
		dockerEnvVar + "=" + shim,
		"TICFAC_DOCKER_BIN=" + wrapped,
		"TICFAC_WRANGLER_BIN=" + w.bin,
		"TICFAC_WRANGLER_PREFIX=" + strings.Join(w.prefix, " "),
	}, nil
}

func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}
