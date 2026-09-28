package cli

// Recording a branch from INSIDE a cloud run — `ticfac cloud branch`, ported
// from ticks' cmd/tk/cmd/cloud_branch.go (at ticks 7b6c0b2f^, before the chz cut deleted it).
//
// The factory decides whether it may push to a branch. Until this command that
// decision was a prefix match on the branch NAME — `tick/…`, `tick-run/…`,
// `epic/…` — and a name is a claim anybody can make. The branches that matter
// are pushed from inside a sandbox container, which holds no database handle
// and never will.
//
// It does hold a voice: the container asks the control plane, over the run's
// OWN gateway token, and the token decides which run is speaking. A container
// cannot record a branch on behalf of a run it is not, cannot record one
// outside its own epic, and cannot say a branch belongs to a PERSON — that
// sentence is a person's, at the operator door (`POST /api/ci/branches`).
//
// image/common.sh and worker.sh call this at the moment they create a branch,
// so the record is written by the substrate and not asked of an agent's prompt.
// This does not widen D21's operator-to-orchestrator vocabulary: it commands no
// run — it starts nothing, stops nothing, changes no run's state, and what it
// writes is read by CI remediation to decide what it may NOT do.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/httpnet"
)

// cloudBranchEndpoint is the factory path this command posts to. It mirrors
// BRANCH_CLAIM_PATH in cloudflare/src/branch-registry.ts.
const cloudBranchEndpoint = "/api/branches"

// The container-side credential names — the same spellings the image exports
// and common.sh reads (sandboximage.Env*), stated here because the container's
// `ticfac cloud branch` is the one caller that answers the environment rather
// than the operator's config file: a sandbox holds no ~/.ticfacrc.
const (
	cloudEnvFactoryURL   = "TICKS_FACTORY_URL"
	cloudEnvFactoryToken = "TICKS_FACTORY_TOKEN"
)

func newCloudBranchCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "branch <name>",
		Short: "Record with the factory that this run created a branch",
		Long: `Record with the factory that this run created a branch.

Branch ownership in the CI remediation loop is a positive record, not a naming
convention: the factory drives a branch back to green only when something says
it created that branch. A branch with no record is refused — the safe
direction — and reported in the daily digest until a person answers.

This is the write side for a container. It authenticates with the run's own
gateway token (TICKS_FACTORY_TOKEN), so the factory derives the project, the
run and the epic from the credential rather than from anything this command
claims. It records "the factory created this" and nothing else; saying a
branch is a person's is a person's sentence, at POST /api/ci/branches.

Only branches of this run's own epic can be recorded, and only in the
namespaces the factory owns. Records are never overwritten: a branch that was
already recorded reports that and changes nothing.

  ticfac cloud branch "$TICKS_RUN_BRANCH"
  ticfac cloud branch epic/ko8 --detail "integration branch for this epic"`,
		Args: cobra.ExactArgs(1),
	}
	detail := cmd.Flags().String("detail", "",
		"how the branch came to exist, for a person reading the record later")
	asJSON := cmd.Flags().Bool("json", false,
		"print one versioned document (ticfac.cloud-branch.v1): the branch, and whether this run recorded it")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(reportCommand("cloud branch", cloudBranchCommand(c.Context(), args[0], *detail, *asJSON, stdout, stderr), stderr))
	}
	return cmd
}

func cloudBranchCommand(ctx context.Context, name, detail string, asJSON bool, stdout, stderr io.Writer) error {
	branch := strings.TrimSpace(name)
	if branch == "" {
		return newExitError(exitUsage, "name the branch to record")
	}
	return recordCloudBranch(ctx, stdout, branch, detail, asJSON)
}

// cloudBranchJSON is `cloud branch --json`'s answer, ticfac.cloud-branch.v1.
// `recorded` is the factory's own answer, so an agent can tell "recorded by
// this run" from "decided by somebody else" without parsing prose.
type cloudBranchJSON struct {
	agentDoc
	Branch   string `json:"branch"`
	Recorded bool   `json:"recorded"`
}

// recordCloudBranch posts one branch to the factory's container door.
//
// The refusal is returned VERBATIM from the factory rather than reinterpreted
// here. Two doors that answer the same question must not describe it
// differently, and a container that recorded nothing needs to know which of
// "the token is dead", "that is not your epic" and "somebody already claimed
// it" happened — they are three different operator problems.
func recordCloudBranch(ctx context.Context, out io.Writer, branch, detail string, asJSON bool) error {
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv(cloudEnvFactoryURL)), "/")
	token := strings.TrimSpace(os.Getenv(cloudEnvFactoryToken))
	if endpoint == "" || token == "" {
		return newExitError(exitGeneric,
			"this container has no factory endpoint or credential, so it cannot record "+
				"branch %s; the factory will refuse to act on it and report it in the daily digest",
			branch)
	}

	payload := map[string]any{"branch": branch}
	if trimmed := strings.TrimSpace(detail); trimmed != "" {
		payload["detail"] = trimmed
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return newExitError(exitGeneric, "encode branch record: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+cloudBranchEndpoint,
		strings.NewReader(string(body)))
	if err != nil {
		return newExitError(exitGeneric, "create branch record request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if cloudHTTPClient == nil {
		cloudHTTPClient = httpnet.Client(15 * time.Second)
	}
	res, err := cloudHTTPClient.Do(req)
	if err != nil {
		return newExitError(exitGeneric,
			"the factory could not be reached to record branch %s: %v", branch, err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusCreated || res.StatusCode == http.StatusOK {
		var recorded struct {
			Recorded bool `json:"recorded"`
		}
		_ = json.NewDecoder(res.Body).Decode(&recorded)
		if asJSON {
			return emitAgentJSON(out, cloudBranchJSON{
				agentDoc: agentDoc{Schema: agentSchemaID("cloud-branch"), State: agentStateDone},
				Branch:   branch,
				Recorded: recorded.Recorded,
			})
		}
		if recorded.Recorded {
			fmt.Fprintf(out, "recorded %s as created by this run\n", branch)
		} else {
			// Not an error: the branch IS decided. But it was decided by
			// somebody else, and a container that just created a branch
			// somebody had already claimed is being told something worth
			// reading in the log.
			fmt.Fprintf(out, "%s was already recorded; this run did not change it\n", branch)
		}
		return nil
	}

	var refusal struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	_ = json.NewDecoder(res.Body).Decode(&refusal)
	detailText := strings.TrimSpace(refusal.Detail)
	if detailText == "" {
		detailText = fmt.Sprintf("the factory answered %d", res.StatusCode)
	}
	return newExitError(exitGeneric,
		"the factory refused to record branch %s (%s): %s",
		branch, strings.TrimSpace(refusal.Error), detailText)
}
