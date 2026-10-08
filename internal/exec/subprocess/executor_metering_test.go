package subprocess

import (
	"testing"
)

// The gateway metering join on THIS executor (tick gzv, absorbing dm2's
// finding) loaded a generated provider override into the pi CLI through
// `--extension`. Since epic 43y (tick hpk) the `pi` runner is the pi-durable
// Node harness: it has no extension surface and refuses any argument it does
// not know (harness/src/local/main.ts parseArgs), so a join loaded into its
// argv would turn every metered local dispatch into a worker that never
// starts. The executor still holds the join the dispatch resolved — the one
// resolution both local substrates share, and the herdr executor's pi CLI
// panes still load it — but a durable launch carries none of it.
func TestAMeteredDurableLaunchCarriesNoExtension(t *testing.T) {
	t.Parallel()

	const model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
	metered := &GatewayMetering{RunID: "epic-hn6", GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw"}

	repo := newRepo(t, "metered-durable")
	e, err := New(Options{Repo: repo.Dir, Runner: "pi", Model: model, Metering: metered})
	if err != nil {
		t.Fatal(err)
	}
	if e.Metering() != metered {
		t.Errorf("the executor dropped the join the dispatch resolved: %v", e.Metering())
	}

	at := launch{
		Prompt:       "PROMPT-BODY",
		GitCommonDir: "/repo/.git",
		Model:        model,
		HarnessDir:   e.harnessDir(),
		StateDir:     t.TempDir(),
	}
	argv, err := resolveRunner("pi", nil, at)
	if err != nil {
		t.Fatal(err)
	}
	if contains(argv, extensionFlag) {
		t.Errorf("the durable harness launch carries the pi CLI's extension flag, which the harness refuses: %v", argv)
	}
	if i := indexOf(argv, "--model"); i < 0 || i+1 >= len(argv) || argv[i+1] != model {
		t.Errorf("the durable launch lost the routed model: %v", argv)
	}
}
