package cli

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/cloudflaresandbox"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// The honoured set's third entry (tick xev): a profile naming
// cloudflare-sandbox resolves and routes to the sandbox executor, so the
// per-tick sandbox executor a run can finally SELECT is the one its profiles
// (profiles-cloudflare-sandbox/, tick njj) name.

// short: table lookups and one constructor call; the door the executor would
// dial is never reached, because the tests assert the routing and the
// constructor's own refusals.
func TestTheHonouredSetNamesTheSandboxExecutor(t *testing.T) {
	known := knownExecutors()
	found := false
	for _, executor := range known {
		if executor.Name != cloudflaresandbox.ExecutorName {
			continue
		}
		found = true
		if executor.PollInterval != cloudflaresandbox.PollInterval {
			t.Errorf("the sandbox executor's cadence is %s, want the executor's own %s: on a substrate that can "+
				"take an unaddressed job away, the poll is the keepalive and the cadence is the executor's to state",
				executor.PollInterval, cloudflaresandbox.PollInterval)
		}
		if executor.AcceptsModel == nil || !executor.AcceptsModel("pi") {
			t.Error("the sandbox executor refuses a model for pi: every cloud profile names one, and the recorded " +
				"model resting on the factory's routing is the standing finding against tick njj, not a lie this " +
				"registration invents by refusing the profile outright")
		}
		runs := false
		for _, runner := range executor.Runners {
			if runner == "pi" {
				runs = true
			}
		}
		if !runs {
			t.Errorf("the sandbox executor launches %v, none of which is pi: the dispatch profile pairs it with pi",
				executor.Runners)
		}
	}
	if !found {
		t.Fatalf("the honoured set is %v, without %s: a run still cannot select the per-tick sandbox executor, "+
			"which is the gap tick xev exists to close", honouredNames(), cloudflaresandbox.ExecutorName)
	}
	if !strings.Contains(honouredNames(), cloudflaresandbox.ExecutorName) {
		t.Errorf("a refusal naming the honoured set says %q, without the sandbox executor", honouredNames())
	}
}

