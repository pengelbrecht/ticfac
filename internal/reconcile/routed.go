package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tempdir"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// A finding routed to ANOTHER repository, disposed of by the run itself (the
// epic-2jn close-out stall, 2026-09-27).
//
// Every finding routed to the run's own repository is decided mechanically
// (absorb.go). A routed one had no such path: the run cannot absorb another
// repository's work, so it waited for a person — and the close-out's
// untriaged-findings hold fired on five of them, some claiming to break done
// items this run can never fix. A step whose only actor is a person is a
// design defect in a factory that runs unattended, so a routed finding now
// gets a disposition with nobody triaging, and NEVER holds a run:
//
//   - it NEVER GATES this epic, whatever done item it claims: the fix lives
//     in a tree this run does not build, so absorbing it would stall the epic
//     on work it cannot do. The decision rests on that rule, not on either
//     gating tier, and the record says so (Basis rule) so the scoring never
//     grades it as a prediction;
//   - a target the repository's runners.toml lets the run file into
//     ([findings.route."owner/name"] file = true) is FILED THERE: a tick in
//     the target's own tracker, one new `.tick/issues/<id>.json` committed
//     onto the target's default branch and pushed — an added file, which no
//     tracker merge driver ever has to merge — and the draft is promoted as
//     "<owner/name>:<id>";
//   - any other target, or a filing that fails terminally, becomes a BACKLOG
//     TICK HERE, titled with the target and carrying the finding verbatim, so
//     a person finds it in the tracker they already read;
//   - a TRANSIENT failure (runstate.ClassifyRemote) is retried by the git
//     runner's own bound, and past it the finding is left for the close-out,
//     which disposes of every routed finding still proposed before it gates —
//     and there a transient failure stops the run as the resumable
//     remote_transient stop, never as a hold.

// ruleReason is the reasoning every routed decision carries: why a finding
// that may claim a done item of this epic gates none of them here.
func ruleReason(finding runstate.Finding) string {
	claim := "it claims no done item of this epic"
	switch item := strings.TrimSpace(finding.DoneItem); {
	case item == "":
	case strings.EqualFold(item, "none"):
		claim = "its reporter claims it breaks no done item"
	default:
		claim = fmt.Sprintf("its reporter claims it breaks done item %s", item)
	}
	return fmt.Sprintf("the finding is routed to %s, and this run cannot fix another repository: %s, and it gates "+
		"none of this epic's done items whatever it claims — absorbing it would hold the epic on work no tick of "+
		"this run can do", finding.Target, claim)
}

// isThisRepository reports whether a finding's target names the repository
// being run — a worker that spelled this repository out is reporting about it,
// not routing anything elsewhere.
func (r *Reconciler) isThisRepository(target string) bool {
	self, err := r.thisRepository()
	return err == nil && strings.EqualFold(self, target)
}

// thisRepository is the repository being run, owner/name, read from the URL
// of the remote the run pushes to.
func (r *Reconciler) thisRepository() (string, error) {
	url, err := r.git.run("", "remote", "get-url", r.opts.Remote)
	if err != nil {
		return "", err
	}
	return forge.ParseRepo(url)
}

