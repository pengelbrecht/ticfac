package reconcile

import (
	"fmt"
	"os"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The person's promotion writes (tick sg5): the triage surface's half of the
// seam `ticfac finding --promote-as` made the operator hold — mint a tick id,
// file a tick record, make it durable — for the everyday triage that must not
// demand a 64-hex key typed by hand or a tick created by hand before the
// command can record anything.
//
// The machinery is npq's, unchanged: the record is written exactly as the
// tracker's owner writes it, against the pinned layout
// (contracts/tracker-layout.json), in a DETACHED worktree of the integration
// branch, and published — the commit and push, under the same compare-and-swap
// every tracker write here keeps — before the create answers. What this file
// adds is the ENTRANCE for a command that is not a whole reconciler: the
// triage surface does not run waves, needs no executor, no gate and no
// profiles, so it takes the promotion writes alone rather than constructing a
// run to reach them.
//
// The idempotency is the run's own: create-if-absent, so a person retrying a
// triage that failed between the tick and the draft's triage re-runs the same
// decision and reaches the same records, and a standing record is the truth,
// never clobbered.

// PromotionOptions addresses the run whose branch a promoted tick lands on —
// the same four facts both findings commands address the drafts with.
type PromotionOptions struct {
	// Repo is the checkout the run works in.
	Repo string
	// Remote is the remote holding the run's durable authority.
	Remote string
	// Branch is the EpicRun integration branch the tick record lands on.
	Branch string
	// RunID is the run's id, for the private fetch refs a writer of the
	// branch must not share.
	RunID string
}

// Promotions is the durable promotion seam for the triage surface: mint a
// tick id the tracker does not already carry, and file a tick record the next
// wave's worker can read. Every create publishes before it returns, so Close
// discards nothing: everything written here is already on the remote.
type Promotions struct {
	tree *trackerTree
}

// OpenPromotions prepares the promotion writes against the integration branch
// as origin has it, in a worktree of its own — never a checkout somebody is
// using, for the same reason the reconciler's tracker keeps one.
func OpenPromotions(opts PromotionOptions) (*Promotions, error) {
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	if opts.Repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		opts.Repo = wd
	}
	if opts.Branch == "" || opts.RunID == "" {
		return nil, fmt.Errorf("reconcile: a promotion names the branch and the run the tick lands on")
	}
	g := &repoGit{dir: opts.Repo, name: "ticfac", email: "ticfac@example.com", remote: opts.Remote,
		retry: runstate.RemoteRetry{}}
	if _, err := g.run("", "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("reconcile: %s is not a git repository: %w", opts.Repo, err)
	}
	tree, err := openTrackerTree(g, opts.Remote, opts.Branch, opts.RunID)
	if err != nil {
		return nil, fmt.Errorf("reconcile: prepare the tracker's worktree on %s: %w", opts.Branch, err)
	}
	return &Promotions{tree: tree}, nil
}

// MintTickID answers a tick id the tracker does not already carry — the same
// mint the reconciler's own absorption mints with, over the same worktree,
// because two minters of the same alphabet in two places are two ways to
// collide.
func (p *Promotions) MintTickID() (string, error) {
	return mintTickID(p.tree)
}

// CreateTick files one tick record durably — CREATE-IF-ABSENT, so a retry of a
// half-made promotion finds the standing record and publishes nothing, the
// same rule the reconciler's own create keeps. The write, then the commit and
// push that make it a record the next wave's worker can read — the same order
// every tracker write here keeps.
func (p *Promotions) CreateTick(tick tk.Tick) (tk.Tick, error) {
	if err := p.tree.sync(); err != nil {
		return tk.Tick{}, err
	}
	filed, existed, err := fileTick(p.tree, tick)
	if err != nil {
		return tk.Tick{}, err
	}
	if existed {
		// The record already there is the truth, never clobbered — and
		// nothing is published for a no-change create.
		return filed, nil
	}
	reason := "create tick " + filed.ID
	if _, err := p.tree.publish(reason); err != nil {
		return filed, fmt.Errorf("%s reached the tracker and not %s: a tracker record that is not pushed is a record "+
			"the next wave's worker cannot read: %w", reason, p.tree.remote, err)
	}
	return filed, nil
}

// Close removes the worktree. Nothing is lost with it: every create published
// before it returned.
func (p *Promotions) Close() { p.tree.close() }
