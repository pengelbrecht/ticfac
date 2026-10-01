package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The deploy's `docker` (cloudflare/scripts/wrangler-docker.sh, which every
// deploy points wrangler at: dockershim.go) owns the registry login for a
// push. Wrangler logs docker in once, after the build,
// with a 15-minute credential and then pushes; deploy-factory run 36735949343
// pushed every layer but one, retried the 209 MB one until that credential
// lapsed, and failed `unauthorized`. These run the script for real against a
// stub docker and a stub wrangler.

// wranglerDockerFixture stubs the docker the shim wraps (named by
// TICFAC_DOCKER_BIN, not on PATH) and the wrangler it mints with (named by
// TICFAC_WRANGLER_BIN with a TICFAC_WRANGLER_PREFIX, as an npx wrangler is). The stub
// docker records every call (and a login's stdin) in the returned log; its
// push fails `unauthorized` for the first failPushes calls.
func wranglerDockerFixture(t *testing.T, failPushes int) (script string, env []string, log string) {
	t.Helper()
	for _, tool := range []string{"bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	script = filepath.Join(root, "cloudflare", "scripts", "wrangler-docker.sh")

	bin := t.TempDir()
	state := t.TempDir()
	log = filepath.Join(state, "calls.log")
	writeStub := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/usr/bin/env bash\nset -eu\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeStub("real-docker", `
log="$STUB_STATE/calls.log"
case "$1" in
  login) printf 'docker %s stdin=%s\n' "$*" "$(cat)" >>"$log" ;;
  push)
    printf 'docker %s\n' "$*" >>"$log"
    n=$(cat "$STUB_STATE/pushes" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$STUB_STATE/pushes"
    if [ "$n" -le "$STUB_FAIL_PUSHES" ]; then echo unauthorized >&2; exit 1; fi
    echo "pushed" ;;
  *) printf 'docker %s\n' "$*" >>"$log" ;;
esac
`)
	writeStub("wrangler", `
n=$(cat "$STUB_STATE/mints" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$STUB_STATE/mints"
printf 'wrangler %s\n' "$*" >>"$STUB_STATE/calls.log"
# Pretty-printed, as wrangler's own JSON output is.
printf '{\n  "account_id": "acct",\n  "registry_host": "registry.cloudflare.com",\n  "username": "v1",\n  "password": "secret-%s"\n}\n' "$n"
`)
	env = append(os.Environ(),
		"TICFAC_DOCKER_BIN="+filepath.Join(bin, "real-docker"),
		"TICFAC_WRANGLER_BIN="+filepath.Join(bin, "wrangler"),
		"TICFAC_WRANGLER_PREFIX=--no wrangler",
		"STUB_STATE="+state,
		"STUB_FAIL_PUSHES="+strconv.Itoa(failPushes),
		"TICFAC_PUSH_RETRY_DELAY=0",
		"TICFAC_BUILDX_BUILDER=",
	)
	return script, env, log
}

func readCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// short: runs a shell script against two stub binaries; well under a second.
func TestWranglerDockerPushLogsInWithALongLivedCredentialAndRetriesAnExpiredOne(t *testing.T) {
	t.Parallel()
	script, env, log := wranglerDockerFixture(t, 1)
	ref := "registry.cloudflare.com/acct/ticks-orchestrator:abc123"

	cmd := exec.Command("bash", script, "push", ref)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("push through the wrapper failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "secret-") {
		t.Errorf("the wrapper printed a registry password:\n%s", out)
	}

	want := []string{
		"wrangler --no wrangler containers registries credentials registry.cloudflare.com --push --pull --expiration-minutes 120 --json",
		"docker login --username v1 --password-stdin registry.cloudflare.com stdin=secret-1",
		"docker push " + ref,
		"wrangler --no wrangler containers registries credentials registry.cloudflare.com --push --pull --expiration-minutes 120 --json",
		"docker login --username v1 --password-stdin registry.cloudflare.com stdin=secret-2",
		"docker push " + ref,
	}
	got := readCalls(t, log)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n  %s\nwant (a fresh 120-minute login before every push attempt, the password on stdin):\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// short: runs a shell script against two stub binaries; well under a second.
func TestWranglerDockerPushGivesUpAfterItsAttempts(t *testing.T) {
	t.Parallel()
	script, env, log := wranglerDockerFixture(t, 9)
	cmd := exec.Command("bash", script, "push", "registry.cloudflare.com/acct/ticks-orchestrator:abc123")
	cmd.Env = append(env, "TICFAC_PUSH_ATTEMPTS=2")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a push that never succeeds must fail the wrapper:\n%s", out)
	}
	pushes := 0
	for _, call := range readCalls(t, log) {
		if strings.HasPrefix(call, "docker push ") {
			pushes++
		}
	}
	if pushes != 2 {
		t.Errorf("pushed %d times, want TICFAC_PUSH_ATTEMPTS=2", pushes)
	}
}

// A push anywhere but the managed registry, and every other command, goes to
// docker untouched.
//
// short: runs a shell script against two stub binaries; well under a second.
func TestWranglerDockerPassesOtherCommandsThrough(t *testing.T) {
	t.Parallel()
	script, env, log := wranglerDockerFixture(t, 0)
	for _, args := range [][]string{
		{"push", "docker.io/library/busybox:latest"},
		{"image", "inspect", "x"},
	} {
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	want := "docker push docker.io/library/busybox:latest\ndocker image inspect x"
	if got := strings.Join(readCalls(t, log), "\n"); got != want {
		t.Errorf("calls:\n%s\nwant:\n%s", got, want)
	}
}