// decideRoutedFinding is the routed half of decideFinding: file the finding in
// its target's tracker when the repository allows it, otherwise backlog it
// here, and record the decision behind which the triage is completed. It is
// resumable the way the local decision is: a filing that landed before the
// kill is found again by its external_ref, and a backlog tick is created
// behind the record that names it.
//
// atCloseout says who is asking: a transient failure at a tick's attempt
// leaves the finding for the close-out rather than failing the attempt that
// reported it, and at the close-out it is the run's resumable transient stop.
func (r *Reconciler) decideRoutedFinding(ctx context.Context, marker attemptHandle, standing runstate.Finding,
	dispatch Dispatch, atCloseout bool) (findingDecision, error) {
	reason := ruleReason(standing)
	record := runstate.Absorption{
		Key:        standing.Key,
		Gating:     false,
		Basis:      runstate.AbsorptionRule,
		Target:     standing.Target,
		DecidedAt:  r.now().UTC().Format(time.RFC3339),
		Provenance: r.attemptProvenance(dispatch),
	}

	why := ""
	route, declared, err := r.findingRoute(standing.Target)
	switch {
	case err != nil:
		why = fmt.Sprintf("the repository's runners.toml could not be read to find a route for it (%v)", err)
	case !declared || !route.File:
		why = fmt.Sprintf("%s is not a repository this factory files into — no [findings.route.%q] file = true in "+
			".tick/runners.toml", standing.Target, standing.Target)
	case runTokenIsRepositoryScoped():
		// Tick gy9: on the factory's GitHub App rung the run's only GitHub
		// credential is an installation token minted for the run's own
		// repository (cloudflare/src/github-app.ts mints it with
		// repositories: [repo], and the token door names no other). A push
		// to the target with it is refused with certainty — run_5c7c spent
		// four of them on ticks — so the run does not make it.
		self, _ := r.thisRepository()
		why = fmt.Sprintf("this run's GitHub credential is the factory App's installation token, minted for %s "+
			"alone, so a write to %s could only be refused (cross-repository; the run did not attempt it). A "+
			"local run, whose operator credential reaches %s, files it there", orUnknown(self), standing.Target,
			standing.Target)
	default:
		id, fileErr := r.fileIntoTarget(standing, route)
		if fileErr == nil {
			record.TickID = standing.Target + ":" + id
			record.Placement = runstate.AbsorptionRouted
			record.Reason = fmt.Sprintf("%s. The repository's runners.toml lets this run file into %s, and it "+
				"filed the finding there as %s", reason, standing.Target, record.TickID)
			return r.recordRoutedDecision(ctx, marker, standing, record)
		}
		if runstate.ClassifyRemote(fileErr) == runstate.RemoteTransient {
			if atCloseout {
				return findingDecision{}, fmt.Errorf("file the finding %s into %s: %w", standing.Key, standing.Target, fileErr)
			}
			r.record(marker.TickID, StageFindingRouteDeferred,
				"finding %s is routed to %s and filing it there failed transiently past the retry bound; the "+
					"close-out files it before it gates: %v", standing.Key, standing.Target, fileErr)
			return findingDecision{Left: fmt.Sprintf("filing it into %s failed transiently, and the close-out "+
				"files it before it gates", standing.Target)}, nil
		}
		why = fmt.Sprintf("it could not be filed in %s (%v)", standing.Target, fileErr)
	}

	// The fallback: a backlog tick HERE, naming the target.
	tickID, err := r.mintTickID()
	if err != nil {
		return findingDecision{}, err
	}
	record.TickID = tickID
	record.Placement = runstate.AbsorptionBacklog
	record.Reason = fmt.Sprintf("%s. The run did not file it there because %s, so it filed a backlog tick in this "+
		"repository naming %s and carrying the finding verbatim, for a person to carry it there", reason, why,
		standing.Target)
	return r.recordRoutedDecision(ctx, marker, standing, record)
}

// runTokenIsRepositoryScoped answers whether this process's GitHub credential
// is a token minted for the run's one repository: the factory's GitHub App
// rung, the only rung whose container is handed the token door
// (forge.FactoryTokenURLEnv; cloudflare/src/github-app.ts containerGitHub).
// A variable so a test can say which rung it is on.
var runTokenIsRepositoryScoped = func() bool {
	return strings.TrimSpace(os.Getenv(forge.FactoryTokenURLEnv)) != ""
}

func orUnknown(s string) string {
	if s == "" {
		return "this repository"
	}
	return s
}

// recordRoutedDecision writes the decision record create-if-absent and
// finishes behind whichever record stands — this one, or a concurrent
// incarnation's.
func (r *Reconciler) recordRoutedDecision(ctx context.Context, marker attemptHandle, standing runstate.Finding,
	record runstate.Absorption) (findingDecision, error) {
	outcome, err := r.store.PutAbsorption(record)
	if err != nil {
		return findingDecision{}, err
	}
	if !outcome.EffectPermitted() {
		recorded, ok, err := r.store.Absorption(record.Key)
		if err != nil || !ok {
			return findingDecision{}, fmt.Errorf("read the decision a concurrent incarnation left for finding %s: %v %v",
				record.Key, ok, err)
		}
		return r.finishAbsorption(ctx, marker, runstate.Finding{}, *recorded)
	}
	return r.finishAbsorption(ctx, marker, standing, record)
}

// findingRoute is the declared route for a target, read from the same
// runners.toml — and the same substrate overlay — the gate reads.
func (r *Reconciler) findingRoute(target string) (runconfig.FindingRoute, bool, error) {
	cfg, err := runconfig.LoadFor(r.opts.GateConfig, r.substrate)
	if err != nil {
		return runconfig.FindingRoute{}, false, err
	}
	route, ok := cfg.FindingRoute(target)
	return route, ok, nil
}