// cloudDispatch is the dispatch a cloud profile routes, carrying the door's
// required fields the reconciler assembled (BaseRef and Title ride the
// Dispatch from the run's own configuration and the tracker's own words).
func cloudDispatch(t *testing.T) reconcile.Dispatch {
	t.Helper()
	return reconcile.Dispatch{
		RunID:    "r1",
		EpicID:   "yoh",
		TickID:   "xev",
		Attempt:  1,
		JobID:    "run-r1/tick-xev/attempt-1",
		Role:     "implement-tick",
		Repo:     t.TempDir(),
		Remote:   "origin",
		WriteRef: "refs/heads/ticfac/run-r1/tick-xev/attempt-1",
		BaseSHA:  "0123456789abcdef0123456789abcdef01234567",
		BaseRef:  "epic/yoh",
		Title:    "Register cloudflare-sandbox as an honoured executor",
		Profile: &profile.Profile{
			Role: "implement-tick", Executor: cloudflaresandbox.ExecutorName,
			Runner: "pi", Model: "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		},
	}
}

// The factory routes a cloud profile's dispatch to the sandbox executor and
// refuses anything it cannot build — fail closed, never a silent fall-back to
// the local executor, because a dispatch through an executor the record does
// not name is provenance that lies.
//
// short: one constructor call per case; the door is never dialled.
func TestTheFactoryRoutesACloudProfileToTheSandboxExecutor(t *testing.T) {
	t.Setenv("TICKS_FACTORY_URL", "https://factory.example.com")
	t.Setenv("TICKS_FACTORY_TOKEN", "run-r1-token")

	d := cloudDispatch(t)
	executor, substrate, err := executorFactory("pi", "")(d)
	if err != nil {
		t.Fatalf("the factory refused a dispatch the honoured set names: %v", err)
	}
	if _, ok := executor.(*cloudflaresandbox.Executor); !ok {
		t.Fatalf("the factory built %T, want the cloudflare-sandbox executor: a cloud profile dispatched "+
			"through anything else is provenance that lies", executor)
	}
	if substrate.Protocol != 0 || substrate.ServerVersion != "" {
		t.Errorf("the substrate is %d/%q, want the zero value: the door carries no versioned protocol to pin",
			substrate.Protocol, substrate.ServerVersion)
	}
}

// A cloud dispatch with the door's environment missing fails BEFORE anything
// is claimed: the constructor refuses an unconfigured door, and a run whose
// profiles name the sandbox executor without the factory to dial learns that
// at the dispatch, not three ticks in.
//
// short: constructor refusals only; no network.
func TestACloudDispatchWithoutADoorFailsAtTheFactory(t *testing.T) {
	t.Setenv("TICKS_FACTORY_URL", "")
	t.Setenv("TICKS_FACTORY_TOKEN", "")

	if _, _, err := executorFactory("pi", "")(cloudDispatch(t)); err == nil {
		t.Fatal("a dispatch with no factory to dial built an executor anyway: it would fail at the first " +
			"door call, after the tick was claimed")
	}
}

// The dispatch's own fields reach the door's start request, because the door
// takes a base_ref and a title per start and the executor must not re-derive
// either: a title minted from anywhere but the tracker would be a title the
// dispatch record does not name. Asserted through the executor's own
// validation, which runs before any network call, against a door that
// refuses the connection instantly — the assertions are about the door's
// field shapes, never about a real factory.
//
// short: spec validation and a refused local connection; no real network.
func TestTheDoorFieldsRideTheDispatch(t *testing.T) {
	// A port nothing listens on: validation runs first, so a dispatch that
	// passes it fails at the CONNECT, immediately and without a name lookup.
	t.Setenv("TICKS_FACTORY_URL", "http://127.0.0.1:9")
	t.Setenv("TICKS_FACTORY_TOKEN", "run-r1-token")

	d := cloudDispatch(t)
	executor, _, err := executorFactory("pi", "")(d)
	if err != nil {
		t.Fatalf("the factory refused a dispatch the honoured set names: %v", err)
	}

	spec := &subprocess.JobSpec{
		SchemaVersion:  subprocess.SchemaVersion,
		JobID:          d.JobID,
		Role:           d.Role,
		Source:         subprocess.Source{Repository: d.Repo, BaseSHA: d.BaseSHA, WriteRef: d.WriteRef},
		Capabilities:   subprocess.Capabilities{Persistence: "durable", Isolation: "process", Network: "restricted"},
		Inputs:         []subprocess.Input{{Kind: "tick", ID: d.TickID}},
		OutputSchema:   "ticfac.job-result.implement-tick.v1",
		ArtifactPrefix: "runs/" + d.RunID + "/" + d.TickID + "/",
		Credentials: subprocess.Credentials{
			Model: subprocess.ModelCredential{Shorthand: "issued-by-host"},
			Source: subprocess.SourceCredential{Grant: &subprocess.SourceGrant{
				Issuer: "host", Grade: "write", WriteRefPrefix: "refs/heads/ticfac/",
			}},
		},
		Limits: subprocess.Limits{WallSeconds: 3600},
	}
	// The start with every door field present gets PAST the field validation
	// and dies at the connect — proof the fields travelled, since the refusal
	// it would otherwise raise names the field that is missing.
	if _, err := executor.Start(spec); err == nil {
		t.Fatal("a start reached a door that does not exist")
	} else if strings.Contains(err.Error(), "title") || strings.Contains(err.Error(), "base_ref") {
		t.Errorf("the start was refused on a field the dispatch carries: %v", err)
	}

	// And a dispatch with no title — the one the door refuses hardest, being
	// for a later boot's re-derivation — is refused with the title named,
	// before any door is dialled at all.
	bare := d
	bare.Title = ""
	bareExecutor, _, err := executorFactory("pi", "")(bare)
	if err != nil {
		t.Fatalf("the factory refused a bare-title dispatch: %v", err)
	}
	if _, err := bareExecutor.Start(spec); err == nil {
		t.Fatal("a start with no title to hand the door was accepted: the door requires one, and a title the " +
			"executor invented would be a title the dispatch record does not name")
	} else if !strings.Contains(err.Error(), "title") {
		t.Errorf("the refusal does not name the missing title: %v", err)
	}
}
