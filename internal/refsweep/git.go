package refsweep

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Git is the one checkout a sweep reads and pushes through, and the remote
// it sweeps.
type Git struct {
	Repo   string
	Remote string
}

// run runs one git command in the checkout. stdin may be nil.
func (g Git) run(ctx context.Context, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, gitbin.Path(), append([]string{"-C", g.Repo}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Env = gitbin.WithNoAutoMaintenance(append(os.Environ(), gitbin.TransportEnv()...))
	done := gitbin.PushQueue(g.Repo, args, nil)
	err := cmd.Run()
	done(err)
	if err != nil {
		return stdout.String(), fmt.Errorf("git %s: %v: %s", firstArgs(args), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// firstArgs keeps an error short: a push of a hundred refspecs is named by
// its first few.
func firstArgs(args []string) string {
	if len(args) > 4 {
		return strings.Join(args[:4], " ") + " …"
	}
	return strings.Join(args, " ")
}

// OwnedPrefixes are the remote ref namespaces a whole-remote sweep lists.
// Each is a namespace of ticfac's own; Classify decides within them.
var OwnedPrefixes = []string{
	"refs/heads/ticfac/run-",
	"refs/heads/tick/",
	"refs/ticfac/start/run-",
	"refs/ticfac/wip/run-",
}

// RunPrefixes are the remote ref namespaces one run owns.
func RunPrefixes(runID string) []string {
	return []string{
		"refs/heads/ticfac/run-" + runID + "/",
		"refs/ticfac/start/run-" + runID + "/",
		"refs/ticfac/wip/run-" + runID + "/",
	}
}

// ListRemote lists the remote's refs under the given prefixes, with their
// shas. Only names Classify owns are answered.
func (g Git) ListRemote(ctx context.Context, prefixes []string) ([]Ref, error) {
	args := []string{"ls-remote", g.Remote}
	for _, p := range prefixes {
		args = append(args, p+"*")
	}
	out, err := g.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || seen[name] {
			continue
		}
		covered := false
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				covered = true
				break
			}
		}
		if _, owned := Classify(name); covered && owned {
			seen[name] = true
			refs = append(refs, Ref{Name: name, SHA: sha})
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

var scanSeq atomic.Uint64

// scratch is a private ref namespace this process fetches into, never a
// shared ref and never FETCH_HEAD; Close removes every ref under it.
type scratch struct {
	g  Git
	ns string
}

func (g Git) newScratch() scratch {
	return scratch{g: g, ns: "refs/ticfac/peek/refsweep/" + strconv.Itoa(os.Getpid()) + "-" +
		strconv.FormatUint(scanSeq.Add(1), 10)}
}

// fetch fetches the remote refs by exact name into the namespace, under
// their own names. A batch that fails is retried ref by ref, so one ref that
// vanished since the listing costs only itself.
func (s scratch) fetch(ctx context.Context, names []string) {
	const batch = 100
	for len(names) > 0 {
		n := min(batch, len(names))
		chunk := names[:n]
		names = names[n:]
		if s.fetchSome(ctx, chunk) != nil {
			for _, name := range chunk {
				_ = s.fetchSome(ctx, []string{name})
			}
		}
	}
}

func (s scratch) fetchSome(ctx context.Context, names []string) error {
	args := []string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--refmap=", s.g.Remote}
	for _, name := range names {
		args = append(args, "+"+name+":"+s.ns+"/"+name)
	}
	_, err := s.g.run(ctx, nil, args...)
	return err
}

// mergedInto answers the shas of the fetched candidates reachable from
// target (a ref under the namespace, or a sha).
func (s scratch) mergedInto(ctx context.Context, target string) map[string]bool {
	out, err := s.g.run(ctx, nil, "for-each-ref", "--merged="+target, "--format=%(objectname)", s.ns+"/c/")
	merged := map[string]bool{}
	if err != nil {
		return merged
	}
	for _, sha := range strings.Fields(out) {
		merged[sha] = true
	}
	return merged
}

// exists answers whether ref resolves in the checkout.
func (s scratch) exists(ctx context.Context, ref string) bool {
	_, err := s.g.run(ctx, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// close removes every ref the scratch namespace holds.
func (s scratch) close(ctx context.Context) {
	out, err := s.g.run(ctx, nil, "for-each-ref", "--format=delete %(refname)", s.ns+"/")
	if err != nil || strings.TrimSpace(out) == "" {
		return
	}
	_, _ = s.g.run(ctx, []byte(out), "update-ref", "--stdin")
}

// mergeOracle fetches every candidate under ns/c/, and the targets, and
// answers a Merged function for Plan: a sha is merged when it is reachable
// from base or from the named epic's integration branch.
func (s scratch) mergeOracle(ctx context.Context, refs []Ref, base string, epicBranch func(epic string) string,
	extraTargets []string) func(sha, epic string) bool {
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		names = append(names, r.Name)
	}
	// Candidates live under ns/c/<name>, targets under ns/<name>.
	cands := scratch{g: s.g, ns: s.ns + "/c"}
	cands.fetch(ctx, names)

	var always []map[string]bool
	if base != "" {
		baseRef := s.ns + "/refs/heads/" + base
		s.fetch(ctx, []string{"refs/heads/" + base})
		if s.exists(ctx, baseRef) {
			always = append(always, s.mergedInto(ctx, baseRef))
		}
	}
	for _, t := range extraTargets {
		if t != "" && s.exists(ctx, t) {
			always = append(always, s.mergedInto(ctx, t))
		}
	}
	perEpic := map[string]map[string]bool{}
	return func(sha, epic string) bool {
		for _, m := range always {
			if m[sha] {
				return true
			}
		}
		if epic == "" || epicBranch == nil {
			return false
		}
		m, ok := perEpic[epic]
		if !ok {
			m = map[string]bool{}
			if branch := epicBranch(epic); branch != "" {
				ref := s.ns + "/refs/heads/" + branch
				s.fetch(ctx, []string{"refs/heads/" + branch})
				if s.exists(ctx, ref) {
					m = s.mergedInto(ctx, ref)
				}
			}
			perEpic[epic] = m
		}
		return m[sha]
	}
}

// Checkpoints reads every run checkpoint the remote's base branch and epic
// branches carry, and answers, per run, the one with the highest sequence:
// a run's checkpoint is copied onto every branch that merges its epic, and
// only the newest copy says where the run is.
func (s scratch) checkpoints(ctx context.Context, base string) map[string]Run {
	_, _ = s.g.run(ctx, nil, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--refmap=", s.g.Remote,
		"+refs/heads/epic/*:"+s.ns+"/refs/heads/epic/*")
	if base != "" {
		s.fetch(ctx, []string{"refs/heads/" + base})
	}
	out, err := s.g.run(ctx, nil, "for-each-ref", "--format=%(refname)", s.ns+"/refs/heads/")
	if err != nil {
		return nil
	}
	var specs []string
	for _, ref := range strings.Fields(out) {
		if strings.HasPrefix(ref, s.ns+"/c/") {
			continue
		}
		tree, err := s.g.run(ctx, nil, "ls-tree", "-r", "--name-only", ref, "--", runstate.Root+"/runs/")
		if err != nil {
			continue
		}
		for _, path := range strings.Fields(tree) {
			if strings.HasSuffix(path, "/checkpoint.json") {
				specs = append(specs, ref+":"+path)
			}
		}
	}
	return readCheckpoints(ctx, s.g, specs)
}

type checkpointCopy struct {
	run Run
	seq int
}

// readCheckpoints reads the blobs in one cat-file --batch and keeps the
// highest-sequence copy of each run.
func readCheckpoints(ctx context.Context, g Git, specs []string) map[string]Run {
	runs := map[string]Run{}
	if len(specs) == 0 {
		return runs
	}
	out, err := g.run(ctx, []byte(strings.Join(specs, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return runs
	}
	best := map[string]checkpointCopy{}
	rd := bufio.NewReader(strings.NewReader(out))
	for {
		header, err := rd.ReadString('\n')
		if err != nil {
			break
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			continue // "<spec> missing"
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			break
		}
		body := make([]byte, size+1)
		if _, err := ioReadFull(rd, body); err != nil {
			break
		}
		var c runstate.Checkpoint
		if json.Unmarshal(body[:size], &c) != nil || c.RunID == "" || c.EpicID == "" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, c.UpdatedAt)
		if prev, ok := best[c.RunID]; ok && (prev.seq > c.Sequence || (prev.seq == c.Sequence && !at.After(prev.run.UpdatedAt))) {
			continue
		}
		best[c.RunID] = checkpointCopy{run: Run{ID: c.RunID, Epic: c.EpicID, State: c.State, UpdatedAt: at}, seq: c.Sequence}
	}
	for id, c := range best {
		runs[id] = c.run
	}
	return runs
}

func ioReadFull(rd *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := rd.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// Delete removes the refs from the remote by exact name, each under a lease
// on the sha its verdict was made on: a ref that moved since is not deleted.
// It answers the refs deleted and, for each one that was not, why.
func (g Git) Delete(ctx context.Context, refs []Ref) (deleted []Ref, failed map[string]string) {
	failed = map[string]string{}
	const batch = 50
	for len(refs) > 0 {
		n := min(batch, len(refs))
		chunk := refs[:n]
		refs = refs[n:]
		if err := g.deleteSome(ctx, chunk); err == nil {
			deleted = append(deleted, chunk...)
			continue
		}
		// One ref the batch could not delete fails the whole push; each is
		// tried alone so the rest still go.
		for _, ref := range chunk {
			if err := g.deleteSome(ctx, []Ref{ref}); err != nil {
				failed[ref.Name] = err.Error()
				continue
			}
			deleted = append(deleted, ref)
		}
	}
	return deleted, failed
}

func (g Git) deleteSome(ctx context.Context, refs []Ref) error {
	args := []string{"push", "--quiet", "--no-verify"}
	for _, ref := range refs {
		args = append(args, "--force-with-lease="+ref.Name+":"+ref.SHA)
	}
	args = append(args, g.Remote)
	for _, ref := range refs {
		args = append(args, ":"+ref.Name)
	}
	_, err := g.run(ctx, nil, args...)
	return err
}