// routedRemote is the URL the target is fetched from and pushed to: the one
// the route declares, else this repository's own remote URL with its
// owner/name replaced by the target's — right for two repositories on one
// forge, which is what the table is for.
func (r *Reconciler) routedRemote(target string, route runconfig.FindingRoute) (string, error) {
	if route.Remote != "" {
		return route.Remote, nil
	}
	url, err := r.git.run("", "remote", "get-url", r.opts.Remote)
	if err != nil {
		return "", err
	}
	self, err := forge.ParseRepo(url)
	if err != nil {
		return "", fmt.Errorf("no URL for %s can be derived from this repository's remote (%v): declare "+
			"[findings.route.%q] remote", target, err, target)
	}
	trimmed := strings.TrimSuffix(strings.TrimSpace(url), "/")
	suffix := ""
	if strings.HasSuffix(trimmed, ".git") {
		trimmed, suffix = strings.TrimSuffix(trimmed, ".git"), ".git"
	}
	if !strings.HasSuffix(trimmed, self) {
		return "", fmt.Errorf("no URL for %s can be derived from this repository's remote %s: declare "+
			"[findings.route.%q] remote", target, url, target)
	}
	return strings.TrimSuffix(trimmed, self) + target + suffix, nil
}

// routedTracker is where the target's tracker keeps a tick record, in the
// layout contracts/tracker-layout.json pins — the layout this repository's own
// tracker writes (trackerRecordPath) and tk reads.
func routedTracker(id string) string {
	return trackerRoot + "/issues/" + id + ".json"
}

// errNotATracker is the target that has no tracker to file into: terminal,
// because no retry grows one.
var errNotATracker = errors.New("the target carries no .tick/ tracker on its default branch")

// fileIntoTarget files the finding as a tick in the target's own tracker and
// answers the tick's id.
//
// The mechanism is the least a write to another repository can be: a
// throwaway bare repository, a depth-one fetch of the target's default branch
// into it, one new record committed on top with plumbing, and a plain push —
// never forced. A push that lost a race is rebuilt on the new head, bounded
// the way the tracker's own writer is. The record is found again, before
// anything is minted, by its external_ref — the finding's own key — so a
// filing a killed incarnation already pushed is answered rather than repeated.
// Every network step goes through the run's git runner, whose retry bound
// waits through a transient failure and says so in the feed.
func (r *Reconciler) fileIntoTarget(finding runstate.Finding, route runconfig.FindingRoute) (string, error) {
	url, err := r.routedRemote(finding.Target, route)
	if err != nil {
		return "", err
	}
	root, remove, err := tempdir.Make("ticfac-route-")
	if err != nil {
		return "", err
	}
	defer remove()
	dir := filepath.Join(root, "target.git")
	if _, err := r.git.run(root, "init", "--quiet", "--bare", dir); err != nil {
		return "", err
	}

	branch, err := r.targetDefaultBranch(dir, url)
	if err != nil {
		return "", err
	}
	ref := "refs/ticfac/route/head"
	externalRef := findingSource + ":" + finding.Key
	for try := 0; try < maxTrackerPushes; try++ {
		if _, err := r.git.run(dir, "fetch", "--no-write-fetch-head", "--refmap=", "--quiet", "--depth=1", url,
			"+"+refFor(branch)+":"+ref); err != nil {
			return "", err
		}
		head, err := r.git.run(dir, "rev-parse", ref)
		if err != nil {
			return "", err
		}
		if _, _, err := r.git.try(dir, "cat-file", "-e", head+":"+trackerRoot); err != nil {
			return "", fmt.Errorf("%s (%s): %w", finding.Target, branch, errNotATracker)
		}
		if id := filedAs(r.git, dir, head, externalRef); id != "" {
			return id, nil
		}
		id := ""
		for _, candidate := range tickIDCandidates() {
			if _, _, err := r.git.try(dir, "cat-file", "-e", head+":"+routedTracker(candidate)); err != nil {
				id = candidate
				break
			}
		}
		if id == "" {
			return "", fmt.Errorf("no tick id of the pinned alphabet is free in %s's tracker", finding.Target)
		}
		commit, err := r.routedCommit(dir, root, head, r.routedTick(finding, id, externalRef))
		if err != nil {
			return "", err
		}
		_, stderr, pushErr := r.git.try(dir, "push", "--quiet", url, commit+":"+refFor(branch))
		if pushErr == nil {
			return id, nil
		}
		if !leaseRefused(stderr) {
			return "", pushErr
		}
		// The target's default branch moved under the filing: rebuild the one
		// record on its new head rather than force over what arrived.
	}
	return "", fmt.Errorf("%s's default branch moved under the filing %d times running; that is an operational "+
		"problem, not a conflict to spin on", finding.Target, maxTrackerPushes)
}

// targetDefaultBranch is the branch the target's HEAD names, asked of the
// target itself.
func (r *Reconciler) targetDefaultBranch(dir, url string) (string, error) {
	out, err := r.git.run(dir, "ls-remote", "--symref", url, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "ref: "); ok {
			name, _, _ := strings.Cut(rest, "\t")
			if branch, ok := strings.CutPrefix(strings.TrimSpace(name), "refs/heads/"); ok && branch != "" {
				return branch, nil
			}
		}
	}
	return "", fmt.Errorf("the target at %s names no default branch: %w", url, errNotATracker)
}

