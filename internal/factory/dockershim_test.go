package factory

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readDeployDockerEnv(t *testing.T, h *harness) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.stateDir, "deploy-docker-env"))
	if err != nil {
		t.Fatalf("the fake wrangler recorded no docker environment: %v", err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		env[k] = v
	}
	return env
}

// Every deploy, a laptop's as much as CI's, runs wrangler's docker through the
// bundle's shim, so a slow push gets a fresh 120-minute registry login rather
// than wrangler's 15-minute one (deploy-factory run 36735949343).
func TestDeployRunsWranglersDockerThroughTheBundlesShim(t *testing.T) {
	h := newHarness(t)
	if _, err := Deploy(context.Background(), h.rolloutOptions()); err != nil {
		t.Fatalf("Deploy: %v\n%s", err, h.log())
	}
	env := readDeployDockerEnv(t, h)
	shim := filepath.Join(h.bundleDir, "scripts", "wrangler-docker.sh")
	if env["WRANGLER_DOCKER_BIN"] != shim {
		t.Errorf("WRANGLER_DOCKER_BIN = %q, want the staged shim %q", env["WRANGLER_DOCKER_BIN"], shim)
	}
	if info, err := os.Stat(shim); err != nil || info.Mode()&0o111 == 0 {
		t.Errorf("the staged shim is not executable (%v)", err)
	}
	if env["TICFAC_DOCKER_BIN"] != "docker" {
		t.Errorf("TICFAC_DOCKER_BIN = %q, want docker when the operator named none", env["TICFAC_DOCKER_BIN"])
	}
	if !strings.HasSuffix(env["TICFAC_WRANGLER_BIN"], "wrangler") {
		t.Errorf("TICFAC_WRANGLER_BIN = %q, want the wrangler the deploy runs", env["TICFAC_WRANGLER_BIN"])
	}
}

// A docker the operator chose with WRANGLER_DOCKER_BIN is the one the shim
// wraps, not one it replaces.
func TestTheDockerShimWrapsTheOperatorsDocker(t *testing.T) {
	h := newHarness(t)
	custom := filepath.Join(t.TempDir(), "my-docker")
	linkFake(t, filepath.Dir(custom), "my-docker", "fake-docker.sh")
	t.Setenv(dockerEnvVar, custom)
	if _, err := Deploy(context.Background(), h.rolloutOptions()); err != nil {
		t.Fatalf("Deploy: %v\n%s", err, h.log())
	}
	if got := readDeployDockerEnv(t, h)["TICFAC_DOCKER_BIN"]; got != custom {
		t.Errorf("TICFAC_DOCKER_BIN = %q, want the operator's %q", got, custom)
	}
}

// `wrangler deploy` runs for minutes; its output must reach the operator (and
// a CI log, with real timestamps) as it is written, not when wrangler exits.
// The stand-in prints a line and then waits for the test to have SEEN it.
func TestWranglerDeployOutputIsStreamedAsItIsWritten(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	bin := filepath.Join(dir, "slow-wrangler")
	script := "#!/bin/sh\necho 'building image'\n" +
		"i=0; while [ ! -e '" + seen + "' ]; do i=$((i+1)); [ $i -gt 200 ] && exit 3; sleep 0.05; done\n" +
		"echo 'pushed' >&2\nprintf 'no newline at the end'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r, wr := io.Pipe()
	w := &wrangler{bin: bin, dir: dir, out: wr}
	done := make(chan struct{})
	var out string
	var err error
	go func() {
		out, err = w.deploy(context.Background())
		wr.Close()
		close(done)
	}()

	lines := bufio.NewScanner(r)
	if !lines.Scan() || lines.Text() != "  building image" {
		t.Fatalf("first streamed line = %q, want it while wrangler is still running", lines.Text())
	}
	if err := os.WriteFile(seen, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var rest []string
	for lines.Scan() {
		rest = append(rest, lines.Text())
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deploy did not return")
	}
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if strings.Join(rest, "|") != "  pushed|  no newline at the end" {
		t.Errorf("rest of the stream = %q", rest)
	}
	if out != "building image\npushed\nno newline at the end" {
		t.Errorf("returned output = %q, want all of it for parsing", out)
	}
}
