package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
)

// configure writes the local mirror a completed `tk factory setup` leaves
// behind, which is what status reads.
func (h *setupHarness) configure(t *testing.T, gatewayKey string) {
	t.Helper()
	rc, err := credentials.LoadFrom(h.ticfacrc)
	if err != nil {
		t.Fatal(err)
	}
	rc.Set(credentials.KeyURL, h.server.URL)
	rc.Set(credentials.KeyVersion, "1.2.3")
	rc.Set(credentials.KeyGitHubToken, testPAT)
	rc.Set(credentials.KeyGitHubLogin, testLogin)
	rc.Set(credentials.KeyGitHubRepo, testRepo)
	rc.Set(credentials.KeyGatewayURL, h.gateway.base())
	rc.Set(credentials.KeyGatewayProvider, "anthropic")
	rc.Set(credentials.KeyGatewayKey, gatewayKey)
	if err := rc.Save(); err != nil {
		t.Fatal(err)
	}
}

func (h *setupHarness) statusOptions() StatusOptions {
	return StatusOptions{
		ConfigPath:        h.ticfacrc,
		GitHubAPIBase:     h.github.base(),
		CloudflareAPIBase: h.cloudflare.base(),
	}
}

// The deployed factory's version is pinned to the tk build that deployed it
// (D16, "upgrades ride the repo"). An upgrade leaves it behind until the
// operator redeploys, and status is where that surfaces — not `tk upgrade`,
// which knows nothing about factories.
func TestStatusFlagsDeploymentAVersionBehind(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	h.configure(t, "sk-provider-key") // factory_version=1.2.3

	opts := h.statusOptions()
	opts.Offline = true

	opts.CurrentVersion = "1.3.0"
	report, err := Status(context.Background(), opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(report.Deployment.Detail, "not this build's 1.3.0") ||
		!strings.Contains(report.Deployment.Detail, "runs 1.2.3") {
		t.Errorf("Deployment.Detail = %q, want it to say the factory runs 1.2.3, not this build's 1.3.0", report.Deployment.Detail)
	}

	// Matching, unset, and a "dev" build (which names no version) all say
	// nothing.
	for _, current := range []string{"1.2.3", "", "dev"} {
		opts.CurrentVersion = current
		report, err = Status(context.Background(), opts)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if strings.Contains(report.Deployment.Detail, "not this build's") {
			t.Errorf("CurrentVersion %q: Deployment.Detail = %q, want no version note", current, report.Deployment.Detail)
		}
	}
}

// Since CI deploys the factory, ~/.ticfacrc's factory_version is this
// machine's last deploy, not the factory's. A live status asks the factory
// (GET /api/deployment) and reports ITS answer — the recorded version and the
// commit it names, the Worker version, the confirmed image — and says when
// the local record disagrees.
func TestStatusReportsWhatTheFactoryRuns(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	seedDeployment(t, h)
	h.configure(t, "sk-provider-key") // factory_version=1.2.3, this machine's
	route := func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/deployment" {
			return false
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":                  "v1.3.0-2-g0123456789ab",
			"bundle_sha256":            "bundlesha",
			"deployed_at":              "2026-09-29T12:00:00Z",
			"image_ref":                "registry/ticks-orchestrator@sha256:abc",
			"image_digest":             "sha256:abc",
			"worker_version_id":        "becc1446-5594-43fb-acfd-1d6c71008891",
			"worker_version_timestamp": "2026-09-29T12:01:00Z",
		})
		return true
	}
	h.routes.Store(&route)

	opts := h.statusOptions()
	opts.CurrentVersion = "dev"
	report, err := Status(context.Background(), opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.Deployed == nil {
		t.Fatalf("Deployed = nil (note %q), want the factory's answer", report.DeployedNote)
	}
	if got := report.Deployed.Commit(); got != "0123456789ab" {
		t.Errorf("Commit() = %q, want the sha the describe names", got)
	}
	if !strings.Contains(report.Deployment.Summary, "runs v1.3.0-2-g0123456789ab") {
		t.Errorf("Summary = %q, want the factory's version, not the local record", report.Deployment.Summary)
	}
	if !strings.Contains(report.Deployment.Detail, "records 1.2.3") {
		t.Errorf("Detail = %q, want the local record's disagreement named", report.Deployment.Detail)
	}

	var buf bytes.Buffer
	report.Write(&buf)
	for _, want := range []string{"commit 0123456789ab", "becc1446-5594-43fb-acfd-1d6c71008891", "sha256:abc"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("status output does not report %q:\n%s", want, buf.String())
		}
	}
}

// A factory deployed before the route existed answers 404; status says so
// instead of failing the rung (the deployment still works).
func TestStatusOnAFactoryThatPredatesTheDeploymentRoute(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	seedDeployment(t, h)
	h.configure(t, "sk-provider-key")

	report, err := Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !report.Deployment.OK {
		t.Errorf("Deployment.OK = false (%s), want the rung to pass", report.Deployment.Detail)
	}
	if report.Deployed != nil || !strings.Contains(report.DeployedNote, "predates") {
		t.Errorf("Deployed = %v, note %q; want no facts and a note that the factory predates the route", report.Deployed, report.DeployedNote)
	}
	var buf bytes.Buffer
	report.Write(&buf)
	if !strings.Contains(buf.String(), "runs          unknown") {
		t.Errorf("status output does not say what the factory runs is unknown:\n%s", buf.String())
	}
}

