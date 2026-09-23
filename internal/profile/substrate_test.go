package profile

import (
	"errors"
	"strings"
	"testing"
)

// The substrate half of profile routing (tick 84z): the same `.tick/runners.toml`
// that routes a role's runner and model routes them PER SUBSTRATE, through the
// `[roles.<name>.substrates.<substrate>]` overlay — so a local run keeps its
// frontier review on opus while a cloud container runs a worker it can actually
// call, off the claude harness entirely.
//
// The failure this must not have: a cloud run that silently falls back to the
// profile as shipped — the shipped review profile IS a claude process. Absent
// cloud routing is a REFUSAL naming the role, never a fall back.

const cloudRoutingDocument = `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.implement.substrates.cloud]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.review]
kind = "claude"
model = "opus"

[roles.review.substrates.cloud]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.closeout]
kind = "claude"
model = "opus"
`

// A declared cloud overlay is what a cloud run resolves against; the local
// substrates keep the role's own cells. The provenance names the overlay, and
// records which substrate was resolved against, so an attempt record can say
// what routed its worker.
func TestTheSubstrateOverlayRoutesTheProfile(t *testing.T) {
	config := writeConfig(t, cloudRoutingDocument)

	cloud, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if cloud.Runner != "pi" || cloud.Model != "cloudflare-workers-ai/@cf/zai-org/glm-5.3" {
		t.Errorf("cloud review-epic routed to %s/%s", cloud.Runner, cloud.Model)
	}
	if !strings.Contains(cloud.Routed, "roles.review.substrates.cloud") {
		t.Errorf("the provenance does not name the cloud overlay: %q", cloud.Routed)
	}
	if cloud.Provenance.Substrate != "cloud" {
		t.Errorf("the provenance does not record the substrate: %q", cloud.Provenance.Substrate)
	}

	local, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "herdr"})
	if err != nil {
		t.Fatal(err)
	}
	if local.Runner != "claude" || local.Model != "opus" {
		t.Errorf("herdr review-epic routed to %s/%s, want the frontier claude/opus", local.Runner, local.Model)
	}
	if local.Provenance.Substrate != "herdr" || strings.Contains(local.Routed, "substrates") {
		t.Errorf("the local resolution misrecords its routing: %+v", local.Provenance)
	}
}

// The two resolutions must not digest the same: a run's evidence says WHICH
// profile it was made under, and a cloud review and a local review are
// different judgements, not two spellings of one.
func TestTheSubstrateChangesTheDigest(t *testing.T) {
	config := writeConfig(t, cloudRoutingDocument)
	cloud, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	local, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "herdr"})
	if err != nil {
		t.Fatal(err)
	}
	if cloud.Digest == local.Digest {
		t.Error("a cloud review digests the same as the frontier local one")
	}
}

// Absent cloud routing is a refusal naming the role, never a fall back to the
// role's own values — the closeout cell above declares none, and the claude
// process its base cell names is exactly what a container must not start.
func TestCloudRoutingThatIsAbsentIsARefusalNotAFallback(t *testing.T) {
	config := writeConfig(t, cloudRoutingDocument)

	_, err := Resolve("closeout-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	if err == nil {
		t.Fatal("a role with no cloud overlay resolved anyway, falling back to its own claude routing")
	}
	for _, want := range []string{"closeout", "roles.closeout", "substrates.cloud"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// A role the config does not declare at all falls back to the profile as
// shipped on every local substrate — and the shipped review and close-out
// profiles ARE claude processes, so on the cloud substrate that silence is a
// refusal too, not a routing nobody chose.
func TestARoleTheCloudConfigDoesNotDeclareIsRefused(t *testing.T) {
	config := writeConfig(t, `version = 2

[roles.implement]
kind = "pi"
model = "glm"
`)

	_, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	if err == nil || !strings.Contains(err.Error(), "review") {
		t.Fatalf("the cloud substrate accepted a role the config does not declare: %v", err)
	}
	if !errors.Is(err, ErrNoCloudRouting) {
		t.Errorf("the refusal is not recognisable as ErrNoCloudRouting: %v", err)
	}
}

// No config named at all is the loudest version of the same refusal: the
// profile as shipped is the only routing there is, and it is a claude
// process.
func TestTheCloudSubstrateRefusesWhenNoConfigWasNamed(t *testing.T) {
	_, err := Resolve("review-epic", Options{Substrate: "cloud"})
	if err == nil {
		t.Fatal("the cloud substrate resolved a profile with no runner configuration to declare its cloud routing")
	}
	if !strings.Contains(err.Error(), "review") {
		t.Errorf("the refusal does not name the role: %v", err)
	}
}

// auto is a policy a decision procedure resolves, not a substrate a profile
// resolves against; a caller passing it has skipped the decision.
func TestAutoIsNotASubstrateAProfileResolvesAgainst(t *testing.T) {
	config := writeConfig(t, cloudRoutingDocument)
	_, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "auto"})
	if err == nil || !strings.Contains(err.Error(), "auto") {
		t.Fatalf("auto was accepted as a substrate to route a profile against: %v", err)
	}
}

// The empty substrate is the substrate-blind resolution the run has always
// had: a caller that knows no substrate keeps the historical behaviour
// exactly, byte for byte.
func TestNoSubstrateIsTheSubstrateBlindResolution(t *testing.T) {
	config := writeConfig(t, cloudRoutingDocument)
	blind, err := Resolve("review-epic", Options{RunnersConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	if blind.Runner != "claude" || blind.Model != "opus" {
		t.Errorf("the substrate-blind review routed to %s/%s, want the role's own claude/opus", blind.Runner, blind.Model)
	}
	if blind.Provenance.Substrate != "" {
		t.Errorf("the substrate-blind resolution records a substrate: %q", blind.Provenance.Substrate)
	}
}

// The refusal is recognisable programmatically, not only by prose: a caller
// that wants to distinguish "the cloud refused this role" from every other
// routing failure gets a sentinel to ask.
func TestTheCloudRefusalIsRecognisableAsErrNoCloudRouting(t *testing.T) {
	config := writeConfig(t, cloudRoutingDocument)
	_, err := Resolve("closeout-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	if err == nil {
		t.Fatal("closeout resolved on the cloud substrate without cloud routing")
	}
	if !errors.Is(err, ErrNoCloudRouting) {
		t.Errorf("the refusal is not recognisable as ErrNoCloudRouting: %v", err)
	}
}
