//go:build !windows

package sandboximage

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

// The container's gits are bounded the way the binary's are. The binary's
// gits carry gitbin.TransportEnv (ssh since tick pul, https since epic-6in's
// thirty-minute checkpoint push), but the image's scripts start git
// themselves — the clone's fetches, the worker's push, the orchestrator's
// run-branch fetch and push — and before this nothing bounded any of them:
// a silent remote held the container until its wall clock.
//
// So this sources common.sh in a real bash, the way every entrypoint does,
// and reads back what it exported: with the variables unset it must export
// exactly what TransportEnv supplies, and a value already set must survive,
// one variable at a time.
//
// short: sources common.sh in one bash per case; no git, no image
func TestTheImageBoundsItsGitTransportLikeTheBinary(t *testing.T) {
	common, err := Path("common.sh")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"GIT_SSH_COMMAND", "GIT_HTTP_LOW_SPEED_LIMIT", "GIT_HTTP_LOW_SPEED_TIME"}
	sourced := func(t *testing.T, set map[string]string) map[string]string {
		t.Helper()
		script := `ME=transport-test; . "$1" >/dev/null; for name in ` + strings.Join(names, " ") +
			`; do printf '%s=%s\n' "$name" "$(printenv "$name")"; done`
		cmd := exec.Command("bash", "-c", script, "bash", common)
		env := []string{}
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			if !slices.Contains(names, name) {
				env = append(env, entry)
			}
		}
		for name, value := range set {
			env = append(env, name+"="+value)
		}
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sourcing common.sh: %v\n%s", err, out)
		}
		got := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if name, value, ok := strings.Cut(line, "="); ok {
				got[name] = value
			}
		}
		return got
	}

	// What the binary would supply on a machine that set none of them.
	for _, name := range names {
		t.Setenv(name, "")
	}
	want := map[string]string{}
	for _, entry := range gitbin.TransportEnv() {
		name, value, _ := strings.Cut(entry, "=")
		want[name] = value
	}
	if len(want) != len(names) {
		t.Fatalf("gitbin.TransportEnv() = %v; this test pins %v and must learn about any other bound", want, names)
	}

	got := sourced(t, nil)
	for _, name := range names {
		if got[name] != want[name] {
			t.Errorf("common.sh exports %s=%q, want gitbin.TransportEnv's %q: the container's gits and the "+
				"binary's would give up on a silent remote at different times, or the container's never",
				name, got[name], want[name])
		}
	}

	operator := map[string]string{"GIT_SSH_COMMAND": "ssh -i /custom/key", "GIT_HTTP_LOW_SPEED_TIME": "600"}
	got = sourced(t, operator)
	for name, value := range operator {
		if got[name] != value {
			t.Errorf("common.sh replaced an already-set %s=%q with %q", name, value, got[name])
		}
	}
	if got["GIT_HTTP_LOW_SPEED_LIMIT"] != want["GIT_HTTP_LOW_SPEED_LIMIT"] {
		t.Errorf("with only the time set, common.sh exports GIT_HTTP_LOW_SPEED_LIMIT=%q, want %q: one "+
			"operator setting must not switch the other bound off", got["GIT_HTTP_LOW_SPEED_LIMIT"],
			want["GIT_HTTP_LOW_SPEED_LIMIT"])
	}
}

// Every image script that reaches a remote runs with the bound in effect,
// which for a script means it sources common.sh (or is common.sh). A new
// script next month that pushes without it is the epic-6in gap again.
//
// short: reads the image scripts' text; no process runs
func TestEveryImageScriptThatReachesARemoteSourcesTheBound(t *testing.T) {
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	scripts, err := filepath.Glob(filepath.Join(dir, "*.sh"))
	if err != nil {
		t.Fatal(err)
	}
	reaching := 0
	for _, path := range scripts {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		if lines := unboundedNetworkGit(name, string(body)); len(lines) > 0 {
			t.Errorf("%s starts git against a remote without sourcing common.sh, so nothing bounds a silent "+
				"remote there: %v", name, lines)
		}
		if len(networkGitLines(string(body))) > 0 {
			reaching++
		}
	}
	if reaching == 0 {
		t.Fatal("no image script reaches a remote, which is not this image: the guard's pattern has drifted")
	}
}

// The guard's negative control.
//
// short: runs the guard over two in-memory scripts
func TestTheImageTransportGuardSeesAnUnboundedPush(t *testing.T) {
	loose := "#!/bin/sh\nset -e\ngit -C \"$dir\" push origin HEAD:refs/heads/x\n"
	if got := unboundedNetworkGit("loose.sh", loose); len(got) != 1 {
		t.Errorf("a script that pushes without sourcing common.sh was not caught: %v", got)
	}
	bound := "#!/bin/sh\n. \"$_ticks_common\"\ngit -C \"$dir\" fetch -q origin main\n"
	if got := unboundedNetworkGit("bound.sh", bound); len(got) != 0 {
		t.Errorf("a script that sources common.sh was reported: %v", got)
	}
}

var (
	networkGit    = regexp.MustCompile(`(^|[\s;(&|$"])git\s[^\n#]*\b(push|fetch|ls-remote|clone|pull)\b`)
	sourcesCommon = regexp.MustCompile(`(?m)^\s*(\.|source)\s+"?\$_ticks_common"?\s*$`)
)

func networkGitLines(body string) []string {
	var lines []string
	for i, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if networkGit.MatchString(line) {
			lines = append(lines, strings.TrimSpace(line)+" (line "+strconv.Itoa(i+1)+")")
		}
	}
	return lines
}

func unboundedNetworkGit(name, body string) []string {
	if name == "common.sh" || sourcesCommon.MatchString(body) {
		return nil
	}
	return networkGitLines(body)
}
