package cli

// The command tests for `ticfac cloud branch`, ported from ticks'
// cmd/tk/cmd/cloud_branch_test.go (ticks commit 7b6c0b2f^, before chz made
// ticks tracker-only): the harness is the one in cloud_test.go
// (newCloudFactory), and ticks' ExecuteArgs-with-captured-output becomes
// Run(args, stdout, stderr) with the exit code asserted.
//
// CI remediation decides whether it may push to a branch from a positive
// record that something created it, not from the branch's name. The write side
// for a container is this command, and these tests pin the three properties
// that make it a boundary rather than a convenience:
//
//   - it posts to the branch door, never anywhere else;
//   - it carries the RUN's own gateway token, never the operator's factory
//     token, so a stop that revokes the run reaches this too (D17);
//   - it names nothing about identity in the body. The project, the run and
//     the epic are the factory's to derive from the credential — a container
//     that could name them could record a branch on behalf of a run it is not.

import (
	"net/http"
	"strings"
	"testing"
)

func TestCloudBranchRecordsThroughTheRunsOwnDoor(t *testing.T) {
	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Method == http.MethodPost && request.Path == "/api/branches" {
			return http.StatusCreated, map[string]any{"recorded": true}
		}
		return http.StatusNotFound, map[string]any{"error": "not_found"}
	})
	t.Setenv("TICKS_RUN_ID", "run_t4y")
	t.Setenv("TICKS_EPIC", "epic1")
	t.Setenv(cloudEnvFactoryURL, endpoint)
	t.Setenv(cloudEnvFactoryToken, "tkr_run_scoped")

	code, out, stderr := runCloudArgs(t, []string{"cloud", "branch", "tick-run/epic1", "--detail", "run branch"})
	if code != exitSuccess {
		t.Fatalf("cloud branch: %s\n%s", stderr.String(), out.String())
	}

	if len(*requests) != 1 {
		t.Fatalf("factory received %d request(s), want exactly one", len(*requests))
	}
	got := (*requests)[0]
	if got.Path != "/api/branches" {
		t.Fatalf("cloud branch posted to %s, want /api/branches", got.Path)
	}
	if got.Auth != "Bearer tkr_run_scoped" {
		t.Errorf("authorization = %q, want the run's own gateway token", got.Auth)
	}
	if got.Body["branch"] != "tick-run/epic1" {
		t.Errorf("branch = %#v", got.Body["branch"])
	}
	if got.Body["detail"] != "run branch" {
		t.Errorf("detail = %#v", got.Body["detail"])
	}
	// Identity is the credential's to establish. A body that named the project,
	// the run or the owner would be a container asserting what only the
	// control plane may decide.
	for _, forbidden := range []string{"project", "run_id", "epic", "owner"} {
		if _, present := got.Body[forbidden]; present {
			t.Errorf("the request body names %q; identity comes from the token, not the body", forbidden)
		}
	}
	if out := out.String(); !strings.Contains(out, "recorded tick-run/epic1") {
		t.Errorf("cloud branch said nothing about what it recorded:\n%s", out)
	}
}

func TestCloudBranchReportsAnAlreadyRecordedBranchWithoutFailing(t *testing.T) {
	endpoint, _ := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		// The door answers 200 with recorded:false when somebody got there
		// first. Records are evidence and are never overwritten.
		return http.StatusOK, map[string]any{"recorded": false}
	})
	t.Setenv("TICKS_RUN_ID", "run_t4y")
	t.Setenv(cloudEnvFactoryURL, endpoint)
	t.Setenv(cloudEnvFactoryToken, "tkr_run_scoped")

	code, out, stderr := runCloudArgs(t, []string{"cloud", "branch", "tick-run/epic1"})
	if code != exitSuccess {
		t.Fatalf("cloud branch on an already-recorded branch: %s\n%s", stderr.String(), out.String())
	}
	if out := out.String(); !strings.Contains(out, "already recorded") {
		t.Errorf("cloud branch did not say the branch was already decided:\n%s", out)
	}
}

func TestCloudBranchSurfacesTheFactorysOwnRefusal(t *testing.T) {
	endpoint, _ := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
		return http.StatusBadRequest, map[string]any{
			"error":  "branch_outside_epic",
			"detail": "tick/other/meo belongs to epic \"other\"",
		}
	})
	t.Setenv("TICKS_RUN_ID", "run_t4y")
	t.Setenv(cloudEnvFactoryURL, endpoint)
	t.Setenv(cloudEnvFactoryToken, "tkr_run_scoped")

	code, _, stderr := runCloudArgs(t, []string{"cloud", "branch", "tick/other/meo"})
	if code != exitGeneric {
		t.Fatalf("a refused branch record exited %d, want %d", code, exitGeneric)
	}
	if out := stderr.String(); !strings.Contains(out, "branch_outside_epic") || !strings.Contains(out, "belongs to epic") {
		t.Errorf("the factory's own refusal was not surfaced verbatim:\n%s", out)
	}
}

// No endpoint or credential in this container: not an error a boot should die
// on, but an honest refusal naming what will happen instead — remediation will
// not act on the branch and will report it in the daily digest.
func TestCloudBranchNamesTheConsequenceOfANakedContainer(t *testing.T) {
	code, _, stderr := runCloudArgs(t, []string{"cloud", "branch", "tick-run/epic1"})
	if code != exitGeneric {
		t.Fatalf("a container with no factory credential exited %d, want %d", code, exitGeneric)
	}
	if out := stderr.String(); !strings.Contains(out, "daily digest") {
		t.Errorf("the refusal does not say what the factory will do about the unrecorded branch:\n%s", out)
	}
}

// --json is one document naming its schema, whichever way the factory
// answered: "recorded by this run" and "already decided by somebody else" are
// both a done answer, told apart by `recorded`, never by parsing prose.
func TestCloudBranchJSONNamesItsSchema(t *testing.T) {
	for _, recorded := range []bool{true, false} {
		endpoint, _ := newCloudFactory(t, func(cloudFactoryRequest) (int, any) {
			return http.StatusOK, map[string]any{"recorded": recorded}
		})
		t.Setenv(cloudEnvFactoryURL, endpoint)
		t.Setenv(cloudEnvFactoryToken, "tkr_run_scoped")

		doc, stderr, code := jsonAnswer(t, []string{"cloud", "branch", "tick-run/epic1", "--json"})
		if code != exitSuccess {
			t.Fatalf("cloud branch --json exited %d:\n%s", code, stderr)
		}
		mustSchema(t, doc, "ticfac.cloud-branch.v1")
		if doc["state"] != agentStateDone {
			t.Errorf("recorded=%v: the state word is %v, want %q", recorded, doc["state"], agentStateDone)
		}
		if doc["branch"] != "tick-run/epic1" || doc["recorded"] != recorded {
			t.Errorf("recorded=%v: the document does not carry the factory's answer: %v", recorded, doc)
		}
	}
}
