//go:build !windows

package sandboximage

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The pi-CLI worker path is deleted (epic 43y, tick jhp): pi-durable is the
// one worker harness, and a hosted attempt's tools run in this container
// while its conversation runs in the factory's WorkerAgent. These guards pin
// the deletion's shape the way the sandbox-image tests pin every other fact
// about the scripts — as text, so the deletion cannot quietly regrow.
//
// short: reads the image files and asserts on their text; no process runs

// readScriptText returns one of the image's run scripts, comments included.
func readScriptText(t *testing.T, name string) string {
	t.Helper()
	p, err := Path(name)
	if err != nil {
		t.Fatalf("locating %s: %v", name, err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// TestTheImageShipsNoPiCLI pins the second half of the tick: the pi CLI is
// deleted from the image, so no container can boot a pi-CLI harness even by
// hand. The Dockerfile's pins are the image's whole software inventory.
func TestTheImageShipsNoPiCLI(t *testing.T) {
	df := readDockerfile(t)
	for _, gone := range []string{
		"@earendil-works/pi-coding-agent",
		"ARG PI_VERSION",
		"pi --version",
	} {
		if strings.Contains(df, gone) {
			t.Errorf("the image still ships the pi CLI (%q is in the Dockerfile): pi-durable is the one worker harness and it is hosted, not run in the container (epic 43y, tick jhp)", gone)
		}
	}
}

// piHarnessCase matches a `pi)` case label in a shell case statement — the
// syntactic shape every harness-kind switch in the image scripts uses.
var piHarnessCase = regexp.MustCompile(`(?m)^\s*pi\)`)

// TestTheScriptsOfferNoPiCLIHarness pins the first half: worker.sh's
// pi-CLI harness path is gone — no launch case, no provider configuration, no
// probe arm, in any of the three scripts a container runs.
func TestTheScriptsOfferNoPiCLIHarness(t *testing.T) {
	for _, script := range []string{string(WorkerScript), string(CommonScript), string(EntrypointScript)} {
		text := readScriptText(t, script)
		if piHarnessCase.MatchString(text) {
			t.Errorf("%s still offers a pi CLI harness case (a `pi)` arm): the pi-CLI worker path is deleted (epic 43y, tick jhp)", script)
		}
		for _, gone := range []string{
			"configure_pi_provider",
			"pi_config_dir",
			"pi_model_overrides",
			"cmd=(pi -p",
			"cmd=(pi ",
		} {
			if strings.Contains(text, gone) {
				t.Errorf("%s still carries pi-CLI wiring (%q): the pi-CLI worker path is deleted (epic 43y, tick jhp)", script, gone)
			}
		}
	}
}

// TestTheScriptsAcceptTheHostedHarness pins what the deletion leaves: the
// container's harness kinds are omp and claude (CLI harnesses for the all-in-one
// path) plus pi-durable — the hosted kind, whose halves are --boot/--finish
// and whose conversation runs in the factory's WorkerAgent, never as a CLI in
// the container.
func TestTheScriptsAcceptTheHostedHarness(t *testing.T) {
	common := readScriptText(t, string(CommonScript))
	// The closed kind set, and the default a hand-driven container falls to.
	if !strings.Contains(common, "omp | claude | pi-durable)") {
		t.Error("common.sh's harness kind set is not `omp | claude | pi-durable`: the pi-CLI kind is deleted and the hosted kind is admitted in its place (epic 43y, tick jhp)")
	}
	if harnessDefault := regexp.MustCompile(`harness="\$\{TICKS_HARNESS:-([a-z-]+)\}"`).FindStringSubmatch(common); harnessDefault == nil {
		t.Error("common.sh no longer spells its default harness; the default is a fact the tests pin")
	} else if harnessDefault[1] != "omp" {
		t.Errorf("the default harness is %s, want omp: a hand-driven container with no TICKS_HARNESS falls to the image's documented default kind, and pi is deleted", harnessDefault[1])
	}
	worker := readScriptText(t, string(WorkerScript))
	if !strings.Contains(worker, "pi-durable") {
		t.Error("worker.sh never names the hosted harness: a pi-durable container must be told its halves are --boot/--finish, and the all-in-one must refuse it loudly")
	}
}