// filedAs is the id of a tick the target's tracker already carries for this
// finding, found by its external_ref, or "" when it carries none.
func filedAs(g *repoGit, dir, head, externalRef string) string {
	out, _, err := g.try(dir, "grep", "-l", "-F", "-e", externalRef, head, "--", trackerRoot+"/issues/")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		path := strings.TrimPrefix(strings.TrimSpace(line), head+":")
		if name, ok := strings.CutPrefix(path, trackerRoot+"/issues/"); ok {
			if id, ok := strings.CutSuffix(name, ".json"); ok && id != "" && !strings.Contains(id, "/") {
				return id
			}
		}
	}
	return ""
}

// routedTick is the tick a filing creates in the target's tracker: the
// finding's own title and text, where it came from, and the external_ref a
// repeat filing is deduplicated on.
func (r *Reconciler) routedTick(finding runstate.Finding, id, externalRef string) tk.Tick {
	from := "a ticfac run"
	if self, err := r.thisRepository(); err == nil {
		from = self
	}
	body := strings.TrimSpace(finding.Body)
	if body == "" {
		body = finding.Title
	}
	how := fmt.Sprintf("Filed by ticfac run %s of %s from the finding %s (%s, severity %s), reported by %s. "+
		"The finding was routed to this repository; the run that found it cannot fix another repository, so it "+
		"filed it here rather than hold its own epic on it.", r.runID, from, finding.Key, finding.Kind,
		finding.Severity, finding.DiscoveredFrom)
	owner := r.opts.Owner
	if epic, err := r.tracker.Show(context.Background(), r.opts.EpicID); err == nil && epic.Owner != "" {
		owner = epic.Owner
	}
	at := r.now().UTC().Format(time.RFC3339)
	return tk.Tick{
		ID:          id,
		Title:       finding.Title,
		Description: body + "\n\n" + how,
		Status:      "open",
		Priority:    2,
		Type:        "task",
		Owner:       owner,
		ExternalRef: externalRef,
		CreatedBy:   "ticfac run " + r.runID,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
}

// routedCommit is the one-record commit on top of head, built with plumbing in
// a throwaway index: nothing else in the target's tree is touched.
func (r *Reconciler) routedCommit(dir, scratch, head string, tick tk.Tick) (string, error) {
	raw, err := json.MarshalIndent(tick, "", "  ")
	if err != nil {
		return "", err
	}
	file := filepath.Join(scratch, tick.ID+".json")
	if err := os.WriteFile(file, append(raw, '\n'), 0o644); err != nil {
		return "", err
	}
	blob, err := r.git.run(dir, "hash-object", "-w", file)
	if err != nil {
		return "", err
	}
	index := filepath.Join(scratch, "index")
	_ = os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, _, err := r.git.tryEnv(dir, env, "read-tree", head); err != nil {
		return "", err
	}
	if _, _, err := r.git.tryEnv(dir, env, "update-index", "--add", "--cacheinfo",
		"100644,"+blob+","+routedTracker(tick.ID)); err != nil {
		return "", err
	}
	tree, _, err := r.git.tryEnv(dir, env, "write-tree")
	if err != nil {
		return "", err
	}
	return r.git.run(dir, "commit-tree", tree, "-p", head, "-m",
		fmt.Sprintf("ticfac run %s: file finding %s as tick %s", r.runID, tick.ExternalRef, tick.ID))
}

// disposeRoutedFindings decides every routed finding still proposed, before
// the close-out gates on findings: the ones a transient failure deferred, and
// any an incarnation before this rule existed left for a person. It is the
// reason the close-out's untriaged hold never fires on a routed finding.
func (r *Reconciler) disposeRoutedFindings(ctx context.Context, marker attemptHandle) error {
	untriaged, err := r.untriagedFindings()
	if err != nil {
		return fmt.Errorf("read the run's drafted findings: %w", err)
	}
	var dispatch *Dispatch
	for _, finding := range untriaged {
		if finding.Target == "" || r.isThisRepository(finding.Target) {
			continue
		}
		if dispatch == nil {
			d, err := r.dispatchFor(marker)
			if err != nil {
				return err
			}
			dispatch = &d
		}
		if recorded, ok, err := r.store.Absorption(finding.Key); err != nil {
			return err
		} else if ok {
			if _, err := r.finishAbsorption(ctx, marker, finding, *recorded); err != nil {
				return err
			}
			continue
		}
		if _, err := r.decideRoutedFinding(ctx, marker, finding, *dispatch, true); err != nil {
			return err
		}
	}
	return nil
}