// A factory can run without cost telemetry, so status has to say so — a
// configured-looking report whose cost budget can never fire is the silent
// failure D17 exists to prevent.
func TestStatusReportsCostTelemetry(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	h.configure(t, "sk-provider-key")

	report, err := Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.Telemetry.Configured {
		t.Error("cost telemetry reads as configured with no token stored")
	}
	var buf bytes.Buffer
	report.Write(&buf)
	for _, want := range []string{"cost telemetry", "--cloudflare-api-token"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("status never mentions %q:\n%s", want, buf.String())
		}
	}

	// With a token stored, the rung is checked against the gateway's own logs.
	rc, err := credentials.LoadFrom(h.ticfacrc)
	if err != nil {
		t.Fatal(err)
	}
	rc.Set(credentials.KeyCloudflareAPIToken, testCloudflareToken)
	if err := rc.Save(); err != nil {
		t.Fatal(err)
	}

	report, err = Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !report.Telemetry.Configured || !report.Telemetry.Checked || !report.Telemetry.OK {
		t.Errorf("cost telemetry state = %+v, want configured, checked and live", report.Telemetry)
	}
	if h.cloudflare.calls.Load() == 0 {
		t.Error("status reported a live telemetry credential without reading the gateway's logs")
	}
}

// The classifier rung (tick tum): Jev runs on Workers AI with the same
// Cloudflare token and the gateway's account, so status reports whether it
// ANSWERS — one tiny classification at the account's /ai/run — rather than
// whether a key exists. Without the token the rung is not configured, and the
// report says what that costs a run; with a refused token it is a failure.
func TestStatusReportsWhetherJevAnswers(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	h.configure(t, "sk-provider-key")

	report, err := Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.Classifier.Configured {
		t.Errorf("the classifier reads as configured with no Cloudflare token stored: %+v", report.Classifier)
	}
	var buf bytes.Buffer
	report.Write(&buf)
	if !strings.Contains(buf.String(), "classifier") || !strings.Contains(buf.String(), "[tier_policy.start]") {
		t.Errorf("status does not say what a run without the classifier does:\n%s", buf.String())
	}

	rc, err := credentials.LoadFrom(h.ticfacrc)
	if err != nil {
		t.Fatal(err)
	}
	rc.Set(credentials.KeyCloudflareAPIToken, testCloudflareToken)
	if err := rc.Save(); err != nil {
		t.Fatal(err)
	}

	offline := h.statusOptions()
	offline.Offline = true
	report, err = Status(context.Background(), offline)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !report.Classifier.Configured || report.Classifier.Checked || len(h.cloudflare.jevRuns()) != 0 {
		t.Errorf("an offline status probed the classifier: %+v", report.Classifier)
	}

	report, err = Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !report.Classifier.Configured || !report.Classifier.Checked || !report.Classifier.OK {
		t.Fatalf("the classifier state = %+v, want configured, checked and answering", report.Classifier)
	}
	if !strings.Contains(report.Classifier.Detail, "jev-1.13.0") {
		t.Errorf("the classifier detail does not name the answering model: %q", report.Classifier.Detail)
	}
	runs := h.cloudflare.jevRuns()
	if len(runs) != 1 || runs[0] != "/accounts/00000000000000000000000000000000/ai/run" {
		t.Errorf("the classifier was asked at %v, want one run at the gateway account's /ai/run", runs)
	}

	rc.Set(credentials.KeyCloudflareAPIToken, "cf_wrong_token")
	if err := rc.Save(); err != nil {
		t.Fatal(err)
	}
	report, err = Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.Classifier.OK || !strings.Contains(strings.Join(report.Failures(), ","), "classifier") {
		t.Errorf("a refused token left the classifier %+v, failures %v", report.Classifier, report.Failures())
	}
}

// A rejected telemetry token is named as a failure like any other credential.
func TestStatusReportsARejectedTelemetryToken(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	h.configure(t, "sk-provider-key")
	rc, err := credentials.LoadFrom(h.ticfacrc)
	if err != nil {
		t.Fatal(err)
	}
	rc.Set(credentials.KeyCloudflareAPIToken, "cf_wrong_token")
	if err := rc.Save(); err != nil {
		t.Fatal(err)
	}

	report, err := Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.Telemetry.OK {
		t.Error("a rejected telemetry token reads as live")
	}
	if !strings.Contains(strings.Join(report.Failures(), ","), "cost telemetry") {
		t.Errorf("Failures() = %v, want the telemetry rung named", report.Failures())
	}
}

// Nothing configured is a normal state, not an error.
func TestStatusWithNothingConfigured(t *testing.T) {
	h := newSetupHarness(t, "")

	report, err := Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.Deployment.Configured || report.GitHub.Configured || report.Gateway.Configured {
		t.Errorf("status reports configuration that does not exist: %+v", report)
	}
	if len(report.Failures()) != 0 {
		t.Errorf("nothing configured must not count as a failure: %v", report.Failures())
	}

	var buf bytes.Buffer
	report.Write(&buf)
	out := buf.String()
	if !strings.Contains(out, "ticfac factory setup") {
		t.Errorf("status does not point at the command that configures it:\n%s", out)
	}
	if h.github.calls.Load() != 0 || h.gateway.calls.Load() != 0 {
		t.Error("status probed endpoints for credentials that do not exist")
	}
}

// Configured and healthy: each credential is checked live and reported working.
func TestStatusChecksEveryCredentialLive(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	h.configure(t, "sk-provider-key")
	// The deployment probe needs the worker to accept the stored token.
	seedDeployment(t, h)
	h.configure(t, "sk-provider-key")

	report, err := Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	for name, state := range map[string]CredentialState{
		"deployment": report.Deployment,
		"github":     report.GitHub,
		"gateway":    report.Gateway,
	} {
		if !state.Configured {
			t.Errorf("%s: Configured = false, want true", name)
		}
		if !state.Checked {
			t.Errorf("%s: Checked = false, want a live check", name)
		}
		if !state.OK {
			t.Errorf("%s: OK = false (%s)", name, state.Detail)
		}
	}
	if len(report.Failures()) != 0 {
		t.Errorf("Failures() = %v, want none", report.Failures())
	}

	var buf bytes.Buffer
	report.Write(&buf)
	out := buf.String()
	for _, want := range []string{testLogin, testRepo, "anthropic"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output does not report %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, testPAT) || strings.Contains(out, "sk-provider-key") {
		t.Errorf("status printed a secret:\n%s", out)
	}
}

// --offline is the flag that makes status safe to run with no network.
func TestStatusOfflineMakesNoProbes(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	h.configure(t, "sk-provider-key")

	opts := h.statusOptions()
	opts.Offline = true
	report, err := Status(context.Background(), opts)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if h.github.calls.Load() != 0 || h.gateway.calls.Load() != 0 {
		t.Errorf("--offline still probed: github=%d gateway=%d", h.github.calls.Load(), h.gateway.calls.Load())
	}
	if report.GitHub.Checked || report.Gateway.Checked || report.Deployment.Checked {
		t.Error("--offline reported live checks it did not make")
	}
	if !report.GitHub.Configured || !report.Gateway.Configured {
		t.Error("--offline lost track of what is configured")
	}
	if len(report.Failures()) != 0 {
		t.Errorf("an unchecked credential is not a failure: %v", report.Failures())
	}
}

// A credential that no longer works is reported as such, and named by
// Failures() so a caller can turn it into a nonzero exit.
func TestStatusReportsARejectedCredential(t *testing.T) {
	h := newSetupHarness(t, "sk-provider-key")
	seedDeployment(t, h)
	h.configure(t, "sk-wrong-key")
	rc, err := credentials.LoadFrom(h.ticfacrc)
	if err != nil {
		t.Fatal(err)
	}
	rc.Set(credentials.KeyGitHubToken, "github_pat_revoked")
	if err := rc.Save(); err != nil {
		t.Fatal(err)
	}

	report, err := Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if report.GitHub.OK {
		t.Error("a revoked PAT was reported as working")
	}
	if report.Gateway.OK {
		t.Error("a rejected gateway key was reported as working")
	}
	failures := strings.Join(report.Failures(), " ")
	if !strings.Contains(failures, "github") || !strings.Contains(failures, "gateway") {
		t.Errorf("Failures() = %v, want both rungs named", report.Failures())
	}

	var buf bytes.Buffer
	report.Write(&buf)
	if out := buf.String(); !strings.Contains(strings.ToLower(out), "rejected") {
		t.Errorf("status does not say the credential was rejected:\n%s", out)
	}
}

// A partially walked ladder is the common real state: report what is there and
// what is missing without treating the gap as a failure.
func TestStatusReportsAPartialConfiguration(t *testing.T) {
	h := newSetupHarness(t, "")
	seedDeployment(t, h)
	rc, err := credentials.LoadFrom(h.ticfacrc)
	if err != nil {
		t.Fatal(err)
	}
	rc.Set(credentials.KeyGitHubToken, testPAT)
	rc.Set(credentials.KeyGitHubRepo, testRepo)
	if err := rc.Save(); err != nil {
		t.Fatal(err)
	}

	report, err := Status(context.Background(), h.statusOptions())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !report.GitHub.Configured || !report.GitHub.OK {
		t.Errorf("github rung: %+v", report.GitHub)
	}
	if report.Gateway.Configured {
		t.Error("an unconfigured gateway was reported as configured")
	}
	if len(report.Failures()) != 0 {
		t.Errorf("a missing rung is not a rejected credential: %v", report.Failures())
	}

	var buf bytes.Buffer
	report.Write(&buf)
	if out := buf.String(); !strings.Contains(out, "not configured") {
		t.Errorf("status does not name the missing rung:\n%s", out)
	}
}
